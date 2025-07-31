# System Architecture
A backup system with intelligent deduplication and integrity verification.


## System Overview

See the System Architecture Diagram showing the complete data flow between components and filesystems.

## Component Connectivity
### Backup Process:

- **brfs** reads files from standard filesystem
- Connects to bwfs via network or Unix socket
- Generates data streams with chunked file content
- bwfs writes needed data to filesystem and metadata to SQLite database

### Restore Process:
**rwfs** reads data from filesystem and queries SQLite database
- Connects to rrfs via network or Unix socket
- Generates data streams to reconstruct files
- rrfs writes files to standard filesystem
```mermaid
graph TB
    subgraph "Source Machine"
        SrcFS[Source Filesystem]
        brfs[brfs<br/>Backup Reader]
    end
    
    subgraph "Backup Machine"
        bwfs[bwfs<br/>Backup Writer]
        rwfs[rwfs<br/>Restore Reader]
        BackupFS[Backup Filesystem]
        DB[(SQLite Database)]
    end
    
    subgraph "Destination Machine"
        rrfs[rrfs<br/>Restore Writer]
        DstFS[Destination Filesystem]
    end
    
    %% Backup Flow 
    SrcFS -->|reads files| brfs
    brfs -->|backup protocol<br/>network/unix socket| bwfs
    bwfs -->|stores chunks| BackupFS
    bwfs -->|stores metadata| DB
    
    %% Restore Flow
    DB -->|queries metadata| rwfs
    BackupFS -->|reads chunks| rwfs
    rwfs -->|restore protocol<br/>network/unix socket| rrfs
    rrfs -->|writes files| DstFS
    
    %% Styling
    classDef filesystem fill:#e1f5fe
    classDef component fill:#f3e5f5
    classDef database fill:#fff3e0
    
    class SrcFS,BackupFS,DstFS filesystem
    class brfs,bwfs,rrfs,rwfs component
    class DB database
```