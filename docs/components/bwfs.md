# bwfs (Backup Writer from File System)

Backup storage server — receives files from a backup reader and stores them on disk with deduplication.

## Usage

```
bwfs <storage_path> <command> [flags]
```

`storage_path` is required by all commands. Only one `bwfs server` process may open a given storage path at a time; `bwfs list` can run alongside a live server.

## Commands

### server

Start the gRPC backup server. Receives files from `brfs` and stores chunks and metadata.

```bash
bwfs /home/user/backup server
bwfs /home/user/backup server --port 8080 --debug
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | config `default_port` | Port to listen on |
| `--debug` | false | Enable debug logging |
| `--quiet` | false | Suppress console logging |

### list

List stored file data. Can run concurrently with a live server.

```bash
bwfs /home/user/backup list
bwfs /home/user/backup list --output json
bwfs /home/user/backup list --filter /var/log
```

| Flag | Default | Description |
|------|---------|-------------|
| `--output` | `table` | Output format: `table` or `json` |
| `--filter` | | Substring filter on file ID |
| `--debug` | false | Enable debug logging |

**Table columns:** SOURCE, TYPE, PATH, TIMESTAMP, SIZE, CHUNKS, VERSIONS

**JSON fields:** `file_data_id`, `source`, `type`, `path`, `timestamp`, `size`, `chunks`, `versions`, `created_at`

## Building

```bash
cd src && make build
```

## See Also

- [brfs](./brfs.md) — Backup Reader for File System
- [backup protocol](../protocols/backup.md) — Wire protocol
- [Architecture](../ARCHITECTURE.md) — System overview
