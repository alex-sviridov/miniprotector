package files

import (
	"os"
	"io/fs"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

// FileInfo holds essential file attributes for backup
type FileInfo struct {
	Path          string
	Name          string
	FileType      rune
	Size          int64
	Mode          uint32
	Owner         uint32
	Group         uint32
	ModTime       time.Time
	AccessTime    time.Time
	ChangeTime    time.Time
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

var fileTypeLookup = [16]struct {
	typeCode                                                rune
}{
	0:  {typeCode: '?'},                  // Unknown
	1:  {typeCode: 'p'},    // S_IFIFO
	2:  {typeCode: 'c'},  // S_IFCHR
	3:  {typeCode: '?'},                  // Unused
	4:  {typeCode: 'd'},     // S_IFDIR
	5:  {typeCode: '?'},                  // Unused
	6:  {typeCode: 'b'},  // S_IFBLK
	7:  {typeCode: '?'},                  // Unused
	8:  {typeCode: 'f'}, // S_IFREG
	9:  {typeCode: '?'},                  // Unused
	10: {typeCode: 'l'}, // S_IFLNK
	11: {typeCode: '?'},                  // Unused
	12: {typeCode: 's'},  // S_IFSOCK
	13: {typeCode: '?'},                  // Unused
	14: {typeCode: '?'},                  // Unused
	15: {typeCode: '?'},                  // Unused
}

func getFileType(mode uint32) rune {
	fileType := (mode >> 12) & 0xF
	lookup := fileTypeLookup[fileType]

	return lookup.typeCode
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
	fileType := getFileType(mode)

	// Extract basename without allocation when possible
	name := filepath.Base(path)

	fileInfo := FileInfo{
		Path:       path,
		Name:       name,
		FileType:   fileType,
		Size:       int64(stat.Size),
		Mode:       mode,
		Owner:      stat.Uid,
		Group:      stat.Gid,
		ModTime:    time.Unix(stat.Mtime.Sec, int64(stat.Mtime.Nsec)),
		AccessTime: time.Unix(stat.Atime.Sec, int64(stat.Atime.Nsec)),
		ChangeTime: time.Unix(stat.Ctime.Sec, int64(stat.Ctime.Nsec)),
	}

	// Get symlink target only if needed - optimized readlink
	if (fileType == 'l') {
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

	err := filepath.WalkDir(sourcePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		

		// Use high-performance direct syscall for metadata
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

func IsDirectory(filepath string) (bool, error) {
	info, err := os.Stat(filepath)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}
