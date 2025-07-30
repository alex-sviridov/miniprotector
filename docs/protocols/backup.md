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
- **Future evolution**: Fixed 512KB will be replaced with variable chunk sizes (content-defined chunking) commonly used in advanced deduplication systems for better efficiency

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

```mermaid
sequenceDiagram

    participant Client as brfs
    participant Server as bwfs
    
    
    Client->>Server: START_STREAM:jobId:streamId
    Server-->>Client: START_STREAM_OK
    
    loop For Each File
        Client->>Server: FILE:filepath,size,modtime,permissions
        
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