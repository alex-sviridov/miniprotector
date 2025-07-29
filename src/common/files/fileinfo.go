package files

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

// FileInfo holds essential file attributes for backup
type FileInfo struct {
	Path          string
	Name          string
	IsDir         bool
	IsSymlink     bool
	IsRegular     bool
	IsDevice      bool
	IsFifo        bool
	IsSocket      bool
	Size          int64
	Mode          uint32
	Owner         uint32
	Group         uint32
	ModTime       time.Time
	AccessTime    time.Time
	ChangeTime    time.Time
	Links         uint64
	DeviceID      uint64
	Rdev          uint64
	SymlinkTarget string
}

// Direct statx syscall constants
const (
	AT_FDCWD            = ^uintptr(99) // -100 as uintptr
	AT_SYMLINK_NOFOLLOW = 0x100
	SYS_STATX           = 332

	STATX_TYPE        = 0x00000001
	STATX_MODE        = 0x00000002
	STATX_NLINK       = 0x00000004
	STATX_UID         = 0x00000008
	STATX_GID         = 0x00000010
	STATX_ATIME       = 0x00000020
	STATX_MTIME       = 0x00000040
	STATX_CTIME       = 0x00000080
	STATX_INO         = 0x00000100
	STATX_SIZE        = 0x00000200
	STATX_BASIC_STATS = 0x000007ff
)

// statx_timestamp matches kernel struct
type statxTimestamp struct {
	Sec      int64
	Nsec     uint32
	Reserved int32
}

// statx matches kernel struct exactly
type statx struct {
	Mask           uint32
	Blksize        uint32
	Attributes     uint64
	Nlink          uint32
	Uid            uint32
	Gid            uint32
	Mode           uint16
	Spare0         uint16
	Ino            uint64
	Size           uint64
	Blocks         uint64
	AttributesMask uint64
	Atime          statxTimestamp
	Btime          statxTimestamp
	Ctime          statxTimestamp
	Mtime          statxTimestamp
	RdevMajor      uint32
	RdevMinor      uint32
	DevMajor       uint32
	DevMinor       uint32
	Spare2         [14]uint64
}

// rawStatx performs direct statx syscall
func rawStatx(path string, stat *statx) error {
	pathPtr := unsafe.Pointer(&[]byte(path + "\x00")[0])

	_, _, errno := syscall.Syscall6(
		SYS_STATX,
		AT_FDCWD,
		uintptr(pathPtr),
		uintptr(AT_SYMLINK_NOFOLLOW),
		uintptr(STATX_BASIC_STATS),
		uintptr(unsafe.Pointer(stat)),
		0,
	)

	if errno != 0 {
		return errno
	}
	return nil
}

// getFileType extracts file type from mode - optimized with lookup
var fileTypeLookup = [8]struct {
	isDir, isSymlink, isRegular, isDevice, isFifo, isSocket bool
}{
	0: {},               // Unknown
	1: {isFifo: true},   // S_IFIFO
	2: {isDevice: true}, // S_IFCHR
	3: {},               // Unused
	4: {isDir: true},    // S_IFDIR
	5: {},               // Unused
	6: {isDevice: true}, // S_IFBLK
	7: {},               // Unused
}

func getFileType(mode uint32) (isDir, isSymlink, isRegular, isDevice, isFifo, isSocket bool) {
	fileType := (mode >> 12) & 0xF

	switch fileType {
	case 8: // S_IFREG
		isRegular = true
	case 10: // S_IFLNK
		isSymlink = true
	case 12: // S_IFSOCK
		isSocket = true
	default:
		if fileType < 8 {
			lookup := fileTypeLookup[fileType]
			isDir = lookup.isDir
			isDevice = lookup.isDevice
			isFifo = lookup.isFifo
		}
	}
	return
}

// Pre-allocated byte slice pool for path conversions
var pathBuffer = make([]byte, 4096)

