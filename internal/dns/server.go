package dns

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/Adatage/ShardDNS/internal/store"
)

const (
	bufSize       = 4096
	maxTCPConns   = 4096
	tcpIdle       = 15 * time.Second
	tcpWriteLimit = 5 * time.Second
	socketBuffer  = 4 << 20
)

type job struct {
	buf  *[]byte
	n    int
	addr netip.AddrPort
}

type Server struct {
	udpConn     *net.UDPConn
	tcpListener net.Listener
	handler     *Handler
	workers     int
	pool        sync.Pool
	jobs        chan job
	logger      *slog.Logger
	addr        string
	tcpSlots    chan struct{}
}

func New(addr string, workers int, resolver store.Resolver, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if workers < 1 {
		workers = 1
	}
	s := &Server{
		handler:  NewHandler(resolver, logger),
		workers:  workers,
		logger:   logger,
		addr:     addr,
		jobs:     make(chan job, workers*16),
		tcpSlots: make(chan struct{}, maxTCPConns),
	}
	s.pool.New = func() any {
		b := make([]byte, bufSize)
		return &b
	}
	return s
}

func (s *Server) Listen() error {
	udpAddr, err := net.ResolveUDPAddr("udp", s.addr)
	if err != nil {
		return fmt.Errorf("dns: resolve udp addr: %w", err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("dns: listen udp: %w", err)
	}
	_ = udpConn.SetReadBuffer(socketBuffer)
	_ = udpConn.SetWriteBuffer(socketBuffer)

	tcpLn, err := net.Listen("tcp", s.addr)
	if err != nil {
		udpConn.Close()
		return fmt.Errorf("dns: listen tcp: %w", err)
	}
	s.udpConn = udpConn
	s.tcpListener = tcpLn
	return nil
}

func (s *Server) Serve(ctx context.Context) error {
	if s.udpConn == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	s.logger.Info("DNS server listening", "addr", s.addr, "workers", s.workers)

	var workers, readers, conns sync.WaitGroup

	workers.Add(s.workers)
	for i := 0; i < s.workers; i++ {
		go func() {
			defer workers.Done()
			for j := range s.jobs {
				s.serveUDP(ctx, j)
			}
		}()
	}

	readers.Add(2)
	go func() {
		defer readers.Done()
		s.udpReadLoop(ctx)
	}()
	go func() {
		defer readers.Done()
		s.tcpAcceptLoop(ctx, &conns)
	}()

	<-ctx.Done()
	s.logger.Info("DNS server shutting down")
	_ = s.udpConn.Close()
	_ = s.tcpListener.Close()
	readers.Wait()
	close(s.jobs)
	workers.Wait()
	conns.Wait()
	return nil
}

func (s *Server) udpReadLoop(ctx context.Context) {
	for {
		bp := s.pool.Get().(*[]byte)
		n, addr, err := s.udpConn.ReadFromUDPAddrPort(*bp)
		if err != nil {
			s.pool.Put(bp)
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			s.logger.Warn("udp read error", "err", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		select {
		case s.jobs <- job{buf: bp, n: n, addr: addr}:
		default:
			s.pool.Put(bp)
		}
	}
}

func (s *Server) serveUDP(ctx context.Context, j job) {
	bp := j.buf
	defer s.pool.Put(bp)

	data := (*bp)[:j.n]
	req, err := ParseMessage(data)
	if err != nil {
		s.logger.Debug("parse error", "err", err)
		if len(data) >= 12 && data[2]&0x80 == 0 {
			out := formErr(data)
			_, _ = s.udpConn.WriteToUDPAddrPort(out, j.addr)
		}
		return
	}
	if req.Flags&FlagQR != 0 {
		return
	}

	resp := s.handler.Handle(ctx, req)
	out, err := resp.PackUDP((*bp)[:0])
	if err != nil {
		s.logger.Warn("pack error", "err", err)
		return
	}
	if _, err := s.udpConn.WriteToUDPAddrPort(out, j.addr); err != nil {
		s.logger.Debug("udp write error", "err", err)
	}
}

func formErr(req []byte) []byte {
	out := make([]byte, 12)
	copy(out[0:2], req[0:2])
	out[2] = 0x80 | req[2]&0x79
	out[3] = RcodeFormatError
	return out
}

func (s *Server) tcpAcceptLoop(ctx context.Context, conns *sync.WaitGroup) {
	for {
		conn, err := s.tcpListener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			s.logger.Warn("tcp accept error", "err", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		select {
		case s.tcpSlots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		conns.Add(1)
		go func() {
			defer conns.Done()
			defer func() { <-s.tcpSlots }()
			s.tcpConnLoop(ctx, conn)
		}()
	}
}

func (s *Server) tcpConnLoop(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	bp := s.pool.Get().(*[]byte)
	defer s.pool.Put(bp)
	var lenBuf [2]byte

	for {
		_ = conn.SetReadDeadline(time.Now().Add(tcpIdle))
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		msgLen := int(binary.BigEndian.Uint16(lenBuf[:]))
		if msgLen < 12 {
			return
		}
		data := make([]byte, msgLen)
		if _, err := io.ReadFull(conn, data); err != nil {
			return
		}

		req, err := ParseMessage(data)
		if err != nil {
			s.logger.Debug("parse error", "err", err)
			if data[2]&0x80 == 0 {
				if !s.writeTCP(conn, formErr(data), bp) {
					return
				}
				continue
			}
			return
		}
		if req.Flags&FlagQR != 0 {
			return
		}

		resp := s.handler.Handle(ctx, req)
		out, err := resp.Pack((*bp)[2:2])
		if err != nil || len(out) > 65535 {
			s.logger.Warn("tcp pack error", "err", err, "size", len(out))
			return
		}
		if !s.writeTCP(conn, out, bp) {
			return
		}
	}
}

func (s *Server) writeTCP(conn net.Conn, msg []byte, bp *[]byte) bool {
	frame := append((*bp)[:0], 0, 0)
	frame = append(frame, msg...)
	binary.BigEndian.PutUint16(frame[:2], uint16(len(msg)))
	_ = conn.SetWriteDeadline(time.Now().Add(tcpWriteLimit))
	_, err := conn.Write(frame)
	return err == nil
}
