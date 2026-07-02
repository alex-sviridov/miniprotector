# Backup Job Tracking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every `brfs` invocation a `job_id`, propagate it to `bwfs` over gRPC metadata, and have `bwfs` record each job (source host, start/finish time) and link every file version it writes to the job that produced it.

**Architecture:** `brfs` generates (or accepts via `--job-id`) a UUID and attaches it as outgoing gRPC metadata on every stream it opens. `bwfs` reads that metadata once per stream, derives the source host from the client's verified mTLS certificate (not client-reported data), idempotently ensures a `backup_jobs` row exists, and uses an in-memory per-job stream refcount to set `finished_at` when the job's last stream closes. Every `file_versions` row gets a `job_id` column (no junction table — the relationship is one job → many versions, never reused across jobs) with a `(job_id, object_id)` uniqueness constraint so a duplicate send within a job is a safe no-op.

**Tech Stack:** Go, GORM (`gorm.io/gorm`) over `modernc.org/sqlite`, gRPC (`google.golang.org/grpc`), Cobra CLI, testify.

**Spec:** `docs/superpowers/specs/2026-07-02-backup-job-tracking-design.md`

## Global Constraints

- No `.proto` changes — `job_id` travels as gRPC metadata (key `job-id`), read via `metadata.FromIncomingContext` / written via `metadata.AppendToOutgoingContext`.
- `source_host` on `backup_jobs` comes from the client's mTLS peer certificate (first SAN entry, falling back to CommonName) — never from client-reported data.
- No junction table for job↔file-version — `job_id` is a column directly on `file_versions`, unique together with `object_id`.
- All idempotent writes (`EnsureBackupJob`, `EnsureFileVersion`) are first-write-wins via `clause.OnConflict{DoNothing: true}`, matching the existing pattern in `src/storage/filesystem/chunks.go`.
- A stream with no `job-id` metadata, or no resolvable peer identity, is rejected immediately (`codes.InvalidArgument` / `codes.Unauthenticated`) — no silent fallback.
- Per `.claude/CLAUDE.md`, any commit touching wire behavior must update `docs/protocols/backup.md`, and component docs must be updated for the new `--job-id` flag and `bwfs` schema/behavior — done in the final task.
- Run `cd src && go test ./...` after every task (this is the Makefile `test` target and excludes `//go:build integration` files). Run `cd src && go test -tags=integration ./cmd/bwfs/...` only from Task 5 onward — it is expected to fail after Task 4 until Task 5 updates the harness; that's called out explicitly in that task.

---

### Task 1: `jobTracker` — in-memory per-job stream refcounting

**Files:**
- Create: `src/cmd/bwfs/jobtracker.go`
- Create: `src/cmd/bwfs/jobtracker_test.go`

**Interfaces:**
- Produces: `newJobTracker() *jobTracker`, `(*jobTracker).Start(jobID string)`, `(*jobTracker).Finish(jobID string) bool` — `Finish` returns `true` exactly when the job's active-stream count reaches zero. Task 4 wires this into `backupServer`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/bwfs/jobtracker_test.go`:

```go
package main

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJobTracker_FinishReturnsTrueOnlyForLastStream(t *testing.T) {
	tr := newJobTracker()
	tr.Start("job-1")
	tr.Start("job-1")

	assert.False(t, tr.Finish("job-1"), "first Finish: one stream still active")
	assert.True(t, tr.Finish("job-1"), "second Finish: last stream closing should report true")
}

func TestJobTracker_IndependentJobs(t *testing.T) {
	tr := newJobTracker()
	tr.Start("job-1")
	tr.Start("job-2")

	assert.True(t, tr.Finish("job-1"), "job-1 has no more active streams")
	assert.True(t, tr.Finish("job-2"), "job-2 has no more active streams")
}

func TestJobTracker_ConcurrentStartFinish(t *testing.T) {
	tr := newJobTracker()
	const streams = 50

	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.Start("job-1")
		}()
	}
	wg.Wait()

	var mu sync.Mutex
	lastCount := 0
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tr.Finish("job-1") {
				mu.Lock()
				lastCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, lastCount, "exactly one Finish call should observe the last-stream transition")
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./cmd/bwfs/... -run TestJobTracker`
Expected: FAIL — `undefined: newJobTracker`

- [ ] **Step 3: Implement jobTracker**

Create `src/cmd/bwfs/jobtracker.go`:

```go
package main

import "sync"

// jobTracker counts the number of currently active streams per backup job,
// so the server can detect when a job's last stream has closed.
type jobTracker struct {
	mu     sync.Mutex
	active map[string]int
}

func newJobTracker() *jobTracker {
	return &jobTracker{active: make(map[string]int)}
}

// Start records a new active stream for jobID.
func (t *jobTracker) Start(jobID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[jobID]++
}

// Finish records that a stream for jobID has ended. It returns true when
// this was the last active stream for that job (the count reached zero).
func (t *jobTracker) Finish(jobID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[jobID]--
	if t.active[jobID] <= 0 {
		delete(t.active, jobID)
		return true
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test -race ./cmd/bwfs/... -run TestJobTracker -v`
Expected: PASS (all 3 tests)

- [ ] **Step 5: Commit**

```bash
cd src && git add cmd/bwfs/jobtracker.go cmd/bwfs/jobtracker_test.go
git commit -m "feat(bwfs): add jobTracker for per-job stream refcounting"
```

---

### Task 2: `mtls.PeerHostname` — verified source host from the client cert

**Files:**
- Create: `src/common/mtls/peer.go`
- Create: `src/common/mtls/peer_test.go`

