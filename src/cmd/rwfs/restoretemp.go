// restoretemp.go names, creates and sweeps the hidden temp files restore
// writes before committing them (see restoresink.go).
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	tempMarker      = ".mptmp-"
	tempSuffixLen   = 8   // hex digits after tempMarker
	maxTempBaseLen  = 200 // leaves room under NAME_MAX (255) for "." + marker + suffix
	tempCreateTries = 5
)

// createTempFile exclusively creates "<dir>/.<base>.mptmp-<8 hex>" next to
// destPath, so a later rename stays on one filesystem.
func createTempFile(destPath string) (*os.File, error) {
	dir, base := filepath.Split(destPath)
	if len(base) > maxTempBaseLen {
		base = base[:maxTempBaseLen]
	}
	for i := 0; i < tempCreateTries; i++ {
		var rnd [tempSuffixLen / 2]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, "."+base+tempMarker+hex.EncodeToString(rnd[:]))
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, defaultRestoreFilePerm)
		if err == nil || !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, fmt.Errorf("create temp file for %s: too many name collisions", destPath)
}

// isTempName reports whether name looks like a createTempFile name.
func isTempName(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	idx := strings.LastIndex(name, tempMarker)
	if idx < 2 { // needs "." plus at least one base character before the marker
		return false
	}
	suffix := name[idx+len(tempMarker):]
	if len(suffix) != tempSuffixLen {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

// destDirs returns the unique parent directories of the files' destinations.
func destDirs(files []restoreFile) []string {
	seen := make(map[string]struct{}, len(files))
	var dirs []string
	for _, f := range files {
		d := filepath.Dir(f.DestPath)
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		dirs = append(dirs, d)
	}
	return dirs
}

// sweepStaleTemp removes leftover temp files (from a crashed earlier run)
// directly inside each directory, so a rerun starts clean. Unreadable or
// missing directories are skipped. Returns how many files were removed.
func sweepStaleTemp(logger *slog.Logger, dirs []string) int {
	removed := 0
	seen := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !isTempName(e.Name()) {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if err := os.Remove(path); err == nil {
				removed++
				logger.Info("removed stale restore temp file", "path", path)
			}
		}
	}
	return removed
}
