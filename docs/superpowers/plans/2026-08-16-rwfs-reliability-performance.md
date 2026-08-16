# rwfs: Reliability, Performance, and Reuse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert `bwfs`'s `ListFiles` RPC to streaming (removing its unary message-size ceiling), add stall-watchdog protection and retry backoff to `rwfs`'s currently-unbounded `ResolveRestoreFiles`/`RestoreFile`/`ListFiles` streams, and extract a generic worker pool plus a shared resolved-row source so `rwfs verify` and `rwfs restore` stop duplicating their stream-consumption loops.

**Architecture:** Two new small, reusable files in `cmd/rwfs` (`workerpool.go`, `watchdog.go`), one new function in `resolve.go` (`streamResolvedRows`) that both `verify --rules-stdin` and `restore` call identically, and a proto change making `ListFiles` a server-streaming RPC consumed the same way by `list` and `verify`'s plain path.

**Tech Stack:** Go 1.26, gRPC/protobuf (`protoc`, `protoc-gen-go`, `protoc-gen-go-grpc`), Go generics, `google.golang.org/grpc/test/bufconn` for tests, `github.com/stretchr/testify` (`require`/`assert`) where the touched file already uses it.

## Global Constraints

- Module path: `github.com/alex-sviridov/miniprotector`
- All proto files live in `src/api/`, regenerated with `make proto` (runs `protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative api/*.proto` from `src/`) — run from the repo root.
- `option go_package = "./proto"` → generated Go package imported as `pb "github.com/alex-sviridov/miniprotector/api"`.
- All source lives under `src/`; build/test commands run from the repo root via `make build` / `make test` (or `go test ./...` from `src/`).
- No new CLI flags. Idle-timeout (60s) and retry backoff (500ms doubling, capped at 5s) are fixed constants.
- `cmd/bwfs` and `cmd/rwfs` are both `package main` and cannot import each other — test doubles that stand in for the other side's server/client are duplicated, not imported (matches the existing pattern in `verify_test.go`'s `testResolveServer`/`recordingRestoreServer`/`realRestoreServer`).
- No behavior change to CLI output, exit codes, or documented not-found/error semantics — this is a reliability/performance/structure pass only.
- Follow this repo's `.claude/CLAUDE.md`: update `docs/protocols/list.md`, `docs/components/rwfs.md`, and add a `CHANGELOG.md` entry before this is considered mergeable.

---

## File Map

| Path | Status | Responsibility |
|------|--------|----------------|
| `src/api/list.proto` | Modify | `ListFiles` becomes `stream FileRow`; remove unused `ListResponse` |
| `src/api/list.pb.go`, `src/api/list_grpc.pb.go` | Generated | Regenerated via `make proto` |
| `src/cmd/bwfs/listserver.go` | Modify | `ListFiles` handler streams rows instead of returning one `ListResponse` |
| `src/cmd/bwfs/listserver_test.go` | Modify | Tests target the streaming handler; add mid-stream send-error case |
| `src/cmd/rwfs/workerpool.go` | Create | Generic `runWorkerPool[T, R]` |
| `src/cmd/rwfs/workerpool_test.go` | Create | Unit tests for the pool |
| `src/cmd/rwfs/watchdog.go` | Create | `withStallWatchdog`, `streamIdleTimeout` |
| `src/cmd/rwfs/watchdog_test.go` | Create | Unit tests for the watchdog |
| `src/cmd/rwfs/resolve.go` | Modify | Adds `dispatchedRow`, `streamResolvedRows` |
| `src/cmd/rwfs/resolve_test.go` | Modify | Adds `streamResolvedRows` coverage incl. a stalled-stream case |
| `src/cmd/rwfs/list.go` | Modify | Splits into `runList` (dial) / `runListWithConn` (streaming consumption) |
| `src/cmd/rwfs/list_test.go` | Create | New coverage for the streaming client path (`list.go` had none before) |
| `src/cmd/rwfs/verify.go` | Modify | Plain path streams `ListFiles`; `--rules-stdin` path uses `streamResolvedRows`; dispatch uses `runWorkerPool`; retry backoff; `verifyFile` watchdog-wrapped |
| `src/cmd/rwfs/verify_test.go` | Modify | `testResolveServer` gains `ListFiles`; new plain-path, backoff, and watchdog tests |
| `src/cmd/rwfs/restore.go` | Modify | Swaps its inline `ResolveRestoreFiles` loop for `streamResolvedRows` |
| `docs/protocols/list.md` | Modify | `ListFiles` streaming shape + updated design-decision rationale |
| `docs/components/rwfs.md` | Modify | Notes watchdog, backoff, internal worker-pool/resolved-row-source reuse |
| `CHANGELOG.md` | Modify | Entry summarizing this round |

---

## Task 1: Convert `ListFiles` to a streaming RPC

**Files:**
- Modify: `src/api/list.proto`
- Generated: `src/api/list.pb.go`, `src/api/list_grpc.pb.go`
- Modify: `src/cmd/bwfs/listserver.go`
- Modify: `src/cmd/bwfs/listserver_test.go`

**Interfaces:**
- Produces: `pb.ListServiceClient.ListFiles(ctx, *pb.ListRequest, ...) (grpc.ServerStreamingClient[pb.FileRow], error)`; server-side `ListServiceServer.ListFiles(*pb.ListRequest, grpc.ServerStreamingServer[pb.FileRow]) error`; type aliases `pb.ListService_ListFilesClient` / `pb.ListService_ListFilesServer`. `pb.ListResponse` no longer exists. Consumed by Task 5 (`list.go`) and Task 6 (`verify.go`).

- [ ] **Step 1: Edit `src/api/list.proto`**

Change the service definition:
```proto
service ListService {
  rpc ListFiles(ListRequest) returns (stream FileRow);
  rpc ResolveRestoreFiles(ResolveRestoreFilesRequest) returns (stream ResolveRestoreFilesResponse);
}
```

Delete the `ListResponse` message entirely:
```proto
// DELETE:
message ListResponse {
  repeated FileRow rows = 1;
}
```

- [ ] **Step 2: Regenerate protobuf code**

```bash
make proto
```

Expected: `src/api/list.pb.go` and `src/api/list_grpc.pb.go` regenerate with no `ListResponse` type and a streaming `ListFiles`. Confirm with:

```bash
grep -n "ListResponse" src/api/list.pb.go src/api/list_grpc.pb.go
```

Expected: no matches.

- [ ] **Step 3: Update `src/cmd/bwfs/listserver.go`**

Replace the whole file's `ListFiles` method and drop the now-unused `context` import:

