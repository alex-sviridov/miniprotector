package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readCursor returns the last replicated seq, or 0 if the cursor file is
// missing or corrupt (first run, or unparseable content — replication
// starts from the beginning). A corrupt cursor is treated the same as a
// missing one rather than propagated as a fatal error: the tradeoff is a
// one-time large resend burst, accepted rather than adding a separate
// integrity check for a single-integer file.
func readCursor(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read cursor: %w", err)
	}
	seq, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, nil
	}
	return seq, nil
}

// writeCursor persists seq atomically: write to a temp file in the same
// directory, then rename over the target. A crash mid-write never leaves a
// torn cursor file, since rename is atomic on the same filesystem.
func writeCursor(path string, seq int64) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(seq, 10)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write temp cursor: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename cursor into place: %w", err)
	}
	return nil
}
