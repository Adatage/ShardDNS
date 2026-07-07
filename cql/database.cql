-- ShardDNS ScyllaDB Schema
-- Run with: cqlsh -f cql/database.cql

CREATE KEYSPACE IF NOT EXISTS sharddns
    WITH replication = {
        'class': 'SimpleStrategy',
        'replication_factor': '3'
    }
    AND durable_writes = true;

CREATE ROLE sharddns_user
    WITH PASSWORD = 'SuperSecretPassword'
    AND LOGIN = TRUE;

GRANT ALL PERMISSIONS
    ON KEYSPACE sharddns
    TO sharddns_user;

CREATE TABLE IF NOT EXISTS sharddns.zones (
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

CREATE TABLE IF NOT EXISTS sharddns.records (
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

CREATE INDEX IF NOT EXISTS records_by_zone ON sharddns.records (zone);
