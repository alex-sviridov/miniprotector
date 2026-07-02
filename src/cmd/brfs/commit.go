package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
)

const (
	commitMaxAttempts = 3
	commitBaseDelay   = 2 * time.Second
)

// successFileHash computes the SHA256 over the sorted, newline-joined IDs
// of every file brfs believes it backed up successfully this run — the same
// computation bwfs performs server-side from its own file_versions rows.
func successFileHash(filesBackupState map[string]bool) []byte {
	ids := make([]string, 0, len(filesBackupState))
	for id, ok := range filesBackupState {
		if ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return sum[:]
}

// commitBackupJob calls BackupCommit, retrying a few times with backoff on
// transport error — this call is the only positive signal that a whole
// backup succeeded, so it's worth insulating from a single flaky blip. A
// clean response (even Success: false, meaning the server rejected the
// backup as incomplete) is returned immediately without retrying — only
// transport-level errors trigger a retry.
func commitBackupJob(ctx context.Context, logger *slog.Logger, client pb.BackupServiceClient, hash []byte) (bool, error) {
	var lastErr error
	for attempt := 1; attempt <= commitMaxAttempts; attempt++ {
		resp, err := client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: hash})
		if err == nil {
			return resp.Success, nil
		}
		lastErr = err
		logger.Warn("BackupCommit failed, retrying", "attempt", attempt, "error", err)
		if attempt < commitMaxAttempts {
			time.Sleep(commitBaseDelay * time.Duration(attempt))
		}
	}
	return false, fmt.Errorf("BackupCommit failed after %d attempts: %w", commitMaxAttempts, lastErr)
}
