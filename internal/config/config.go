package config

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

type Config struct {
	DNSAddr string
	GRPCAddr string
	ScyllaHosts []string
	ScyllaKeyspace string
	ScyllaUsername string
	ScyllaPassword string
	Workers int
	LogLevel string
}

func Load() *Config {
	cfg := &Config{
		DNSAddr:        getenv("DNS_ADDR", ":53"),
		GRPCAddr:       getenv("GRPC_ADDR", ":9053"),
		ScyllaHosts:    splitCSV(getenv("SCYLLA_HOSTS", "127.0.0.1")),
		ScyllaKeyspace: getenv("SCYLLA_KEYSPACE", "sharddns"),
		ScyllaUsername: os.Getenv("SCYLLA_USERNAME"),
		ScyllaPassword: os.Getenv("SCYLLA_PASSWORD"),
		Workers:        getenvInt("WORKERS", 0),
		LogLevel:       getenv("LOG_LEVEL", "info"),
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4 * runtime.NumCPU()
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