**Interfaces:**
- Consumes: existing fixture certs at `src/common/testdata/certs/client.crt` (SAN `bwfs.internal`), already used by `src/common/mtls/mtls_test.go` (same package, shares the `fixtureCertsDir` const — do not redeclare it).
- Produces: `mtls.PeerHostname(ctx context.Context) (string, error)`. Task 4 calls this from `cmd/bwfs/server.go`.

- [ ] **Step 1: Write the failing tests**

Create `src/common/mtls/peer_test.go`:

```go
package mtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func loadFixtureCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	certPEM, err := os.ReadFile(path)
	require.NoError(t, err)
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block, "no PEM block found in %s", path)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

func selfSignedCertNoSAN(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

func TestPeerHostname_ReturnsFirstSAN(t *testing.T) {
	cert := loadFixtureCert(t, fixtureCertsDir+"/client.crt")
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})

	host, err := PeerHostname(ctx)
	require.NoError(t, err)
	assert.Equal(t, "bwfs.internal", host)
}

func TestPeerHostname_FallsBackToCommonName(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "cn-only-node")
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})

	host, err := PeerHostname(ctx)
	require.NoError(t, err)
	assert.Equal(t, "cn-only-node", host)
}

func TestPeerHostname_NoPeerInContext(t *testing.T) {
	_, err := PeerHostname(context.Background())
	assert.Error(t, err)
}

func TestPeerHostname_NoTLSAuthInfo(t *testing.T) {
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: nil})
	_, err := PeerHostname(ctx)
	assert.Error(t, err)
}

func TestPeerHostname_NoPeerCertificates(t *testing.T) {
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}},
	})
	_, err := PeerHostname(ctx)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./common/mtls/... -run TestPeerHostname`
Expected: FAIL — `undefined: PeerHostname`

- [ ] **Step 3: Implement PeerHostname**

Create `src/common/mtls/peer.go`:

```go
package mtls

import (
	"context"
	"fmt"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// PeerHostname extracts the verified hostname identity from the client
// certificate presented on ctx's gRPC peer connection: the first SAN entry,
// falling back to the Subject CommonName if no SAN is present. certrequest
// always places the primary hostname first in a cert's SAN list, so this
// reflects the CA-verified node identity rather than anything the caller
// could self-report over the wire.
func PeerHostname(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("no peer information in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", fmt.Errorf("peer connection is not authenticated via TLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", fmt.Errorf("no peer certificate presented")
	}
	cert := tlsInfo.State.PeerCertificates[0]
	if len(cert.DNSNames) > 0 {
		return cert.DNSNames[0], nil
	}
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName, nil
	}
	return "", fmt.Errorf("peer certificate has no SAN or CommonName")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/mtls/... -run TestPeerHostname -v`
Expected: PASS (all 5 tests)

- [ ] **Step 5: Commit**

```bash
cd src && git add common/mtls/peer.go common/mtls/peer_test.go
git commit -m "feat(mtls): add PeerHostname to extract verified client identity"
```

---

### Task 3: `backup_jobs` table and `EnsureBackupJob`/`FinishBackupJob`

**Files:**
- Modify: `src/storage/filesystem/models.go`
- Modify: `src/storage/filesystem/db.go`
- Create: `src/storage/filesystem/backupjob.go`
- Modify: `src/storage/interface.go`
- Modify: `src/storage/filesystem/store_test.go`

**Interfaces:**
- Produces: `(s *Store) EnsureBackupJob(jobID, sourceHost string) error`, `(s *Store) FinishBackupJob(jobID string) error`, model `filesystem.BackupJobRecord{JobID, SourceHost, StartedAt, FinishedAt *time.Time}`. Task 4 does not call these directly (Task 4 calls them from `cmd/bwfs`, importing `storage.BackupStore`), but the interface additions here are what make that possible.

- [ ] **Step 1: Write the failing tests**

Add to `src/storage/filesystem/store_test.go` (after `TestOpenDB_CreatesSchemaAndFile`, i.e. anywhere after the imports/helpers — exact position doesn't matter, Go doesn't order by declaration):

```go
func TestEnsureBackupJob_CreatesRow(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, "host-a", record.SourceHost)
	assert.Nil(t, record.FinishedAt)
	assert.WithinDuration(t, time.Now(), record.StartedAt, 5*time.Second)
}

func TestEnsureBackupJob_SecondCallIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))
	require.NoError(t, store.EnsureBackupJob("job-1", "host-b"))

	var count int64
	require.NoError(t, store.db.Model(&BackupJobRecord{}).Where("job_id = ?", "job-1").Count(&count).Error)
	assert.Equal(t, int64(1), count)

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, "host-a", record.SourceHost, "first write should win")
}

func TestFinishBackupJob_SetsFinishedAt(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))

	require.NoError(t, store.FinishBackupJob("job-1"))

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	require.NotNil(t, record.FinishedAt)
	assert.WithinDuration(t, time.Now(), *record.FinishedAt, 5*time.Second)
}
```

Also add one line to the existing `TestOpenDB_CreatesSchemaAndFile` (right after the `file_version_records` assertion), so the schema test covers the new table:

```go
	assert.NoError(t, db.Exec("SELECT 1 FROM backup_job_records LIMIT 1").Error)
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./storage/... -run 'TestEnsureBackupJob|TestFinishBackupJob'`
Expected: FAIL — `undefined: BackupJobRecord` / `undefined: store.EnsureBackupJob`

- [ ] **Step 3: Add the model**

In `src/storage/filesystem/models.go`, add after the `FileVersionRecord` struct:

