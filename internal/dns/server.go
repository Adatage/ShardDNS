package dns

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/Adatage/ShardDNS/internal/config"
	"github.com/Adatage/ShardDNS/internal/store"
)

type job struct {
	data    []byte
	udpAddr *net.UDPAddr
	tcpConn net.Conn
}

type Server struct {
	udpConn     *net.UDPConn
	tcpListener net.Listener
	handler     *Handler
	workers     int
	bufSize     int
	pool        sync.Pool
	jobs        chan job
	logger      *slog.Logger
	addr        string
}

func New(cfg *config.Config, s *store.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	srv := &Server{
		handler: NewHandler(s, logger),
		workers: cfg.Workers,
		bufSize: 4096,
		logger:  logger,
		addr:    cfg.DNSAddr,
		jobs: make(chan job, cfg.Workers*4),
	}
	bufSize := 4096
	srv.pool.New = func() any {
		b := make([]byte, bufSize)
		return &b
	}
	return srv
}

func (s *Server) getBuf() *[]byte {
	return s.pool.Get().(*[]byte)
}

func (s *Server) putBuf(b *[]byte) {
	if cap(*b) < s.bufSize {
		return
	}
	*b = (*b)[:s.bufSize]
	s.pool.Put(b)
}

func (s *Server) Start(ctx context.Context) error {
	udpAddr, err := net.ResolveUDPAddr("udp", s.addr)
	if err != nil {
		return fmt.Errorf("dns: resolve udp addr: %w", err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("dns: listen udp: %w", err)
	}
	s.udpConn = udpConn

	tcpLn, err := net.Listen("tcp", s.addr)
	if err != nil {
		s.udpConn.Close()
		return fmt.Errorf("dns: listen tcp: %w", err)
	}
	s.tcpListener = tcpLn

	s.logger.Info("DNS server listening", "addr", s.addr, "workers", s.workers)

	var wg sync.WaitGroup

	wg.Add(s.workers)
	for i := 0; i < s.workers; i++ {
		go func() {
			defer wg.Done()
			s.workerLoop(ctx)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.udpReadLoop(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.tcpAcceptLoop(ctx)
	}()

	<-ctx.Done()
	s.logger.Info("DNS server shutting down")
	_ = s.udpConn.Close()
	_ = s.tcpListener.Close()
	close(s.jobs)
	wg.Wait()
	return nil
}

func (s *Server) udpReadLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		bufPtr := s.getBuf()
		buf := *bufPtr
		n, addr, err := s.udpConn.ReadFromUDP(buf)
		if err != nil {
			s.putBuf(bufPtr)
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			s.logger.Warn("udp read error", "err", err)
			continue
		}
		select {
		case s.jobs <- job{data: buf[:n], udpAddr: addr}:
		case <-ctx.Done():
			s.putBuf(bufPtr)
			return
		}
	}
}

func (s *Server) tcpAcceptLoop(ctx context.Context) {
	for {
		conn, err := s.tcpListener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			s.logger.Warn("tcp accept error", "err", err)
			continue
		}
		go s.tcpConnLoop(ctx, conn)
	}
}

func (s *Server) tcpConnLoop(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var lenBuf [2]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.logger.Debug("tcp read length", "err", err)
			}
			return
		}
		msgLen := int(binary.BigEndian.Uint16(lenBuf[:]))
		if msgLen == 0 || msgLen > 65535 {
			return
		}
		data := make([]byte, msgLen)
		if _, err := io.ReadFull(conn, data); err != nil {
			s.logger.Debug("tcp read body", "err", err)
			return
		}
		select {
		case s.jobs <- job{data: data, tcpConn: conn}:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) workerLoop(ctx context.Context) {
	for j := range s.jobs {
		s.serve(ctx, j)
	}
}

func (s *Server) serve(ctx context.Context, j job) {
	req, err := ParseMessage(j.data)
	if err != nil {
		if j.udpAddr != nil {
			s.recycle(j.data)
		}
		s.logger.Debug("parse error", "err", err)
		return
	}

	resp := s.handler.Handle(ctx, req)

	if j.udpAddr != nil {
		outPtr := s.getBuf()
		out, err := resp.Pack((*outPtr)[:0])
		if err != nil {
			s.putBuf(outPtr)
			s.recycle(j.data)
			s.logger.Warn("pack error", "err", err)
			return
		}
		if len(out) > MaxUDPMessageSize {
			tcResp := &Message{
				Header:    resp.Header,
				Questions: resp.Questions,
			}
			tcResp.Flags |= FlagTC
			out2, err := tcResp.Pack(out[:0])
			if err == nil {
				out = out2
			}
		}
		if _, err := s.udpConn.WriteToUDP(out, j.udpAddr); err != nil {
			s.logger.Debug("udp write error", "err", err)
		}
		bp := out
		s.pool.Put(&bp)
		s.recycle(j.data)
		return
	}

	out, err := resp.Pack(nil)
	if err != nil {
		s.logger.Warn("pack error", "err", err)
		return
	}
	if len(out) > 65535 {
		s.logger.Warn("response too large for TCP", "size", len(out))
		return
	}
	var lp [2]byte
	binary.BigEndian.PutUint16(lp[:], uint16(len(out)))
	_ = j.tcpConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := j.tcpConn.Write(lp[:]); err != nil {
		return
	}
	if _, err := j.tcpConn.Write(out); err != nil {
		return
	}
}

func (s *Server) recycle(data []byte) {
	if cap(data) < s.bufSize {
		return
	}
	full := data[:cap(data)]
	s.pool.Put(&full)
}