```go
package main

import (
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type listServer struct {
	pb.UnimplementedListServiceServer
	store  *wfs.Store
	logger *slog.Logger
}

func NewListServer(store *wfs.Store, logger *slog.Logger) *listServer {
	return &listServer{store: store, logger: logger}
}

func (s *listServer) ListFiles(req *pb.ListRequest, stream pb.ListService_ListFilesServer) error {
	rows, err := queryFileRows(s.store, req.GetServerName(), req.GetPath(), req.GetFilter())
	if err != nil {
		s.logger.Error("ListFiles query failed", "error", err)
		return err
	}

	for _, r := range rows {
		row := &pb.FileRow{
			FileUuid:  r.FileUUID,
			Source:    r.Source,
			Type:      r.Type,
			Path:      r.Path,
			Timestamp: r.Timestamp,
			Size:      r.Size,
			Chunks:    int32(r.Chunks),
			Versions:  r.Versions,
			CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		}
		if err := stream.Send(row); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Rewrite `src/cmd/bwfs/listserver_test.go`**

Replace the whole file:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

func newTestListServer(t *testing.T) (*listServer, *wfs.Store) {
	t.Helper()
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewListServer(store, logger), store
}

// collectingStream is a grpc.ServerStreamingServer[pb.FileRow] test double
// that records every row Send is called with, letting a unit test call
// listServer.ListFiles directly without a real network round trip.
type collectingStream struct {
	grpc.ServerStream
	rows []*pb.FileRow
}

func (s *collectingStream) Send(row *pb.FileRow) error {
	s.rows = append(s.rows, row)
	return nil
}

// erroringStream is a grpc.ServerStreamingServer[pb.FileRow] test double
// that fails every Send call after the first, proving ListFiles surfaces
// a mid-stream send error from the handler rather than swallowing it.
type erroringStream struct {
	grpc.ServerStream
	sent int
}

func (s *erroringStream) Send(row *pb.FileRow) error {
	s.sent++
	if s.sent > 1 {
		return fmt.Errorf("simulated send failure")
	}
	return nil
}

func TestListFiles_EmptyStoreReturnsEmptyRows(t *testing.T) {
	srv, _ := newTestListServer(t)

	stream := &collectingStream{}
	err := srv.ListFiles(&pb.ListRequest{}, stream)
	require.NoError(t, err)
	assert.Empty(t, stream.rows)
}

func TestListFiles_FiltersByServerName(t *testing.T) {
	srv, store := newTestListServer(t)

	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hostb:f:/data/b.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hostb:f:/data/b.txt:1000", []byte{5, 6, 7, 8}))

	stream := &collectingStream{}
	err := srv.ListFiles(&pb.ListRequest{ServerName: "hosta"}, stream)
	require.NoError(t, err)
	require.Len(t, stream.rows, 1)
	assert.Equal(t, "hosta", stream.rows[0].Source)
	assert.Equal(t, "/data/a.txt", stream.rows[0].Path)
}

func TestListFiles_FiltersByPathPrefix(t *testing.T) {
	srv, store := newTestListServer(t)

	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hosta:f:/other/c.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/other/c.txt:1000", []byte{5, 6, 7, 8}))

	stream := &collectingStream{}
	err := srv.ListFiles(&pb.ListRequest{Path: "/data"}, stream)
	require.NoError(t, err)
	require.Len(t, stream.rows, 1)
	assert.Equal(t, "/data/a.txt", stream.rows[0].Path)
}

func TestListFiles_MidStreamSendErrorSurfaces(t *testing.T) {
	srv, store := newTestListServer(t)
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/b.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/b.txt:1000", []byte{5, 6, 7, 8}))

	stream := &erroringStream{}
	err := srv.ListFiles(&pb.ListRequest{}, stream)
	require.Error(t, err, "the handler must propagate Send's error instead of swallowing it")
	assert.Equal(t, 2, stream.sent, "the second Send call is where erroringStream fails")
}

func TestListFiles_GRPCRoundTrip(t *testing.T) {
	srv, store := newTestListServer(t)
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewListServiceClient(conn)
	stream, err := client.ListFiles(context.Background(), &pb.ListRequest{ServerName: "hosta"})
	require.NoError(t, err)

	var rows []*pb.FileRow
	for {
		row, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		rows = append(rows, row)
	}
	require.Len(t, rows, 1)
	assert.Equal(t, "/data/a.txt", rows[0].Path)
}
```

- [ ] **Step 5: Run bwfs tests**

```bash
cd src && go test ./cmd/bwfs/... -run TestListFiles -v
```

Expected: all `TestListFiles_*` tests PASS.

- [ ] **Step 6: Full build check**

```bash
make build
```

Expected: all binaries build (this also catches any other, unexpected caller of the old `ListFiles`/`ListResponse` shape).

- [ ] **Step 7: Commit**

```bash
git add src/api/list.proto src/api/list.pb.go src/api/list_grpc.pb.go src/cmd/bwfs/listserver.go src/cmd/bwfs/listserver_test.go
git commit -m "feat(bwfs): convert ListFiles to a streaming RPC

Removes the unary response's implicit ~4MB gRPC message-size ceiling
on a large per-host listing. bwfs still runs one queryFileRows call
per request (unchanged SQL); only the wire shape changes to one
FileRow per Send instead of one ListResponse."
```

---

## Task 2: Generic worker pool

**Files:**
- Create: `src/cmd/rwfs/workerpool.go`
- Create: `src/cmd/rwfs/workerpool_test.go`

**Interfaces:**
- Produces: `runWorkerPool[T, R any](ctx context.Context, streams int, in <-chan T, work func(context.Context, T) R) <-chan R`. Consumed by Task 6 (`verify.go`), and intended as the drop-in point for a future phase-2 file-content restore.

- [ ] **Step 1: Write the failing tests — create `src/cmd/rwfs/workerpool_test.go`**

```go
package main

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestRunWorkerPool_ProcessesAllItems(t *testing.T) {
	in := make(chan int)
	go func() {
		for i := 0; i < 5; i++ {
			in <- i
		}
		close(in)
	}()

	out := runWorkerPool(context.Background(), 3, in, func(_ context.Context, item int) int {
		return item * 2
	})

	var got []int
	for r := range out {
		got = append(got, r)
	}
	sort.Ints(got)

	want := []int{0, 2, 4, 6, 8}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestRunWorkerPool_EmptyInputClosesCleanly(t *testing.T) {
	in := make(chan int)
	close(in)

	out := runWorkerPool(context.Background(), 3, in, func(_ context.Context, item int) int { return item })

	count := 0
	for range out {
		count++
	}
	if count != 0 {
		t.Fatalf("expected zero results from empty input, got %d", count)
	}
}

// TestRunWorkerPool_RunsWorkConcurrentlyAcrossAllStreams proves streams
// goroutines really run at once, not sequentially: every worker blocks on
// a shared gate until all `streams` of them have started, so the test
// cannot complete unless true concurrency of that width actually happened
// -- no arbitrary sleep, no flakiness window.
func TestRunWorkerPool_RunsWorkConcurrentlyAcrossAllStreams(t *testing.T) {
	const streams = 4
	in := make(chan int)
	go func() {
		for i := 0; i < streams; i++ {
			in <- i
		}
		close(in)
	}()

	var mu sync.Mutex
	inFlight := 0
	maxInFlight := 0
	release := make(chan struct{})
	var releaseOnce sync.Once

	out := runWorkerPool(context.Background(), streams, in, func(_ context.Context, item int) int {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		reached := inFlight == streams
		mu.Unlock()
		if reached {
			releaseOnce.Do(func() { close(release) })
		}
		<-release
		return item
	})

	done := make(chan struct{})
	go func() {
		for range out {
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workers never reached full concurrency -- release was never closed")
	}

	if maxInFlight != streams {
		t.Fatalf("expected all %d workers to run concurrently, max in flight was %d", streams, maxInFlight)
	}
}

// TestRunWorkerPool_PassesContextToWorkFunction proves the pool's ctx
// reaches the work function unmodified -- the mechanism verify.go relies
// on for per-call cancellation (e.g. via a watchdog-derived context).
func TestRunWorkerPool_PassesContextToWorkFunction(t *testing.T) {
	type ctxKey string
	const key ctxKey = "k"
	ctx := context.WithValue(context.Background(), key, "v")

	in := make(chan int, 1)
	in <- 1
	close(in)

	var gotValue any
	out := runWorkerPool(ctx, 1, in, func(c context.Context, item int) int {
		gotValue = c.Value(key)
		return item
	})
	for range out {
	}

	if gotValue != "v" {
		t.Fatalf("expected the pool's ctx to be passed through to the work function, got %v", gotValue)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd src && go test ./cmd/rwfs/... -run TestRunWorkerPool -v
```

Expected: FAIL — `runWorkerPool` is undefined.

- [ ] **Step 3: Create `src/cmd/rwfs/workerpool.go`**

