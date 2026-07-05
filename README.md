# Miniprotector

A backup system with intelligent deduplication and dual-layer integrity verification.

## Overview

Miniprotector is a self-learning pet-project based on my Backup & Recovery experience. It won't be working for a long time but the idea to create a simple but powerfull enterprise-grade backup tool. By starting simple and adding enterprise features progressively, I aim to incorporate ten years of backup and recovery expertise. Take the best from existing solutions to make it simple and functional.

## Core Goals
- 🎛️ Central control server managing all backup operations
- 📅 Job scheduling, queuing, and resource management
- 📊 Complete backup history tracking and reporting
- 🔐 Role-based access control (RBAC)
- 💾 Space and network efficiency by deduplication where possible
- 🛡️ Reliability by multiple integrity verification layers
- 🔌 Pluggable architecture for easier integration of new readers, writers and workloads
- 🔄 Loose coupling by using message queues for control layer communication
- 🎯 Application-aware support for databases, VMs, filesystems

## Quick Start

**Backup files:**
```bash
# Start backup writer
bwfs /backup/storage server --port 8080

# Backup a directory
brfs /home/user/documents --destination localhost:8080
```

**Query what's backed up:**
```bash
# List all files in the backup store (local)
bwfs /backup/storage list

# List files for a specific host and path prefix (local)
bwfs /backup/storage list myhost:/var/log

# Query a remote bwfs server
rwfs list localhost:8080
rwfs list myhost:/var/log localhost:8080
```

**Verify backup integrity:**
```bash
# Verify all files backed up from the current host
rwfs verify localhost:8080

# Verify with 8 concurrent workers, suppress success lines
rwfs verify localhost:8080 --streams 8 --quiet
```

## Components

- **[brfs](docs/components/brfs.md)** - Backup Reader for File System — reads files and streams them to `bwfs`
- **[bwfs](docs/components/bwfs.md)** - Backup Writer for File System — receives, deduplicates, and stores files; also serves the list subprotocol
- **[rwfs](docs/components/rwfs.md)** - Restore Writer for File System — queries a remote `bwfs` for backed-up file listings; verifies backup integrity
- **[certclient](docs/components/certclient.md)** - Bootstraps or renews a node's mTLS bootstrap credential from the CA, and refreshes its short-lived operating certificate from `issuer`
- **[agent](docs/components/agent.md)** - Node agent — reconciles local state against embedded policies (v1: mTLS certificate renewal via `certclient`)
- **[client-manager](docs/components/client-manager.md)** - Owns the enrolled-client list and mints enrollment tokens directly: descriptions, RBAC-bound attributes, SAN aliases, revoked status (control-plane component, runs on the CA host)
- **[catalogsync](docs/components/catalogsync.md)** - Replicates a bwfs node's file versions to a backup catalog, asynchronously and independent of bwfs's own availability
- **[catalog](docs/components/catalog.md)** - Backup Catalog — receives `catalogsync`'s replicated file versions over gRPC and persists them centrally; control-plane component

## Documentation

- **[Architecture](docs/ARCHITECTURE.md)** - System design and data flow
- **[Backup Protocol](docs/protocols/backup.md)** - brfs → bwfs chunked backup protocol
- **[List Protocol](docs/protocols/list.md)** - rwfs → bwfs list subprotocol
- **[Restore Protocol](docs/protocols/restore.md)** - rwfs → bwfs restore/verify subprotocol
- **[Catalog Sync Protocol](docs/protocols/catalog-sync.md)** - catalogsync → catalog replication protocol
- **[Issuer Protocol](docs/protocols/issuer.md)** - issuer operating-certificate minting protocol
- **[Components](docs/components/)** - Individual component documentation

## Building

```bash
# Build all components
make build

# Run unit and integration tests
make test

# Run go vet
make lint

# Run Docker-based e2e tests (requires Docker daemon, ~3 min)
make test-e2e
```