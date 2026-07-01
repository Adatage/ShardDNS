-- ShardDNS ScyllaDB Schema
-- Run with: cqlsh -f cql/database.cql

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

CREATE INDEX IF NOT EXISTS records_by_zone ON records (zone);

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