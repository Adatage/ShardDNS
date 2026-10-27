package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DNSAddr         string
	APIAddr         string
	APIToken        string
	DatabaseURL     string
	DBMaxConns      int
	Workers         int
	LogLevel        string
	CacheEnabled    bool
	CacheTTL        time.Duration
	CacheMaxEntries int
	NATSURL         string
	NATSSubject     string
}

func Load() (*Config, error) {
	cfg := &Config{
		DNSAddr:         getenv("DNS_ADDR", ":53"),
		APIAddr:         getenv("API_ADDR", ":9053"),
		APIToken:        os.Getenv("API_TOKEN"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		DBMaxConns:      getenvInt("DB_MAX_CONNS", 0),
		Workers:         getenvInt("WORKERS", 0),
		LogLevel:        getenv("LOG_LEVEL", "info"),
		CacheEnabled:    getenvBool("CACHE_ENABLED", false),
		CacheMaxEntries: getenvInt("CACHE_MAX_ENTRIES", 100000),
		NATSURL:         os.Getenv("NATS_URL"),
		NATSSubject:     getenv("NATS_SUBJECT", "sharddns.changes"),
	}
	ttl, err := time.ParseDuration(getenv("CACHE_TTL", "0"))
	if err != nil || ttl < 0 {
		return nil, fmt.Errorf("config: CACHE_TTL must be a non-negative duration")
	}
	cfg.CacheTTL = ttl

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4 * runtime.NumCPU()
	}
	if cfg.DBMaxConns <= 0 {
		cfg.DBMaxConns = max(8, 2*runtime.NumCPU())
	}
	if cfg.CacheMaxEntries <= 0 {
		return nil, fmt.Errorf("config: CACHE_MAX_ENTRIES must be positive")
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	n, err := strconv.Atoi(os.Getenv(k))
	if err != nil {
		return def
	}
	return n
}

func getenvBool(k string, def bool) bool {
	switch strings.ToLower(os.Getenv(k)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
