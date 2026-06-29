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
bwfs /backup/storage --port 8080

# Backup directory
brfs /home/user/documents --destination localhost:8080
```

## Components

- **[brfs](docs/components/brfs.md)** - Backup Reader from File System
- **[bwfs](docs/components/bwfs.md)** - Backup Writer to File System  

## Documentation

- **[Architecture](docs/architecture.md)** - System design and data flow
- **[Backup Protocol](docs/protocols/backup.md)** - Communication specification
- **[Components](docs/components/)** - Individual component documentation

## Building

```bash
# Build all components
make build
```