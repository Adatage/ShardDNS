package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

var ErrNotFound = errors.New("store: not found")

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

func normalizeZone(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".")
	return name
}

func (s *Store) CreateZone(ctx context.Context, z *Zone) error {
	z.Name = normalizeZone(z.Name)
	if z.Name == "" {
		return fmt.Errorf("store: zone name required")
	}
	now := time.Now().UTC()
	if z.CreatedAt.IsZero() {
		z.CreatedAt = now
	}
	z.UpdatedAt = now
	if z.Serial == 0 {
		z.Serial = now.Unix()
	}
	return s.Session.Query(s.stmts.insertZone,
		z.Name, z.PrimaryNS, z.AdminEmail, z.Serial,
		z.Refresh, z.Retry, z.Expire, z.MinTTL,
		z.CreatedAt, z.UpdatedAt,
	).WithContext(ctx).Consistency(gocql.LocalQuorum).Exec()
}

func (s *Store) GetZone(ctx context.Context, name string) (*Zone, error) {
	name = normalizeZone(name)
	z := &Zone{}
	err := s.Session.Query(s.stmts.selectZone, name).
		WithContext(ctx).
		Consistency(gocql.LocalOne).
		Scan(&z.Name, &z.PrimaryNS, &z.AdminEmail, &z.Serial,
			&z.Refresh, &z.Retry, &z.Expire, &z.MinTTL,
			&z.CreatedAt, &z.UpdatedAt)
	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return z, nil
}

func (s *Store) UpdateZone(ctx context.Context, z *Zone) error {
	z.Name = normalizeZone(z.Name)
	if z.Name == "" {
		return fmt.Errorf("store: zone name required")
	}
	now := time.Now().UTC()
	z.UpdatedAt = now
	if z.Serial == 0 {
		z.Serial = now.Unix()
	}
	return s.Session.Query(s.stmts.updateZone,
		z.PrimaryNS, z.AdminEmail, z.Serial,
		z.Refresh, z.Retry, z.Expire, z.MinTTL,
		z.UpdatedAt, z.Name,
	).WithContext(ctx).Consistency(gocql.LocalQuorum).Exec()
}

func (s *Store) DeleteZone(ctx context.Context, name string) error {
	name = normalizeZone(name)
	return s.Session.Query(s.stmts.deleteZone, name).
		WithContext(ctx).
		Consistency(gocql.LocalQuorum).
		Exec()
}

func (s *Store) ListZones(ctx context.Context, pageSize int, pageState []byte) ([]*Zone, []byte, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	q := s.Session.Query(s.stmts.listZones).
		WithContext(ctx).
		Consistency(gocql.LocalOne).
		PageSize(pageSize)
	if len(pageState) > 0 {
		q = q.PageState(pageState)
	}
	iter := q.Iter()
	defer iter.Close()

	zones := make([]*Zone, 0, pageSize)
	for {
		z := &Zone{}
		if !iter.Scan(&z.Name, &z.PrimaryNS, &z.AdminEmail, &z.Serial,
			&z.Refresh, &z.Retry, &z.Expire, &z.MinTTL,
			&z.CreatedAt, &z.UpdatedAt) {
			break
		}
		zones = append(zones, z)
	}
	next := iter.PageState()

	var nextCopy []byte
	if len(next) > 0 {
		nextCopy = append(nextCopy, next...)
	}
	if err := iter.Close(); err != nil {
		return nil, nil, err
	}
	return zones, nextCopy, nil
}