```go

type BackupJobRecord struct {
	JobID      string `gorm:"primaryKey"`
	SourceHost string
	StartedAt  time.Time
	FinishedAt *time.Time
}
```

- [ ] **Step 4: Register the model for migration**

In `src/storage/filesystem/db.go`, add `&BackupJobRecord{}` to the `AutoMigrate` call:

```go
	if err := db.AutoMigrate(
		&ChunkRecord{},
		&FileDataRecord{},
		&FileDataChunkRecord{},
		&FileVersionRecord{},
		&BackupJobRecord{},
	); err != nil {
```

- [ ] **Step 5: Implement EnsureBackupJob and FinishBackupJob**

Create `src/storage/filesystem/backupjob.go`:

```go
package filesystem

import (
	"time"

	"gorm.io/gorm/clause"
)

// EnsureBackupJob idempotently records that a backup job has started. Safe
// to call once per stream of a multi-stream job — only the first call for a
// given jobID creates a row; later calls are no-ops.
func (s *Store) EnsureBackupJob(jobID, sourceHost string) error {
	record := BackupJobRecord{
		JobID:      jobID,
		SourceHost: sourceHost,
		StartedAt:  time.Now(),
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "job_id"}},
		DoNothing: true,
	}).Create(&record).Error
}

// FinishBackupJob marks a backup job complete by setting finished_at.
func (s *Store) FinishBackupJob(jobID string) error {
	return s.db.Model(&BackupJobRecord{}).
		Where("job_id = ?", jobID).
		Update("finished_at", time.Now()).Error
}
```

- [ ] **Step 6: Add the methods to the BackupStore interface**

In `src/storage/interface.go`, add to the `BackupStore` interface (near the `FileVersion operations` comment block):

```go
	// Backup job operations - track discrete backup runs (one brfs invocation each).
	EnsureBackupJob(jobID, sourceHost string) error
	FinishBackupJob(jobID string) error
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd src && go test ./storage/... -v -run 'TestEnsureBackupJob|TestFinishBackupJob|TestOpenDB_CreatesSchemaAndFile'`
Expected: PASS (all tests)

- [ ] **Step 8: Run the full storage package suite**

Run: `cd src && go test ./storage/...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
cd src && git add storage/filesystem/models.go storage/filesystem/db.go storage/filesystem/backupjob.go storage/interface.go storage/filesystem/store_test.go
git commit -m "feat(storage): add backup_jobs table with idempotent Ensure/FinishBackupJob"
```

---

### Task 4: Job-aware file versions end to end

This task makes `file_versions` job-aware (schema + idempotent write) and wires the entire live request path (`ProcessBackupStream` → `streamHandler` → `EnsureFileVersion`) to actually use jobs. These are split across `storage` and `cmd/bwfs`, but must land together — `cmd/bwfs` calls the storage interface method being renamed here, so splitting them would leave the repo non-compiling between commits.

**Files:**
- Modify: `src/storage/filesystem/models.go`
- Modify: `src/storage/filesystem/fileversion.go`
- Modify: `src/storage/interface.go`
- Modify: `src/storage/filesystem/store_test.go`
- Modify: `src/cmd/bwfs/handler.go`
- Modify: `src/cmd/bwfs/server.go`

**Interfaces:**
- Consumes: `newJobTracker() *jobTracker`, `(*jobTracker).Start`/`.Finish` (Task 1); `mtls.PeerHostname(ctx) (string, error)` (Task 2); `(s *Store) EnsureBackupJob`/`FinishBackupJob` (Task 3).
- Produces: `(s *Store) EnsureFileVersion(jobID, objectID string, metadata []byte, ctime int64) error` (replaces `CreateFileVersion`, which is deleted); `newStreamHandler(ctx, logger, store, jobID string) *streamHandler` (new `jobID` parameter); `backupServer.jobs *jobTracker` field.

- [ ] **Step 1: Update the FileVersionRecord model**

In `src/storage/filesystem/models.go`, change `FileVersionRecord` to:

```go
type FileVersionRecord struct {
	UUID      string `gorm:"primaryKey"`
	ObjectID  string `gorm:"uniqueIndex:idx_job_object"`
	JobID     string `gorm:"uniqueIndex:idx_job_object"`
	Metadata  []byte
	Ctime     int64
	CreatedAt time.Time
}
```

- [ ] **Step 2: Rewrite the storage tests for EnsureFileVersion**

In `src/storage/filesystem/store_test.go`, replace `TestCreateFileVersion_ReturnsID` with:

```go
func TestEnsureFileVersion_CreatesRow(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta"), 12345))

	v, err := store.LatestFileVersion("obj-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("meta"), v.Metadata)
	assert.Equal(t, int64(12345), v.Ctime)
}

func TestEnsureFileVersion_DuplicateWithinJobIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("first"), 100))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("second"), 200))

	var count int64
	require.NoError(t, store.db.Model(&FileVersionRecord{}).
		Where("job_id = ? AND object_id = ?", "job-1", "obj-1").
		Count(&count).Error)
	assert.Equal(t, int64(1), count)

	v, err := store.LatestFileVersion("obj-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("first"), v.Metadata, "first write within a job should win")
}
```

