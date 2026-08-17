# Restore: File Content Phase Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `rwfs restore` actually write file content to the destination filesystem (phase 2), on top of the already-real directory-structure phase (phase 1) -- fetching each resolved file's chunks via `RestoreFile`, verifying per-chunk BLAKE3 and whole-file CRC32 as it writes, respecting `--overwrite`, and reporting final statistics.

**Architecture:** One new file (`restorefile.go`) holds the pure per-file worker (`writeRestoreFile`) that fetches, verifies, and writes a single file, mirroring `restoredirectory.go`'s existing per-directory shape. `restore.go` gains a new phase-2 driver (`restoreFileContent`, alongside the existing phase-1 driver `createRestoreDirectoryStructure`) that runs the existing generic `runWorkerPool` over every resolved file, sized by a new `--streams` flag, cancelling every other in-flight transfer the instant one fails.

**Tech Stack:** Go 1.26, gRPC/protobuf (already-generated `src/api/restore.pb.go`, no proto change needed), `bufio` for buffered writes, `lukechampine.com/blake3` + `hash/crc32` for integrity checks (same as `verify.go`), `google.golang.org/grpc/test/bufconn` + `github.com/stretchr/testify` for tests.

## Global Constraints

- Module path: `github.com/alex-sviridov/miniprotector`.
- All source lives under `src/`; run tests from the repo root via `cd src && go test ./cmd/rwfs/...` (or `make test`).
- No proto changes -- `RestoreService.RestoreFile` (`src/api/restore.pb.go`) already exists and is unchanged by this plan.
- No retries in phase 2 (unlike `verify`): any failure aborts the whole restore immediately.
- No `file.Sync()` -- matches the rest of the codebase, which never calls it.
- No OS-specific (`//go:build`) code -- everything phase 2 needs (`os.OpenFile`, `bufio.Writer`, `Truncate`, `Close`, `Remove`) is portable standard library.
- Default file permission for every created/overwritten file: `0o644` (a stub, like directories' `0o755` -- no real permission restore yet).
- Per-file success (`"file written"` / `"file skipped, already exists"`) logs at `slog.LevelDebug` only, called unconditionally (no manual `if` -- matches `brfs`'s existing per-item logging convention, e.g. `onefile.go`'s `logger.Debug("Sending chunk data")`). `--quiet` does not gate it.
- On any phase-2 failure, no summary line is logged -- mirrors phase 1's existing, already-tested convention (`TestRunRestore_AbortsOnDirectoryCreationFailureBeforeSummary`).
- Follow `.claude/CLAUDE.md`: update `docs/components/rwfs.md`, `docs/protocols/restore.md`, `docs/ARCHITECTURE.md`, and add a `CHANGELOG.md` entry before this is mergeable.
- Design spec: `docs/superpowers/specs/2026-08-17-restore-file-content-design.md` -- consult it for anything this plan doesn't spell out.

---

## File Map

| Path | Status | Responsibility |
|------|--------|----------------|
| `src/cmd/rwfs/restorefile.go` | Create | `restoreFile`, `restoreFileResult` types; `writeRestoreFile`, the pure per-file fetch/verify/write worker |
| `src/cmd/rwfs/restorefile_test.go` | Create | Unit tests for `writeRestoreFile` (skip, overwrite, hard errors, BLAKE3/CRC32 mismatch + cleanup, missing parent) |
| `src/cmd/rwfs/restore.go` | Modify | Adds `restoreFileContent` (phase 2 driver); `runRestoreWithConn`/`runRestore` gain a `streams int` parameter and now collect `[]restoreFile` alongside `[]restoreDirectory`, running phase 2 once phase 1 succeeds |
| `src/cmd/rwfs/restore_test.go` | Modify | `runRestoreWithDialer` gains a `streams` parameter (all existing call sites updated); new end-to-end phase-2 tests (real write, overwrite skip/replace, write failure abort, concurrent-cancellation) |
| `src/cmd/rwfs/arguments.go` | Modify | New `--streams` flag on `restore` (default 4, validated); `--overwrite`'s help text updated (no longer "logged only") |
| `src/cmd/rwfs/arguments_test.go` | Modify | New tests: `--streams` default and validation for `restore` |
| `src/cmd/rwfs/main.go` | Modify | Threads `arguments.Streams` into the `runRestore` call |
| `docs/components/rwfs.md` | Modify | `## restore` section: phase 2 is real, `--streams` flag documented, new log lines documented |
| `docs/protocols/restore.md` | Modify | CLI → RPC Mapping: `rwfs restore --rules-stdin` now calls `RestoreFile` too |
| `docs/ARCHITECTURE.md` | Modify | Component table, agent description, Restore/Verify Process section, mermaid comment |
| `CHANGELOG.md` | Modify | New entry, most recent first |

---

## Task 1: `restorefile.go` -- `writeRestoreFile`, the per-file primitive

**Files:**
- Create: `src/cmd/rwfs/restorefile.go`
- Test: `src/cmd/rwfs/restorefile_test.go`

**Interfaces:**
- Consumes: `pb.RestoreServiceClient` (existing, `src/api/restore.pb.go`), `withStallWatchdog`/`streamIdleTimeout` (`watchdog.go`, existing), `checksum.FeedChunk` (`common/checksum`, existing).
- Produces: `type restoreFile struct { FileUUID, Source, Path, DestPath string }`; `type restoreFileResult struct { Source, Path, DestPath string; Bytes int64; Skipped bool; Err error }`; `func writeRestoreFile(parent context.Context, client pb.RestoreServiceClient, f restoreFile, overwrite bool) restoreFileResult`. Task 2's `restoreFileContent` (`restore.go`) is the only caller.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/rwfs/restorefile_test.go`:

```go
package main

import (
	"context"
	"net"
	"os"
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// dialRestoreClient starts srv on an in-memory bufconn listener and
// returns a RestoreServiceClient dialed against it -- this file only ever
// needs RestoreServiceServer (not ListServiceServer), unlike
// restore_test.go's full end-to-end fixtures, so it gets its own minimal
// dial helper rather than reusing runRestoreWithDialer.
func dialRestoreClient(t *testing.T, srv pb.RestoreServiceServer) pb.RestoreServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterRestoreServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.GracefulStop)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return pb.NewRestoreServiceClient(conn)
}

// hashMismatchRestoreServer serves a valid Meta event followed by one
// chunk whose Hash field doesn't match its Data -- drives
// writeRestoreFile's BLAKE3 mismatch abort path without needing a real
// corrupted store.
type hashMismatchRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
}

func (s *hashMismatchRestoreServer) RestoreFile(_ *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	data := []byte("some file content")
	if err := stream.Send(&pb.RestoreEvent{Payload: &pb.RestoreEvent_Meta{Meta: &pb.RestoreFileMeta{
		Size:             int64(len(data)),
		ChunkCount:       1,
		ExpectedChecksum: []byte{0, 0, 0, 0},
	}}}); err != nil {
		return err
	}
	return stream.Send(&pb.RestoreEvent{Payload: &pb.RestoreEvent_Chunk{Chunk: &pb.RestoreChunk{
		Index: 0,
		Hash:  []byte{0x00}, // deliberately wrong
		Data:  data,
		Eof:   true,
	}}})
}

// crcMismatchRestoreServer serves a Meta event with a deliberately wrong
// ExpectedChecksum, followed by one chunk whose Hash correctly matches its
// Data -- drives writeRestoreFile's whole-file CRC32 mismatch path, distinct
// from the per-chunk BLAKE3 path above.
type crcMismatchRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
}

func (s *crcMismatchRestoreServer) RestoreFile(_ *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	data := []byte("some file content")
	hash := blake3Sum(data)
	if err := stream.Send(&pb.RestoreEvent{Payload: &pb.RestoreEvent_Meta{Meta: &pb.RestoreFileMeta{
		Size:             int64(len(data)),
		ChunkCount:       1,
		ExpectedChecksum: []byte{0xDE, 0xAD, 0xBE, 0xEF}, // deliberately wrong
	}}}); err != nil {
		return err
	}
	return stream.Send(&pb.RestoreEvent{Payload: &pb.RestoreEvent_Chunk{Chunk: &pb.RestoreChunk{
		Index: 0,
		Hash:  hash,
		Data:  data,
		Eof:   true,
	}}})
}

func TestWriteRestoreFile_WritesFileContent(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	chunks := [][]byte{[]byte("hello "), []byte("world!")}
	fileUUID := seedRestorableFileChunks(t, store, "hosta", "/data/a.txt", "job1", 1000, chunks)

	client := dialRestoreClient(t, &realRestoreServer{store: store})

	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.NoError(t, result.Err)
	assert.False(t, result.Skipped)
	assert.EqualValues(t, 12, result.Bytes)

	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "hello world!", string(got))
}

func TestWriteRestoreFile_SkipsWhenExistsAndNotOverwrite(t *testing.T) {
	restoreSrv := &recordingRestoreServer{}
	client := dialRestoreClient(t, restoreSrv)

	destPath := t.TempDir() + "/a.txt"
	require.NoError(t, os.WriteFile(destPath, []byte("original"), 0o644))

	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "does-not-matter", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.NoError(t, result.Err)
	assert.True(t, result.Skipped)
	assert.Empty(t, restoreSrv.Requested(), "RestoreFile must never be called for a skipped file")

	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "original", string(got), "a skipped file must be left untouched")
}

func TestWriteRestoreFile_OverwritesWhenExistsAndOverwriteTrue(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	fileUUID := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte("new content"))

	client := dialRestoreClient(t, &realRestoreServer{store: store})

	destPath := t.TempDir() + "/a.txt"
	require.NoError(t, os.WriteFile(destPath, []byte("stale content, longer than the replacement"), 0o644))

	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, true)

	require.NoError(t, result.Err)
	assert.False(t, result.Skipped)

	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "new content", string(got))
}

func TestWriteRestoreFile_DirectoryAtDestinationIsHardError(t *testing.T) {
	restoreSrv := &recordingRestoreServer{}
	client := dialRestoreClient(t, restoreSrv)

	destPath := t.TempDir() + "/a-directory"
	require.NoError(t, os.Mkdir(destPath, 0o755))

	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "does-not-matter", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "directory")
	assert.Empty(t, restoreSrv.Requested(), "RestoreFile must never be called when the destination is a directory")
}

func TestWriteRestoreFile_BlakeMismatchAbortsAndRemovesPartialFile(t *testing.T) {
	client := dialRestoreClient(t, &hashMismatchRestoreServer{})

	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "blake3_mismatch")
	_, statErr := os.Stat(destPath)
	assert.True(t, os.IsNotExist(statErr), "a BLAKE3 mismatch must remove the partial file")
}

func TestWriteRestoreFile_CRCMismatchAbortsAndRemovesPartialFile(t *testing.T) {
	client := dialRestoreClient(t, &crcMismatchRestoreServer{})

	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "crc_mismatch")
	_, statErr := os.Stat(destPath)
	assert.True(t, os.IsNotExist(statErr), "a CRC32 mismatch must remove the partial file")
}

func TestWriteRestoreFile_StreamErrorReturnsErrorAndCreatesNoFile(t *testing.T) {
	restoreSrv := &recordingRestoreServer{} // always fails RestoreFile with codes.Unimplemented
	client := dialRestoreClient(t, restoreSrv)

	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
	_, statErr := os.Stat(destPath)
	assert.True(t, os.IsNotExist(statErr), "a stream error before any chunk arrives must never create a file")
}

func TestWriteRestoreFile_MissingParentDirectoryIsHardError(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	fileUUID := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte("content"))

	client := dialRestoreClient(t, &realRestoreServer{store: store})

	destPath := t.TempDir() + "/missing-parent/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
}

// blake3Sum is a tiny local wrapper so crcMismatchRestoreServer above
// doesn't need its own top-level blake3 import alias collision with
// anything else in this file.
func blake3Sum(data []byte) []byte {
	h := blake3Hash(data)
	return h[:]
}
```

`recordingRestoreServer`, `realRestoreServer`, `seedRestorableFile`, `seedRestorableFileChunks` are all
already defined in `verify_test.go` (same package `main`) -- no import or duplication needed.

- [ ] **Step 2: Fix the `blake3Sum` helper to actually call blake3**

The stub above needs a real `blake3Hash` -- rather than inventing a second wrapper, replace the
`blake3Sum` function at the bottom of the test file (from Step 1) with a direct call:

```go
func blake3Sum(data []byte) []byte {
	sum := blake3.Sum256(data)
	return sum[:]
}
```

Add `"lukechampine.com/blake3"` to this file's import block.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestWriteRestoreFile -v`
Expected: FAIL -- `restoreFile`/`restoreFileResult`/`writeRestoreFile` don't exist yet (compile error).

- [ ] **Step 4: Implement `restorefile.go`**

Create `src/cmd/rwfs/restorefile.go`:

```go
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
		return base
	}

	firstEvent, err := stream.Recv()
	if err != nil {
		base.Err = fmt.Errorf("stream error: %w", err)
		return base
	}
	touch()
	meta := firstEvent.GetMeta()
	if meta == nil {
		base.Err = fmt.Errorf("stream error: expected RestoreFileMeta as first event")
		return base
	}

	out, err := os.OpenFile(f.DestPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, defaultRestoreFilePerm)
	if err != nil {
		base.Err = err
		return base
	}
	success := false
	defer func() {
		out.Close() // Windows disallows removing a file that's still open -- close before remove.
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

	for {
		event, err := stream.Recv()
		if err != nil {
			base.Err = fmt.Errorf("stream error: %w", err)
			return base
		}
		touch()
		chunk := event.GetChunk()
		if chunk == nil {
			base.Err = fmt.Errorf("stream error: expected RestoreChunk")
			return base
		}

		computed := blake3.Sum256(chunk.Data)
		if !bytes.Equal(computed[:], chunk.Hash) {
			base.Err = fmt.Errorf("blake3_mismatch: chunk %d", chunk.Index)
			return base
		}

		if _, err := bufw.Write(chunk.Data); err != nil {
			base.Err = fmt.Errorf("write error: %w", err)
			return base
		}
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

	success = true
	base.Bytes = meta.Size
	return base
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestWriteRestoreFile -v`
Expected: PASS, all 8 tests.

- [ ] **Step 6: Run the full rwfs package test suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/rwfs/restorefile.go src/cmd/rwfs/restorefile_test.go
git commit -m "feat(rwfs): add writeRestoreFile, the per-file phase 2 primitive

Fetches a file's chunks via RestoreFile, verifies per-chunk BLAKE3
and the whole-file CRC32 exactly as verifyFile does, writes through
a buffered writer sized above the 64KB chunk size, and removes any
partial file on failure. Overwrite semantics and the
directory-at-destination hard error are handled before any RPC call.
No caller yet -- Task 2 wires this into rwfs restore's phase 2
driver."
```

---

## Task 2: `restore.go` -- phase 2 driver and wiring

**Files:**
- Modify: `src/cmd/rwfs/restore.go`
- Modify: `src/cmd/rwfs/restore_test.go`

**Interfaces:**
- Consumes: Task 1's `restoreFile`/`restoreFileResult`/`writeRestoreFile`; the existing generic `runWorkerPool[T, R]` (`workerpool.go`).
- Produces: `runRestoreWithConn`/`runRestore` gain a `streams int` parameter (new signatures:
  `runRestoreWithConn(logger *slog.Logger, conn *grpc.ClientConn, overwrite bool, rules []RestoreRule, quiet bool, streams int, jobID string) error`;
  `runRestore(logger *slog.Logger, host string, port int, overwrite bool, stdin io.Reader, quiet bool, streams int, certsDir, jobID string) error`).
  `func restoreFileContent(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, files []restoreFile, overwrite bool, streams int) error`.
  Task 3 (CLI wiring) is the only caller of the new `runRestore` parameter from outside tests.

- [ ] **Step 1: Update `runRestoreWithDialer` and every existing call site in `restore_test.go`**

In `src/cmd/rwfs/restore_test.go`, change the helper's signature and its call to `runRestoreWithConn`:

```go
func runRestoreWithDialer(t *testing.T, logger *slog.Logger, lis *bufconn.Listener, rulesJSON string, overwrite bool, streams int) error {
	t.Helper()

	rules, err := parseRulesStdin(strings.NewReader(rulesJSON))
	require.NoError(t, err)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	return runRestoreWithConn(logger, conn, overwrite, rules, false, streams, "test-job")
}
```

Then update every existing call site. Seven of the eight existing calls are the exact same line
`err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)` -- replace all of them at once:

```bash
cd src && sed -i 's/runRestoreWithDialer(t, logger, lis, rulesJSON, false)/runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)/g' cmd/rwfs/restore_test.go
```

The eighth (`TestRunRestore_LogsResolvedFileWithRenamedDestPath`) passes `true`:

```bash
cd src && sed -i 's/runRestoreWithDialer(t, logger, lis, rulesJSON, true)/runRestoreWithDialer(t, logger, lis, rulesJSON, true, 4)/g' cmd/rwfs/restore_test.go
```

- [ ] **Step 2: Run the existing tests to confirm they still compile and pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestRunRestore -v`
Expected: FAIL to compile -- `runRestoreWithConn` doesn't accept a `streams` argument yet. This is
expected; proceed to Step 4. (If it compiles at this point, Step 1 didn't actually change
`runRestoreWithConn`'s call inside the helper -- double check.)

- [ ] **Step 3: Write the new failing tests**

Add to `src/cmd/rwfs/restore_test.go` (needs new imports -- add `"io"`, `"google.golang.org/grpc/codes"`,
and `"google.golang.org/grpc/status"` to the existing import block; `"time"` is already imported):

```go
func TestRunRestore_WritesFileContent(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("vacation photo bytes"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destBase+"/recovered")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.NoError(t, err)

	got, readErr := os.ReadFile(destBase + "/recovered/vacation.jpg")
	require.NoError(t, readErr)
	assert.Equal(t, "vacation photo bytes", string(got))

	out := logBuf.String()
	assert.Contains(t, out, "restoring file content")
	assert.Contains(t, out, "restore complete")
	assert.Contains(t, out, "files_written=1")
	assert.NotContains(t, out, "file written",
		"the per-file success line must not appear at the default (Info) log level")
}

// TestRunRestore_DebugLogsPerFileSuccessLine is
// TestRunRestore_WritesFileContent's counterpart at Debug level -- proves
// the per-file "file written" line exists and is gated purely by the
// logger's level (slog.LevelDebug), not by a separate --quiet-style flag.
func TestRunRestore_DebugLogsPerFileSuccessLine(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("vacation photo bytes"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destBase+"/recovered")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.NoError(t, err)

	assert.Contains(t, logBuf.String(), "file written")
}

func TestRunRestore_OverwriteFalseSkipsExistingFile(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("new content from bwfs"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	require.NoError(t, os.Mkdir(destBase+"/recovered", 0o755))
	require.NoError(t, os.WriteFile(destBase+"/recovered/vacation.jpg", []byte("original content on disk"), 0o644))
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destBase+"/recovered")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.NoError(t, err)

	got, readErr := os.ReadFile(destBase + "/recovered/vacation.jpg")
	require.NoError(t, readErr)
	assert.Equal(t, "original content on disk", string(got), "overwrite=false must leave the existing file untouched")

	out := logBuf.String()
	assert.Contains(t, out, "files_written=0")
	assert.Contains(t, out, "skipped=1")
}

func TestRunRestore_OverwriteTrueReplacesExistingFile(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("new content from bwfs"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	require.NoError(t, os.Mkdir(destBase+"/recovered", 0o755))
	require.NoError(t, os.WriteFile(destBase+"/recovered/vacation.jpg", []byte("stale content on disk"), 0o644))
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destBase+"/recovered")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, true, 4)
	require.NoError(t, err)

	got, readErr := os.ReadFile(destBase + "/recovered/vacation.jpg")
	require.NoError(t, readErr)
	assert.Equal(t, "new content from bwfs", string(got))

	out := logBuf.String()
	assert.Contains(t, out, "files_written=1")
	assert.Contains(t, out, "skipped=0")
}

func TestRunRestore_FileWriteFailureAbortsWithoutSummary(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 5000, []byte("content"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	// A file-level rule has no accompanying folder rule, so phase 1 never
	// creates any directory -- dest_path's parent is never created, so
	// phase 2's write must fail with the parent missing.
	destBase := t.TempDir()
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"hosta","path":"/data/a.txt","include":true,"dest_path":%q}]}`, destBase+"/missing-parent/a.txt")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.Error(t, err)

	out := logBuf.String()
	assert.Contains(t, out, "restoring file content")
	assert.Contains(t, out, "failed to restore file")
	assert.NotContains(t, out, "restore complete",
		"the summary line must never be logged when phase 2 aborts")
}

