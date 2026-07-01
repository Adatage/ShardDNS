package store

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

type stmts struct {
	insertZone string
	selectZone string
	updateZone string
	deleteZone string
	listZones  string

	insertRecord      string
	deleteRecord      string
	deleteZoneRecords string
	listRecordsByZone string
	getRecords        string
	getSOA            string
	lookupRecords     string
}

type Store struct {
	Session *gocql.Session
	stmts   stmts
}

func Open(ctx context.Context, hosts []string, keyspace, username, password string, workers int) (*Store, error) {
	if len(hosts) == 0 {
		return nil, fmt.Errorf("store: no ScyllaDB hosts provided")
	}
	cluster := gocql.NewCluster(hosts...)
	cluster.Keyspace = keyspace
	cluster.Consistency = gocql.LocalOne
	cluster.SerialConsistency = gocql.LocalSerial
	cluster.Timeout = 3 * time.Second
	cluster.ConnectTimeout = 5 * time.Second
	cluster.ProtoVersion = 4
	cluster.PoolConfig.HostSelectionPolicy = gocql.TokenAwareHostPolicy(
		gocql.RoundRobinHostPolicy(),
	)
	cluster.RetryPolicy = &gocql.ExponentialBackoffRetryPolicy{
		NumRetries: 3,
		Min:        20 * time.Millisecond,
		Max:        500 * time.Millisecond,
	}

	numConns := workers / 2
	if numConns < 4 {
		numConns = 4
	}
	cluster.NumConns = numConns

	if username != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: username,
			Password: password,
		}
	}

	sess, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("store: create session: %w", err)
	}

	if err := sess.Query("SELECT now() FROM system.local").WithContext(ctx).Exec(); err != nil {
		sess.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	s := &Store{
		Session: sess,
		stmts: stmts{
			insertZone: `INSERT INTO zones
				(name, primary_ns, admin_email, serial, refresh, retry, expire, minimum_ttl, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			selectZone: `SELECT name, primary_ns, admin_email, serial, refresh, retry, expire, minimum_ttl, created_at, updated_at
				FROM zones WHERE name = ?`,
			updateZone: `UPDATE zones
				SET primary_ns = ?, admin_email = ?, serial = ?, refresh = ?, retry = ?, expire = ?, minimum_ttl = ?, updated_at = ?
				WHERE name = ?`,
			deleteZone: `DELETE FROM zones WHERE name = ?`,
			listZones: `SELECT name, primary_ns, admin_email, serial, refresh, retry, expire, minimum_ttl, created_at, updated_at
				FROM zones`,

			insertRecord: `INSERT INTO records
				(zone, name, type, ttl, rdata, created_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
			deleteRecord:      `DELETE FROM records WHERE zone = ? AND name = ? AND type = ? AND rdata = ?`,
			deleteZoneRecords: `DELETE FROM records WHERE zone = ? AND name = ?`,
			listRecordsByZone: `SELECT zone, name, type, ttl, rdata, created_at FROM records WHERE zone = ?`,
			getRecords: `SELECT zone, name, type, ttl, rdata, created_at
				FROM records WHERE zone = ? AND name = ? AND type = ?`,
			getSOA: `SELECT zone, name, type, ttl, rdata, created_at
				FROM records WHERE zone = ? AND name = ? AND type = 'SOA'`,
			lookupRecords: `SELECT zone, name, type, ttl, rdata, created_at
				FROM records WHERE zone = ? AND name = ? AND type = ?`,
		},
	}
	return s, nil
}

func (s *Store) Close() {
	if s != nil && s.Session != nil {
		s.Session.Close()
	}
}
