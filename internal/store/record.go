package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

// Record is a single resource record stored in ScyllaDB.
type Record struct {
	Zone      string
	Name      string
	Type      string
	TTL       int32
	RData     string
	CreatedAt time.Time
}

// normalizeName lowercases the name and strips a trailing dot. An empty
// input (or "@") is treated as the zone apex, represented as "@" in
// storage. Callers that want to store the actual apex label should pass
// zone == name.
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

// CreateRecord inserts a resource record. Records are keyed by
// ((zone, name), type, rdata) so multiple rdata values for the same
// (name, type) coexist cleanly.
func (s *Store) CreateRecord(ctx context.Context, r *Record) error {
	r.Zone = normalizeZone(r.Zone)
	r.Name = normalizeName(r.Name)
	r.Type = normalizeType(r.Type)
	if r.Zone == "" || r.Name == "" || r.Type == "" {
		return fmt.Errorf("store: zone, name and type required")
	}
	// Apex records (name == zone) are always stored as "@" so that
	// LookupRecords / GetSOA can find them with a consistent key.
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

// DeleteRecord removes a single resource record identified by
// (zone, name, type, rdata).
func (s *Store) DeleteRecord(ctx context.Context, zone, name, rtype, rdata string) error {
	zone = normalizeZone(zone)
	name = normalizeName(name)
	rtype = normalizeType(rtype)
	return s.Session.Query(s.stmts.deleteRecord, zone, name, rtype, rdata).
		WithContext(ctx).
		Consistency(gocql.LocalQuorum).
		Exec()
}

// ListRecords returns up to pageSize records for a zone.
//
// This uses the secondary index `records_by_zone` because the base table is
// partitioned by (zone, name). It is intended for admin/UI listing, not the
// hot DNS query path.
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

// GetRecords returns every record matching (zone, name, type). Used by the
// admin gRPC API.
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

// GetSOA returns the SOA record for a zone (stored at name == "@"). Returns
// ErrNotFound if the zone has no SOA yet.
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

// LookupRecords is the hot path used by the DNS server.
//
// It performs a single point-select on ((zone, name), type) using LOCAL_ONE.
// If the qname equals the zone apex, the storage form "@" is queried
// automatically.
func (s *Store) LookupRecords(ctx context.Context, zone, name, rtype string) ([]*Record, error) {
	zone = normalizeZone(zone)
	name = normalizeName(name)
	rtype = normalizeType(rtype)

	// If name == zone apex, storage key is "@".
	if name == zone {
		name = "@"
	}

	iter := s.Session.Query(s.stmts.lookupRecords, zone, name, rtype).
		WithContext(ctx).
		Consistency(gocql.LocalOne).
		Iter()
	defer iter.Close()

	// Most lookups return 1–4 rows; sizing avoids growth.
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

// LookupAllTypes returns every record for (zone, name) regardless of type.
// Used by ANY queries. Executed as a partition-restricted scan.
func (s *Store) LookupAllTypes(ctx context.Context, zone, name string) ([]*Record, error) {
	zone = normalizeZone(zone)
	name = normalizeName(name)
	if name == zone {
		name = "@"
	}
	// Ad-hoc query — not in the prepared cache because ANY is uncommon.
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
