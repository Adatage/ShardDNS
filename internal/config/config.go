// Package config loads runtime configuration exclusively from environment
// variables. There are no config files and no third-party libraries used —
// only the standard library.
package config

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Config holds all runtime configuration for ShardDNS.
type Config struct {
	// DNSAddr is the UDP/TCP address the authoritative DNS server binds to.
	DNSAddr string
	// GRPCAddr is the address the gRPC admin API binds to.
	GRPCAddr string
	// ScyllaHosts is the list of ScyllaDB contact points.
	ScyllaHosts []string
	// ScyllaKeyspace is the ScyllaDB keyspace containing zones/records.
	ScyllaKeyspace string
	// ScyllaUsername for authentication (empty disables auth).
	ScyllaUsername string
	// ScyllaPassword for authentication.
	ScyllaPassword string
	// Workers is the size of the DNS worker pool.
	Workers int
	// DNSReadBufSize is the size of pooled UDP read buffers.
	DNSReadBufSize int
	// LogLevel: debug|info|warn|error.
	LogLevel string
}

// Load reads configuration from the process environment.
func Load() *Config {
	cfg := &Config{
		DNSAddr:        getenv("DNS_ADDR", ":53"),
		GRPCAddr:       getenv("GRPC_ADDR", ":9053"),
		ScyllaHosts:    splitCSV(getenv("SCYLLA_HOSTS", "127.0.0.1")),
		ScyllaKeyspace: getenv("SCYLLA_KEYSPACE", "sharddns"),
		ScyllaUsername: os.Getenv("SCYLLA_USERNAME"),
		ScyllaPassword: os.Getenv("SCYLLA_PASSWORD"),
		Workers:        getenvInt("WORKERS", 0),
		DNSReadBufSize: getenvInt("DNS_READ_BUF_SIZE", 4096),
		LogLevel:       getenv("LOG_LEVEL", "info"),
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4 * runtime.NumCPU()
	}
	if cfg.DNSReadBufSize <= 0 {
		cfg.DNSReadBufSize = 4096
	}
	return cfg
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