// getFileInfoFast gets all basic attributes with single statx syscall
func getFileInfoFast(path string) (FileInfo, error) {
	var stat statx

	// Direct syscall - no Go wrapper overhead
	if err := rawStatx(path, &stat); err != nil {
		return FileInfo{}, err
	}

	mode := uint32(stat.Mode)
	isDir, isSymlink, isRegular, isDevice, isFifo, isSocket := getFileType(mode)

	// Extract basename without allocation when possible
	var name string
	if lastSlash := len(path) - 1; lastSlash >= 0 {
		for i := lastSlash; i >= 0; i-- {
			if path[i] == '/' {
				name = path[i+1:]
				break
			}
		}
		if name == "" {
			name = path
		}
	} else {
		name = path
	}

	fileInfo := FileInfo{
		Path:       path,
		Name:       name,
		IsDir:      isDir,
		IsSymlink:  isSymlink,
		IsRegular:  isRegular,
		IsDevice:   isDevice,
		IsFifo:     isFifo,
		IsSocket:   isSocket,
		Size:       int64(stat.Size),
		Mode:       mode,
		Owner:      stat.Uid,
		Group:      stat.Gid,
		Links:      uint64(stat.Nlink),
		DeviceID:   uint64(stat.DevMajor)<<32 | uint64(stat.DevMinor),
		Rdev:       uint64(stat.RdevMajor)<<32 | uint64(stat.RdevMinor),
		ModTime:    time.Unix(stat.Mtime.Sec, int64(stat.Mtime.Nsec)),
		AccessTime: time.Unix(stat.Atime.Sec, int64(stat.Atime.Nsec)),
		ChangeTime: time.Unix(stat.Ctime.Sec, int64(stat.Ctime.Nsec)),
	}

	// Get symlink target only if needed - optimized readlink
	if isSymlink {
		if len(pathBuffer) > len(path)+1 {
			copy(pathBuffer, path)
			pathBuffer[len(path)] = 0

			n, _, errno := syscall.Syscall(
				syscall.SYS_READLINK,
				uintptr(unsafe.Pointer(&pathBuffer[0])),
				uintptr(unsafe.Pointer(&pathBuffer[len(path)+1])),
				uintptr(len(pathBuffer)-len(path)-1),
			)

			if errno == 0 && n > 0 {
				fileInfo.SymlinkTarget = string(pathBuffer[len(path)+1 : len(path)+1+int(n)])
			}
		} else {
			// Fallback for very long paths
			if target, err := os.Readlink(path); err == nil {
				fileInfo.SymlinkTarget = target
			}
		}
	}

	return fileInfo, nil
}

// Pre-allocate result slice to avoid repeated growth
func estimateFileCount(path string) int {
	// TODO: estimate based on directory depth and typical file counts
	// This is a rough estimate to reduce slice reallocations
	return 1000
}

// ListRecursive - maximum performance pure Go implementation
func ListRecursive(sourcePath string) ([]FileInfo, error) {
	// Pre-allocate with estimated capacity
	items := make([]FileInfo, 0, estimateFileCount(sourcePath))

	err := filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Use high-performance direct syscall
		fileInfo, err := getFileInfoFast(path)
		if err != nil {
			return err
		}

		items = append(items, fileInfo)
		return nil
	})

	return items, err
}

func SplitByStreams(files []FileInfo, streams int) [][]FileInfo {

	if streams <= 0 {
		return nil
	}

	result := make([][]FileInfo, streams)
	if streams == 1 {
		result[0] = files
		return result
	}
	if len(files) == 0 {
		// Return empty slices for each stream

		for i := range result {
			result[i] = make([]FileInfo, 0)
		}
		return result
	}
	filesPerStream := len(files) / streams
	remainder := len(files) % streams

	start := 0
	for i := 0; i < streams; i++ {
		// Calculate chunk size for this stream
		chunkSize := filesPerStream
		if i < remainder {
			chunkSize++ // Distribute remainder across first streams
		}

		end := start + chunkSize
		if end > len(files) {
			end = len(files)
		}

		if start < len(files) {
			result[i] = files[start:end]
		} else {
			result[i] = make([]FileInfo, 0)
		}

		start = end
	}

	return result
}
