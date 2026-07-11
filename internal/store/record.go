package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

type Record struct {
	Zone      string
	Name      string
	Type      string
	TTL       int32
	RData     string
	CreatedAt time.Time
}

func normalizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return "@"
	}
	return name
}

func normalizeType(t string) string {
	return strings.ToUpper(strings.TrimSpace(t))
}

func (s *Store) CreateRecord(ctx context.Context, r *Record) error {
	r.Zone = normalizeZone(r.Zone)
	r.Name = normalizeName(r.Name)
	r.Type = normalizeType(r.Type)
	if r.Zone == "" || r.Name == "" || r.Type == "" {
		return fmt.Errorf("store: zone, name and type required")
	}

	if r.Name == r.Zone {
		r.Name = "@"
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	return s.Session.Query(s.stmts.insertRecord,
		r.Zone, r.Name, r.Type, r.TTL, r.RData, r.CreatedAt,
	).WithContext(ctx).Consistency(gocql.LocalQuorum).Exec()
}

func (s *Store) DeleteRecord(ctx context.Context, zone, name, rtype, rdata string) error {
	zone = normalizeZone(zone)
	name = normalizeName(name)
	rtype = normalizeType(rtype)
	return s.Session.Query(s.stmts.deleteRecord, zone, name, rtype, rdata).
		WithContext(ctx).
		Consistency(gocql.LocalQuorum).
		Exec()
}

func (s *Store) ListRecords(ctx context.Context, zone string, pageSize int) ([]*Record, error) {
	zone = normalizeZone(zone)
	if pageSize <= 0 {
		pageSize = 100
	}
	iter := s.Session.Query(s.stmts.listRecordsByZone, zone).
		WithContext(ctx).
		Consistency(gocql.LocalOne).
		PageSize(pageSize).
		Iter()
	defer iter.Close()

	records := make([]*Record, 0, pageSize)
	for {
		r := &Record{}
		if !iter.Scan(&r.Zone, &r.Name, &r.Type, &r.TTL, &r.RData, &r.CreatedAt) {
			break
		}
		records = append(records, r)
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) GetRecords(ctx context.Context, zone, name, rtype string) ([]*Record, error) {
	zone = normalizeZone(zone)
	name = normalizeName(name)
	rtype = normalizeType(rtype)
	iter := s.Session.Query(s.stmts.getRecords, zone, name, rtype).
		WithContext(ctx).
		Consistency(gocql.LocalOne).
		Iter()
	defer iter.Close()

	records := make([]*Record, 0, 4)
	for {
		r := &Record{}
		if !iter.Scan(&r.Zone, &r.Name, &r.Type, &r.TTL, &r.RData, &r.CreatedAt) {
			break
		}
		records = append(records, r)
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) GetSOA(ctx context.Context, zone string) (*Record, error) {
	zone = normalizeZone(zone)
	iter := s.Session.Query(s.stmts.getSOA, zone, "@").
		WithContext(ctx).
		Consistency(gocql.LocalOne).
		Iter()
	defer iter.Close()

	r := &Record{}
	if iter.Scan(&r.Zone, &r.Name, &r.Type, &r.TTL, &r.RData, &r.CreatedAt) {
		if err := iter.Close(); err != nil {
			return nil, err
		}
		return r, nil
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return nil, ErrNotFound
}

func (s *Store) LookupRecords(ctx context.Context, zone, name, rtype string) ([]*Record, error) {
	zone = normalizeZone(zone)
	name = normalizeName(name)
	rtype = normalizeType(rtype)

	if name == zone {
		name = "@"
	}

	iter := s.Session.Query(s.stmts.lookupRecords, zone, name, rtype).
		WithContext(ctx).
		Consistency(gocql.LocalOne).
		Iter()
	defer iter.Close()

	records := make([]*Record, 0, 4)
	for {
		r := &Record{}
		if !iter.Scan(&r.Zone, &r.Name, &r.Type, &r.TTL, &r.RData, &r.CreatedAt) {
			break
		}
		records = append(records, r)
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) NameExists(ctx context.Context, zone, name string) (bool, error) {
	zone = normalizeZone(zone)
	name = normalizeName(name)
	if name == zone {
		name = "@"
	}
	var typ string
	err := s.Session.Query(
		`SELECT type FROM records WHERE zone = ? AND name = ? LIMIT 1`,
		zone, name,
	).WithContext(ctx).Consistency(gocql.LocalOne).Scan(&typ)
	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Store) LookupAllTypes(ctx context.Context, zone, name string) ([]*Record, error) {
	zone = normalizeZone(zone)
	name = normalizeName(name)
	if name == zone {
		name = "@"
	}

	iter := s.Session.Query(
		`SELECT zone, name, type, ttl, rdata, created_at FROM records WHERE zone = ? AND name = ?`,
		zone, name,
	).WithContext(ctx).Consistency(gocql.LocalOne).Iter()
	defer iter.Close()

	records := make([]*Record, 0, 8)
	for {
		r := &Record{}
		if !iter.Scan(&r.Zone, &r.Name, &r.Type, &r.TTL, &r.RData, &r.CreatedAt) {
			break
		}
		records = append(records, r)
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return records, nil
}