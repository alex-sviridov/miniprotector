// restorefile.go implements phase 2 of `rwfs restore`: fetching a
// resolved file's chunks via RestoreFile and writing them to its
// (dest_path-renamed) destination, verifying per-chunk BLAKE3 and the
// whole-file CRC32 exactly as verify.go's verifyFile already does -- see
// docs/superpowers/specs/2026-08-17-restore-file-content-design.md.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/checksum"
	"lukechampine.com/blake3"
)

const (
	// defaultRestoreFilePerm is every created/overwritten file's mode -- a
	// stub, like createRestoreDirectory's 0o755, pending real
	// captured-permission restore in a future round.
	defaultRestoreFilePerm = 0o644
	// restoreWriteBufferSize coalesces 64KB chunks (see
	// workload/filesystem/chunker.go's ChunkSize) into far fewer syscalls --
	// ~16 chunks per Write instead of one syscall per chunk.
	restoreWriteBufferSize = 1 << 20 // 1MB
)

// restoreFile is one file phase 2 must fetch and write to its
// (dest_path-renamed) destination.
type restoreFile struct {
	FileUUID string
	Source   string
	Path     string
	DestPath string
}

// restoreFileResult is writeRestoreFile's outcome. Source/Path/DestPath
// are carried through unchanged from the input restoreFile so the driver
// (restore.go's restoreFileContent) can log without a side lookup.
type restoreFileResult struct {
	Source, Path, DestPath string
	Bytes                  int64
	Skipped                bool
	Err                    error
	// Retryable is true only when Err comes from the network/RPC-facing
	// call to bwfs (connect, Recv, or an unexpected event type) -- the
	// same class of failure verifyFileWithRetry already retries. It
	// stays false (the zero value) for every other failure: integrity
	// mismatches (blake3_mismatch, crc_mismatch), a pre-existing
	// directory at the destination, and local disk I/O errors -- none of
	// which retrying can fix. See writeRestoreFileWithRetry.
	Retryable bool
}

// writeRestoreFile fetches f's content via RestoreFile and writes it to
// f.DestPath, verifying per-chunk BLAKE3 and the whole-file CRC32 exactly
// as verifyFile (verify.go) does. A pre-existing destination file is
// skipped (not an error) when overwrite is false; a pre-existing
// directory at the destination is always a hard error, regardless of
// overwrite. On any failure, a partially-written destination file is
// removed (best-effort) so a corrupt/incomplete file never looks
// restored. writeRestoreFile does no logging itself -- see
// restoreFileContent's per-result handling (restore.go).
func writeRestoreFile(parent context.Context, client pb.RestoreServiceClient, f restoreFile, overwrite bool) restoreFileResult {
	base := restoreFileResult{Source: f.Source, Path: f.Path, DestPath: f.DestPath}

	info, statErr := os.Stat(f.DestPath)
	switch {
	case statErr == nil && info.IsDir():
		base.Err = fmt.Errorf("path exists and is a directory: %s", f.DestPath)
		return base
	case statErr == nil && !overwrite:
		base.Skipped = true
		return base
	case statErr != nil && !os.IsNotExist(statErr):
		base.Err = statErr
		return base
	}
	// Falls through here in exactly two cases: the file exists and
	// overwrite is true (will truncate below), or it doesn't exist at all
	// (will create below) -- both proceed identically via O_CREATE|O_TRUNC.

	ctx, touch, _, _, stop := withStallWatchdog(parent, streamIdleTimeout)
	defer stop()

	stream, err := client.RestoreFile(ctx, &pb.RestoreRequest{FileUuid: f.FileUUID})
	if err != nil {
		base.Err = fmt.Errorf("stream error: %w", err)
		base.Retryable = true
		return base
	}

	firstEvent, err := stream.Recv()
	if err != nil {
		base.Err = fmt.Errorf("stream error: %w", err)
		base.Retryable = true
		return base
	}
	touch()
	meta := firstEvent.GetMeta()
	if meta == nil {
		base.Err = fmt.Errorf("stream error: expected RestoreFileMeta as first event")
		base.Retryable = true
		return base
	}

	out, err := os.OpenFile(f.DestPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, defaultRestoreFilePerm)
	if err != nil {
		base.Err = err
		return base
	}
	success := false
	closed := false
	defer func() {
		if !closed {
			out.Close() // Windows disallows removing a file that's still open -- close before remove.
		}
		if !success {
			os.Remove(f.DestPath)
		}
	}()

	if err := out.Truncate(meta.Size); err != nil {
		base.Err = err
		return base
	}

	bufw := bufio.NewWriterSize(out, restoreWriteBufferSize)
	hasher := crc32.NewIEEE()
	var written int64

	for {
		event, err := stream.Recv()
		if err != nil {
			base.Err = fmt.Errorf("stream error: %w", err)
			base.Retryable = true
			return base
		}
		touch()
		chunk := event.GetChunk()
		if chunk == nil {
			base.Err = fmt.Errorf("stream error: expected RestoreChunk")
			base.Retryable = true
			return base
		}

		computed := blake3.Sum256(chunk.Data)
		if !bytes.Equal(computed[:], chunk.Hash) {
			base.Err = fmt.Errorf("blake3_mismatch: chunk %d", chunk.Index)
			return base
		}

		n, err := bufw.Write(chunk.Data)
		if err != nil {
			base.Err = fmt.Errorf("write error: %w", err)
			return base
		}
		written += int64(n)
		checksum.FeedChunk(hasher, crc32.ChecksumIEEE(chunk.Data))

		if chunk.Eof {
			break
		}
	}

	if err := bufw.Flush(); err != nil {
		base.Err = fmt.Errorf("write error: %w", err)
		return base
	}

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
	if !bytes.Equal(buf[:], meta.ExpectedChecksum) {
		base.Err = fmt.Errorf("crc_mismatch")
		return base
	}

	closeErr := out.Close()
	closed = true
	if closeErr != nil {
		base.Err = fmt.Errorf("write error: close: %w", closeErr)
		return base
	}

	success = true
	base.Bytes = written
	return base
}

// writeRestoreFileWithRetry retries writeRestoreFile up to maxRetries
// times on a retryable (network/RPC-facing) failure, sharing withRetry's
// backoff with verifyFileWithRetry (retry.go) so the two commands can't
// drift apart. A retry is safe with no extra cleanup: writeRestoreFile's
// own defer already removes any partial destination file before
// returning on failure, so each attempt starts from a clean slate (fresh
// stat, open, truncate). A non-retryable failure (integrity mismatch,
// pre-existing directory, local disk error) surfaces on the first
// attempt with no backoff wait.
func writeRestoreFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, f restoreFile, overwrite bool, maxRetries int) restoreFileResult {
	return withRetry(ctx, logger.With("source", f.Source, "path", f.Path, "dest_path", f.DestPath), maxRetries,
		func(ctx context.Context) restoreFileResult { return writeRestoreFile(ctx, client, f, overwrite) },
		func(r restoreFileResult) bool { return r.Err != nil && r.Retryable },
		func(r restoreFileResult) string { return r.Err.Error() },
	)
}