Replace `TestLatestFileVersion_ReturnsNewest` with (two different jobs — that's the real-world case, since duplicate-within-a-job is now a no-op):

```go
func TestLatestFileVersion_ReturnsNewest(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta-old"), 100))
	require.NoError(t, store.EnsureFileVersion("job-2", "obj-1", []byte("meta-new"), 200))

	v, err := store.LatestFileVersion("obj-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("meta-new"), v.Metadata)
	assert.Equal(t, int64(200), v.Ctime)
}
```

Replace `TestRemoveFileVersion_Removes` with (constructs the row directly since `EnsureFileVersion` no longer returns a UUID — matching how `TestFileVersionAtTime_ReturnsMostRecentBefore` already builds rows directly below):

```go
func TestRemoveFileVersion_Removes(t *testing.T) {
	store := newTestStore(t)
	id := uuid.New().String()
	require.NoError(t, store.db.Create(&FileVersionRecord{
		UUID: id, JobID: "job-1", ObjectID: "obj-1", Metadata: []byte("meta"), Ctime: 100, CreatedAt: time.Now(),
	}).Error)

	require.NoError(t, store.RemoveFileVersion(id))

	_, err := store.LatestFileVersion("obj-1")
	assert.Error(t, err)
}
```

In `TestFileVersionAtTime_ReturnsMostRecentBefore`, add distinct `JobID` values so the two rows (both `ObjectID: "obj-1"`) don't collide on the new unique index:

```go
	old := FileVersionRecord{UUID: uuid.New().String(), JobID: "job-old", ObjectID: "obj-1", Metadata: []byte("old"), Ctime: 1, CreatedAt: now.Add(-2 * time.Hour)}
	recent := FileVersionRecord{UUID: uuid.New().String(), JobID: "job-recent", ObjectID: "obj-1", Metadata: []byte("recent"), Ctime: 2, CreatedAt: now.Add(-1 * time.Hour)}
```

In `TestFileVersionsInPeriod_ReturnsAll`, add `JobID` values for realism (not strictly required since the two rows already have different `ObjectID`s, but keeps every constructed row valid going forward):

```go
	r1 := FileVersionRecord{UUID: uuid.New().String(), JobID: "job-1", ObjectID: "obj-1", CreatedAt: now.Add(-3 * time.Hour)}
	r2 := FileVersionRecord{UUID: uuid.New().String(), JobID: "job-2", ObjectID: "obj-2", CreatedAt: now.Add(-1 * time.Hour)}
```

In `TestStoreInfo_CountsCorrectly`, replace:

```go
	_, err := store.CreateFileVersion("obj-1", []byte("meta"), 0)
	require.NoError(t, err)
```

with:

```go
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta"), 0))
```

- [ ] **Step 3: Run tests to verify they fail to compile**

Run: `cd src && go test ./storage/...`
Expected: FAIL — `undefined: store.EnsureFileVersion` (and `store.CreateFileVersion` no longer referenced by tests, but still exists in `fileversion.go` until the next step)

- [ ] **Step 4: Rewrite fileversion.go**

Replace `CreateFileVersion` in `src/storage/filesystem/fileversion.go` with:

```go
// EnsureFileVersion idempotently records that objectID was observed during
// jobID's backup run. The first observation of a given (jobID, objectID)
// pair wins — a duplicate send of the same object within the same job (e.g.
// a future retry) is a safe no-op rather than a second catalog row.
func (s *Store) EnsureFileVersion(jobID, objectID string, metadata []byte, ctime int64) error {
	record := FileVersionRecord{
		UUID:      uuid.New().String(),
		JobID:     jobID,
		ObjectID:  objectID,
		Metadata:  metadata,
		Ctime:     ctime,
		CreatedAt: time.Now(),
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&record).Error
}
```

Add `"gorm.io/gorm/clause"` to the file's imports.

- [ ] **Step 5: Update the BackupStore interface**

In `src/storage/interface.go`, replace:

```go
	CreateFileVersion(objectID string, metadata []byte, ctime int64) (versionID string, err error)
```

with:

```go
	EnsureFileVersion(jobID, objectID string, metadata []byte, ctime int64) error
```

- [ ] **Step 6: Run the storage package tests**

Run: `cd src && go test ./storage/... -v`
Expected: PASS (all tests, including the new/rewritten ones)

- [ ] **Step 7: Add jobID to streamHandler and wire its two EnsureFileVersion call sites**

In `src/cmd/bwfs/handler.go`, add a `jobID` field and thread it through the constructor:

```go
type streamHandler struct {
	config             *config.Config
	store              storage.BackupStore
	logger             *slog.Logger
	jobID              string
	currentFile        *filesystem.FileInfo
	fileChecksumHasher hash.Hash32 // incremental CRC32 over chunk checksums
	EOF                bool
	handlerMap         map[string]RequestHandlerFunc
}

func newStreamHandler(ctx context.Context, logger *slog.Logger, store storage.BackupStore, jobID string) *streamHandler {
	handler := &streamHandler{
		config: config.GetConfigFromContext(ctx),
		store:  store,
		logger: logger,
		jobID:  jobID,
	}
	handler.handlerMap = map[string]RequestHandlerFunc{
		fmt.Sprintf("%T", &pb.FileRequest_FileInfo{}):  handler.handleFileInfoRequest,
		fmt.Sprintf("%T", &pb.FileRequest_ChunkHash{}): handler.handleChunkHashRequest,
		fmt.Sprintf("%T", &pb.FileRequest_ChunkData{}): handler.handleChunkDataRequest,
	}
	handler.logger.Info("New backup stream connected")
	return handler
}
```

In `handleFileInfoRequest`, replace the skip-path version creation:

```go
		if _, err := h.store.CreateFileVersion(
			h.currentFile.ID(),
			h.currentFile.MetadataBlob(),
			h.currentFile.Ctime(),
		); err != nil {
			return fmt.Errorf("create file version: %w", err)
		}
```

with:

```go
		if err := h.store.EnsureFileVersion(
			h.jobID,
			h.currentFile.ID(),
			h.currentFile.MetadataBlob(),
			h.currentFile.Ctime(),
		); err != nil {
			return fmt.Errorf("ensure file version: %w", err)
		}
```

In `fileWritten`, replace:

```go
	if _, err := h.store.CreateFileVersion(
		h.currentFile.ID(),
		h.currentFile.MetadataBlob(),
		h.currentFile.Ctime(),
	); err != nil {
		return fmt.Errorf("create file version: %w", err)
	}
```

with:

```go
	if err := h.store.EnsureFileVersion(
		h.jobID,
		h.currentFile.ID(),
		h.currentFile.MetadataBlob(),
		h.currentFile.Ctime(),
	); err != nil {
		return fmt.Errorf("ensure file version: %w", err)
	}
```

- [ ] **Step 8: Wire job-id metadata, source host, and the job lifecycle into ProcessBackupStream**

Replace the full contents of `src/cmd/bwfs/server.go` with:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/alex-sviridov/miniprotector/storage"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"

	pb "github.com/alex-sviridov/miniprotector/api"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type backupServer struct {
	pb.UnimplementedBackupServiceServer
	config *config.Config
	store  storage.BackupStore
	logger *slog.Logger
	jobs   *jobTracker
}

func NewBackupServer(ctx context.Context, logger *slog.Logger, storagePath string) (*backupServer, error) {
	conf := config.GetConfigFromContext(ctx)

	store, err := wfs.New(storagePath)
	if err != nil {
		return nil, err
	}
	return &backupServer{
		logger: logger,
		config: conf,
		store:  store,
		jobs:   newJobTracker(),
	}, nil
}

// jobIDFromMetadata reads the job-id gRPC metadata key that brfs attaches
// when it opens each stream. There is no default: a stream without it is
// rejected rather than silently treated as jobless.
func jobIDFromMetadata(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("no metadata in request")
	}
	values := md.Get("job-id")
	if len(values) == 0 || values[0] == "" {
		return "", fmt.Errorf("missing job-id metadata")
	}
	return values[0], nil
}