// cancelDetectingRestoreServer serves file_uuid "slow" by sending Meta
// then blocking until its stream context is cancelled (recording that on
// cancelled) or a generous safety timeout elapses -- proving
// restoreFileContent's cancel-on-first-failure contract deterministically,
// without a wall-clock race. Any other file_uuid ("fail") fails
// immediately with a plain stream error.
type cancelDetectingRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
	cancelled chan struct{}
}

func (s *cancelDetectingRestoreServer) RestoreFile(req *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	if req.GetFileUuid() != "slow" {
		return status.Error(codes.Internal, "simulated failure")
	}
	if err := stream.Send(&pb.RestoreEvent{
		Payload: &pb.RestoreEvent_Meta{Meta: &pb.RestoreFileMeta{Size: 4, ChunkCount: 1, ExpectedChecksum: []byte{0, 0, 0, 0}}},
	}); err != nil {
		return err
	}
	select {
	case <-stream.Context().Done():
		close(s.cancelled)
		return stream.Context().Err()
	case <-time.After(5 * time.Second):
		return fmt.Errorf("test timeout: stream was never cancelled")
	}
}

func TestRestoreFileContent_FirstFailureCancelsOtherInFlightTransfers(t *testing.T) {
	srv := &cancelDetectingRestoreServer{cancelled: make(chan struct{})}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterRestoreServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()
	client := pb.NewRestoreServiceClient(conn)

	destBase := t.TempDir()
	files := []restoreFile{
		{FileUUID: "slow", Source: "hosta", Path: "/data/slow.bin", DestPath: destBase + "/slow.bin"},
		{FileUUID: "fail", Source: "hosta", Path: "/data/fail.bin", DestPath: destBase + "/fail.bin"},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = restoreFileContent(context.Background(), logger, client, files, false, 2)
	require.Error(t, err)

	select {
	case <-srv.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal(`the in-flight "slow" transfer was never cancelled after "fail" failed`)
	}
}
```

`net` is already imported in `restore_test.go`; check before adding it again.

- [ ] **Step 4: Implement phase 2 in `restore.go`**

Replace the whole file's content:

```go
// restore.go implements `rwfs restore`: for every row streamResolvedRows
// yields (already run through restoreResolver.Feed's precedence
// tie-break), it logs the row's source path and its computed destination
// path (restoreDestPath's dest_path rename applied), plus the run's
// overwrite setting once at start. Once resolution completes with zero
// not-found failures, phase 1 (createRestoreDirectoryStructure) recreates
// every resolved directory on the destination filesystem, then phase 2
// (restoreFileContent) fetches and writes every resolved file's content
// (writeRestoreFile, restorefile.go), verifying per-chunk BLAKE3 and the
// whole-file CRC32 as it writes -- see
// docs/superpowers/specs/2026-08-17-restore-file-content-design.md.
// Reuses streamResolvedRows, the exact same resolved-row source
// `rwfs verify --rules-stdin` uses (resolve.go) -- only the per-row
// action differs.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"google.golang.org/grpc"
)

