package filesystem

import (
	"fmt"
	"io/fs"
	"time"

	"github.com/alex-sviridov/miniprotector/workload"
)

// FileInfo holds essential file attributes for backup operations across platforms
// To check file type, use: fileInfo.Mode.Type() == fs.ModeDir (directory), fs.ModeSymlink (symlink), etc.
// Or use fileInfo.GetType() for a single character representation
type FileInfo struct {
	host          string
	path          string
	name          string
	size          int64
	mode          fs.FileMode // Full mode (type + permissions)
	owner         uint32      // Unix UID, Windows SID hash
	group         uint32      // Unix GID, Windows primary group SID hash
	modTime       time.Time
	accessTime    time.Time
	cTime         time.Time // Unix: change time, Windows: creation time
	symlinkTarget string
	// Platform-specific fields
	attributes []byte // Platform-specific attributes (Windows file attributes, Unix extended attributes, etc.)
	acl        []byte // Platform-specific ACL data (Unix extended ACLs or Windows Security Descriptor)
}

// File type mapping from fs.FileMode to single character representation
var fileTypeMap = map[fs.FileMode]rune{
	fs.ModeDir:                        'd', // Directory
	fs.ModeSymlink:                    'l', // Symbolic link
	fs.ModeNamedPipe:                  'p', // Named pipe (FIFO)
	fs.ModeSocket:                     's', // Socket
	fs.ModeDevice:                     'b', // Block device
	fs.ModeCharDevice:                 'c', // Character device
	fs.ModeDevice | fs.ModeCharDevice: 'c', // Character device (alternative)
	0:                                 'f', // Regular file
}

// GetType returns a single character representing the file type
// 'd' = directory, 'f' = regular file, 'l' = symlink, 'p' = named pipe,
// 'c' = character device, 'b' = block device, 's' = socket, '?' = unknown
func (fi FileInfo) GetType() rune {
	if typeRune, exists := fileTypeMap[fi.mode.Type()]; exists {
		return typeRune
	}
	return '?' // Unknown file type
}

// GetId returns unique id of file backup object fs://host:type:path:mtime
// and this ID will be used to perform file-level dedup
func (fi FileInfo) ID() string {
	return fmt.Sprintf("fs://%s:%c:%s:%d", fi.host, fi.GetType(), fi.path, fi.modTime.Unix())
}

func (fi FileInfo) MetadataBlob() []byte {
	blob, err := fi.Encode()
	if err != nil {
		return nil
	}
	return blob
}

// GetSize returns object size in bytes
func (fi FileInfo) Size() int64 {
	return fi.size
}

// GetSource returns source hostname
func (fi FileInfo) Source() string {
	return fi.host
}

// GetMtime returns data modification time
func (fi FileInfo) Mtime() int64 {
	return fi.modTime.Unix()
}

// GetCtime returns metadata change time
func (fi FileInfo) Ctime() int64 {
	return fi.cTime.Unix()
}

// Path returns the object's original filesystem path.
func (fi FileInfo) Path() string {
	return fi.path
}

// Owner returns the file's owning UID (Unix) or SID hash (Windows).
func (fi FileInfo) Owner() uint32 {
	return fi.owner
}

// Group returns the file's owning GID (Unix) or primary group SID hash
// (Windows).
func (fi FileInfo) Group() uint32 {
	return fi.group
}

// Mode returns the full file mode (type + permissions).
func (fi FileInfo) Mode() fs.FileMode {
	return fi.mode
}

// String returns a string containing basic file attributes in unix-like style
// Format: drwxr-xr-x uid gid size mtime name
func (fi FileInfo) String() string {
	return fmt.Sprintf("%c%s %d %d %d %s %s",
		fi.GetType(),
		fi.mode.String(),
		fi.owner,
		fi.group,
		fi.size,
		fi.modTime.Format("Jan 02 15:04"),
		fi.name,
	)
}

// Ensure FileInfo implements BackupObject interface
var _ workload.BackupObject = (*FileInfo)(nil)
