// Package store is the ScyllaDB-backed persistence layer for ShardDNS.
//
// The design goals are:
//   - Every DNS query hits ScyllaDB directly (no in-process cache layer). We
//     rely on ScyllaDB's per-shard row cache and NVMe throughput instead of
//     duplicating state in the DNS server.
//   - All hot statements are prepared once at startup so the driver reuses
//     the same query plan on every invocation.
//   - Reads use LOCAL_ONE for lowest latency; writes use LOCAL_QUORUM for
//     durability.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

// stmts holds the CQL text for every prepared statement used by the store.
// gocql caches prepared statements per-session keyed by the query text, so
// re-executing the same string amounts to a lookup in an in-memory map.
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

// Store is the ScyllaDB-backed persistence layer.
type Store struct {
	Session *gocql.Session
	stmts   stmts
}

// Open connects to ScyllaDB and returns a ready-to-use Store.
//
// The connection is tuned for high throughput:
//   - NumConns = max(workers/2, 4) per host so each shard has multiple
//     in-flight TCP connections.
//   - Retry policy uses exponential backoff with a hard cap of 3 attempts.
//   - Reads default to LOCAL_ONE, writes to LOCAL_QUORUM. Individual queries
//     override consistency as needed.
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

	// Sanity check that the keyspace is reachable.
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

// Close releases the underlying gocql session.
func (s *Store) Close() {
	if s != nil && s.Session != nil {
		s.Session.Close()
	}
}