// runRestore resolves --rules-stdin against a remote bwfs store and
// restores it: creates the resolved directory structure (phase 1), then
// fetches and writes every resolved file's content (phase 2). jobID rides
// every RPC call as outgoing job-id metadata, the same convention
// runVerify uses.
func runRestore(logger *slog.Logger, host string, port int, overwrite bool, stdin io.Reader, quiet bool, streams int, certsDir, jobID string) error {
	rules, err := parseRulesStdin(stdin)
	if err != nil {
		return err
	}

	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	return runRestoreWithConn(logger, conn, overwrite, rules, quiet, streams, jobID)
}

// runRestoreWithConn is runRestore's body, parameterized on an
// already-dialed conn -- split out purely so tests can exercise it over a
// bufconn dial without duplicating anything past the transport-level
// connect (runRestore itself is the only production caller). See
// restore_test.go's runRestoreWithDialer.
func runRestoreWithConn(logger *slog.Logger, conn *grpc.ClientConn, overwrite bool, rules []RestoreRule, quiet bool, streams int, jobID string) error {
	callCtx := jobid.Outgoing(context.Background(), jobID)

	logger.Info("restore starting", "overwrite", overwrite, "rules", len(rules))

	listClient := pb.NewListServiceClient(conn)
	restoreClient := pb.NewRestoreServiceClient(conn)
	rowsCh, resolver, errCh := streamResolvedRows(callCtx, listClient, rules)

	total := 0
	var dirs []restoreDirectory
	var files []restoreFile
	for r := range rowsCh {
		destPath := restoreDestPath(rules[r.RuleIndex], r.Row.GetPath())

		if r.Row.GetType() == "d" {
			dirs = append(dirs, restoreDirectory{DestPath: destPath})
			continue
		}

		files = append(files, restoreFile{
			FileUUID: r.Row.GetFileUuid(),
			Source:   r.Row.GetSource(),
			Path:     r.Row.GetPath(),
			DestPath: destPath,
		})

		total++
		if !quiet {
			logger.Info("resolved",
				"source", r.Row.GetSource(),
				"path", r.Row.GetPath(),
				"dest_path", destPath,
			)
		}
	}
	// Return a stream failure before anything else: resolver.NotFound below
	// is only meaningful on a fully and successfully drained stream (rules
	// that never resolved would otherwise be misreported as missing).
	// verify.go deliberately logs its summary before returning the stream
	// error instead; each command preserves the behavior it already had, and
	// the asymmetry is intentional.
	if err := <-errCh; err != nil {
		return err
	}

	warnings := 0
	for _, nf := range resolver.NotFound() {
		warnings++
		logger.Warn("resolution failed", "source", nf.Host, "path", nf.Path, "reason", nf.Reason)
	}

	logger.Info("summary", "resolved", total, "warnings", warnings)
	if warnings > 0 {
		return fmt.Errorf("%d file(s) failed resolution", warnings)
	}

	if err := createRestoreDirectoryStructure(logger, dirs); err != nil {
		return err
	}

	return restoreFileContent(callCtx, logger, restoreClient, files, overwrite, streams)
}

