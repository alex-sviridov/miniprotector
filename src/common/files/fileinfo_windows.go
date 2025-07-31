//go:build windows

package files

import (
	"io/fs"
	"os"
	"time"
	"golang.org/x/sys/windows"
)

// getWindowsFileInfo extracts detailed file information on Windows systems
func getFileInfo(path string) (FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return FileInfo{}, err
	}

	fileInfo := FileInfo{
		Path:    path,
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(), // Full mode (type + permissions)
		ModTime: info.ModTime(),
		ACL:     getACL(path), // Extract platform-specific ACLs
	}

	// Extract Windows-specific information
	if winStat, ok := info.Sys().(*windows.Win32FileAttributeData); ok {
		// Store Windows attributes as 4 bytes
		attrs := make([]byte, 4)
		attrs[0] = byte(winStat.FileAttributes)
		attrs[1] = byte(winStat.FileAttributes >> 8)
		attrs[2] = byte(winStat.FileAttributes >> 16)
		attrs[3] = byte(winStat.FileAttributes >> 24)
		fileInfo.Attributes = attrs

		// Convert Windows FILETIME to time.Time
		fileInfo.AccessTime = time.Unix(0, winStat.LastAccessTime.Nanoseconds())
		fileInfo.CTime = time.Unix(0, winStat.CreationTime.Nanoseconds()) // Creation time as CTime
	} else {
		// Fallback for cases where Win32FileAttributeData is not available
		fileInfo.AccessTime = info.ModTime() // Best we can do
		fileInfo.CTime = info.ModTime()
	}

	// Windows doesn't have traditional Unix owner/group, set to 0
	fileInfo.Owner = 0
	fileInfo.Group = 0

	// Handle Windows symbolic links and junctions
	if info.Mode()&fs.ModeSymlink != 0 {
		if target, err := os.Readlink(path); err == nil {
			fileInfo.SymlinkTarget = target
		}
	}

	return fileInfo, nil
}

// getACL extracts platform-specific ACL data
func getACL(path string) []byte {
	// Note: This would require Windows security syscalls like GetNamedSecurityInfo
	// Implementation would use syscall to advapi32.dll GetNamedSecurityInfoW
	return nil
}
