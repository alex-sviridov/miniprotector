//go:build linux

package main

import "syscall"

// diskUsage returns total and used bytes of the filesystem holding path.
func diskUsage(path string) (total, used uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	total = st.Blocks * uint64(st.Bsize)
	used = total - st.Bavail*uint64(st.Bsize)
	return total, used, nil
}
