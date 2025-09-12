# Storage Package

The storage package provides a backup storage system that separates file data from file metadata and uses content-addressable chunk storage for deduplication.

## Design Philosophy

This storage system prioritizes **simplicity and understandability** over premature optimization:

- **Simple Go idioms**: Clear, straightforward code that's easy to read and maintain
- **Database-driven concurrency**: Rely on SQLite/GORM rather than complex application-level locking
- **Atomic operations**: File writes use temp + rename pattern for reliability
- **Clear separation**: File data (content) vs file metadata (attributes) are properly separated
- **Minimal interfaces**: BackupStore handles most use cases; Repository adds only essential advanced features

## Core Concepts

### File Data vs File Metadata Separation

- **File Data**: Actual file content (size, CRC, chunks) - changes when file content changes (`mtime`)
- **File Metadata**: File attributes, permissions, etc. - changes when metadata changes (`ctime`)
- Same file content can have multiple metadata versions over time

### Content-Addressable Storage

- Chunks are stored using BLAKE3 hash as filename: `aa/bb/aabbccddee...`
- Automatic deduplication: identical chunks share the same storage
- Chunks stored on filesystem, only metadata in database
- BLAKE3 provides fast, secure, and parallel hashing
