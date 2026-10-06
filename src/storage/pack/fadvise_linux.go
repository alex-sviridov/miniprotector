package pack

import (
	"os"

	"golang.org/x/sys/unix"
)

// dropPageCache tells the kernel the file's clean pages will not be needed
// soon, so a big backup does not evict everything else from the page cache.
// It is only advice: errors are ignored and nothing depends on it.
func dropPageCache(f *os.File) {
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}
