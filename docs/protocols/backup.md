# Chunked Backup Protocol - Design Overview

## **Core Concept**
A dual-layer integrity system with smart deduplication that processes files in 512KB chunks, optimizing for both network efficiency and data reliability.

## **Protocol Flow**
1. **File-level filtering**: Send metadata first, get `SEND_FILE` or `SKIP_FILE` to avoid unnecessary processing
2. **Chunk-based transfer**: Split files into 512KB chunks, send hash batches, receive selective requests  
3. **Dual integrity verification**: BLAKE3 per-chunk + CRC32 whole-file validation

## **Key Design Decisions**

**Why 512KB chunks?**
- Optimal balance: large enough for network efficiency, small enough for granular deduplication
- Memory-friendly: predictable RAM usage regardless of file size
- **Future evolution**: Fixed 512KB will be replaced with variable chunk sizes based on https://github.com/PlakarKorp/go-cdc-chunkers

**Why batch hashes but send chunks individually?**
- Hashes are small (~32 bytes) → efficient to batch
- Chunks are large (512KB) → individual sending avoids massive memory buffers

**Why dual integrity (BLAKE3 + CRC32)?**
- **BLAKE3**: Ensures each chunk survives network transmission intact
- **CRC32**: Verifies complete file assembly (correct order, no missing chunks)
- Composable CRC32 calculated during read → no extra I/O overhead

**Why file-level pre-filtering?**
- Server decides upfront: "Do I need this file at all?"
- Eliminates hash calculation and chunk processing for existing files
- Massive efficiency gain for incremental backups

**How does the system recover from a corrupted chunk?**
- A finalized DB record only proves a file was fully backed up *at some point* — it doesn't prove the chunk files are still on disk. The chunk store is a separate filesystem tree that can lose data independently of the metadata DB (deletion, disk corruption)
- Rather than re-verifying every chunk on every backup (which would erase the efficiency gain from file-level pre-filtering above — it'd mean reading all previously-backed-up data on every run), the server assumes the chunk store is healthy and only reacts when a read actually fails
- Any chunk read failure during restore or verify (`bwfs`'s `RestoreFile`, used by both `rwfs restore` and `rwfs verify`) marks that chunk corrupted: the chunk file is removed if still present, its DB records are deleted, and the `FileData` of every file that referenced it is invalidated. The DB portion runs inside a single transaction, so a concurrent backup that links a new file to the same chunk hash can never lose that link without its `FileData` being invalidated too
- The next backup run then sees those files as not-yet-backed-up (their `FileData` is gone) and re-uploads them via the normal `SEND_FILE` path — chunk-level dedup still skips any of the file's chunks that are intact, so only the actually-missing data is re-transferred
- This is a reactive, not a proactive, self-heal: corruption is only detected and fixed when something tries to read the affected chunk (a `verify` run, or a real restore). A proactive integrity-scan routine is a possible future addition, not implemented now

## **Backup Job Tracking**

Every `ProcessBackupStream` call carries a `job-id` gRPC metadata key, attached by `brfs` when it
opens the stream (not a message in the `FileRequest`/`FileResponse` protobuf — this is transport
metadata, so it requires no `.proto` changes). A stream with no `job-id` metadata is rejected
immediately with `codes.InvalidArgument`, before any file is processed.

One `brfs` invocation is one backup job: `brfs` generates a UUID at startup, or uses the value
passed via `--job-id`, and attaches it to every one of its `--streams` concurrent streams.

On the `bwfs` side, the first stream carrying a given `job-id` causes a `backup_jobs` row to be
created (idempotently — every stream of the job attempts this, only the first succeeds); the row's
`source_host` is read from the client's mTLS certificate (first SAN entry, falling back to
CommonName), not from anything the client reports in-band. `bwfs` tracks the number of currently
open streams per job in memory; when the last stream of a job closes, `finished_at` is set. If
`brfs` crashes mid-run, or `bwfs` restarts while a job has open streams, `finished_at` simply never
gets set for that job — this is treated as the correct signal that the run didn't complete cleanly,
not a bug.

Every file version `bwfs` records (`file_versions` table) carries the `job_id` of the stream that
produced it. A duplicate observation of the same object within the same job (e.g. a future retry
re-sending a file) is a safe no-op — the first write for a given `(job_id, object_id)` pair wins.

See [bwfs](../components/bwfs.md) for the schema and [brfs](../components/brfs.md) for the
`--job-id` flag.

Note on the sequence diagram below: the `START_STREAM:jobId:streamId` step shown there is
conceptual — in the actual gRPC transport this is the `job-id` metadata described above, attached
when the stream is opened, not a discrete message exchanged over the stream.

```mermaid
sequenceDiagram

    participant Client as brfs
    participant Server as bwfs
    
    
    Client->>Server: START_STREAM:jobId:streamId
    Server-->>Client: START_STREAM_OK
    
    loop For Each File
        Client->>Server: FILE:type;size;perms;times;path
        Note right of Server: FileType rune; Size int64; Mode uint32; Owner uint32; Group uint32; ModTime time; AccessTime time; ChangeTime time; Path string
        
        alt Server Needs File
            Server-->>Client: SEND_FILE
            
            loop For Each Chunk Batch
                Note left of Client: Read N chunks (512KB each)<br/>Calculate BLAKE3 hashes<br/>Update file CRC32 incrementally<br/>(same memory buffer, no re-read)
                
                Client->>Server: HASHES:hash1,hash2,hash3,...
                Note right of Server: Analyze hashes against existing data<br/>Determine needed chunks
                
                alt Some Chunks Needed
                    Server-->>Client: NEED:hash1,hash3
                    
                    loop For Each Chunk Needed
                        Client->>Server: CHUNK:chunk_data
                        
                        Note left of Client: Send only requested chunk
                        Note right of Server: Calculate BLAKE3\nCalculate CRC32 and store in DB
                        
                        alt Chunk Valid
                            Server-->>Client: CHUNK_OK
                            Note right of Server: BLAKE3 calculated and correct
                        else Chunk Invalid
                            Server-->>Client: CHUNK_ERROR
                            Note right of Server: Network corruption detected
                            Client->>Server: CHUNK:chunk_data
                            Note left of Client: Resend failed chunk
                            Server-->>Client: CHUNK_OK
                            Note right of Server: BLAKE3 calculated and correct
                        end
                    end
                    
                else No Chunks Needed in Batch
                    Server-->>Client: SKIP_ALL
                    Note left of Client: Skip to next batch
                    Note right of Server: All chunks in batch exist on server
                end
            end
            
            Client->>Server: FILE_CRC:checksum
            Note left of Client: File transfer complete\nSend total file CRC32 checksum
            
            Note right of Server: Calculate total CRC32 over chunk CRCs in DB\nVerify complete file integrity
            
            alt File Assembly Correct
                Server-->>Client: FILE_OK
                Note right of Server: File complete and verified\nFile assembled correctly (CRC32)
            else File Assembly Error
                Server-->>Client: FILE_CRC_ERROR
                Note right of Server: File corruption detected\nFile assembled correctly (wrong CRC32)
            end
            
        else Server Already Has File
            Server-->>Client: SKIP_FILE
        end
    end
    
    Note over Client,Server: All Files Processed
    
    Client->>Server: Close Stream
    Server-->>Client: Stream Closed
```