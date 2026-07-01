package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	dnsmgr "github.com/Adatage/ShardDNS/api"
	"github.com/Adatage/ShardDNS/internal/config"
	dnssrv "github.com/Adatage/ShardDNS/internal/dns"
	"github.com/Adatage/ShardDNS/internal/grpcserver"
	"github.com/Adatage/ShardDNS/internal/store"

	"google.golang.org/grpc"
)

func main() {
	cfg := config.Load()
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := connectStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to connect to ScyllaDB", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	dnsServer := dnssrv.New(cfg, st, logger)

	grpcSrv := grpc.NewServer()
	dnsmgr.RegisterDNSManagerServer(grpcSrv, grpcserver.New(st))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := dnsServer.Start(ctx); err != nil {
			logger.Error("DNS server exited with error", "err", err)
			cancel()
		}
	}()

	wg.Add(1)
	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		logger.Error("failed to listen for gRPC", "addr", cfg.GRPCAddr, "err", err)
		os.Exit(1)
	}
	go func() {
		defer wg.Done()
		logger.Info("gRPC admin API listening", "addr", cfg.GRPCAddr)
		if err := grpcSrv.Serve(lis); err != nil {
			logger.Error("gRPC server exited with error", "err", err)
			cancel()
		}
	}()

	select {
	case sig := <-sigCh:
		logger.Info("received signal, shutting down", "signal", sig.String())
	case <-ctx.Done():
	}
	cancel()

	stopped := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		grpcSrv.Stop()
	}

	wg.Wait()
	logger.Info("shutdown complete")
}

func connectStore(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*store.Store, error) {
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		st, err := store.Open(ctx, cfg.ScyllaHosts, cfg.ScyllaKeyspace,
			cfg.ScyllaUsername, cfg.ScyllaPassword, cfg.Workers)
		if err == nil {
			logger.Info("connected to ScyllaDB",
				"hosts", cfg.ScyllaHosts,
				"keyspace", cfg.ScyllaKeyspace,
				"attempt", attempt)
			return st, nil
		}
		lastErr = err
		logger.Warn("ScyllaDB connect failed, retrying",
			"attempt", attempt, "err", err)
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
