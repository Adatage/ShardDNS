# ShardDNS

ShardDNS is a horizontally-scalable **authoritative DNS server** written in Go.
Every DNS query is answered by a single point-select against **ScyllaDB** —
there is no in-process cache layer. This trades a few hundred microseconds of
per-query latency for a fully consistent view of the zone data across every
node in the cluster, and lets you scale reads simply by adding more Scylla
nodes.

- **DNS**: authoritative UDP + TCP on port 53 (RFC 1035 wire format
  parsed/built by hand — no `miekg/dns` dependency).
- **Admin API**: gRPC on port 9053 (defined in
  [`proto/dns_manager.proto`](proto/dns_manager.proto)).
- **Storage**: ScyllaDB, accessed with `gocql` prepared statements at
  `LOCAL_ONE` for reads and `LOCAL_QUORUM` for writes.
- **CLI**: `sharddns-cli` speaks to the gRPC API for zone / record CRUD.

Supported RR types: `A`, `NS`, `CNAME`, `SOA`, `PTR`, `MX`, `TXT`, `AAAA`,
`SRV`, `ANY`.

## Architecture

```
                    ┌──────────────────────────┐
   UDP/TCP :53 ───► │  DNS server              │
                    │  (worker pool + sync.Pool)│  ── LOCAL_ONE ──►┐
                    └──────────────────────────┘                    │
                                                                    ▼
                    ┌──────────────────────────┐            ┌───────────────┐
   gRPC :9053  ───► │  Admin API               │──LOCAL_QUORUM──►│  ScyllaDB    │
                    │  (grpcserver)            │            │  cluster      │
                    └──────────────────────────┘            └───────────────┘
                              ▲
                              │
                    ┌──────────────────────────┐
                    │  sharddns-cli            │
                    └──────────────────────────┘
```

There is **no cache**. Scylla's per-shard row cache plus NVMe throughput are
fast enough that adding another cache tier in the DNS server would only
introduce staleness.

## Prerequisites

