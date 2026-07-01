-- ShardDNS ScyllaDB Schema
-- Run with: cqlsh -f cql/database.cql
--
-- For production clusters increase replication_factor to match your DC node count.
-- Tablets are disabled so the keyspace supports LWT and CDC if needed.

CREATE KEYSPACE IF NOT EXISTS sharddns
    WITH replication = {
        'class': 'SimpleStrategy',
        'replication_factor': '1'
    }
    AND durable_writes = true;

USE sharddns;

CREATE TABLE IF NOT EXISTS zones (
    name        text PRIMARY KEY,
    primary_ns  text,
    admin_email text,
    serial      bigint,
    refresh     int,
    retry       int,
    expire      int,
    minimum_ttl int,
    created_at  timestamp,
    updated_at  timestamp
);

-- records partitioned by (zone, name) for O(1) DNS lookups
-- clustered by (type, rdata) to allow efficient type-filtered queries
CREATE TABLE IF NOT EXISTS records (
    zone       text,
    name       text,
    type       text,
    ttl        int,
    rdata      text,
    created_at timestamp,
    PRIMARY KEY ((zone, name), type, rdata)
) WITH CLUSTERING ORDER BY (type ASC, rdata ASC)
  AND compaction = {
      'class': 'LeveledCompactionStrategy',
      'sstable_size_in_mb': 160
  }
  AND compression = {
      'sstable_compression': 'LZ4Compressor'
  };

-- Index to list all records for a zone efficiently
CREATE INDEX IF NOT EXISTS records_by_zone ON records (zone);

-- DNSSEC signing keys, one row per (zone, flags, key_tag).
CREATE TABLE IF NOT EXISTS dnssec_keys (
    zone        text,
    flags       int,
    key_tag     int,
    algorithm   int,
    public_key  blob,
    private_key blob,
    active      boolean,
    created_at  timestamp,
    expires_at  timestamp,
    PRIMARY KEY ((zone), flags, key_tag)
);
