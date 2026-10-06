//go:build windows

package main

import "golang.org/x/sys/windows"

// diskUsage returns total and used bytes of the filesystem holding path.
func diskUsage(path string) (total, used uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var avail, tot, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &tot, &free); err != nil {
		return 0, 0, err
	}
	return tot, tot - avail, nil
}