func (server *backupServer) ProcessBackupStream(stream pb.BackupService_ProcessBackupStreamServer) error {
	ctx := stream.Context()

	jobID, err := jobIDFromMetadata(ctx)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "job-id metadata required: %v", err)
	}

	sourceHost, err := mtls.PeerHostname(ctx)
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "resolve peer identity: %v", err)
	}

	if err := server.store.EnsureBackupJob(jobID, sourceHost); err != nil {
		return status.Errorf(codes.Internal, "ensure backup job: %v", err)
	}
	server.jobs.Start(jobID)
	defer func() {
		if server.jobs.Finish(jobID) {
			if err := server.store.FinishBackupJob(jobID); err != nil {
				server.logger.Error("Failed to finish backup job", "job_id", jobID, "error", err)
			}
		}
	}()

	var clientAddr, clientAuthType string = "unknown", "none"
	if peer, ok := peer.FromContext(ctx); ok {
		clientAddr = peer.Addr.String()
		if peer.AuthInfo != nil {
			clientAuthType = peer.AuthInfo.AuthType()
		}
	}

	streamInfo := fmt.Sprintf("%p", stream)
	logger := server.logger.With(
		slog.String("client_addr", clientAddr),
		slog.Any("grpc_auth_type", clientAuthType),
		slog.String("stream_id", streamInfo),
		slog.String("job_id", jobID),
	)
	ctx = context.WithValue(ctx, config.ContextKey, server.config)

	h := newStreamHandler(ctx, logger, server.store, jobID)

	for {
		request, err := stream.Recv()
		if err == io.EOF {
			h.logger.Info("Client stopped sending")
			return nil
		}
		if err != nil {
			h.logger.Error("Error receiving", "error", err)
			return err
		}
		if request == nil {
			continue
		}
		if err := h.handleRequest(ctx, stream, request); err != nil {
			h.logger.Error("Error handling request", "error", err)
		}
		if h.EOF {
			if err := h.fileWritten(ctx, stream); err != nil {
				h.logger.Error("Error finalizing file", "error", err)
			}
		}
	}
}
```

- [ ] **Step 9: Run go vet and build the whole module**

Run: `cd src && go vet ./... && go build ./...`
Expected: no errors (this confirms `cmd/bwfs`, `storage`, and every other package still compile together)

- [ ] **Step 10: Run the full non-integration test suite**

Run: `cd src && go test ./...`
Expected: PASS. Note: do NOT run `go test -tags=integration ./cmd/bwfs/...` yet — `integration_test.go` still uses insecure bufconn credentials and sends no `job-id` metadata, so it will fail until Task 5 updates the harness. That is expected and fixed next.

- [ ] **Step 11: Commit**

```bash
cd src && git add storage/filesystem/models.go storage/filesystem/fileversion.go storage/interface.go storage/filesystem/store_test.go cmd/bwfs/handler.go cmd/bwfs/server.go
git commit -m "feat(bwfs): require job-id metadata and mTLS source host, link file versions to jobs"
```

---

### Task 5: bwfs integration test harness — real mTLS + job coverage

**Files:**
- Modify: `src/cmd/bwfs/integration_test.go`

**Interfaces:**
- Consumes: `mtls.LoadServerCredentials(certsDir string)`, `mtls.LoadClientCredentials(certsDir, host string)` (existing, `src/common/mtls/mtls.go`); fixture certs at `src/common/testdata/certs` (SAN `bwfs.internal`, already used by `src/common/mtls/mtls_test.go` and `src/common/connection/mtls_wiring_test.go`); `storagefs.BackupJobRecord`, `storagefs.FileVersionRecord`, `(*storagefs.Store).RawDB()` (`src/storage/filesystem`).

- [ ] **Step 1: Switch the test harness from insecure bufconn credentials to real mTLS**

In `src/cmd/bwfs/integration_test.go`, update the import block — remove `"google.golang.org/grpc/credentials/insecure"` and add:

```go
	"github.com/alex-sviridov/miniprotector/common/mtls"
