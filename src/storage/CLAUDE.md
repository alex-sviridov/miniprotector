# Storage Package

The storage package provides a backup storage system that separates file data from file metadata and uses content-addressed chunks, packed into append-only segments, for deduplication.

## Design Philosophy

This storage system prioritizes **simplicity and understandability** over premature optimization:

- **Simple Go idioms**: Clear, straightforward code that's easy to read and maintain
- **Database-driven concurrency**: Rely on SQLite/GORM rather than complex application-level locking
- **Durability by group commit**: chunk bytes are appended to pack segments; a file only becomes complete after its bytes are fsynced and its index rows committed in one transaction (index rows never point at non-durable bytes)
- **Clear separation**: File data (content) vs file metadata (attributes) are properly separated
- **Minimal interfaces**: BackupStore handles most use cases; Repository adds only essential advanced features

## Core Concepts

### File Data vs File Metadata Separation

- **File Data**: Actual file content (size, CRC, chunks) - changes when file content changes (`mtime`)
- **File Metadata**: File attributes, permissions, etc. - changes when metadata changes (`ctime`)
- Same file content can have multiple metadata versions over time

### Content-Addressable Storage

- Chunks are identified by BLAKE3 hash; automatic deduplication: identical chunks are stored once
- Chunk bytes are appended to segment files `packs/NNNNNNNNNN.pack` (`pack/` package, 256 MiB each); each
  record is `MPKR | len | BLAKE3 | data`. SQLite (`chunk_records`) holds each chunk's segment and offset
- Reads verify the BLAKE3 hash; opening recovers the last segment (torn tail truncated)
- Vacuum deletes orphan rows, then compacts sealed segments under 50% live and removes dead ones
- An unusable chunk (`MarkChunkCorrupted`, or a bad record found by compaction) loses its row and links, but the
  `FileData` that used it is flagged (`damaged_at`), not deleted; dedup and `FileData()` ignore flagged rows
- Legacy `chunks/` stores are rejected (no migration); Linux only
- BLAKE3 provides fast, secure, and parallel hashing