// createRestoreDirectoryStructure is restore's phase 1: recreate every
// resolved directory, parent before child, stopping at the first failure
// (per docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md).
// dirs may contain duplicate DestPaths -- two different rules resolving to
// the same destination -- which this collapses to one create-or-reuse
// rather than flagging as a conflict (see the design's Non-Goals).
func createRestoreDirectoryStructure(logger *slog.Logger, dirs []restoreDirectory) error {
	if len(dirs) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(dirs))
	var unique []restoreDirectory
	for _, d := range dirs {
		if seen[d.DestPath] {
			continue
		}
		seen[d.DestPath] = true
		unique = append(unique, d)
	}
	// Precompute each directory's depth once rather than recomputing
	// ancestorsOrSelfRestorePath inside less on every comparison (O(n log
	// n) redundant allocations for a value that's fixed per element). The
	// depth rides alongside its directory in one struct slice so a sort
	// swap can never desync depth from directory the way two
	// independently-sorted parallel slices could. sort.SliceStable costs
	// nothing extra here -- same-depth directories never nest (a directory
	// can't be its own sibling's parent), so ordering among same-depth
	// entries never affects correctness.
	withDepth := make([]struct {
		dir   restoreDirectory
		depth int
	}, len(unique))
	for i, d := range unique {
		withDepth[i].dir = d
		withDepth[i].depth = len(ancestorsOrSelfRestorePath(d.DestPath))
	}
	sort.SliceStable(withDepth, func(i, j int) bool {
		return withDepth[i].depth < withDepth[j].depth
	})
	for i, wd := range withDepth {
		unique[i] = wd.dir
	}

	logger.Info("creating restored directory structure")
	created, reused := 0, 0
	for _, dir := range unique {
		wasCreated, err := createRestoreDirectory(dir)
		if err != nil {
			logger.Error("failed to create restored directory", "path", dir.DestPath, "reason", err)
			return fmt.Errorf("create restored directory %s: %w", dir.DestPath, err)
		}
		if wasCreated {
			created++
		} else {
			reused++
		}
	}
	logger.Info("restored directory structure created", "created", created, "reused", reused)
	return nil
}