```

Add a new const next to `bufSize`:

```go
const testCertsDir = "../../common/testdata/certs"
```

In `newTestEnv`, replace:

```go
	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer()
	pb.RegisterBackupServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
```

with:

```go
	serverCreds, err := mtls.LoadServerCredentials(testCertsDir)
	require.NoError(t, err)

	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterBackupServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)

	clientCreds, err := mtls.LoadClientCredentials(testCertsDir, "bwfs.internal")
	require.NoError(t, err)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(clientCreds),
	)
	require.NoError(t, err)
```

- [ ] **Step 2: Add a job-context helper**

Add near `newTestEnv`:

```go
// jobContext attaches job-id gRPC metadata, as brfs does when opening a
// stream. Tests that don't call this and use context.Background() directly
// are exercising the "no job-id" rejection path.
func jobContext(jobID string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "job-id", jobID)
}
```

Add `"google.golang.org/grpc/metadata"` to the import block.

- [ ] **Step 3: Attach job-id metadata to every existing test's context**

In `TestIntegration_SkipPath_DirectoryAndSymlink`, replace `ctx := context.Background()` with `ctx := jobContext("job-skip-path")`.

In `TestIntegration_NewFile_TransferPath`, replace `ctx := context.Background()` with `ctx := jobContext("job-new-file")`.

In `TestIntegration_DedupPath_SecondBackupSkipsChunks`, replace `ctx := context.Background()` with `ctx := jobContext("job-dedup")` (both `stream1` and `stream2` reuse this same `ctx`, matching how the test already works — two sequential streams of the same job).

In `TestIntegration_MultipleFiles_OneStream`, replace `ctx := context.Background()` with `ctx := jobContext("job-multi-file")`.

In `TestIntegration_ConcurrentStreams_SameFileContent`, replace `ctx := context.Background()` with `ctx := jobContext("job-concurrent")` (shared by all 5 goroutines — this test now also exercises `EnsureBackupJob`'s conflict handling under concurrent first-callers for the same job).

- [ ] **Step 4: Run the existing integration suite to confirm the harness change alone is sufficient**

Run: `cd src && go test -tags=integration ./cmd/bwfs/... -v`
Expected: PASS (all 5 pre-existing integration tests)

- [ ] **Step 5: Add a helper to read a backup_jobs row, and the new job-coverage tests**

Add near the top of the file (after the existing helpers), importing `storagefs "github.com/alex-sviridov/miniprotector/storage/filesystem"` and `"io"` in the import block:

```go
// backupJobRow reads a backup_jobs row directly, bypassing the BackupStore
// interface (which intentionally has no query surface for jobs — see the
// design doc's Non-Goals). Only valid because newTestEnv always constructs
// the filesystem-backed store.
func backupJobRow(t *testing.T, env *testEnv, jobID string) (storagefs.BackupJobRecord, error) {
	t.Helper()
	concrete, ok := env.store.store.(*storagefs.Store)
	require.True(t, ok, "test env must use the filesystem store implementation")
	var record storagefs.BackupJobRecord
	err := concrete.RawDB().First(&record, "job_id = ?", jobID).Error
	return record, err
}
```

Add the new tests at the end of the file:

```go
// TestIntegration_MissingJobID_StreamRejected verifies a stream opened
// without job-id metadata is rejected before any file processing.
func TestIntegration_MissingJobID_StreamRejected(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	ctx := context.Background() // no job-id metadata attached
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestIntegration_BackupJob_RecordedWithSourceHost verifies a completed
// stream creates a backup_jobs row with the mTLS-verified source host and a
// non-nil finished_at once the stream closes.
func TestIntegration_BackupJob_RecordedWithSourceHost(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)

	ctx := jobContext("job-source-host")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			_, err := backupOneFile(ctx, t, stream, f)
			require.NoError(t, err)
		}
	}
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	require.Eventually(t, func() bool {
		record, err := backupJobRow(t, env, "job-source-host")
		return err == nil && record.SourceHost == "bwfs.internal" && record.FinishedAt != nil
	}, time.Second, 10*time.Millisecond, "backup job should be recorded with source host and finished_at")
}

// TestIntegration_BackupJob_FinishedAtWaitsForAllStreams verifies finished_at
// is not set while any stream of the job is still open, and is set once the
// last one closes.
func TestIntegration_BackupJob_FinishedAtWaitsForAllStreams(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-multi-stream")

	stream1, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	stream2, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	_, err = backupOneFile(ctx, t, stream1, target)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := backupJobRow(t, env, "job-multi-stream")
		return err == nil
	}, time.Second, 10*time.Millisecond, "backup job row should exist once the first stream starts")

	require.NoError(t, stream1.CloseSend())
	_, err = stream1.Recv()
	require.ErrorIs(t, err, io.EOF)

	// stream2 still open — finished_at must not be set yet.
	record, err := backupJobRow(t, env, "job-multi-stream")
	require.NoError(t, err)
	assert.Nil(t, record.FinishedAt, "finished_at must stay nil while stream2 is still open")

	require.NoError(t, stream2.CloseSend())
	_, err = stream2.Recv()
	require.ErrorIs(t, err, io.EOF)

	require.Eventually(t, func() bool {
		record, err := backupJobRow(t, env, "job-multi-stream")
		return err == nil && record.FinishedAt != nil
	}, time.Second, 10*time.Millisecond, "finished_at should be set once both streams close")
}

