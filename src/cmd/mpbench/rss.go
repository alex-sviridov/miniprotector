package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// peakRSS returns the high-water resident set size of a running process, from
// VmHWM in /proc/<pid>/status (Linux).
func peakRSS(pid int) (int64, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "VmHWM:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest) // "  12345 kB"
		if len(fields) == 0 {
			break
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("VmHWM not found for pid %d", pid)
}

// resetPeakRSS restarts a process's VmHWM from its current RSS, so the next
// peakRSS reports the peak since this call.
func resetPeakRSS(pid int) error {
	return os.WriteFile(fmt.Sprintf("/proc/%d/clear_refs", pid), []byte("5"), 0o200)
}

// dropPageCache flushes dirty pages and drops the Linux page, dentry and
// inode caches (needs root).
func dropPageCache() error {
	syscall.Sync()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200)
}
