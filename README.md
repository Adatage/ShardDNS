<div align="center">

# ⚡ ShardDNS

**A stateless, cloud-native DNS server built for high throughput — backed by ScyllaDB and controlled via gRPC.**

[![CI](https://github.com/Adatage/ShardDNS/actions/workflows/ci.yml/badge.svg)](https://github.com/Adatage/ShardDNS/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![ScyllaDB](https://img.shields.io/badge/storage-ScyllaDB-53CFCD?logo=apache-cassandra&logoColor=white)](https://www.scylladb.com/)
[![gRPC](https://img.shields.io/badge/control--plane-gRPC-244c5a?logo=grpc&logoColor=white)](https://grpc.io/)
[![Docker](https://img.shields.io/badge/ghcr.io-sharddns-2496ED?logo=docker&logoColor=white)](https://github.com/Adatage/ShardDNS/pkgs/container/sharddns)

</div>

---

## Overview

ShardDNS is a high-performance authoritative DNS server written in Go. It uses **ScyllaDB** as its only storage backend, making every server instance completely stateless and horizontally scalable. Zones and records are managed at runtime through a **gRPC API** — no config files, no restarts.

### Key Features

- 🚀 **High Throughput** — concurrent worker pool with buffer pooling for zero-allocation hot paths
- 🌐 **UDP & TCP** — full RFC-compliant DNS over both transports with TC-bit truncation
- 🗄️ **ScyllaDB Backend** — LZ4-compressed records table with Leveled Compaction for low-latency lookups
- 🔌 **gRPC Control Plane** — manage zones and records dynamically without restarts
- ☁️ **Cloud Native** — stateless design, Docker-ready, environment-variable configuration
- 🔁 **CNAME Chaining** — automatic in-zone CNAME target resolution
- 📋 **SOA & NXDOMAIN** — authoritative negative responses with SOA records in the authority section

---

## Architecture

```
                ┌──────────────┐
  DNS Clients   │   ShardDNS   │   gRPC Clients
 ─────────────► │              │ ◄──────────────
  UDP / TCP :53 │  Worker Pool │  :9053
                │              │
                └──────┬───────┘
                       │
                ┌──────▼───────┐
                │   ScyllaDB   │
                │  zones       │
                │  records     │
                └──────────────┘
```

---

## Container Image

Images are published to the **GitHub Container Registry** on every push to `main` and on version tags.

```bash
# Latest
docker pull ghcr.io/adatage/sharddns:latest

# Specific version
docker pull ghcr.io/adatage/sharddns:v1.2.3
```

| Tag pattern        | When published                  |
|--------------------|---------------------------------|
| `latest`           | Every push to `main`            |
| `v1.2.3`           | On git tag `v1.2.3`             |
| `1.2`              | On git tag `v1.2.x`             |

---

## Quick Start

### Docker Compose (Recommended)

Spin up ScyllaDB, apply the schema, and start ShardDNS in one command:

```bash
docker compose up -d
```

The default `docker-compose.yml` builds from source. To use the pre-built image from GHCR instead, set the `sharddns` service image:

```yaml
sharddns:
  image: ghcr.io/adatage/sharddns:latest
```

This starts:
- **ScyllaDB** on port `9042`
- **ShardDNS** DNS server on port `53` (UDP/TCP)
- **ShardDNS** gRPC server on port `9053`

### Build from Source

```bash
# Install buf (for protobuf generation)
# https://buf.build/docs/installation

make build-all      # generate proto + build both binaries
make run            # build and run the DNS server
```

Binaries are output to `bin/`:
- `bin/sharddns` — DNS + gRPC server
- `bin/sharddns-cli` — management CLI

### Go Client

The generated gRPC client package is committed to this repo and importable directly:

```bash
go get github.com/Adatage/ShardDNS/api
```

```go
import dnsmgr "github.com/Adatage/ShardDNS/api"
```

---

## Configuration

ShardDNS is configured entirely via environment variables (see [`.env.example`](.env.example)):

| Variable           | Default       | Description                                        |
|--------------------|---------------|----------------------------------------------------|
| `DNS_ADDR`         | `:53`         | Address the DNS server listens on (UDP & TCP)      |
| `GRPC_ADDR`        | `:9053`       | Address the gRPC management server listens on      |
| `WORKERS`          | `0`           | Worker goroutines (`0` = `runtime.NumCPU()`)       |
| `LOG_LEVEL`        | `info`        | Log level: `debug`, `info`, `warn`, `error`        |
| `SCYLLA_HOSTS`     | `127.0.0.1`   | Comma-separated ScyllaDB hostnames or IPs          |
| `SCYLLA_KEYSPACE`  | `sharddns`    | ScyllaDB keyspace                                  |
| `SCYLLA_USERNAME`  | *(empty)*     | ScyllaDB username (optional)                       |
| `SCYLLA_PASSWORD`  | *(empty)*     | ScyllaDB password (optional)                       |

---

## gRPC API

The control plane is defined in [`proto/dns_manager.proto`](proto/dns_manager.proto) and exposed on the `GRPC_ADDR` port.

### Zone Management

| RPC           | Description                     |
|---------------|---------------------------------|
| `CreateZone`  | Create a new DNS zone           |
| `GetZone`     | Fetch a zone by name            |
| `UpdateZone`  | Update SOA parameters of a zone |
| `DeleteZone`  | Delete a zone                   |
| `ListZones`   | Paginated list of all zones     |

### Record Management

| RPC            | Description                             |
|----------------|-----------------------------------------|
| `CreateRecord` | Add a DNS record to a zone              |
| `DeleteRecord` | Remove a specific DNS record            |
| `ListRecords`  | List all records in a zone (paginated)  |
| `GetRecords`   | Look up records by zone, name, and type |

#### Example — create a zone and add an A record

```bash
# Using grpcurl
grpcurl -plaintext -d '{
  "name": "example.com",
  "primary_ns": "ns1.example.com",
  "admin_email": "admin.example.com",
  "refresh": 3600,
  "retry": 900,
  "expire": 604800,
  "minimum_ttl": 300
}' localhost:9053 dns_manager.DNSManager/CreateZone

grpcurl -plaintext -d '{
  "zone": "example.com",
  "name": "www.example.com",
  "type": "A",
  "ttl": 300,
  "rdata": "93.184.216.34"
}' localhost:9053 dns_manager.DNSManager/CreateRecord
```

---

## Database Schema

The schema lives in [`cql/database.cql`](cql/database.cql) and is applied automatically by Docker Compose.

| Table          | Primary Key                    | Notes                                      |
|----------------|--------------------------------|--------------------------------------------|
| `zones`        | `name`                         | SOA parameters per zone                    |
| `records`      | `(zone, name)` + `type, rdata` | LZ4 compression, Leveled Compaction        |
| `dnssec_keys`  | `(zone)` + `flags, key_tag`    | Reserved for future DNSSEC support         |

Apply the schema manually:

```bash
make schema
```

---

## Development

```bash
make proto        # regenerate gRPC/protobuf code from .proto files
make build        # compile server and CLI binaries
make test         # run all tests
make lint         # run go vet
make docker-up    # start ScyllaDB + ShardDNS via Docker Compose
make docker-down  # stop all containers
make clean        # remove build artifacts
```

> **Note:** The `api/` directory (generated protobuf code) is committed to the repo. After editing any `.proto` file, run `make proto` and commit the updated `api/` files. CI will fail if the committed files are out of sync with the proto definitions.

---

## License

Distributed under the [Apache 2.0 License](LICENSE).