```go
package main

import (
	"context"
	"sync"
)

// runWorkerPool runs work concurrently across streams goroutines, each
// consuming items from in until it closes, and emits one result per item
// processed on the returned channel. The returned channel closes once
// every worker has drained in -- callers range over it to know when all
// work is done. streams must be >= 1.
//
// Replaces verify.go's previous hand-rolled channel/sync.WaitGroup
// plumbing (which mirrored brfs's filesstream.go almost exactly), made
// generic so a future phase-2 file-content restore can reuse it with a
// different work function -- see
// docs/superpowers/specs/2026-08-16-rwfs-reliability-performance-design.md.
func runWorkerPool[T, R any](ctx context.Context, streams int, in <-chan T, work func(context.Context, T) R) <-chan R {
	out := make(chan R, streams)
	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range in {
				out <- work(ctx, item)
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd src && go test ./cmd/rwfs/... -run TestRunWorkerPool -v
```

Expected: all four tests PASS.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/rwfs/workerpool.go src/cmd/rwfs/workerpool_test.go
git commit -m "feat(rwfs): add runWorkerPool, a generic reusable worker pool"
```

---

## Task 3: Stall watchdog

**Files:**
- Create: `src/cmd/rwfs/watchdog.go`
- Create: `src/cmd/rwfs/watchdog_test.go`

**Interfaces:**
- Produces: `streamIdleTimeout` (package-level `var`, `time.Duration`, default 60s -- a `var`, not a `const`, specifically so tests can temporarily shrink it instead of waiting out the real 60s; see Task 4/6's stalled-stream tests) and `withStallWatchdog(parent context.Context, idle time.Duration) (ctx context.Context, touch func(), stop func())`. Consumed by Task 4 (`resolve.go`), Task 5 (`list.go`), and Task 6 (`verify.go`).

- [ ] **Step 1: Write the failing tests — create `src/cmd/rwfs/watchdog_test.go`**

```go
package main

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestWithStallWatchdog_CancelsAfterIdleWindowWithNoTouch(t *testing.T) {
	ctx, _, stop := withStallWatchdog(context.Background(), 30*time.Millisecond)
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected the watchdog to cancel ctx after the idle window elapsed with no touch")
	}
}

func TestWithStallWatchdog_RepeatedTouchesPreventCancellation(t *testing.T) {
	ctx, touch, stop := withStallWatchdog(context.Background(), 40*time.Millisecond)
	defer stop()

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		touch()
		time.Sleep(10 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatalf("expected ctx to remain live under repeated touches, got %v", ctx.Err())
	}
}

func TestWithStallWatchdog_StopReleasesGoroutineWithoutLeaking(t *testing.T) {
	before := runtime.NumGoroutine()
	_, _, stop := withStallWatchdog(context.Background(), time.Hour)
	stop()

	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Fatalf("expected watchdog goroutine to exit after stop(), goroutine count still %d (baseline %d)", got, before)
	}
}