// restoreFileContent is restore's phase 2: fetch and write every resolved
// file's content, verifying per-chunk BLAKE3 and the whole-file CRC32 as
// it writes (writeRestoreFile, restorefile.go), stopping at the first
// failure and cancelling every other in-flight transfer immediately (per
// docs/superpowers/specs/2026-08-17-restore-file-content-design.md). Runs
// only once phase 1 has fully succeeded -- a file's destination directory
// must already exist. On failure, no summary line is logged, mirroring
// createRestoreDirectoryStructure's existing convention; the triggering
// file's own logged error carries the diagnostic.
func restoreFileContent(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, files []restoreFile, overwrite bool, streams int) error {
	if len(files) == 0 {
		return nil
	}

	logger.Info("restoring file content")

	writeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workCh := make(chan restoreFile)
	go func() {
		defer close(workCh)
		for _, f := range files {
			select {
			case workCh <- f:
			case <-writeCtx.Done():
				return
			}
		}
	}()

	resultCh := runWorkerPool(writeCtx, streams, workCh, func(ctx context.Context, f restoreFile) restoreFileResult {
		return writeRestoreFile(ctx, client, f, overwrite)
	})

	var firstErr error
	filesWritten, skipped := 0, 0
	var bytesWritten int64
	for result := range resultCh {
		switch {
		case result.Err != nil && firstErr == nil:
			firstErr = fmt.Errorf("restore file %s: %w", result.DestPath, result.Err)
			logger.Error("failed to restore file",
				"source", result.Source,
				"path", result.Path,
				"dest_path", result.DestPath,
				"reason", result.Err,
			)
			cancel()
		case result.Err != nil:
			// Expected fallout of cancel() above -- not a new independent
			// failure, so it's not logged individually.
		case result.Skipped:
			skipped++
			logger.Debug("file skipped, already exists",
				"source", result.Source, "path", result.Path, "dest_path", result.DestPath)
		default:
			filesWritten++
			bytesWritten += result.Bytes
			logger.Debug("file written",
				"source", result.Source, "path", result.Path, "dest_path", result.DestPath, "bytes", result.Bytes)
		}
	}

	if firstErr != nil {
		return firstErr
	}
	logger.Info("restore complete", "files_written", filesWritten, "bytes_written", bytesWritten, "skipped", skipped)
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run 'TestRunRestore|TestRestoreFileContent' -v`
Expected: PASS, all tests including the 6 new ones and the 8 pre-existing ones.

- [ ] **Step 6: Run the full rwfs package test suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS. (`main.go` and `arguments.go` still call `runRestore` with the old signature at this
point -- that's a compile error in `package main`'s non-test files, expected until Task 3. If `go
test ./cmd/rwfs/...` fails to build because of this, that's expected; proceed to Task 3 before
considering this task's build green. If your toolchain allows it, confirm at least
`go vet ./cmd/rwfs/... 2>&1 | grep -v main.go` shows no other issues, then continue to Task 3
immediately -- don't leave the tree in a non-building state across a commit boundary any longer than
necessary.)

- [ ] **Step 7: Commit**

```bash
git add src/cmd/rwfs/restore.go src/cmd/rwfs/restore_test.go
git commit -m "feat(rwfs): rwfs restore writes real file content (phase 2)

Phase 2 runs only once phase 1 (directories) has fully succeeded:
every resolved file is fetched via RestoreFile and written through
writeRestoreFile, streams wide via the existing generic worker pool.
On the first failure every other in-flight transfer is cancelled
immediately and no summary line is logged, mirroring phase 1's
existing abort convention. This is a breaking change to
runRestoreWithConn/runRestore's signatures (new streams parameter)
-- Task 3 wires the CLI flag through; until then cmd/rwfs does not
build."
```

Note: this commit intentionally leaves `main.go`/`arguments.go` uncompilable against the new
`runRestore` signature -- Task 3 is the very next task and fixes it. If your workflow requires every
commit to build standalone, squash Tasks 2 and 3 into one commit instead; the step-by-step TDD cycle
above is unaffected either way.

---

## Task 3: CLI -- `--streams` flag on `restore`

**Files:**
- Modify: `src/cmd/rwfs/arguments.go`
- Modify: `src/cmd/rwfs/arguments_test.go`
- Modify: `src/cmd/rwfs/main.go`

**Interfaces:**
- Consumes: Task 2's new `runRestore` signature.
- Produces: `Arguments.Streams` is now populated for `restore` too (previously verify-only); nothing
  else depends on this outside `main.go`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/rwfs/arguments_test.go`:

```go
func TestParseArguments_RestoreStreamsFlag_DefaultsToFour(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, 4, args.Streams)
	})
}

func TestParseArguments_RestoreInvalidStreamsErrors(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin", "--streams", "0"}, func() {
		_, err := parseArguments(testConfig())
		assert.Error(t, err)
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestParseArguments_Restore -v`
Expected: FAIL -- `TestParseArguments_RestoreStreamsFlag_DefaultsToFour` fails because `--streams`
isn't a registered flag on `restoreCmd` yet (cobra rejects the unknown flag);
`TestParseArguments_RestoreInvalidStreamsErrors` fails because there's nothing to reject `0` yet
(same unknown-flag error, not the validation error the test wants -- either way, both currently fail).

- [ ] **Step 3: Update `arguments.go`**

Change the `Streams` field's comment:

```go
	Streams    int  // verify only
```
to:
```go
	Streams    int  // verify, restore
```

Add the new flag and update `--overwrite`'s help text, in the `restoreCmd.Flags()` block:

```go
	restoreCmd.Flags().BoolVar(&args.RulesStdin, "rules-stdin", false, "Read {\"rules\":[{host,path,include,dest_path}]} from stdin (required)")
	restoreCmd.Flags().BoolVar(&args.Overwrite, "overwrite", false, "Whether a real restore would overwrite existing destination files (logged only, not yet enforced)")
	restoreCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
```
to:
```go
	restoreCmd.Flags().BoolVar(&args.RulesStdin, "rules-stdin", false, "Read {\"rules\":[{host,path,include,dest_path}]} from stdin (required)")
	restoreCmd.Flags().BoolVar(&args.Overwrite, "overwrite", false, "Whether to overwrite existing destination files")
	restoreCmd.Flags().IntVar(&args.Streams, "streams", 4, "Number of concurrent file restore workers")
	restoreCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
```

Add validation, right after the existing `if args.Action == "verify" { ... }` block:

```go
	if args.Action == "verify" {
		if err := common.ValidateStreamsCount(args.Streams); err != nil {
			return nil, fmt.Errorf("--streams: %w", err)
		}
		if args.Retries < 1 {
			return nil, fmt.Errorf("--retries must be at least 1, got: %d", args.Retries)
		}
	}

	if args.Action == "restore" {
		if err := common.ValidateStreamsCount(args.Streams); err != nil {
			return nil, fmt.Errorf("--streams: %w", err)
		}
	}
```

- [ ] **Step 4: Update `main.go`**

Change:

```go
	case "restore":
		if err := runRestore(logger, arguments.BwfsHost, arguments.BwfsPort, arguments.Overwrite, os.Stdin, arguments.Quiet, certsDir, jobID); err != nil {
```
to:
```go
	case "restore":
		if err := runRestore(logger, arguments.BwfsHost, arguments.BwfsPort, arguments.Overwrite, os.Stdin, arguments.Quiet, arguments.Streams, certsDir, jobID); err != nil {
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestParseArguments_Restore -v`
Expected: PASS.

- [ ] **Step 6: Run the full rwfs package test suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS -- this is also the first point since Task 2 where the package builds cleanly again
(main.go now matches runRestore's new signature).

- [ ] **Step 7: Build the binary**

Run: `cd src && go build ./cmd/rwfs/...`
Expected: builds with no errors.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/rwfs/arguments.go src/cmd/rwfs/arguments_test.go src/cmd/rwfs/main.go
git commit -m "feat(rwfs): add --streams flag to rwfs restore

Controls phase 2's (file content) concurrency, same default and
validation as verify --streams. --overwrite's help text no longer
claims to be logged-only, since Task 2 made it real. This is the
commit that restores cmd/rwfs to a building state after Task 2's
signature change."
```

---

## Task 4: Documentation and changelog

**Files:**
- Modify: `docs/components/rwfs.md`
- Modify: `docs/protocols/restore.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: the final, shipped behavior of Tasks 1-3.
- Produces: nothing consumed by other tasks -- terminal documentation task, per `.claude/CLAUDE.md`'s
  feature-change and changelog rules.

- [ ] **Step 1: `docs/components/rwfs.md`**

In the `## restore` section, replace the intro paragraph:

```markdown
Resolves a restore policy's rules against a remote `bwfs` server's file listing. **File content
restore is still log-only** -- it resolves and logs what a real file restore would do, writing
nothing. **Directory structure restore is real**: for every resolved directory (see [list
protocol](../protocols/list.md#directory-rows)), `rwfs restore` actually creates it on the
destination filesystem -- phase 1 of two, in parent-before-child order, before any file content
would be written (phase 2, still unbuilt). Requires `--rules-stdin` (the only way to select
anything; there is no plain-listing restore mode). See [Design: Restore Directory Structure
Phase](../superpowers/specs/2026-08-16-restore-directory-structure-design.md).
```

with:

```markdown
Resolves a restore policy's rules against a remote `bwfs` server's file listing, then restores it:
both phases are real, disk-mutating operations. Phase 1 creates every resolved directory (see [list
protocol](../protocols/list.md#directory-rows)) on the destination filesystem, parent before child.
Phase 2, once phase 1 has fully succeeded, fetches every resolved file's chunks via the [restore
protocol](../protocols/restore.md) and writes them to disk, verifying per-chunk BLAKE3 and the
whole-file CRC32 exactly as `rwfs verify` does. Requires `--rules-stdin` (the only way to select
anything; there is no plain-listing restore mode). See [Design: Restore File Content
Phase](../superpowers/specs/2026-08-17-restore-file-content-design.md) and [Design: Restore Directory
Structure Phase](../superpowers/specs/2026-08-16-restore-directory-structure-design.md).
```

Replace the flags table:

```markdown
| Flag | Default | Description |
|------|---------|--------------|
| `--rules-stdin` | | **Required.** Read `{"rules":[...]}` from stdin -- same shape `verify --rules-stdin` uses. |
| `--overwrite` | false | Logged only; not yet enforced. |
| `--quiet` | false | Suppress per-file resolved lines (warnings and summary always shown) |
| `--job-id` | auto-generated UUID | Correlation ID for this invocation's logs; also sent to `bwfs` as `job-id` gRPC metadata |
```

with:

```markdown
| Flag | Default | Description |
|------|---------|--------------|
| `--rules-stdin` | | **Required.** Read `{"rules":[...]}` from stdin -- same shape `verify --rules-stdin` uses. |
| `--overwrite` | false | A pre-existing destination file is skipped when false, overwritten when true. Has no effect on directories (always reused) or on a non-file occupying a destination path (always a hard error). |
| `--streams` | 4 | Concurrent file restore workers (phase 2 only; phase 1's directory creation is sequential) |
| `--quiet` | false | Suppress per-file resolved lines (warnings and summary always shown) |
| `--job-id` | auto-generated UUID | Correlation ID for this invocation's logs; also sent to `bwfs` as `job-id` gRPC metadata |
```

Replace the paragraphs after the flags table (from "Exit code follows..." through the final "must be
created ahead of time." sentence) with:

```markdown
Exit code follows the same not-found rule `verify --rules-stdin` uses: a file-level rule matching no
row is a failure (non-zero exit); a folder-level rule matching nothing is not. A not-found failure
aborts before directory creation (phase 1) ever starts.

Phase 1 logs `creating restored directory structure` once at start, then either a `restored
directory structure created` summary (with `created`/`reused` counts) on full success, or a
`failed to create restored directory` error and an immediate abort on the first failure -- no
further directories are attempted, and the summary line is never reached. A pre-existing directory
is always reused, regardless of `--overwrite`; a pre-existing non-directory at the destination path
is always a hard error. Directories are created with `os.Mkdir`, not a recursive `MkdirAll`, so
the shallowest directory in any resolved set -- a folder rule's own `dest_path`, verbatim -- fails
immediately if its parent doesn't already exist on the destination host; that parent must be
created ahead of time.

Phase 2 (file content) runs only once phase 1 has fully succeeded. It logs `restoring file content`
once at start, fetches each resolved file's chunks via `RestoreFile` (concurrently, `--streams`
workers wide), and writes them to its `dest_path`-renamed destination -- verifying every chunk's
BLAKE3 hash and the whole-file CRC32 exactly as `rwfs verify` does, aborting on a mismatch the same
way a stream or disk-write error would. On the first failure, every other in-flight file transfer is
cancelled immediately, the failing (partial) file is removed from disk, a `failed to restore file`
error is logged for it, and no summary line is logged -- the same abort convention phase 1 already
uses. On full success, a `restore complete` line reports `files_written`, `bytes_written`, and
`skipped` (files left untouched because they already existed and `--overwrite` was false). Per-file
success (`file written` / `file skipped, already exists`) is logged at `Debug` level only -- pass
`--debug` to see it; it is not controlled by `--quiet`. Every created or overwritten file uses a
fixed default permission (`0o644`, directories use `0o755`) -- real captured-permission restore is
still unbuilt, for both files and directories.
```

- [ ] **Step 2: `docs/protocols/restore.md`**

Replace the final paragraph of the `## CLI → RPC Mapping` section:

```markdown
`rwfs restore --rules-stdin` calls only `ListService.ResolveRestoreFiles` -- unlike `rwfs verify
--rules-stdin`, it never calls `RestoreFile`, since this round only resolves and logs the file list without reading any chunk data.
```

with:

```markdown
`rwfs restore --rules-stdin` calls `ListService.ResolveRestoreFiles` to resolve the selection, then,
once its directory-structure phase has fully succeeded, calls `RestoreFile` for each resolved file's
`file_uuid` -- the same RPC `rwfs verify --rules-stdin` calls, but to actually write the chunks to
disk (verifying per-chunk BLAKE3 and the whole-file CRC32 as it writes) rather than merely checking
them. Concurrency is controlled by `restore`'s own `--streams` flag, independent of `verify`'s.
```

- [ ] **Step 3: `docs/ARCHITECTURE.md`**

Change the `rwfs` row of the component status table:

```markdown
| rwfs | Restore Writer for File System — queries bwfs (list, verify, restore) | list + verify implemented; `restore` resolves rules, creates the resolved directory structure on disk, and logs the would-be file restore (file content not yet written) |
```

to:

```markdown
| rwfs | Restore Writer for File System — queries bwfs (list, verify, restore) | list, verify, and restore fully implemented -- `restore` creates the resolved directory structure and writes real file content to the destination filesystem |
```

Change the `agent` row:

```markdown
| agent | Node Agent — reconciles local state against embedded policies | Implemented (bootstrap credential renewal, operating-certificate refresh via `issuer`, policy fetch via `policyclient`, policy-driven backup execution via `brfs`, one-shot restore-policy verification via `rwfs verify`, and one-shot restore execution via `rwfs restore` for `mode: "restore"` policies -- directory structure creation is real, file content restore is still log-only) |
```

to:

```markdown
| agent | Node Agent — reconciles local state against embedded policies | Implemented (bootstrap credential renewal, operating-certificate refresh via `issuer`, policy fetch via `policyclient`, policy-driven backup execution via `brfs`, one-shot restore-policy verification via `rwfs verify`, and one-shot restore execution via `rwfs restore` for `mode: "restore"` policies -- both directory structure creation and file content restore are real) |
```

In the prose paragraph describing agent's restore-policy handling, replace:

```markdown
cached `"restore"`-typed policy, executing `rwfs verify` against the resolved source `bwfs` (or,
when that policy's `mode` is `"restore"`, `rwfs restore`, which creates the resolved directory
structure on disk and logs the would-be file restore -- file content restore is still unbuilt) —
```

with:

```markdown
cached `"restore"`-typed policy, executing `rwfs verify` against the resolved source `bwfs` (or,
when that policy's `mode` is `"restore"`, `rwfs restore`, which creates the resolved directory
structure on disk and then writes the resolved file content to it) —
```

In the `## Restore/Verify Process` section, replace the last bullet:

```markdown
- **rwfs restore** creates the resolved directory structure on the destination filesystem (real,
  as of this round); file content restore remains future work
```

with:

```markdown
- **rwfs restore** creates the resolved directory structure on the destination filesystem and then
  writes the resolved file content to it, verifying per-chunk BLAKE3 and the whole-file CRC32 as it
  writes
```

In the mermaid diagram, change the comment:

```
    %% Restore Flow (list/verify implemented)
```

to:

```
    %% Restore Flow (list/verify/restore implemented)
```

- [ ] **Step 4: Add a CHANGELOG entry**

In `CHANGELOG.md`, insert a new entry immediately after line 3 (the `All notable changes...` line),
before the current top entry:

```markdown
## 2026-08-17 — restore execution: file content phase

`rwfs restore` now writes real file content to the destination filesystem -- the last unbuilt piece
of the restore-execution line. For every resolved file, once the directory-structure phase (phase 1)
has fully succeeded, `rwfs restore` fetches its chunks via `RestoreFile` (the same RPC `rwfs verify`
already used to check files) and writes them to disk, verifying per-chunk BLAKE3 and the whole-file
CRC32 exactly as `verify` does. Multiple files transfer concurrently via a new `--streams` flag
(default 4); on the first failure, every other in-flight transfer is cancelled immediately, the
partial file is removed, and the whole restore aborts. `--overwrite` is now enforced: an existing
destination file is skipped when false, replaced when true; a non-file occupying a file's
destination is always a hard error. Per-file success is now logged at `Debug` level only, keeping
the default log output from being flooded on large restores.

```

- [ ] **Step 5: Verify the doc edits render sensibly**

Run: `git diff docs/ CHANGELOG.md`
Expected: a clean, readable diff -- no broken markdown, no stray blank lines inside the CHANGELOG
entry. Spot-check the new cross-link resolves:
`ls docs/superpowers/specs/2026-08-17-restore-file-content-design.md`.

- [ ] **Step 6: Commit**

```bash
git add docs/components/rwfs.md docs/protocols/restore.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document the restore file content phase

Per .claude/CLAUDE.md's feature-change and changelog rules."
```
