package files

import (
	"io/fs"
	"time"
)


// FileInfo holds essential file attributes for backup operations across platforms
// To check file type, use: fileInfo.Mode.Type() == fs.ModeDir (directory), fs.ModeSymlink (symlink), etc.
// Or use fileInfo.GetType() for a single character representation
type FileInfo struct {
	Path          string
	Name          string
	Size          int64
	Mode          fs.FileMode // Full mode (type + permissions)
	Owner         uint32      // Unix UID, Windows SID hash
	Group         uint32      // Unix GID, Windows primary group SID hash
	ModTime       time.Time
	AccessTime    time.Time
	CTime         time.Time // Unix: change time, Windows: creation time
	SymlinkTarget string
	// Platform-specific fields
	Attributes    []byte // Platform-specific attributes (Windows file attributes, Unix extended attributes, etc.)
	ACL           []byte // Platform-specific ACL data (Unix extended ACLs or Windows Security Descriptor)
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
	if typeRune, exists := fileTypeMap[fi.Mode.Type()]; exists {
		return typeRune
	}
	return '?' // Unknown file type
}