func TestWithStallWatchdog_StopAlsoCancelsCtx(t *testing.T) {
	ctx, _, stop := withStallWatchdog(context.Background(), time.Hour)
	stop()
	if ctx.Err() == nil {
		t.Fatal("expected stop() to cancel ctx -- a caller done with the stream must not leak a live context")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd src && go test ./cmd/rwfs/... -run TestWithStallWatchdog -v
```

Expected: FAIL — `withStallWatchdog` is undefined.

- [ ] **Step 3: Create `src/cmd/rwfs/watchdog.go`**

```go
package main

import (
	"context"
	"time"
)

// streamIdleTimeout is the idle window withStallWatchdog uses for
// ResolveRestoreFiles, RestoreFile, and ListFiles streaming calls: a
// stream that's actively producing messages never hits this, no matter
// how long the overall call runs; only an actual stall (no message
// received for this long) does. A var rather than a const purely so
// tests can shrink it instead of waiting out the real 60s -- not a
// user-facing setting; there is no flag for it. See
// docs/superpowers/specs/2026-08-16-rwfs-reliability-performance-design.md.
var streamIdleTimeout = 60 * time.Second

// withStallWatchdog returns a context derived from parent that is
// cancelled if touch isn't called within idle of the last call (or of
// ctx's creation, for the first call) -- an idle timeout, not a
// total-duration timeout, so a stream that's genuinely still producing
// output is never penalized for running long. stop releases the
// watchdog's goroutine and must be called exactly once when the caller is
// done with ctx, whether the underlying call succeeded, failed, or was
// itself cancelled by touch never being called again; it also cancels
// ctx, so a caller doesn't need a separate cancel of its own.
func withStallWatchdog(parent context.Context, idle time.Duration) (ctx context.Context, touch func(), stop func()) {
	ctx, cancel := context.WithCancel(parent)
	timer := time.NewTimer(idle)
	touchCh := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				cancel()
				return
			case <-touchCh:
				if !timer.Stop() {
					<-timer.C
				}
				timer.Reset(idle)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	touch = func() {
		select {
		case touchCh <- struct{}{}:
		default: // a touch is already pending; the pending one already resets the timer
		}
	}
	stop = func() {
		close(done)
		cancel()
	}
	return ctx, touch, stop
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd src && go test ./cmd/rwfs/... -run TestWithStallWatchdog -v
```

Expected: all four tests PASS.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/rwfs/watchdog.go src/cmd/rwfs/watchdog_test.go
git commit -m "feat(rwfs): add withStallWatchdog, an idle-timeout context wrapper"
```

---

## Task 4: Shared resolved-row source

**Files:**
- Modify: `src/cmd/rwfs/resolve.go`
- Modify: `src/cmd/rwfs/resolve_test.go`

**Interfaces:**
- Consumes: `withStallWatchdog`/`streamIdleTimeout` (Task 3); existing `buildRestoreFilters`, `newRestoreResolver`, `restoreResolver.Feed`, `restoreResolver.NotFound` (unchanged, already in `resolve.go`).
- Produces: `type dispatchedRow struct { Row *pb.FileRow; RuleIndex int }` and `streamResolvedRows(ctx context.Context, client pb.ListServiceClient, rules []RestoreRule) (rows <-chan dispatchedRow, resolver *restoreResolver, errCh <-chan error)`. Consumed by Task 6 (`verify.go`) and Task 7 (`restore.go`).

- [ ] **Step 1: Write the failing tests — append to `src/cmd/rwfs/resolve_test.go`**

Add these imports to the existing file's import block (it currently only imports `pb` and `testing`):

```go
import (
	"context"
	"net"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)
```

Append these tests (this file's existing tests use plain `t.Fatalf`, not testify — match that style):

```go
// TestStreamResolvedRows_DispatchesMatchingRowsAndReportsNotFound drives
// streamResolvedRows over a real bufconn gRPC round trip against
// testResolveServer (defined in verify_test.go, same package) -- one rule
// matches a real row, the other matches nothing and must surface via
// resolver.NotFound() once rows is drained.
func TestStreamResolvedRows_DispatchesMatchingRowsAndReportsNotFound(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.CreateFileData("fs://hosta:f:/etc/a.conf:1000", 4); err != nil {
		t.Fatalf("create file data: %v", err)
	}
	if err := store.FinalizeFileData("fs://hosta:f:/etc/a.conf:1000", expectedCRC32(t, [][]byte{{1, 2, 3, 4}})); err != nil {
		t.Fatalf("finalize file data: %v", err)
	}
	if err := store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/etc/a.conf:1000", JobID: "job1", CreatedAt: time.Unix(5000, 0)}).Error; err != nil {
		t.Fatalf("create file version record: %v", err)
	}

	listSrv := &testResolveServer{store: store}
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	rules := []RestoreRule{
		{Host: "hosta", Path: "/etc/a.conf", Include: true},
		{Host: "hosta", Path: "/etc/missing.conf", Include: true},
	}
	client := pb.NewListServiceClient(conn)
	rowsCh, resolver, errCh := streamResolvedRows(context.Background(), client, rules)

	var got []dispatchedRow
	for r := range rowsCh {
		got = append(got, r)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 dispatched row, got %d: %+v", len(got), got)
	}
	if got[0].Row.GetPath() != "/etc/a.conf" || got[0].RuleIndex != 0 {
		t.Fatalf("unexpected dispatched row: %+v", got[0])
	}

	notFound := resolver.NotFound()
	if len(notFound) != 1 || notFound[0].Path != "/etc/missing.conf" {
		t.Fatalf("expected one not-found entry for /etc/missing.conf, got %+v", notFound)
	}
}

// stallingResolveServer sends nothing and blocks until its context is
// cancelled -- proves streamResolvedRows's watchdog actually fires on a
// genuinely stalled ResolveRestoreFiles stream, not just a fast one.
type stallingResolveServer struct {
	pb.UnimplementedListServiceServer
}

func (s *stallingResolveServer) ResolveRestoreFiles(_ *pb.ResolveRestoreFilesRequest, stream pb.ListService_ResolveRestoreFilesServer) error {
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestStreamResolvedRows_WatchdogCancelsAStalledStream(t *testing.T) {
	original := streamIdleTimeout
	streamIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = original })

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, &stallingResolveServer{})
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	rules := []RestoreRule{{Host: "hosta", Path: "/etc/a.conf", Include: true}}
	client := pb.NewListServiceClient(conn)
	rowsCh, _, errCh := streamResolvedRows(context.Background(), client, rules)

	for range rowsCh {
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected the stalled stream to surface an error once the watchdog fires")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("streamResolvedRows never returned after the watchdog should have fired")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd src && go test ./cmd/rwfs/... -run TestStreamResolvedRows -v
```

Expected: FAIL to compile — `streamResolvedRows` and `dispatchedRow` are undefined.

- [ ] **Step 3: Add `streamResolvedRows` to `src/cmd/rwfs/resolve.go`**

Add these imports to the existing file's import block (currently just `pb`):

```go
import (
	"context"
	"fmt"
	"io"

	pb "github.com/alex-sviridov/miniprotector/api"
)
```

Append at the end of the file:

```go
// dispatchedRow is one row streamResolvedRows has already run through
// restoreResolver.Feed's precedence/type gating and confirmed should be
// acted on. RuleIndex is the winning rule's index into the rules slice
// streamResolvedRows was called with -- restore.go needs it for
// dest_path attribution; verify.go ignores it.
type dispatchedRow struct {
	Row       *pb.FileRow
	RuleIndex int
}

// streamResolvedRows wraps a stall-watchdog-protected ResolveRestoreFiles
// call and resolver.Feed's existing precedence/type gating in one
// reusable stream, used identically by verify --rules-stdin and restore --
// previously each drove this loop independently, in two different shapes.
// The returned resolver's NotFound must only be called once rows is fully
// drained (closed). errCh receives exactly one value (nil on a clean
// end-of-stream, non-nil otherwise), sent as the producer goroutine's
// final act before it closes rows -- so a caller that fully drains rows
// first is guaranteed errCh already has its value ready to read.
func streamResolvedRows(ctx context.Context, client pb.ListServiceClient, rules []RestoreRule) (rows <-chan dispatchedRow, resolver *restoreResolver, errCh <-chan error) {
	filters, filterToRuleIndex := buildRestoreFilters(rules)
	resolver = newRestoreResolver(rules, filterToRuleIndex)

	out := make(chan dispatchedRow)
	errs := make(chan error, 1)

	watchdogCtx, touch, stop := withStallWatchdog(ctx, streamIdleTimeout)

	stream, err := client.ResolveRestoreFiles(watchdogCtx, &pb.ResolveRestoreFilesRequest{Filters: filters})
	if err != nil {
		stop()
		close(out)
		errs <- fmt.Errorf("resolve restore files: %w", err)
		return out, resolver, errs
	}

	go func() {
		defer stop()
		defer close(out)
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				errs <- nil
				return
			}
			if err != nil {
				errs <- fmt.Errorf("resolve restore files: %w", err)
				return
			}
			touch()
			if dispatch, ruleIndex := resolver.Feed(resp.GetRow(), resp.GetFilterIndex()); dispatch {
				out <- dispatchedRow{Row: resp.GetRow(), RuleIndex: ruleIndex}
			}
		}
	}()
	return out, resolver, errs
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd src && go test ./cmd/rwfs/... -run "TestStreamResolvedRows|TestRestoreResolver|TestBuildRestoreFilters" -v
```

Expected: all PASS, including the pre-existing `TestRestoreResolver_*`/`TestBuildRestoreFilters_*` tests (unchanged — `resolver.Feed`/`buildRestoreFilters` themselves are untouched).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/rwfs/resolve.go src/cmd/rwfs/resolve_test.go
git commit -m "feat(rwfs): add streamResolvedRows, the shared resolved-row source

Wraps ResolveRestoreFiles + resolver.Feed's dispatch gating in one
watchdog-protected stream -- previously verify.go and restore.go each
drove this loop independently, in two different shapes."
```

---

## Task 5: `rwfs list` streams `ListFiles`

**Files:**
- Modify: `src/cmd/rwfs/list.go`
- Modify: `src/cmd/rwfs/verify_test.go` (add a `ListFiles` method to the existing `testResolveServer`, and a new shared `failingAfterFirstRowListServer` test double)
- Create: `src/cmd/rwfs/list_test.go`

**Interfaces:**
- Consumes: Task 1's streaming `pb.ListServiceClient.ListFiles`; Task 3's `withStallWatchdog`/`streamIdleTimeout`.
- Produces: `runListWithConn(conn *grpc.ClientConn, serverName, pathFilter, filter, output, jobID string) error` (consumed by `runList`, and by Task 6's plain-path verify tests indirectly via the same `testResolveServer.ListFiles` and `failingAfterFirstRowListServer` this task adds). `testResolveServer.ListFiles` and `failingAfterFirstRowListServer` become available package-wide for Task 6 to reuse.

- [ ] **Step 1: Add `ListFiles` to `testResolveServer` and a new `failingAfterFirstRowListServer` in `src/cmd/rwfs/verify_test.go`**

`testResolveServer` (defined in `verify_test.go`) currently only implements `ResolveRestoreFiles`. Add a `ListFiles` method right after it (it's a minimal reimplementation of `bwfs`'s real `queryFileRows`/`ListFiles`, same duplication rationale as the rest of that type's doc comment already states -- `cmd/bwfs` is a separate `package main` and can't be imported here):

```go
// ListFiles is a minimal reimplementation of bwfs's real ListFiles
// handler (cmd/bwfs/listserver.go + cmd/bwfs/list.go's queryFileRows),
// streaming FileRow directly instead of returning one ListResponse --
// enough to drive a real gRPC/bufconn round trip against the streaming
// shape from Task 1. Duplicated, not imported: cmd/bwfs is a different
// "package main" and cannot be imported from here.
func (s *testResolveServer) ListFiles(req *pb.ListRequest, stream pb.ListService_ListFilesServer) error {
	query := s.store.RawDB().
		Table("file_data_records fd").
		Select("fd.uuid AS uuid, fd.source_host AS source_host, fd.path AS path, fd.size AS size, fd.chunk_count AS chunk_count").
		Where("fd.checksum IS NOT NULL").
		Order("fd.source_host ASC, fd.path ASC")
	if req.GetServerName() != "" {
		query = query.Where("fd.source_host = ?", req.GetServerName())
	}
	if req.GetPath() != "" {
		query = query.Where("fd.path LIKE ?", req.GetPath()+"%")
	}

	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uuid, source, path string
		var size int64
		var chunkCount int
		if err := rows.Scan(&uuid, &source, &path, &size, &chunkCount); err != nil {
			return err
		}
		if err := stream.Send(&pb.FileRow{
			FileUuid: uuid,
			Source:   source,
			Type:     "f",
			Path:     path,
			Size:     size,
			Chunks:   int32(chunkCount),
		}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// failingAfterFirstRowListServer sends exactly one FileRow then returns an
// error -- used by list_test.go (whose consumer never calls RestoreFile,
// so any row content works) and by verify_test.go's plain-path mid-stream
// test (Task 6), which needs Row to carry a real file_uuid a running
// RestoreServiceServer can actually serve.
type failingAfterFirstRowListServer struct {
	pb.UnimplementedListServiceServer
	Row *pb.FileRow
}

func (s *failingAfterFirstRowListServer) ListFiles(_ *pb.ListRequest, stream pb.ListService_ListFilesServer) error {
	if err := stream.Send(s.Row); err != nil {
		return err
	}
	return fmt.Errorf("simulated mid-stream failure")
}
```

- [ ] **Step 2: Write the failing tests — create `src/cmd/rwfs/list_test.go`**

```go
package main

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// what was written -- listformat.RenderTable/RenderJSON both write
// directly to os.Stdout with no writer injection seam.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	fn()
	require.NoError(t, w.Close())
	os.Stdout = old
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}

func runListWithDialer(t *testing.T, lis *bufconn.Listener, serverName, pathFilter, filter, output string) error {
	t.Helper()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	return runListWithConn(conn, serverName, pathFilter, filter, output, "test-job")
}

func TestRunList_StreamsMultipleRowsIntoTableOutput(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/b.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/b.txt:1000", []byte{5, 6, 7, 8}))

	listSrv := &testResolveServer{store: store}
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	out := captureStdout(t, func() {
		err := runListWithDialer(t, lis, "hosta", "", "", "table")
		require.NoError(t, err)
	})
	require.True(t, strings.Contains(out, "/data/a.txt") && strings.Contains(out, "/data/b.txt"),
		"expected both streamed rows in the rendered table, got:\n%s", out)
}

func TestRunList_MidStreamErrorDiscardsPartialOutputAndReturnsError(t *testing.T) {
	listSrv := &failingAfterFirstRowListServer{
		Row: &pb.FileRow{Source: "hosta", Path: "/data/a.txt", Type: "f", Size: 4},
	}
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	var callErr error
	out := captureStdout(t, func() {
		callErr = runListWithDialer(t, lis, "", "", "", "table")
	})
	require.Error(t, callErr)
	require.Empty(t, out, "a mid-stream failure must never render partial output")
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
cd src && go test ./cmd/rwfs/... -run TestRunList -v
```

Expected: FAIL to compile — `runListWithConn` is undefined.

- [ ] **Step 4: Rewrite `src/cmd/rwfs/list.go`**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/listformat"
	"google.golang.org/grpc"
)

// runList lists a remote bwfs store's files. jobID rides the ListFiles RPC
// as outgoing job-id metadata, so bwfs's log for this exact call is
// correlatable back to this process's local log -- the same convention brfs
// and policyclient already follow.
func runList(host string, port int, serverName, pathFilter, filter, output, certsDir, jobID string) error {
	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	return runListWithConn(conn, serverName, pathFilter, filter, output, jobID)
}

// runListWithConn is runList's body, parameterized on an already-dialed
// conn -- split out purely so tests can exercise it over a bufconn dial
// without duplicating anything past the transport-level connect (runList
// itself is the only production caller). See list_test.go's
// runListWithDialer. ListFiles is watchdog-protected the same way
// ResolveRestoreFiles is (Task 4): an idle timeout, not a total-duration
// one, so a large legitimate listing is never penalized for taking a
// while, only a genuinely stalled stream is.
func runListWithConn(conn *grpc.ClientConn, serverName, pathFilter, filter, output, jobID string) error {
	client := pb.NewListServiceClient(conn)

	watchdogCtx, touch, stop := withStallWatchdog(jobid.Outgoing(context.Background(), jobID), streamIdleTimeout)
	defer stop()

	stream, err := client.ListFiles(watchdogCtx, &pb.ListRequest{
		ServerName: serverName,
		Path:       pathFilter,
		Filter:     filter,
	})
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}

	var rows []listformat.Row
	for {
		row, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Discard whatever was collected so far -- no partial
			// table/JSON output, matching the old unary call's
			// all-or-nothing behavior.
			return fmt.Errorf("list files: %w", err)
		}
		touch()
		createdAt, _ := time.Parse(time.RFC3339, row.CreatedAt)
		rows = append(rows, listformat.Row{
			FileUUID:  row.FileUuid,
			Source:    row.Source,
			Type:      row.Type,
			Path:      row.Path,
			Timestamp: row.Timestamp,
			Size:      row.Size,
			Chunks:    int(row.Chunks),
			Versions:  row.Versions,
			CreatedAt: createdAt,
		})
	}

	switch output {
	case "json":
		return listformat.RenderJSON(rows)
	default:
		return listformat.RenderTable(rows)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd src && go test ./cmd/rwfs/... -run TestRunList -v
```

Expected: both `TestRunList_*` tests PASS.

- [ ] **Step 6: Run the full rwfs package test suite**

```bash
cd src && go test ./cmd/rwfs/... -v
```

Expected: everything still PASSES (existing `arguments_test.go`, `resolve_test.go`, `restore_test.go`, `restoredirectory_test.go`, `rules_test.go`, `verify_test.go` are all unaffected by this task).

- [ ] **Step 7: Commit**

```bash
git add src/cmd/rwfs/list.go src/cmd/rwfs/list_test.go src/cmd/rwfs/verify_test.go
git commit -m "feat(rwfs): rwfs list consumes the new streaming ListFiles RPC

Splits runList into a dial step and runListWithConn for testability
(list.go had no tests before this). A mid-stream error discards any
partial output, matching the old unary call's all-or-nothing
behavior."
```

---

## Task 6: `rwfs verify` uses the shared pieces

**Files:**
- Modify: `src/cmd/rwfs/verify.go`
- Modify: `src/cmd/rwfs/verify_test.go`

**Interfaces:**
- Consumes: `runWorkerPool` (Task 2), `withStallWatchdog`/`streamIdleTimeout` (Task 3), `streamResolvedRows`/`dispatchedRow` (Task 4), streaming `ListFiles` (Task 1), `testResolveServer.ListFiles`/`failingAfterFirstRowListServer` (Task 5).
- `runVerifyWithConn`'s signature is unchanged: `func runVerifyWithConn(logger *slog.Logger, conn *grpc.ClientConn, serverName, pathFilter, filter string, rulesStdin bool, rules []RestoreRule, streams, retries int, quiet bool, jobID string) error`.

- [ ] **Step 1: Write the failing tests — append to `src/cmd/rwfs/verify_test.go`**

Add this dialer helper (mirrors the existing `runVerifyWithDialer`, but for the plain, non-`--rules-stdin` path):

```go
func runVerifyPlainWithDialer(t *testing.T, logger *slog.Logger, lis *bufconn.Listener, serverName, pathFilter, filter string, quiet bool) error {
	t.Helper()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()
	return runVerifyWithConn(logger, conn, serverName, pathFilter, filter, false, nil, 4, 1, quiet, "test-job")
}
```

Append these tests:

```go
// TestRunVerify_PlainPath_StreamsAndVerifiesAllFiles proves the plain
// (non-rules-stdin) path -- previously an atomic unary ListFiles call --
// now streams rows straight into the worker pool and every real file
// still gets verified.
func TestRunVerify_PlainPath_StreamsAndVerifiesAllFiles(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	uuidA := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte{1, 2, 3, 4})
	uuidB := seedRestorableFile(t, store, "hosta", "/data/b.txt", "job1", 1000, []byte{5, 6, 7, 8})

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

	err = runVerifyPlainWithDialer(t, logger, lis, "hosta", "", "", true)
	require.NoError(t, err, "plain verify must succeed for two genuinely valid files: %s", logBuf.String())
	assert.ElementsMatch(t, []string{uuidA, uuidB}, restoreSrv.Requested())
}

// TestRunVerify_PlainPath_MidStreamErrorReportedAlongsideSummary proves
// the new mid-stream failure mode (impossible under the old atomic unary
// call): a row received before the failure was legitimately verified and
// counts, and the run still fails overall.
func TestRunVerify_PlainPath_MidStreamErrorReportedAlongsideSummary(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	uuidA := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte{1, 2, 3, 4})

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &failingAfterFirstRowListServer{
		Row: &pb.FileRow{FileUuid: uuidA, Source: "hosta", Path: "/data/a.txt", Type: "f", Size: 4},
	}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	err = runVerifyPlainWithDialer(t, logger, lis, "hosta", "", "", true)
	require.Error(t, err)
	assert.Equal(t, []string{uuidA}, restoreSrv.Requested(),
		"the row received before the mid-stream failure must still have been dispatched and verified")
}

// TestVerifyFileWithRetry_BacksOffBetweenAttempts proves real backoff
// happens between attempts (not mocked time) -- recordingRestoreServer
// always fails RestoreFile with a plain stream error (never a checksum
// mismatch), so every attempt short of the last one waits.
func TestVerifyFileWithRetry_BacksOffBetweenAttempts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
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
	row := &pb.FileRow{FileUuid: "does-not-exist", Source: "hosta", Path: "/x"}

	start := time.Now()
	verifyFileWithRetry(context.Background(), logger, client, row, 3)
	elapsed := time.Since(start)

	// 3 attempts -> 2 waits; backoff starts at 500ms and doubles, so the
	// floor is 500ms + 1s = 1.5s (well under the 5s cap). Assert a
	// slightly relaxed floor to absorb scheduler jitter.
	if elapsed < 1300*time.Millisecond {
		t.Fatalf("expected at least ~1.5s of backoff across 2 waits, took %v", elapsed)
	}
}

// stallingRestoreServer sends nothing and blocks until its context is
// cancelled -- proves verifyFile's watchdog actually fires on a
// genuinely stalled RestoreFile stream.
type stallingRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
}

func (s *stallingRestoreServer) RestoreFile(_ *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestVerifyFile_WatchdogCancelsAStalledRestoreFileStream(t *testing.T) {
	original := streamIdleTimeout
	streamIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = original })

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterRestoreServiceServer(grpcSrv, &stallingRestoreServer{})
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
	row := &pb.FileRow{FileUuid: "x", Source: "hosta", Path: "/x"}

	done := make(chan verifyResult, 1)
	go func() { done <- verifyFile(context.Background(), client, row) }()

	select {
	case result := <-done:
		require.False(t, result.ok)
		assert.Contains(t, result.reason, "stream error")
	case <-time.After(2 * time.Second):
		t.Fatal("verifyFile never returned after the watchdog should have fired")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd src && go test ./cmd/rwfs/... -run "TestRunVerify_PlainPath|TestVerifyFileWithRetry_BacksOff|TestVerifyFile_Watchdog" -v
```

Expected: FAIL — `TestRunVerify_PlainPath_StreamsAndVerifiesAllFiles` fails because plain verify still does an atomic unary call today (it should already compile and mostly work, but `TestRunVerify_PlainPath_MidStreamErrorReportedAlongsideSummary` and the backoff/watchdog tests fail: the mid-stream test currently can't even reach a mid-stream state under the old unary `ListFiles`, and there's no backoff or watchdog yet).

- [ ] **Step 3: Rewrite `src/cmd/rwfs/verify.go`**

```go
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/checksum"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"google.golang.org/grpc"
	"lukechampine.com/blake3"
)

const (
	retryBackoffInitial = 500 * time.Millisecond
	retryBackoffCap     = 5 * time.Second
)

type verifyResult struct {
	fileUUID   string
	source     string
	path       string
	ok         bool
	reason     string
	chunkIndex int64
	size       int64
	chunkCount int32
}

// rulesStdinPayload is the JSON shape read from stdin when --rules-stdin is
// set -- {"rules": [...]}, the same field name policy-server's
// RestorePolicy.Rules and agent's restore.go use.
type rulesStdinPayload struct {
	Rules []RestoreRule `json:"rules"`
}

// notFoundRule records a file-level rule (non-empty Host) that matched no
// row from ResolveRestoreFiles -- reported as a verification failure,
// unlike a folder-level rule (empty Host) matching nothing, which is a
// legitimate outcome (an empty or already-fully-excluded folder), not an
// error. Reason distinguishes a version outside a requested timeframe from
// a path that plain doesn't exist on this store at all -- populated by
// resolve.go's restoreResolver.NotFound.
type notFoundRule struct {
	Host   string
	Path   string
	Reason string
}

// parseRulesStdin reads and validates the --rules-stdin payload.
//
// An empty rule set is rejected rather than accepted as a no-op: it would
// select zero rows and so report success without having verified anything,
// and a one-shot caller (agent's restore task) would record that vacuous
// success as permanently done. agent skips a rules-less policy before it
// ever gets here (cmd/agent/restore.go); this is the belt-and-suspenders
// half of the same guarantee, for any other caller.
func parseRulesStdin(stdin io.Reader) ([]RestoreRule, error) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read rules from stdin: %w", err)
	}
	var payload rulesStdinPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse rules from stdin: %w", err)
	}
	if len(payload.Rules) == 0 {
		return nil, fmt.Errorf("--rules-stdin requires at least one rule")
	}
	return payload.Rules, nil
}

// runVerify verifies files on a remote bwfs store. jobID rides both the
// ListFiles and the per-file RestoreFile RPCs as outgoing job-id metadata,
// so bwfs's logs for this run correlate with this process's own log -- the
// same convention brfs and policyclient already follow.
func runVerify(logger *slog.Logger, host string, port int, serverName, pathFilter, filter string, rulesStdin bool, stdin io.Reader, streams, retries int, quiet bool, certsDir, jobID string) error {
	// Read and validate the rule set before dialing: it's an argument-shaped
	// error, and ListFiles below is unscoped (see docs/components/rwfs.md),
	// so there's no reason to pay for it only to reject the rules after.
	var rules []RestoreRule
	if rulesStdin {
		parsed, err := parseRulesStdin(stdin)
		if err != nil {
			return err
		}
		rules = parsed
	}

	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	return runVerifyWithConn(logger, conn, serverName, pathFilter, filter, rulesStdin, rules, streams, retries, quiet, jobID)
}

// runVerifyWithConn is runVerify's body, parameterized on an already-dialed
// conn -- split out purely so tests can exercise it over a bufconn dial
// without duplicating anything past the transport-level connect (runVerify
// itself is the only production caller). See verify_test.go's
// runVerifyWithDialer / runVerifyPlainWithDialer.
func runVerifyWithConn(logger *slog.Logger, conn *grpc.ClientConn, serverName, pathFilter, filter string, rulesStdin bool, rules []RestoreRule, streams, retries int, quiet bool, jobID string) error {
	callCtx := jobid.Outgoing(context.Background(), jobID)

	restoreClient := pb.NewRestoreServiceClient(conn)
	workCh := make(chan *pb.FileRow, streams)

	var resolver *restoreResolver
	var streamErrCh <-chan error

	if rulesStdin {
		listClient := pb.NewListServiceClient(conn)
		var rowsCh <-chan dispatchedRow
		rowsCh, resolver, streamErrCh = streamResolvedRows(callCtx, listClient, rules)

		go func() {
			defer close(workCh)
			for r := range rowsCh {
				// resolver.Feed also dispatches directory rows (Type ==
				// "d") for restore.go's benefit -- verify has no use for
				// those (a directory's FileUuid is always empty, and
				// RestoreFile answers an empty/unknown file_uuid with
				// NotFound), so gate on row type here too.
				if r.Row.GetType() == "f" {
					workCh <- r.Row
				}
			}
		}()
	} else {
		listClient := pb.NewListServiceClient(conn)
		errCh := make(chan error, 1)
		streamErrCh = errCh

		watchdogCtx, touch, stop := withStallWatchdog(callCtx, streamIdleTimeout)
		stream, err := listClient.ListFiles(watchdogCtx, &pb.ListRequest{
			ServerName: serverName,
			Path:       pathFilter,
			Filter:     filter,
		})
		if err != nil {
			stop()
			return fmt.Errorf("list files: %w", err)
		}

		go func() {
			defer stop()
			defer close(workCh)
			for {
				row, err := stream.Recv()
				if err == io.EOF {
					errCh <- nil
					return
				}
				if err != nil {
					errCh <- fmt.Errorf("list files: %w", err)
					return
				}
				touch()
				if row.Type == "f" && row.Size > 0 {
					workCh <- row
				}
			}
		}()
	}

	resultCh := runWorkerPool(callCtx, streams, workCh, func(ctx context.Context, row *pb.FileRow) verifyResult {
		return verifyFileWithRetry(ctx, logger, restoreClient, row, retries)
	})

	total := 0
	warnings := 0
	for result := range resultCh {
		total++
		if result.ok {
			if !quiet {
				logger.Info("verified",
					"source", result.source,
					"path", result.path,
					"file_uuid", result.fileUUID,
					"chunks", result.chunkCount,
					"size", result.size,
				)
			}
		} else {
			warnings++
			attrs := []any{
				"source", result.source,
				"path", result.path,
				"file_uuid", result.fileUUID,
				"reason", result.reason,
			}
			if result.reason == "blake3_mismatch" {
				attrs = append(attrs, "chunk_index", result.chunkIndex)
			}
			logger.Warn("verification failed", attrs...)
		}
	}

	if streamErr := <-streamErrCh; streamErr != nil {
		return streamErr
	}

	var notFound []notFoundRule
	if rulesStdin {
		notFound = resolver.NotFound()
	}
	for _, nf := range notFound {
		warnings++
		logger.Warn("verification failed", "source", nf.Host, "path", nf.Path, "reason", nf.Reason)
	}

	logger.Info("summary", "verified", total, "warnings", warnings)
	if warnings > 0 {
		return fmt.Errorf("%d file(s) failed verification", warnings)
	}
	return nil
}

func verifyFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, row *pb.FileRow, maxRetries int) verifyResult {
	backoff := retryBackoffInitial
	var result verifyResult
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result = verifyFile(ctx, client, row)
		if result.ok || result.reason == "blake3_mismatch" || result.reason == "crc_mismatch" {
			return result
		}
		if attempt < maxRetries {
			logger.Warn("stream error, retrying",
				"path", row.Path,
				"file_uuid", row.FileUuid,
				"attempt", attempt,
				"reason", result.reason,
			)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return result
			}
			backoff = min(backoff*2, retryBackoffCap)
		}
	}
	return result
}

func verifyFile(parent context.Context, client pb.RestoreServiceClient, row *pb.FileRow) verifyResult {
	base := verifyResult{
		fileUUID: row.FileUuid,
		source:   row.Source,
		path:     row.Path,
	}

	ctx, touch, stop := withStallWatchdog(parent, streamIdleTimeout)
	defer stop()

	stream, err := client.RestoreFile(ctx, &pb.RestoreRequest{FileUuid: row.FileUuid})
	if err != nil {
		base.reason = fmt.Sprintf("stream error: %v", err)
		return base
	}

	firstEvent, err := stream.Recv()
	if err != nil {
		base.reason = fmt.Sprintf("stream error: %v", err)
		return base
	}
	touch()
	meta := firstEvent.GetMeta()
	if meta == nil {
		base.reason = "stream error: expected RestoreFileMeta as first event"
		return base
	}
	base.size = meta.Size
	base.chunkCount = meta.ChunkCount

	hasher := crc32.NewIEEE()

	for {
		event, err := stream.Recv()
		if err != nil {
			base.reason = fmt.Sprintf("stream error: %v", err)
			return base
		}
		touch()
		chunk := event.GetChunk()
		if chunk == nil {
			base.reason = "stream error: expected RestoreChunk"
			return base
		}

		computed := blake3.Sum256(chunk.Data)
		if !bytes.Equal(computed[:], chunk.Hash) {
			base.reason = "blake3_mismatch"
			base.chunkIndex = chunk.Index
			return base
		}

		checksum.FeedChunk(hasher, crc32.ChecksumIEEE(chunk.Data))

		if chunk.Eof {
			break
		}
	}

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
	if !bytes.Equal(buf[:], meta.ExpectedChecksum) {
		base.reason = "crc_mismatch"
		return base
	}

	base.ok = true
	return base
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd src && go test ./cmd/rwfs/... -v
```

Expected: every test in the package PASSES, including all pre-existing `TestRunVerify_RulesStdin_*` tests (their behavior is unchanged by this refactor) and the new ones from Step 1.

- [ ] **Step 5: Full build and test check**

```bash
make build && make test
```

Expected: clean build, full suite passes.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/rwfs/verify.go src/cmd/rwfs/verify_test.go
git commit -m "refactor(rwfs): verify uses runWorkerPool + streamResolvedRows

Plain path now streams ListFiles (was an atomic unary call) and
gains watchdog protection; --rules-stdin path uses the shared
streamResolvedRows instead of its own inline goroutine; the worker
dispatch loop uses the generic pool from Task 2; retry backoff added
between stream-error retries; each RestoreFile stream is now
watchdog-wrapped."
```

---

## Task 7: `rwfs restore` uses the shared resolved-row source

**Files:**
- Modify: `src/cmd/rwfs/restore.go`

**Interfaces:**
- Consumes: `streamResolvedRows`/`dispatchedRow` (Task 4).
- `runRestoreWithConn`'s signature and all logged output/error text are unchanged.

- [ ] **Step 1: Rewrite `runRestoreWithConn` in `src/cmd/rwfs/restore.go`**

Replace the whole file's imports and `runRestoreWithConn` function (`createRestoreDirectoryStructure` below it is untouched):

```go
// restore.go implements `rwfs restore`: for every row streamResolvedRows
// yields (already run through restoreResolver.Feed's precedence
// tie-break), it logs the row's source path and its computed destination
// path (restoreDestPath's dest_path rename applied), plus the run's
// overwrite setting once at start. Once resolution completes with zero
// not-found failures, phase 1 (createRestoreDirectoryStructure) actually
// recreates every resolved directory on the destination filesystem -- file
// content restore (phase 2, still no RestoreFile call) remains unbuilt --
// see docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md.
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

// runRestore resolves --rules-stdin against a remote bwfs store and logs
// what a real restore of this policy would do. jobID rides the
// ResolveRestoreFiles call as outgoing job-id metadata, the same
// convention runVerify uses.
func runRestore(logger *slog.Logger, host string, port int, overwrite bool, stdin io.Reader, quiet bool, certsDir, jobID string) error {
	rules, err := parseRulesStdin(stdin)
	if err != nil {
		return err
	}

	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	return runRestoreWithConn(logger, conn, overwrite, rules, quiet, jobID)
}

// runRestoreWithConn is runRestore's body, parameterized on an
// already-dialed conn -- split out purely so tests can exercise it over a
// bufconn dial without duplicating anything past the transport-level
// connect (runRestore itself is the only production caller). See
// restore_test.go's runRestoreWithDialer.
func runRestoreWithConn(logger *slog.Logger, conn *grpc.ClientConn, overwrite bool, rules []RestoreRule, quiet bool, jobID string) error {
	callCtx := jobid.Outgoing(context.Background(), jobID)

	logger.Info("restore starting", "overwrite", overwrite, "rules", len(rules))

	listClient := pb.NewListServiceClient(conn)
	rowsCh, resolver, errCh := streamResolvedRows(callCtx, listClient, rules)

	total := 0
	var dirs []restoreDirectory
	for r := range rowsCh {
		destPath := restoreDestPath(rules[r.RuleIndex], r.Row.GetPath())

		if r.Row.GetType() == "d" {
			dirs = append(dirs, restoreDirectory{DestPath: destPath})
			continue
		}

		total++
		if !quiet {
			logger.Info("resolved",
				"source", r.Row.GetSource(),
				"path", r.Row.GetPath(),
				"dest_path", destPath,
			)
		}
	}
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

	return createRestoreDirectoryStructure(logger, dirs)
}
```

`createRestoreDirectoryStructure` (below this function in the same file) is unchanged -- do not modify it.

- [ ] **Step 2: Run the full rwfs test suite**

```bash
cd src && go test ./cmd/rwfs/... -v
```

Expected: every pre-existing `TestRunRestore_*` test in `restore_test.go` still PASSES unchanged (this is a pure internal refactor -- logged text, error text, and exit-code behavior are all identical to before).

- [ ] **Step 3: Full build and test check**

```bash
make build && make test
```

Expected: clean build, full suite passes.

- [ ] **Step 4: Commit**

```bash
git add src/cmd/rwfs/restore.go
git commit -m "refactor(rwfs): restore uses the shared streamResolvedRows

Replaces restore.go's own inline ResolveRestoreFiles consumption
loop with the same streamResolvedRows verify --rules-stdin already
uses (Task 4) -- no behavior change, same logged/error text."
```

---

## Task 8: Documentation and changelog

**Files:**
- Modify: `docs/protocols/list.md`
- Modify: `docs/components/rwfs.md`
- Modify: `CHANGELOG.md`

**Interfaces:** None (docs only).

- [ ] **Step 1: Update `docs/protocols/list.md`**

Replace the protocol definition block:

```proto
service ListService {
  rpc ListFiles(ListRequest) returns (stream FileRow);
  rpc ResolveRestoreFiles(ResolveRestoreFilesRequest) returns (stream ResolveRestoreFilesResponse);
}

message ListRequest {
  string server_name = 1; // exact hostname filter; empty = all sources
  string path        = 2; // prefix filter on file path; empty = no filter
  string filter      = 3; // free-text substring filter; empty = no filter
}

message FileRow {
  string file_uuid     = 1;
  string source        = 2;
  string type          = 3;
  string path          = 4;
  int64  timestamp      = 5; // Unix mtime from file ID
  int64  size           = 6; // bytes
  int32  chunks         = 7;
  int64  versions       = 8; // distinct FileVersionRecords for this file
  string created_at     = 9; // RFC3339 UTC — when this FileDataRecord was finalized
}
```

Replace the "Why unary RPC instead of server streaming?" paragraph under "Key Design Decisions" with:

```markdown
**Why did ListFiles move from unary to server streaming?**
The original unary response was capped by gRPC's default ~4MB `MaxRecvMsgSize` — a host with a
large listing could hit that ceiling outright, and a caller had to wait for the whole response
before processing anything. `bwfs` still runs one `queryFileRows` query per request and
materializes the full filtered result server-side before streaming it out row by row — this
change removes the wire-transfer ceiling, not server-side memory scale for a store with millions
of rows, which remains the same "thousands of entries per host, not millions" tradeoff the
original design accepted, just narrowed to where it actually still applies. See
[rwfs's reliability/performance design](../superpowers/specs/2026-08-16-rwfs-reliability-performance-design.md).
```

Also update the sentence right below the protocol flow diagram that used to read "The RPC is unary (not streaming): the server collects all matching rows from SQLite and returns them in a single response..." — replace it with:

```markdown
The RPC streams one `FileRow` per matching row rather than returning a single batched response,
removing any hard cap on how many rows a listing can contain on the wire.
```

- [ ] **Step 2: Update `docs/components/rwfs.md`**

In the `## verify` section, right after the paragraph describing `job_id` correlation (the one starting "Every line `rwfs` logs carries `job_id`..."), add:

```markdown
`ResolveRestoreFiles` (used by `--rules-stdin`) and each per-file `RestoreFile` stream are protected
by an idle-timeout watchdog (60s, fixed): a stream that's actively producing data is never
penalized for running long, but one that goes idle that long is cancelled rather than hanging
forever. `verifyFileWithRetry` also waits between retry attempts (capped, doubling backoff starting
at 500ms) instead of retrying immediately, so a struggling `bwfs` isn't hammered. Internally, `list`,
`verify`, and `restore` share a generic worker pool and a common resolved-row source for
`ResolveRestoreFiles` consumption — none of this is CLI-visible, but it's the reusable shape a future
file-content restore phase is expected to build on. See
[Design: rwfs Reliability, Performance, and Reuse](../superpowers/specs/2026-08-16-rwfs-reliability-performance-design.md).
```

- [ ] **Step 3: Add a `CHANGELOG.md` entry**

Insert at the top, right after the `# Changelog` header and its intro line, above the existing `## 2026-08-16 — restore execution: directory structure phase` entry:

```markdown
## 2026-08-16 — rwfs: streaming ListFiles, stall protection, and shared internals

`bwfs`'s `ListFiles` RPC is now server-streaming instead of unary, removing the implicit ~4MB
gRPC message-size ceiling a large per-host listing could hit, and letting `rwfs verify`'s plain
path start verifying as rows arrive instead of waiting for the whole listing. `rwfs`'s two
previously-unbounded streaming calls (`ResolveRestoreFiles` and each per-file `RestoreFile`) now
carry an idle-timeout watchdog so a stalled `bwfs` can no longer hang a run forever, and verify's
retry loop backs off between attempts instead of retrying immediately. Internally, `verify` and
`restore` no longer each hand-roll their own stream-consumption loop — both now share one
resolved-row source, and verify's worker pool is a generic, reusable piece rather than
hand-rolled channel plumbing. No CLI-visible behavior change.
```

- [ ] **Step 4: Commit**

```bash
git add docs/protocols/list.md docs/components/rwfs.md CHANGELOG.md
git commit -m "docs(rwfs): document streaming ListFiles, watchdog, and backoff"
```

---

## Final Verification

- [ ] **Run the complete test suite and build from repo root**

```bash
make build && make test
```

Expected: clean build, all tests pass.

- [ ] **Spot-check the CLI manually against a local demo store** (per this repo's guidance to exercise real functionality, not just tests)

```bash
# Terminal 1
bin/bwfs /tmp/rwfs-plan-demo-store server --port 8080 &

# Terminal 2 -- back up a small tree, then exercise list/verify/restore
mkdir -p /tmp/rwfs-plan-demo-src/a/b
echo hello > /tmp/rwfs-plan-demo-src/a/b/c.txt
bin/brfs /tmp/rwfs-plan-demo-src --destination localhost:8080
bin/rwfs list localhost:8080
bin/rwfs verify localhost:8080 --streams 4
echo '{"rules":[{"host":"","path":"/tmp/rwfs-plan-demo-src","include":true,"dest_path":"/tmp/rwfs-plan-demo-restored"}]}' \
  | bin/rwfs restore localhost:8080 --rules-stdin
```

Expected: `list` shows the backed-up file and its ancestor directories; `verify` reports `verified=1 warnings=0` (or however many real files/dirs were backed up); `restore` logs `restored directory structure created` and `/tmp/rwfs-plan-demo-restored/a/b` actually exists on disk afterward.
