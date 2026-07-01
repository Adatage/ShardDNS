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

// job carries a single inbound request from a reader goroutine to a worker.
// Exactly one of udpAddr and tcpConn is set.
type job struct {
	data    []byte
	udpAddr *net.UDPAddr
	tcpConn net.Conn
}

// Server is the UDP+TCP authoritative DNS listener. Requests are handed off
// to a fixed worker pool, and read buffers are recycled through a sync.Pool.
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

// New constructs a Server. The listeners are not opened until Start is
// called so callers can inject the store before binding.
func New(cfg *config.Config, s *store.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	srv := &Server{
		handler: NewHandler(s, logger),
		workers: cfg.Workers,
		bufSize: cfg.DNSReadBufSize,
		logger:  logger,
		addr:    cfg.DNSAddr,
		// Buffered jobs channel provides back-pressure without dropping
		// packets when the worker pool briefly stalls.
		jobs: make(chan job, cfg.Workers*4),
	}
	bufSize := cfg.DNSReadBufSize
	srv.pool.New = func() any {
		b := make([]byte, bufSize)
		return &b
	}
	return srv
}

// getBuf/putBuf recycle byte slices through the pool. We deliberately store
// *[]byte rather than []byte to avoid the well-known allocation caused by
// putting a slice header into an interface value.
func (s *Server) getBuf() *[]byte { return s.pool.Get().(*[]byte) }
func (s *Server) putBuf(b *[]byte) {
	if cap(*b) < s.bufSize {
		return
	}
	*b = (*b)[:s.bufSize]
	s.pool.Put(b)
}

// Start opens both listeners, spins up the worker pool, and blocks until
// ctx is cancelled. The two accept loops (UDP + TCP) run in their own
// goroutines.
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

	// Workers.
	wg.Add(s.workers)
	for i := 0; i < s.workers; i++ {
		go func() {
			defer wg.Done()
			s.workerLoop(ctx)
		}()
	}

	// UDP reader.
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.udpReadLoop(ctx)
	}()

	// TCP accept loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.tcpAcceptLoop(ctx)
	}()

	<-ctx.Done()
	s.logger.Info("DNS server shutting down")
	// Closing the listeners unblocks the reader goroutines with a "use of
	// closed network connection" error.
	_ = s.udpConn.Close()
	_ = s.tcpListener.Close()
	close(s.jobs)
	wg.Wait()
	return nil
}

// udpReadLoop reads packets and enqueues jobs. A fresh buffer is taken
// from the pool per packet so the worker can safely own it.
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

// tcpAcceptLoop accepts TCP connections. Each connection is handled in its
// own goroutine which reads DNS messages (2-byte length-prefixed) and
// enqueues them onto the shared work queue.
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

// tcpConnLoop reads length-prefixed DNS messages from a single TCP
// connection. Per RFC 1035 §4.2.2 each message is preceded by a two-byte
// length in network byte order.
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

// workerLoop pulls jobs from the queue and dispatches them.
func (s *Server) workerLoop(ctx context.Context) {
	for j := range s.jobs {
		s.serve(ctx, j)
	}
}

// serve parses, handles, packs, and sends a single request.
func (s *Server) serve(ctx context.Context, j job) {
	req, err := ParseMessage(j.data)
	if err != nil {
		// Return the UDP buffer to the pool if we took it.
		if j.udpAddr != nil {
			s.recycle(j.data)
		}
		s.logger.Debug("parse error", "err", err)
		return
	}

	resp := s.handler.Handle(ctx, req)

	// UDP response — truncate if it exceeds 512 bytes and set TC.
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
			// Truncate: only keep the header + question section and set TC.
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
		// Return the buffer we packed into if it was pool-owned. Note the
		// out slice may have grown past bufSize; putBuf tolerates that.
		bp := out
		s.pool.Put(&bp)
		s.recycle(j.data)
		return
	}

	// TCP response — always length-prefixed.
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

// recycle returns a UDP read buffer to the pool. data is a sub-slice of the
// pooled buffer; we re-extend to bufSize before Put.
func (s *Server) recycle(data []byte) {
	if cap(data) < s.bufSize {
		return
	}
	full := data[:cap(data)]
	s.pool.Put(&full)
}
