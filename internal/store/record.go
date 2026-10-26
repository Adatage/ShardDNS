package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const recordColumns = `id, zone, name, type, ttl, rdata, created_at`

func (s *Store) scanRecord(row pgx.Row) (*Record, error) {
	r := &Record{}
	if err := row.Scan(&r.ID, &r.Zone, &r.Name, &r.Type, &r.TTL, &r.RData, &r.CreatedAt); err != nil {
		return nil, err
	}
	if s.encode != nil {
		r.Wire, r.WireErr = s.encode(r.Type, r.RData)
	}
	return r, nil
}

func (s *Store) collectRecords(rows pgx.Rows) ([]*Record, error) {
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r, err := s.scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateRecord(ctx context.Context, r *Record) (*Record, error) {
	zone := NormalizeZone(r.Zone)
	name := NormalizeName(zone, r.Name)
	rtype := NormalizeType(r.Type)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `UPDATE zones SET `+bumpSerial+` WHERE name = $1`, zone)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}

	if rtype == "CNAME" {
		if name == zone {
			return nil, fmt.Errorf("%w: CNAME not allowed at zone apex", ErrConflict)
		}
		var other bool
		err = tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM records WHERE zone = $1 AND name = $2 AND (type <> 'CNAME' OR md5(rdata) <> md5($3)))`,
			zone, name, r.RData).Scan(&other)
		if err != nil {
			return nil, err
		}
		if other {
			return nil, fmt.Errorf("%w: CNAME cannot coexist with other records at %s", ErrConflict, name)
		}
	} else {
		var cname bool
		err = tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM records WHERE zone = $1 AND name = $2 AND type = 'CNAME')`,
			zone, name).Scan(&cname)
		if err != nil {
			return nil, err
		}
		if cname {
			return nil, fmt.Errorf("%w: %s has a CNAME record", ErrConflict, name)
		}
	}

	out, err := s.scanRecord(tx.QueryRow(ctx,
		`INSERT INTO records (zone, name, name_rev, type, ttl, rdata)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (zone, name, type, md5(rdata)) DO UPDATE SET ttl = EXCLUDED.ttl
		 RETURNING `+recordColumns,
		zone, name, reverseName(name), rtype, r.TTL, r.RData))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE records SET ttl = $4 WHERE zone = $1 AND name = $2 AND type = $3 AND ttl <> $4`,
		zone, name, rtype, r.TTL); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DeleteRecord(ctx context.Context, zone, name, rtype, rdata string) error {
	zone = NormalizeZone(zone)
	name = NormalizeName(zone, name)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`DELETE FROM records WHERE zone = $1 AND name = $2 AND type = $3 AND md5(rdata) = md5($4)`,
		zone, name, NormalizeType(rtype), rdata)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE zones SET `+bumpSerial+` WHERE name = $1`, zone); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListRecords(ctx context.Context, zone string, limit int, after string) ([]*Record, string, error) {
	var afterID int64
	if after != "" {
		id, err := strconv.ParseInt(after, 10, 64)
		if err != nil || id < 0 {
			return nil, "", fmt.Errorf("store: invalid page token")
		}
		afterID = id
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+recordColumns+` FROM records WHERE zone = $1 AND id > $2 ORDER BY id LIMIT $3`,
		NormalizeZone(zone), afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	records, err := s.collectRecords(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(records) > limit {
		records = records[:limit]
		next = strconv.FormatInt(records[limit-1].ID, 10)
	}
	return records, next, nil
}

func (s *Store) GetRecords(ctx context.Context, zone, name, rtype string) ([]*Record, error) {
	zone = NormalizeZone(zone)
	rows, err := s.pool.Query(ctx,
		`SELECT `+recordColumns+` FROM records WHERE zone = $1 AND name = $2 AND type = $3 ORDER BY id`,
		zone, NormalizeName(zone, name), NormalizeType(rtype))
	if err != nil {
		return nil, err
	}
	return s.collectRecords(rows)
}

func (s *Store) LookupName(ctx context.Context, zone, name string) (*NameSet, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+recordColumns+` FROM records WHERE zone = $1 AND name = $2`, zone, name)
	if err != nil {
		return nil, err
	}
	records, err := s.collectRecords(rows)
	if err != nil {
		return nil, err
	}
	set := &NameSet{Records: records}
	if len(records) > 0 {
		return set, nil
	}
	prefix := reverseName(name) + "."
	err = s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM records WHERE zone = $1 AND name_rev >= $2 AND name_rev < $3)`,
		zone, prefix, prefix[:len(prefix)-1]+"/").Scan(&set.HasChildren)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return set, nil
}
