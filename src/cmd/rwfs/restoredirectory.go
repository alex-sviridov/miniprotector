// restoredirectory.go implements phase 1 of `rwfs restore`: recreating a
// resolved selection's directory structure on the destination filesystem,
// before any file content is written (phase 2 -- see restorefile.go and
// docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md).
package main

import (
	"fmt"
	"os"
)

// restoreDirectory is one directory phase 1 must ensure exists at its
// (dest_path-renamed) destination.
type restoreDirectory struct {
	DestPath string
}

// createRestoreDirectory checks whether dir.DestPath exists, creates it if
// not (its parent must already exist -- callers are responsible for
// creating in parent-before-child order), and would apply captured
// permissions/ownership once that metadata is threaded through from bwfs.
//
// TODO: apply dir's captured permissions/ownership once FileRow carries
// the metadata blob -- deferred until that step is actually built (see
// this design's Non-Goals).
func createRestoreDirectory(dir restoreDirectory) (created bool, err error) {
	info, statErr := os.Stat(dir.DestPath)
	switch {
	case statErr == nil && info.IsDir():
		return false, nil
	case statErr == nil:
		return false, fmt.Errorf("path exists and is not a directory: %s", dir.DestPath)
	case !os.IsNotExist(statErr):
		return false, statErr
	}
	if err := os.Mkdir(dir.DestPath, 0o755); err != nil {
		return false, err
	}
	return true, nil
}
