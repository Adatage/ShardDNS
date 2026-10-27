package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	sharddnsv1 "github.com/Adatage/ShardDNS/api/sharddns/v1"
	"github.com/Adatage/ShardDNS/api/sharddns/v1/sharddnsv1connect"
	"github.com/Adatage/ShardDNS/internal/api"
	"github.com/Adatage/ShardDNS/internal/config"
	"github.com/Adatage/ShardDNS/internal/dns"
	"github.com/Adatage/ShardDNS/internal/events"
	"github.com/Adatage/ShardDNS/internal/store"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-version", "--version":
			fmt.Println(version)
			return
		case "-healthcheck", "--healthcheck":
			os.Exit(healthcheck())
		}
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = ":9053"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/readyz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := connectStore(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		return err
	}

	var (
		resolver store.Resolver = st
		cached   *store.CachedResolver
	)
	if cfg.CacheEnabled {
		cached = store.NewCachedResolver(st, cfg.CacheMaxEntries, cfg.CacheTTL)
		resolver = cached
		logger.Info("cache enabled", "ttl", cfg.CacheTTL.String(), "max_entries", cfg.CacheMaxEntries)
	}

	var bus *events.Bus
	if cfg.NATSURL != "" {
		var onReconnect func()
		if cached != nil {
			onReconnect = func() { cached.InvalidateAll() }
		}
		bus, err = events.Connect(cfg.NATSURL, cfg.NATSSubject, onReconnect, logger)
		if err != nil {
			return err
		}
		defer bus.Close()
		if cached != nil {
			err = bus.Subscribe(func(ev *sharddnsv1.ChangeEvent) {
				if ev.GetKind() == sharddnsv1.ChangeEvent_KIND_ZONE {
					cached.Invalidate(ev.GetZone(), "")
				} else {
					cached.Invalidate(ev.GetZone(), ev.GetName())
				}
			})
			if err != nil {
				return fmt.Errorf("subscribe to change events: %w", err)
			}
		}
		logger.Info("NATS change events enabled", "subject", cfg.NATSSubject)
	} else if cached != nil {
		logger.Warn("cache enabled without NATS_URL; other instances will serve stale data until their entries are evicted or CACHE_TTL expires")
	}

	notify := func(ctx context.Context, kind sharddnsv1.ChangeEvent_Kind, zone, name string) {
		if cached != nil {
			if kind == sharddnsv1.ChangeEvent_KIND_ZONE {
				name = ""
			}
			cached.Invalidate(zone, name)
		}
		if bus != nil {
			bus.Publish(ctx, kind, zone, name)
		}
	}

	dnsServer := dns.New(cfg.DNSAddr, cfg.Workers, resolver, logger)
	if err := dnsServer.Listen(); err != nil {
		return err
	}

	httpSrv, apiLn, err := newAPIServer(cfg, st, api.NewService(st, notify, logger), bus)
	if err != nil {
		return err
	}

	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := dnsServer.Serve(ctx); err != nil {
			errCh <- fmt.Errorf("dns server: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		logger.Info("API listening", "addr", apiLn.Addr().String(), "version", version)
		if err := httpSrv.Serve(apiLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("api server: %w", err)
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case runErr = <-errCh:
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	wg.Wait()
	logger.Info("shutdown complete")
	return runErr
}

func newAPIServer(cfg *config.Config, st *store.Store, svc *api.Service, bus *events.Bus) (*http.Server, net.Listener, error) {
	var opts []connect.HandlerOption
	if cfg.APIToken != "" {
		opts = append(opts, connect.WithInterceptors(api.NewAuthInterceptor(cfg.APIToken)))
	}

	mux := http.NewServeMux()
	mux.Handle(sharddnsv1connect.NewDNSManagerHandler(svc, opts...))
	reflector := grpcreflect.NewStaticReflector(sharddnsv1connect.DNSManagerName)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := st.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		if bus != nil && !bus.Healthy() {
			http.Error(w, "nats unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	ln, err := net.Listen("tcp", cfg.APIAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s: %w", cfg.APIAddr, err)
	}
	return &http.Server{
		Handler:           mux,
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}, ln, nil
}

func connectStore(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*store.Store, error) {
	opts := store.Options{
		MaxConns: int32(cfg.DBMaxConns),
		Encoder: func(rtype, rdata string) ([]byte, error) {
			return dns.BuildRData(dns.TypeCode(rtype), rdata)
		},
	}
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		st, err := store.Open(ctx, cfg.DatabaseURL, opts)
		if err == nil {
			logger.Info("connected to PostgreSQL", "attempt", attempt)
			return st, nil
		}
		lastErr = err
		logger.Warn("PostgreSQL connect failed, retrying", "attempt", attempt, "err", err)
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
