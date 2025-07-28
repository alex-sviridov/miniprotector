# Miniprotector

Miniprotector is a self-learning pet-project based on my Backup & Recovery experience. It won't be working for a long time but the idea to create a simple but powerfull backup tool with the native sliding window deduplication engine, capable of storing data in the S3 cloud.

## Components

### Backup Workflow

* **brfs** - Backup Reader for File System, handles all the operations related to listing files, creating snapshots, performing effecient reading and ensuring the files are consistend.
* **bddup** - Backup Deduplicator, 