// TestIntegration_DuplicateFileWithinJob_OneFileVersionRow verifies that
// sending the same file twice within one job (simulating a retry) does not
// create two file_version rows.
func TestIntegration_DuplicateFileWithinJob_OneFileVersionRow(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-duplicate")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	// Same file, same stream, sent again within the same job.
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)

	require.NoError(t, stream.CloseSend())

	concrete, ok := env.store.store.(*storagefs.Store)
	require.True(t, ok)
	var count int64
	require.NoError(t, concrete.RawDB().Model(&storagefs.FileVersionRecord{}).
		Where("job_id = ? AND object_id = ?", "job-duplicate", target.ID()).
		Count(&count).Error)
	assert.Equal(t, int64(1), count)
}
```

Also add `"google.golang.org/grpc/codes"` and `"google.golang.org/grpc/status"` to the import block (used by `TestIntegration_MissingJobID_StreamRejected`).

- [ ] **Step 6: Run the full integration suite**

Run: `cd src && go test -tags=integration ./cmd/bwfs/... -v`
Expected: PASS (all 9 tests: 5 pre-existing + 4 new)

- [ ] **Step 7: Run the full non-integration suite too, to confirm no regressions**

Run: `cd src && go test ./...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
cd src && git add cmd/bwfs/integration_test.go
git commit -m "test(bwfs): exercise real mTLS and job tracking in integration tests"
```

---

### Task 6: brfs generates/accepts a job ID and sends it to bwfs

**Files:**
- Modify: `src/cmd/brfs/arguments.go`
- Modify: `src/cmd/brfs/arguments_test.go`
- Modify: `src/cmd/brfs/main.go`

**Interfaces:**
- Produces: `Arguments.JobID string` (empty if `--job-id` wasn't passed); `main.go` resolves this to a UUID when empty and attaches it as `job-id` gRPC metadata on the context used to open every stream.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/brfs/arguments_test.go`:

