# brfs (Backup Reader from File System)

Backup tool for reading files from a source directory and sending them to a backup writer.

## Purpose

Reads all files from a specified directory and transmits them to `bw*` (backup writer) via:
- Network connection (for remote backup writers)  
- Unix socket (for local backup writers)

## Usage

```bash
brfs <source_folder> --destination <host:port>
```

## Arguments and Flags

- `<source_folder>` - Directory to backup **(required)**
- `--destination <host:port>` - Writer destination address **(required)**
- `--streams <number>` - Number of concurrent streams *(default: config->default_streams)*
- `--debug` - Enable debug logging
- `--quiet` - Suppress stdout logging

## Examples

```bash
# Backup to remote writer
brfs /home/user/documents --destination 192.168.1.100:8080

# Backup to local writer with debug
brfs /var/log --destination localhost:8080 --debug --streams 5
```

## Protocol

Communicates with [bwfs](./bwfs.md) (backup writer) using the protocol specified in [doc/protocols/backup.md](../protocols/backup.md).

## Building

```bash
cd srv
make build
```

## See Also

- [bwfs](./bwfs.md) - Backup Writer for File System
- [doc/protocols/backup.md](../protocols/backup.md) - Communication protocol
- [Architecture](../ARCHITECTURE.md) - System overview