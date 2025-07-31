# bwfs (Backup Writer from File System)

Backup storage server, recieving files from a backup reader and writing them into filesystem.

## Purpose

Recieves files from `brfs*` (backup reader) via:
- Network connection (for remote backup readers)  
- Unix socket (for local backup readers)
Makes decision if file is needed based on it's metadata and stored files database. 
Makes decision if file chunk is needed based on it's hash and stored hashes. 
Checks file consistency in the end of file backup.

## Usage

```bash
bwfs <storage_path> --destination <host:port>
```

## Arguments and Flags

- `<storage_path>` - Directory to backup **(required)**
- `--port <port>` - Server listening port *(default: config->default_port)*
- `--debug` - Enable debug logging
- `--quiet` - Suppress stdout logging

## Examples

```bash
# Listen on port 8080 and write into /home/user/backup
bwfs /home/user/backup --port 8080
```

## Protocol

Communicates with [brfs](./brfs.md) (backup reader) using the protocol specified in [doc/protocols/backup.md](../protocols/backup.md).

## Building

```bash
cd srv
make build
```

## See Also

- [brfs](./brfs.md) - Backup Reader for File System
- [doc/protocols/backup.md](../protocols/backup.md) - Communication protocol
- [Architecture](../ARCHITECTURE.md) - System overview