```go
func TestParseArguments_JobIDFlag_ParsesValue(t *testing.T) {
	dir := t.TempDir()
	withArgs(t, []string{"brfs", dir, "--job-id", "custom-job-123"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "custom-job-123", args.JobID)
	})
}

func TestParseArguments_JobIDFlag_DefaultsEmpty(t *testing.T) {
	dir := t.TempDir()
	withArgs(t, []string{"brfs", dir}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Empty(t, args.JobID)
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/brfs/... -run TestParseArguments_JobIDFlag`
Expected: FAIL — `args.JobID undefined` (field doesn't exist)

- [ ] **Step 3: Add the --job-id flag**

In `src/cmd/brfs/arguments.go`, add `jobIDFlag` to the package-level var block:

```go
var (
	destination string
	streams     int
	debug       bool
	quiet       bool
	jobIDFlag   string
)
```

Add `JobID` to the `Arguments` struct:

```go
type Arguments struct {
	SourceFolder string
	WriterHost   string
	WriterPort   int
	Streams      int
	Debug        bool
	Quiet        bool
	JobID        string
}
```

Register the flag alongside the others:

```go
	cmd.Flags().StringVar(&jobIDFlag, "job-id", "", "Backup job ID (auto-generated if omitted)")
```

Return it in the constructed `Arguments`:

```go
	return &Arguments{
		SourceFolder: validatedSourceFolder,
		WriterHost:   host,
		WriterPort:   port,
		Streams:      streams,
		Debug:        debug,
		Quiet:        quiet,
		JobID:        jobIDFlag,
	}, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/brfs/... -run TestParseArguments_JobIDFlag -v`
Expected: PASS

- [ ] **Step 5: Generate/resolve the job ID and send it as gRPC metadata**

In `src/cmd/brfs/main.go`, replace:

```go
	// Configuration constants
	const (
		appName = "brfs"
		jobId   = "BackupJob"
	)

	// Put context variables
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = context.WithValue(ctx, "appName", appName)
	ctx = context.WithValue(ctx, "jobId", jobId)
```

with:

```go
	// Configuration constants
	const (
		appName = "brfs"
	)

	// Put context variables
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = context.WithValue(ctx, "appName", appName)
```

(the hardcoded `jobId` is gone from here — the real one is only known once `arguments` is parsed below.)

Then replace:

```go
	ctx = context.WithValue(ctx, common.HostnameContextKey, common.GetHostname())

	// Initialize logger
	logger, logfile := logging.NewLogger(ctx) 
```

with:

```go
	ctx = context.WithValue(ctx, common.HostnameContextKey, common.GetHostname())

	jobID := arguments.JobID
	if jobID == "" {
		jobID = uuid.New().String()
	}
	ctx = context.WithValue(ctx, "jobId", jobID)
	ctx = metadata.AppendToOutgoingContext(ctx, "job-id", jobID)

	// Initialize logger
	logger, logfile := logging.NewLogger(ctx)
```

Update the startup log line to include the job ID:

```go
	logger.Info("Backup reader started",
		"sourceFolder", arguments.SourceFolder,
		"writerHost", arguments.WriterHost,
		"writerPort", arguments.WriterPort,
		"streamsCount", arguments.Streams,
		"jobId", jobID,
	)
```

Add two imports:

```go
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
```

- [ ] **Step 6: Run go vet and build**

Run: `cd src && go vet ./... && go build ./...`
Expected: no errors

- [ ] **Step 7: Run the full test suite**

Run: `cd src && go test ./...`
Expected: PASS

- [ ] **Step 8: Manually verify end-to-end with the real binaries**

Run:
```bash
cd src && go build -o /tmp/bwfs ./cmd/bwfs && go build -o /tmp/brfs ./cmd/brfs
```
Then use whatever cert/config setup the repo's `make build`/dev docs describe to run `/tmp/bwfs <storage> server` and `/tmp/brfs <source_folder> --destination <host:port>` against each other, and confirm the run completes with exit code 0. (This is the one step in this plan that needs a live mTLS-configured environment; the automated integration tests in Task 5 already cover the protocol-level behavior without needing this.)

- [ ] **Step 9: Commit**

```bash
cd src && git add cmd/brfs/arguments.go cmd/brfs/arguments_test.go cmd/brfs/main.go
git commit -m "feat(brfs): generate or accept --job-id and send it as gRPC metadata"
```

---

### Task 7: Documentation

Per `.claude/CLAUDE.md`, this change affects wire behavior (new required metadata key) and component behavior (new flag, new schema), so the protocol and component docs must be updated before this work is considered done.

**Files:**
- Modify: `docs/protocols/backup.md`
- Modify: `docs/components/brfs.md`
- Modify: `docs/components/bwfs.md`

- [ ] **Step 1: Document the job-id metadata requirement in the protocol doc**

In `docs/protocols/backup.md`, add a new section after the `## **Key Design Decisions**` block (before the mermaid diagram):

```markdown
## **Backup Job Tracking**

Every `ProcessBackupStream` call carries a `job-id` gRPC metadata key, attached by `brfs` when it
opens the stream (not a message in the `FileRequest`/`FileResponse` protobuf — this is transport
metadata, so it requires no `.proto` changes). A stream with no `job-id` metadata is rejected
immediately with `codes.InvalidArgument`, before any file is processed.

One `brfs` invocation is one backup job: `brfs` generates a UUID at startup, or uses the value
passed via `--job-id`, and attaches it to every one of its `--streams` concurrent streams.

On the `bwfs` side, the first stream carrying a given `job-id` causes a `backup_jobs` row to be
created (idempotently — every stream of the job attempts this, only the first succeeds); the row's
`source_host` is read from the client's mTLS certificate (first SAN entry, falling back to
CommonName), not from anything the client reports in-band. `bwfs` tracks the number of currently
open streams per job in memory; when the last stream of a job closes, `finished_at` is set. If
`brfs` crashes mid-run, or `bwfs` restarts while a job has open streams, `finished_at` simply never
gets set for that job — this is treated as the correct signal that the run didn't complete cleanly,
not a bug.

Every file version `bwfs` records (`file_versions` table) carries the `job_id` of the stream that
produced it. A duplicate observation of the same object within the same job (e.g. a future retry
re-sending a file) is a safe no-op — the first write for a given `(job_id, object_id)` pair wins.

See [bwfs](../components/bwfs.md) for the schema and [brfs](../components/brfs.md) for the
`--job-id` flag.

Note on the sequence diagram below: the `START_STREAM:jobId:streamId` step shown there is
conceptual — in the actual gRPC transport this is the `job-id` metadata described above, attached
when the stream is opened, not a discrete message exchanged over the stream.
```

- [ ] **Step 2: Document the --job-id flag on brfs**

`docs/components/brfs.md` lists flags as a plain bullet list under `## Arguments and Flags`, not a
table (unlike `bwfs.md`). Add a bullet matching that style:

```markdown
- `--job-id <id>` - Backup job ID *(default: auto-generated UUID)*
```

so the section reads:

```markdown
## Arguments and Flags

- `<source_folder>` - Directory to backup **(required)**
- `--destination <host:port>` - Writer destination address **(required)**
- `--streams <number>` - Number of concurrent streams *(default: config->default_streams)*
- `--job-id <id>` - Backup job ID *(default: auto-generated UUID)*
- `--debug` - Enable debug logging
- `--quiet` - Suppress stdout logging
```

And add a short paragraph after that bullet list, before `## Examples`:

```markdown
Each `brfs` run is a distinct backup job. If `--job-id` is omitted, `brfs` generates a UUID at
startup; passing one explicitly is useful for correlating a run with an external scheduler's own
job identifier. The ID is sent to `bwfs` as gRPC metadata on every stream this run opens — see
[backup protocol](../protocols/backup.md) for the wire-level detail.
```

- [ ] **Step 3: Document backup_jobs on bwfs**

In `docs/components/bwfs.md`, add a new subsection after the `### server` section (before `### list`):

```markdown
#### Backup Job Tracking

Every stream `bwfs` accepts must carry `job-id` gRPC metadata (sent by `brfs` — see
[brfs](./brfs.md)); a stream without it is rejected before any file is processed. `bwfs` records
each job in a `backup_jobs` table (`job_id`, `source_host`, `started_at`, `finished_at`) and tags
every row in `file_versions` with the `job_id` of the run that produced it. `source_host` is read
from the client's mTLS certificate, not from client-reported data. `finished_at` is set once the
job's last concurrent stream closes; a job whose `brfs` crashed mid-run, or that was still open
when `bwfs` restarted, is left with `finished_at` unset — that's the correct signal, not a bug. See
[backup protocol](../protocols/backup.md) for the full lifecycle.
```

- [ ] **Step 4: Review the rendered docs for accuracy**

Read back all three modified files and confirm the additions read coherently in context (no dangling references, consistent terminology with the rest of each doc).

- [ ] **Step 5: Commit**

```bash
git add docs/protocols/backup.md docs/components/brfs.md docs/components/bwfs.md
git commit -m "docs: document backup job tracking protocol, flag, and schema"
```
