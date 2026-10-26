package store

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

const migrationLockID int64 = 0x5348415244444e53

var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

type Zone struct {
	Name       string
	PrimaryNS  string
	AdminEmail string
	Serial     int64
	Refresh    int32
	Retry      int32
	Expire     int32
	MinTTL     int32
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Record struct {
	ID        int64
	Zone      string
	Name      string
	Type      string
	TTL       int32
	RData     string
	CreatedAt time.Time
	Wire      []byte
	WireErr   error
}

type NameSet struct {
	Records     []*Record
	HasChildren bool
}

func (n *NameSet) Exists() bool {
	return len(n.Records) > 0 || n.HasChildren
}

type Encoder func(rtype, rdata string) ([]byte, error)

type Resolver interface {
	FindZone(ctx context.Context, qname string) (*Zone, error)
	LookupName(ctx context.Context, zone, name string) (*NameSet, error)
}

type Store struct {
	pool   *pgxpool.Pool
	encode Encoder
}

type Options struct {
	MaxConns int32
	Encoder  Encoder
}

func Open(ctx context.Context, url string, opts Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: parse database url: %w", err)
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool, encode: opts.Encoder}, nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("store: migration lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID)

	if _, err := conn.Exec(ctx, schema, pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("store: apply schema: %w", err)
	}
	return nil
}

func isPgCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func NormalizeZone(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

func NormalizeName(zone, name string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" || name == "@" || name == zone {
		return zone
	}
	if strings.HasSuffix(name, "."+zone) {
		return name
	}
	return name + "." + zone
}

func NormalizeType(t string) string {
	return strings.ToUpper(strings.TrimSpace(t))
}

func reverseName(name string) string {
	labels := strings.Split(name, ".")
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return strings.Join(labels, ".")
}
