package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const zoneColumns = `name, primary_ns, admin_email, serial, refresh, retry, expire, minimum_ttl, created_at, updated_at`

const bumpSerial = `serial = GREATEST(serial + 1, extract(epoch from now())::bigint), updated_at = now()`

func scanZone(row pgx.Row) (*Zone, error) {
	z := &Zone{}
	err := row.Scan(&z.Name, &z.PrimaryNS, &z.AdminEmail, &z.Serial,
		&z.Refresh, &z.Retry, &z.Expire, &z.MinTTL, &z.CreatedAt, &z.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return z, nil
}

func (s *Store) CreateZone(ctx context.Context, z *Zone) (*Zone, error) {
	name := NormalizeZone(z.Name)
	if name == "" {
		return nil, fmt.Errorf("store: zone name required")
	}
	out, err := scanZone(s.pool.QueryRow(ctx,
		`INSERT INTO zones (name, primary_ns, admin_email, serial, refresh, retry, expire, minimum_ttl)
		 VALUES ($1, $2, $3, extract(epoch from now())::bigint, $4, $5, $6, $7)
		 RETURNING `+zoneColumns,
		name, z.PrimaryNS, z.AdminEmail, z.Refresh, z.Retry, z.Expire, z.MinTTL))
	if err != nil {
		if isPgCode(err, "23505") {
			return nil, fmt.Errorf("%w: zone %q already exists", ErrConflict, name)
		}
		return nil, err
	}
	return out, nil
}

func (s *Store) GetZone(ctx context.Context, name string) (*Zone, error) {
	return scanZone(s.pool.QueryRow(ctx,
		`SELECT `+zoneColumns+` FROM zones WHERE name = $1`, NormalizeZone(name)))
}

func (s *Store) UpdateZone(ctx context.Context, z *Zone) (*Zone, error) {
	return scanZone(s.pool.QueryRow(ctx,
		`UPDATE zones SET primary_ns = $2, admin_email = $3, refresh = $4, retry = $5, expire = $6, minimum_ttl = $7, `+bumpSerial+`
		 WHERE name = $1 RETURNING `+zoneColumns,
		NormalizeZone(z.Name), z.PrimaryNS, z.AdminEmail, z.Refresh, z.Retry, z.Expire, z.MinTTL))
}

func (s *Store) DeleteZone(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM zones WHERE name = $1`, NormalizeZone(name))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListZones(ctx context.Context, limit int, after string) ([]*Zone, string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+zoneColumns+` FROM zones WHERE name > $1 ORDER BY name LIMIT $2`, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	zones := make([]*Zone, 0, limit)
	for rows.Next() {
		z, err := scanZone(rows)
		if err != nil {
			return nil, "", err
		}
		zones = append(zones, z)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(zones) > limit {
		zones = zones[:limit]
		next = zones[limit-1].Name
	}
	return zones, next, nil
}

func (s *Store) FindZone(ctx context.Context, qname string) (*Zone, error) {
	qname = NormalizeZone(qname)
	candidates := make([]string, 0, 8)
	for name := qname; name != ""; {
		candidates = append(candidates, name)
		i := strings.IndexByte(name, '.')
		if i < 0 {
			break
		}
		name = name[i+1:]
	}
	if len(candidates) == 0 {
		return nil, ErrNotFound
	}
	return scanZone(s.pool.QueryRow(ctx,
		`SELECT `+zoneColumns+` FROM zones WHERE name = ANY($1) ORDER BY length(name) DESC LIMIT 1`,
		candidates))
}
