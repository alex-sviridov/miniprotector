//go:build linux

package files

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// getUnixFileInfo extracts detailed file information on Unix systems
func getFileInfo(path string) (FileInfo, error) {
	// print current path
	info, err := os.Lstat(path)
	if err != nil {
		return FileInfo{}, fmt.Errorf("os.Lstat(path): %v", err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileInfo{}, fmt.Errorf("info.Sys().(*syscall.Stat_t): %v", unix.ENOSYS)
	}
	
	fileInfo := FileInfo{
		Path:       path,
		Name:       info.Name(),
		Size:       info.Size(),
		Mode:       info.Mode(), // Full mode (type + permissions)
		Owner:      stat.Uid,
		Group:      stat.Gid,
		ModTime:    info.ModTime(),
		AccessTime: time.Unix(stat.Atim.Sec, stat.Atim.Nsec),
		CTime:      time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec),
		ACL:        getACL(path), // Extract platform-specific ACLs
	}

	// Read symlink target if it's a symbolic link
	if info.Mode()&fs.ModeSymlink != 0 {
		if target, err := os.Readlink(path); err == nil {
			fileInfo.SymlinkTarget = target
		}
	}

	return fileInfo, nil
}

// getACL extracts platform-specific ACL data
func getACL(path string) []byte {
	// Unix/Linux: This would require the 'acl' package or syscalls to getxattr
	// Implementation would use getxattr with "system.posix_acl_access" and "system.posix_acl_default"
	return nil
}