- Go **1.24+**
- Docker + Docker Compose (for local ScyllaDB)
- [`buf`](https://buf.build/docs/installation) (for regenerating gRPC stubs)

## Quick start

```bash
# 1. Bring up ScyllaDB + sharddns
docker compose up -d --build

# 2. Load the schema
make schema

# 3. Create a zone via the CLI (from inside the container or locally)
docker compose exec sharddns /sharddns-cli zone create example.com \
    --ns ns1.example.com. --email admin.example.com.

# 4. Add an A record
docker compose exec sharddns /sharddns-cli record add example.com www A 300 192.0.2.10

# 5. Query it
dig @127.0.0.1 www.example.com A
```

## Configuration

All configuration is via environment variables — no config files.

| Variable            | Default        | Description                                    |
| ------------------- | -------------- | ---------------------------------------------- |
| `DNS_ADDR`          | `:53`          | UDP/TCP bind address for the DNS server        |
| `GRPC_ADDR`         | `:9053`        | Bind address for the gRPC admin API            |
| `SCYLLA_HOSTS`      | `127.0.0.1`    | Comma-separated ScyllaDB contact points        |
| `SCYLLA_KEYSPACE`   | `sharddns`     | ScyllaDB keyspace                              |
| `SCYLLA_USERNAME`   | *(empty)*      | Optional username for password auth            |
| `SCYLLA_PASSWORD`   | *(empty)*      | Optional password                              |
| `WORKERS`           | `4 * NumCPU`  | DNS worker pool size                           |
| `DNS_READ_BUF_SIZE` | `4096`         | Size of pooled UDP read buffers                |
| `LOG_LEVEL`         | `info`         | `debug`, `info`, `warn`, `error`               |

See [`.env.example`](.env.example).

## CLI usage

```
sharddns-cli [--addr HOST:PORT] <command> [args]
```

Zone management:

```bash
sharddns-cli zone create example.com \
    [--ns ns1.example.com.] [--email admin.example.com.] \
    [--refresh 3600] [--retry 900] [--expire 604800] [--ttl 300]
sharddns-cli zone list
sharddns-cli zone get example.com
sharddns-cli zone delete example.com
```

Record management:

```bash
sharddns-cli record add example.com www A     300 192.0.2.10
sharddns-cli record add example.com @   NS    300 ns1.example.com.
sharddns-cli record add example.com @   MX    300 "10 mail.example.com."
sharddns-cli record add example.com _svc._tcp SRV 300 "10 20 443 target.example.com."
sharddns-cli record add example.com www TXT   300 "hello world"

sharddns-cli record list  example.com
sharddns-cli record get   example.com www A
sharddns-cli record delete example.com www A   192.0.2.10
```

Records are keyed by `(zone, name, type, rdata)` so you can have multiple
values per name/type. Use `@` as the name to target the zone apex.

## gRPC API

The service is defined in [`proto/dns_manager.proto`](proto/dns_manager.proto):

- `CreateZone`, `GetZone`, `UpdateZone`, `DeleteZone`, `ListZones`
- `CreateRecord`, `DeleteRecord`, `ListRecords`, `GetRecords`

`CreateZone` / `UpdateZone` automatically materialize the zone's SOA record
so DNS negative responses work immediately.

Regenerate the Go stubs with:

```bash
make proto
```

## ScyllaDB schema

Full DDL: [`cql/database.cql`](cql/database.cql).

```cql
CREATE TABLE zones (
    name text PRIMARY KEY, ...
);

CREATE TABLE records (
    zone text, name text, type text, ttl int, rdata text, created_at timestamp,
    PRIMARY KEY ((zone, name), type, rdata)
) WITH CLUSTERING ORDER BY (type ASC, rdata ASC);
```

Rationale:

- Partition on `(zone, name)` so every DNS lookup hits a single Scylla
  partition — no scatter/gather.
- Cluster on `(type, rdata)` so `WHERE zone=? AND name=? AND type=?` is a
  contiguous slice, and multiple rdata values for the same name/type coexist
  as separate rows.
- LeveledCompactionStrategy keeps read amplification low, which matters
  because DNS is a read-mostly workload.

## Performance notes

- **Worker pool** — `WORKERS = 4 * NumCPU` goroutines drain a buffered job
  queue fed by the UDP reader and TCP acceptor.
- **`sync.Pool`** — read buffers are recycled between requests. All pooled
  values are stored as `*[]byte` to avoid the slice-header allocation.
- **Prepared statements** — every hot CQL query is prepared and cached by
  gocql keyed on statement text.
- **`LOCAL_ONE` reads** — the DNS server accepts eventual consistency in
  exchange for the lowest possible latency; the admin API uses
  `LOCAL_QUORUM` on writes.
- **Token-aware host policy** — gocql routes queries directly to the
  replica owning the partition, saving one network hop.
- **No cache** — one consistent source of truth. TTLs are answered by the
  data itself. Scaling reads = add Scylla nodes.

## Development setup

```bash
# 1. Install buf: https://buf.build/docs/installation
# 2. Generate protobuf stubs
make proto

# 3. Build both binaries into ./bin
make build

# 4. Run tests / vet
make test
make lint

# 5. Bring up Scylla in the background and run the server locally
docker compose up -d scylla
make schema
./bin/sharddns
```

## Layout

```
cmd/
  server/       # sharddns entry-point (DNS + gRPC + shutdown)
  cli/          # sharddns-cli
internal/
  config/       # env-var configuration loader
  store/        # ScyllaDB client, zone/record CRUD, hot-path LookupRecords
  dns/          # RFC 1035 message codec + UDP/TCP server + handler
  grpcserver/   # DNSManager gRPC service implementation
proto/          # .proto sources
api/            # generated protobuf/gRPC code (produced by `make proto`)
cql/            # ScyllaDB schema
```

## License

See [LICENSE](LICENSE).
