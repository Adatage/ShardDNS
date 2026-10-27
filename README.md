<div align="center">

# ⚡ ShardDNS

**A stateless, high-throughput authoritative DNS server backed by PostgreSQL, with a ConnectRPC control plane, an in-memory cache and optional NATS cache invalidation.**

[![CI](https://github.com/Adatage/ShardDNS/actions/workflows/ci.yml/badge.svg)](https://github.com/Adatage/ShardDNS/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![PostgreSQL](https://img.shields.io/badge/storage-PostgreSQL-4169E1?logo=postgresql&logoColor=white)](https://www.postgresql.org/)
[![ConnectRPC](https://img.shields.io/badge/control--plane-ConnectRPC-244c5a)](https://connectrpc.com/)
[![Docker](https://img.shields.io/badge/ghcr.io-sharddns-2496ED?logo=docker&logoColor=white)](https://github.com/Adatage/ShardDNS/pkgs/container/sharddns)

</div>

---

## Overview

ShardDNS is an authoritative DNS server written in Go. PostgreSQL is the only source of truth, so every `sharddnsd` instance is stateless and horizontally scalable. Zones and records are managed at runtime through a **ConnectRPC** API (Connect, gRPC and gRPC-Web clients all work), with no config files and no restarts.

### Features

- **UDP and TCP** with EDNS0, correct TC-bit truncation, case-preserving answers (0x20 safe) and bounded TCP connections
- **In-memory cache** (opt-in) with negative caching, request coalescing and bounded memory
- **Multi-instance cache invalidation** over NATS (opt-in), carried as protobuf change events
- **Record types:** A, AAAA, NS, CNAME, DNAME, PTR, MX, TXT, SRV, CAA, NAPTR, SSHFP, TLSA, DS, DNSKEY, HINFO; SOA is generated from the zone
- **Correct negative answers:** NXDOMAIN vs NODATA (including empty non-terminals), SOA in the authority section with RFC 2308 TTLs
- **Wildcards** (`*.example.com`), **CNAME chains** (also across zones served by the same instance), **DNAME** synthesis, **delegations** with glue, and additional-section processing for MX, NS and SRV
- **Validated writes:** record data is parsed on create, CNAME exclusivity is enforced, RRset TTLs stay consistent, and the zone serial advances on every change
- **Operations:** schema created automatically, `/healthz` and `/readyz`, optional bearer-token auth, graceful shutdown, JSON logs, gRPC reflection

---

## Architecture

```
                     ┌────────────────────────────┐
   DNS clients       │         sharddnsd          │      sharddns CLI / any
 ──────────────────► │  UDP/TCP :53   API :9053   │ ◄──  Connect, gRPC client
                     │  ┌────────────────────┐    │
                     │  │ in-memory cache    │    │
                     │  └─────────┬──────────┘    │
                     └───────┬────┴───────┬───────┘
                             │            │ change events (optional)
                      ┌──────▼─────┐   ┌──▼───┐
                      │ PostgreSQL │   │ NATS │──► other sharddnsd instances
                      └────────────┘   └──────┘
```

On a write, the instance that handled it invalidates its own cache and publishes a `ChangeEvent`. Every other instance subscribed to the subject invalidates the affected names. Cache entries do not expire on their own; they are dropped when invalidated or evicted at `CACHE_MAX_ENTRIES`. Without NATS, other instances only notice changes if you set `CACHE_TTL`.

---

## Quick Start

### Docker Compose

```bash
docker compose up -d --build
```

This starts PostgreSQL, NATS and `sharddnsd` (cache and NATS invalidation enabled). The DNS server listens on `53/udp+tcp` and the API on `9053`. Override host ports with `DNS_PORT` and `API_PORT`, and set `API_TOKEN` to require authentication.

```bash
docker compose exec sharddnsd sharddns zone create example.com
docker compose exec sharddnsd sharddns record add example.com www A 300 93.184.216.34
dig @127.0.0.1 www.example.com A
```

### Build from source

```bash
make build        # bin/sharddnsd, bin/sharddns, bin/dnsbench
export DATABASE_URL=postgres://sharddns:sharddns@127.0.0.1:5432/sharddns?sslmode=disable
sudo setcap cap_net_bind_service=+ep bin/sharddnsd    # or set DNS_ADDR=:5353
./bin/sharddnsd
```

### Container image

```bash
docker pull ghcr.io/adatage/sharddns:latest
```

| Tag      | When published         |
|----------|------------------------|
| `latest` | every push to `main`   |
| `2.0.0`  | on git tag `v2.0.0`    |
| `2.0`    | on git tag `v2.0.x`    |

The image contains `sharddnsd` (entrypoint) and the `sharddns` CLI.

---

## Configuration

All configuration is through environment variables (see [`.env.example`](.env.example)).

| Variable            | Default            | Description                                                              |
|---------------------|--------------------|--------------------------------------------------------------------------|
| `DATABASE_URL`      | *(required)*       | PostgreSQL connection URL                                                |
| `DB_MAX_CONNS`      | `max(8, 2×CPUs)`   | Connection pool size                                                     |
| `DNS_ADDR`          | `:53`              | DNS listen address (UDP and TCP)                                         |
| `API_ADDR`          | `:9053`            | ConnectRPC API listen address                                            |
| `API_TOKEN`         | *(empty)*          | If set, API calls must send `Authorization: Bearer <token>`              |
| `WORKERS`           | `0`                | DNS worker goroutines (`0` = 4 × CPUs)                                   |
| `LOG_LEVEL`         | `info`             | `debug`, `info`, `warn`, `error`                                         |
| `CACHE_ENABLED`     | `false`            | Enable the in-memory cache                                               |
| `CACHE_TTL`         | `0`                | Maximum age of a cache entry (`0` = no expiry, rely on invalidation)     |
| `CACHE_MAX_ENTRIES` | `100000`           | Upper bound on cached names                                              |
| `NATS_URL`          | *(empty)*          | Enable change-event publishing and cache invalidation over NATS          |
| `NATS_SUBJECT`      | `sharddns.changes` | Subject used for change events                                           |

Run several instances against one database, enable the cache and point them at the same NATS server to scale reads horizontally.

---

## API

The service is defined in [`proto/sharddns/v1/sharddns.proto`](proto/sharddns/v1/sharddns.proto) and served at `/sharddns.v1.DNSManager/`. It speaks the Connect, gRPC and gRPC-Web protocols over HTTP/1.1 and cleartext HTTP/2, and supports server reflection.

| RPC            | Description                                                      |
|----------------|------------------------------------------------------------------|
| `CreateZone`   | Create a zone (SOA defaults are filled in)                       |
| `GetZone`      | Fetch a zone                                                     |
| `UpdateZone`   | Change SOA parameters (zero values keep the current setting)     |
| `DeleteZone`   | Delete a zone and all of its records                             |
| `ListZones`    | Paginated list of zones                                          |
| `CreateRecord` | Add a record (idempotent for identical data, validated)          |
| `DeleteRecord` | Remove one record by zone, name, type and rdata                  |
| `ListRecords`  | Paginated list of the records in a zone                          |
| `GetRecords`   | Records for a name and type                                      |

Record names may be relative (`www`), absolute (`www.example.com`) or `@` for the apex. Names inside rdata (NS, CNAME, MX, SRV ...) are fully qualified.

| Type    | rdata format                                      |
|---------|---------------------------------------------------|
| `A`, `AAAA` | IP address                                    |
| `NS`, `CNAME`, `DNAME`, `PTR` | domain name                 |
| `MX`    | `10 mail.example.com.`                            |
| `TXT`   | `v=spf1 -all` or `"part one" "part two"`          |
| `SRV`   | `10 5 5060 sip.example.com.`                      |
| `CAA`   | `0 issue "letsencrypt.org"`                       |
| `NAPTR` | `100 10 "u" "E2U+sip" "!^.*$!sip:x@example.com!" .` |
| `SSHFP` | `1 2 <hex>`                                       |
| `TLSA`  | `3 1 1 <hex>`                                     |
| `DS`    | `12345 13 2 <hex>`                                |
| `DNSKEY`| `257 3 13 <base64>`                               |
| `HINFO` | `"amd64" "linux"`                                 |

### CLI

```bash
export SHARDDNS_ADDR=http://localhost:9053   # default
export SHARDDNS_TOKEN=...                    # if API_TOKEN is set

sharddns zone create example.com --ns ns1.example.com. --email hostmaster.example.com.
sharddns zone list
sharddns record add example.com www A 300 93.184.216.34
sharddns record add example.com @ MX 300 "10 mail.example.com."
sharddns record list example.com
sharddns record delete example.com www A 93.184.216.34
```

### curl and grpcurl

```bash
curl -s -H 'Content-Type: application/json' -d '{"name":"example.com"}' \
  http://localhost:9053/sharddns.v1.DNSManager/CreateZone

grpcurl -plaintext -d '{"zone":"example.com","name":"www","type":"A","ttl":300,"rdata":"93.184.216.34"}' \
  localhost:9053 sharddns.v1.DNSManager/CreateRecord
```

### Go client

```bash
go get github.com/Adatage/ShardDNS
```

```go
import (
    sharddnsv1 "github.com/Adatage/ShardDNS/api/sharddns/v1"
    "github.com/Adatage/ShardDNS/api/sharddns/v1/sharddnsv1connect"
)

client := sharddnsv1connect.NewDNSManagerClient(http.DefaultClient, "http://localhost:9053")
resp, err := client.GetZone(ctx, connect.NewRequest(&sharddnsv1.GetZoneRequest{Name: "example.com"}))
```

---

## Database

The schema is embedded in the binary ([`internal/store/schema.sql`](internal/store/schema.sql)). On startup any missing tables and indexes are created, under an advisory lock so several instances can start at once.

| Table     | Key                                   | Notes                                                    |
|-----------|---------------------------------------|----------------------------------------------------------|
| `zones`   | `name`                                | SOA parameters and serial                                |
| `records` | `id`, unique `(zone, name, type, md5(rdata))` | cascades on zone delete; reversed-name index for empty-non-terminal lookups |

---

## Performance

`bench/` (built as `bin/dnsbench`) seeds a zone and drives UDP or TCP load.

```bash
bin/dnsbench -seed -api http://127.0.0.1:9053 -names 10000
bin/dnsbench -server 127.0.0.1:53 -duration 15s -c 64
```

Measured on one 12-thread machine with PostgreSQL, the load generator and `sharddnsd` all sharing the same CPUs (10,000 names, 5% NXDOMAIN, 64 concurrent clients, UDP):

| Mode                   | Throughput    | p50 latency | p99 latency |
|------------------------|---------------|-------------|-------------|
| PostgreSQL, no cache   | ~50,000 qps   | ~1.15 ms    | ~3.3 ms     |
| In-memory cache        | ~100,000-118,000 qps | ~0.15-0.19 ms | ~4.5 ms |

Absolute numbers depend heavily on hardware and on how far the database is from the server; the ratio is what to look at.

---

## Development

```bash
make proto             # regenerate api/ with buf (remote plugins, pinned versions)
make build             # build all binaries into bin/
make test              # unit tests (integration tests skip without a database)
make test-integration TEST_DATABASE_URL=postgres://...   # includes the PostgreSQL tests
make lint              # go vet
make docker-up         # PostgreSQL + NATS + sharddnsd
```

> The integration tests truncate the `zones` and `records` tables of the database they run against. Use a throwaway database.

> The generated code in `api/` is committed. After editing the `.proto` file run `make proto` and commit the result; CI fails if it drifts.

---

## License

Distributed under the [Apache 2.0 License](LICENSE).
