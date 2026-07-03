# Backup Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `catalog`, the gRPC service that receives `catalogsync`'s replicated `bwfs` file-version batches and persists them idempotently to its own SQLite database, wire `catalogsync` to talk to it for real, and ship `catalog` as an independently deployable control-plane component.

**Architecture:** A new standalone binary (`src/cmd/catalog`) exposes one unary gRPC RPC (`SyncFileVersions`) secured by the project's existing mTLS pattern, backed by a new small storage package (`src/storage/catalog`) with a single idempotent table keyed by `(source_node, job_id, object_id)` — `source_node` comes from the CA-verified client certificate, not the wire payload. `catalogsync` gets a second `Sender` implementation (`GrpcSender`) that dials this service, config-gated by two new `local.conf` keys. A pre-existing gap in `common/mtls` (certificates loaded once and never refreshed) is fixed for every server/client in the project, not just this pair, since it bites here first. Finally, `catalog` ships its own `docker compose` deployment mirroring `ca/`'s.

**Tech Stack:** Go 1.26, gRPC + protobuf, GORM + modernc.org/sqlite (WAL mode), cobra for CLI, testify for tests, Docker/docker-compose for deployment.

## Global Constraints

- `catalog_host` / `catalog_port` are config-only (`local.conf`), no CLI flag — consistent with the existing `ca_host` precedent.
- `catalog_port` defaults to `15723` and serves two roles from the same field: `catalog`'s own default listen port, and the dial target `catalogsync` reads.
- `catalog` is receive-and-store only in this phase — no query/read API.
- Idempotency key is `(source_node, job_id, object_id)`, not `(job_id, object_id)` — `source_node` disambiguates across a fleet of `bwfs` nodes and comes from `mtls.PeerHostname(ctx)`, never from the RPC payload.
- One unary RPC (`SyncFileVersions`) per batch — no streaming.
- `ca.crt`/`ClientCAs`/`RootCAs` stay loaded once at startup everywhere; only the identity leaf cert (`client.crt`/`client.key`) gets the hot-reload treatment.
- `catalogsync` must never be blocked from starting or running just because `catalog` is unreachable — falls back to `LoggingSender`.
- Every new `.proto` file needs a corresponding `docs/protocols/` doc before commit; every feature change needs the relevant `docs/components/*.md`, `README.md`, `docs/ARCHITECTURE.md` updated (per `.claude/CLAUDE.md`).

---

## File Structure

New:
- `src/api/catalog.proto` (+ generated `catalog.pb.go`, `catalog_grpc.pb.go`)
- `src/storage/catalog/{db.go,models.go,store.go,store_test.go}` — catalog's own SQLite store
- `src/cmd/catalog/{main.go,arguments.go,server.go,server_test.go}` — the `catalog` binary
- `src/cmd/catalogsync/{grpcsender.go,grpcsender_test.go,sender_select.go,sender_select_test.go}`
- `catalog/{Dockerfile,entrypoint.sh,docker-compose.yml,local.conf,README.md}` — deployment
- `docs/components/catalog.md`, `docs/protocols/catalog-sync.md`
- `src/e2e/catalog_test.go`, `src/e2e/catalog_validate.go`

Modified:
- `src/common/config/config.go`, `config_test.go` — `CatalogHost`, `CatalogPort`
- `src/common/mtls/mtls.go`, `mtls_test.go` — certificate hot-reload
- `src/cmd/catalogsync/main.go` — sender selection wiring
- `Makefile` — `catalog` target
- `README.md`, `docs/ARCHITECTURE.md`, `docs/components/catalogsync.md`
- `src/e2e/Dockerfile`, `src/e2e/docker.go`, `src/e2e/config.conf`

---

### Task 1: Config — `catalog_host` / `catalog_port`

**Files:**
- Modify: `src/common/config/config.go`
- Test: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `Config.CatalogHost string` (default `""`), `Config.CatalogPort int` (default `15723`), both optional in `local.conf`.

- [ ] **Step 1: Write the failing tests**

Append to `src/common/config/config_test.go`:

```go
func TestParseConfig_CatalogHostOptional(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "", conf.CatalogHost)
}

func TestParseConfig_CatalogHostParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\ncatalog_host=catalog.backup.internal\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "catalog.backup.internal", conf.CatalogHost)
}

func TestParseConfig_CatalogPortDefaultsTo15723(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 15723, conf.CatalogPort)
}

func TestParseConfig_CatalogPortParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\ncatalog_port=9443\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9443, conf.CatalogPort)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/config/... -run TestParseConfig_Catalog -v`
Expected: FAIL — `conf.CatalogHost`/`conf.CatalogPort` undefined (compile error).

- [ ] **Step 3: Add the fields and parsing**

In `src/common/config/config.go`, add to the `Config` struct (after `CatalogSyncMaxBackoffSec`):

```go
	CatalogHost                string
	CatalogPort                int
```

In `ParseConfig`, add `CatalogPort: 15723` to the defaults literal:

```go
	config := &Config{
		JobTimeoutSec:              30,
		CatalogSyncBatchSize:       500,
		CatalogSyncPollIntervalSec: 5,
		CatalogSyncMaxBackoffSec:   60,
		CatalogPort:                15723,
	}
```

Add two new `case` branches in the `switch key` block, alongside `case "ca_host"`:

```go
		case "catalog_host":
			config.CatalogHost = value
			foundFields["catalog_host"] = true
		case "catalog_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid catalog_port value at line %d: %s", lineNum, value)
			}
			config.CatalogPort = port
			foundFields["catalog_port"] = true
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS, all tests including the four new ones.

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add catalog_host and catalog_port keys"
```

---

### Task 2: mTLS certificate hot-reload

**Files:**
- Modify: `src/common/mtls/mtls.go`
- Test: `src/common/mtls/mtls_test.go`

**Interfaces:**
- Produces: `LoadServerCredentials`/`LoadClientCredentials` (unchanged signatures) now re-read `client.crt`/`client.key` from `certsDir` on every new TLS handshake instead of once at build time. `loadCertAndPool(certsDir string) (tls.Certificate, *x509.CertPool, error)` keeps its existing signature (used by `mtls_test.go`'s untrusted-cert test) — internally composed from two new unexported helpers, `loadIdentityCert` and `loadCAPool`.

- [ ] **Step 1: Write the failing tests**

Add to `src/common/mtls/mtls_test.go` (needs a new import, `"path/filepath"`):

```go
func copyCertsDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	for _, name := range []string{"ca.crt", "client.crt", "client.key"} {
		copyFile(t, filepath.Join(src, name), filepath.Join(dst, name))
	}
	return dst
}

func TestServerTLSConfig_ReloadsCertificateOnEachNewConnection(t *testing.T) {
	dir := copyCertsDir(t, fixtureCertsDir)
	addr := startTestServer(t, dir)

	clientCfg, err := clientTLSConfig(fixtureCertsDir, "bwfs.internal")
	require.NoError(t, err)

	// Baseline: valid cert on disk, handshake succeeds.
	require.NoError(t, dial(addr, clientCfg))

	// Corrupt the server's identity cert on disk without restarting the
	// listener. If GetCertificate were caching the cert captured when
	// serverTLSConfig was built instead of re-reading, this would still
	// succeed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), []byte("not a cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), []byte("not a key"), 0o600))
	assert.Error(t, dial(addr, clientCfg))

	// Restore a valid cert — proves this is a live re-read, not a one-time
	// failure that got cached.
	copyFile(t, fixtureCertsDir+"/client.crt", filepath.Join(dir, "client.crt"))
	copyFile(t, fixtureCertsDir+"/client.key", filepath.Join(dir, "client.key"))
	assert.NoError(t, dial(addr, clientCfg))
}

func TestClientTLSConfig_ReloadsCertificateOnEachNewConnection(t *testing.T) {
	dir := copyCertsDir(t, fixtureCertsDir)
	cfg, err := clientTLSConfig(dir, "bwfs.internal")
	require.NoError(t, err)

	addr := startTestServer(t, fixtureCertsDir)

	// Baseline succeeds.
	require.NoError(t, dial(addr, cfg))

	// Corrupt the client's identity cert on disk. The test server requires
	// and verifies client certs, so a stale-cached client cert would still
	// dial successfully if GetClientCertificate weren't re-reading.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), []byte("not a cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), []byte("not a key"), 0o600))
	assert.Error(t, dial(addr, cfg))

	copyFile(t, fixtureCertsDir+"/client.crt", filepath.Join(dir, "client.crt"))
	copyFile(t, fixtureCertsDir+"/client.key", filepath.Join(dir, "client.key"))
	assert.NoError(t, dial(addr, cfg))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/mtls/... -run ReloadsCertificate -v`
Expected: FAIL — the corrupted-cert `dial` calls succeed instead of erroring, because certs are currently static.

- [ ] **Step 3: Implement hot-reload**

In `src/common/mtls/mtls.go`, replace `loadCertAndPool` and the two `*TLSConfig` functions:

```go
func loadIdentityCert(certsDir string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, identCertFile),
		filepath.Join(certsDir, identKeyFile),
	)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load identity cert/key from %s: %w", certsDir, err)
	}
	return cert, nil
}

func loadCAPool(certsDir string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(filepath.Join(certsDir, caCertFile))
	if err != nil {
		return nil, fmt.Errorf("read CA cert from %s: %w", certsDir, err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse CA cert from %s: no valid certificates found", certsDir)
	}
	return caPool, nil
}

func loadCertAndPool(certsDir string) (tls.Certificate, *x509.CertPool, error) {
	cert, err := loadIdentityCert(certsDir)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return cert, caPool, nil
}

func serverTLSConfig(certsDir string) (*tls.Config, error) {
	// Fail fast at build time if certsDir is missing/broken, rather than
	// only on the first handshake.
	if _, err := loadIdentityCert(certsDir); err != nil {
		return nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := loadIdentityCert(certsDir)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
		ClientCAs:  caPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}, nil
}
```

Replace `clientTLSConfig`:

```go
func clientTLSConfig(certsDir, host string) (*tls.Config, error) {
	if _, err := loadIdentityCert(certsDir); err != nil {
		return nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return nil, err
	}

	getClientCert := func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		cert, err := loadIdentityCert(certsDir)
		if err != nil {
			return nil, err
		}
		return &cert, nil
	}

	if isLoopbackHost(host) {
		return &tls.Config{
			GetClientCertificate:  getClientCert,
			InsecureSkipVerify:    true, // hostname check disabled; chain is still verified below
			VerifyPeerCertificate: verifyChainOnly(caPool),
		}, nil
	}

	return &tls.Config{
		GetClientCertificate: getClientCert,
		RootCAs:              caPool,
		ServerName:            host,
	}, nil
}
```

`loadCertAndPool`'s existing callers (`TestHandshake_ServerRejectsUntrustedClientCert` and `TestLoadClientCredentials_MissingCAFile`) are unaffected — its signature and behavior are unchanged.

- [ ] **Step 4: Run all mtls tests to verify they pass**

Run: `cd src && go test ./common/mtls/... -v`
Expected: PASS — all existing tests plus the two new ones.

- [ ] **Step 5: Commit**

```bash
git add src/common/mtls/mtls.go src/common/mtls/mtls_test.go
git commit -m "fix(mtls): reload identity certificate on every new connection"
```

---

### Task 3: `catalog` storage package

**Files:**
- Create: `src/storage/catalog/db.go`
- Create: `src/storage/catalog/models.go`
- Create: `src/storage/catalog/store.go`
- Test: `src/storage/catalog/store_test.go`

**Interfaces:**
- Produces: `catalog.New(basePath string) (*Store, error)`; `type Entry struct { SourceNode, JobID, ObjectID string; Metadata []byte; Ctime, SourceSeq int64; SourceCreatedAt time.Time }`; `(*Store) EnsureEntries(batch []Entry) error`; `(*Store) Count() (int64, error)`; `(*Store) Close() error`.

- [ ] **Step 1: Write the failing tests**

Create `src/storage/catalog/store_test.go`:

```go
package catalog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureEntries_PersistsBatch(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", Ctime: 100, SourceSeq: 1, SourceCreatedAt: time.Now()},
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", Ctime: 200, SourceSeq: 2, SourceCreatedAt: time.Now()},
	}
	require.NoError(t, store.EnsureEntries(batch))

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestEnsureEntries_DuplicateSameSourceIsNoOp(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceCreatedAt: time.Now()}}
	require.NoError(t, store.EnsureEntries(batch))
	require.NoError(t, store.EnsureEntries(batch)) // resend, e.g. after a retried RPC

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestEnsureEntries_SameJobObjectDifferentSourceNodeAreDistinctRows(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceCreatedAt: time.Now()},
		{SourceNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-1", SourceCreatedAt: time.Now()},
	}
	require.NoError(t, store.EnsureEntries(batch))

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestEnsureEntries_EmptyBatchSucceeds(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	assert.NoError(t, store.EnsureEntries(nil))
}

func TestNew_CreatesMissingStorageDir(t *testing.T) {
	base := t.TempDir() + "/does/not/exist/yet"

	store, err := New(base)
	require.NoError(t, err)
	defer store.Close()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: FAIL — package `catalog` doesn't exist yet (no `.go` files).

- [ ] **Step 3: Implement the package**

Create `src/storage/catalog/models.go`:

```go
package catalog

import "time"

// EntryRecord is one replicated file-version entry received from a bwfs
// node via catalogsync. (SourceNode, JobID, ObjectID) is the idempotency
// key: JobID/ObjectID alone are only unique within a single bwfs node, so
// SourceNode (the CA-verified hostname of the sending node, from the
// client's mTLS certificate) disambiguates across a fleet of bwfs nodes
// replicating to the same catalog.
type EntryRecord struct {
	ID              int64  `gorm:"primaryKey;autoIncrement"`
	SourceNode      string `gorm:"uniqueIndex:idx_source_job_object"`
	JobID           string `gorm:"uniqueIndex:idx_source_job_object"`
	ObjectID        string `gorm:"uniqueIndex:idx_source_job_object"`
	Metadata        []byte
	Ctime           int64
	SourceSeq       int64
	SourceCreatedAt time.Time
	ReceivedAt      time.Time
}
```

Create `src/storage/catalog/db.go`:

```go
package catalog

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

func openDB(basePath string) (*gorm.DB, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}

	dbPath := filepath.Join(basePath, "catalog.db") + "?_busy_timeout=5000"

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	if err := db.AutoMigrate(&EntryRecord{}); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("automigrate: %w", err)
	}
	return db, nil
}
```

Create `src/storage/catalog/store.go`:

```go
package catalog

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func New(basePath string) (*Store, error) {
	db, err := openDB(basePath)
	if err != nil {
		return nil, fmt.Errorf("open catalog db: %w", err)
	}
	return &Store{db: db}, nil
}

// Entry mirrors EntryRecord's replicated fields, decoupled from the gorm
// model so callers (the gRPC server) don't need to import gorm tags.
type Entry struct {
	SourceNode      string
	JobID           string
	ObjectID        string
	Metadata        []byte
	Ctime           int64
	SourceSeq       int64
	SourceCreatedAt time.Time
}

// EnsureEntries idempotently persists batch: a row already present for a
// given (SourceNode, JobID, ObjectID) is left untouched rather than
// erroring — catalogsync retries a batch it isn't sure was received, so a
// resend after a partial success must be a safe no-op.
func (s *Store) EnsureEntries(batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]EntryRecord, len(batch))
	now := time.Now()
	for i, e := range batch {
		records[i] = EntryRecord{
			SourceNode:      e.SourceNode,
			JobID:           e.JobID,
			ObjectID:        e.ObjectID,
			Metadata:        e.Metadata,
			Ctime:           e.Ctime,
			SourceSeq:       e.SourceSeq,
			SourceCreatedAt: e.SourceCreatedAt,
			ReceivedAt:      now,
		}
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_node"}, {Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&records).Error
}

// Count returns the total number of persisted entries.
func (s *Store) Count() (int64, error) {
	var n int64
	err := s.db.Model(&EntryRecord{}).Count(&n).Error
	return n, err
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS, all five tests.

- [ ] **Step 5: Commit**

```bash
git add src/storage/catalog/
git commit -m "feat(catalog): add catalog's own SQLite storage layer"
```

---

### Task 4: `catalog.proto` and generated code

**Files:**
- Create: `src/api/catalog.proto`
- Generate: `src/api/catalog.pb.go`, `src/api/catalog_grpc.pb.go` (committed, same as the project's other `*.pb.go` files)

**Interfaces:**
- Produces: `pb.CatalogServiceServer` (interface with `SyncFileVersions(context.Context, *SyncRequest) (*SyncResponse, error)`), `pb.UnimplementedCatalogServiceServer`, `pb.CatalogServiceClient` (interface with `SyncFileVersions(ctx, *SyncRequest, ...grpc.CallOption) (*SyncResponse, error)`), `pb.RegisterCatalogServiceServer(*grpc.Server, CatalogServiceServer)`, `pb.NewCatalogServiceClient(grpc.ClientConnInterface) CatalogServiceClient`, `pb.SyncRequest{Entries []*FileVersionEntry}`, `pb.SyncResponse{}`, `pb.FileVersionEntry{JobId, ObjectId string; Metadata []byte; Ctime, SourceSeq, CreatedAt int64}`.

- [ ] **Step 1: Write the proto file**

Create `src/api/catalog.proto`:

```protobuf
syntax = "proto3";

package catalogservice;

option go_package = "./proto";

service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
}

message FileVersionEntry {
  string job_id     = 1;
  string object_id  = 2;
  bytes  metadata   = 3;
  int64  ctime      = 4;
  int64  source_seq = 5; // bwfs's local file_versions.seq — informational only
  int64  created_at = 6; // unix seconds; bwfs's original recording time
}

message SyncRequest {
  repeated FileVersionEntry entries = 1;
}

message SyncResponse {} // empty ack — GrpcSender only checks error/nil
```

- [ ] **Step 2: Install the protoc plugins if not already on PATH**

Run:
```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
export PATH="$PATH:$(go env GOPATH)/bin"
```
Expected: both binaries appear on `$(go env GOPATH)/bin`; `which protoc-gen-go protoc-gen-go-grpc` both resolve.

- [ ] **Step 3: Generate the Go code**

Run: `make proto`
Expected output includes: `Protobuf code generated in src/api/`. Verify:
```bash
ls src/api/catalog.pb.go src/api/catalog_grpc.pb.go
```
Expected: both files exist.

- [ ] **Step 4: Verify it compiles**

Run: `cd src && go build ./...`
Expected: exits 0, no errors.

- [ ] **Step 5: Commit**

```bash
git add src/api/catalog.proto src/api/catalog.pb.go src/api/catalog_grpc.pb.go
git commit -m "feat(api): add catalog.proto and generated CatalogService code"
```

---

### Task 5: `catalog` gRPC server — `SyncFileVersions`

**Files:**
- Create: `src/cmd/catalog/server.go`
- Test: `src/cmd/catalog/server_test.go`

**Interfaces:**
- Consumes: `catalogstore.Store`/`catalogstore.Entry`/`catalogstore.New` (Task 3), `pb.*` types (Task 4), `mtls.PeerHostname(ctx context.Context) (string, error)` (existing, `src/common/mtls/peer.go`, unchanged), `connection.StartServer`/`connection.Connect` (existing, `src/common/connection`).
- Produces: `NewCatalogServer(store *catalogstore.Store, logger *slog.Logger) *catalogServer`, implementing `pb.CatalogServiceServer`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/catalog/server_test.go`. This includes one real-mTLS round-trip test
(`TestSyncFileVersions_RealMTLSRoundTrip`, using `common/testdata/certs` and the actual
`connection.StartServer`/`connection.Connect` helpers production code uses) in addition to the
bufconn/fake-context unit tests — `GrpcSender` (Task 7) can't be reused here for a true
client↔server integration test because Go disallows importing one `main` package from another, so
this is the one place that exercises a genuine TLS handshake end to end for this RPC:

```go
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	catalogstore "github.com/alex-sviridov/miniprotector/storage/catalog"
)

const fixtureCertsDir = "../../common/testdata/certs"

func newTestCatalogServer(t *testing.T) (*catalogServer, *catalogstore.Store) {
	t.Helper()
	store, err := catalogstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewCatalogServer(store, logger), store
}

// fakeAuthContext builds a context carrying a self-signed certificate with
// the given hostname as its SAN, simulating what a real mTLS handshake
// leaves in a gRPC handler's context — without needing a real TLS
// connection or a CA-signed cert.
func fakeAuthContext(t *testing.T, hostname string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}

func TestSyncFileVersions_PersistsBatchUnderPeerHostname(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: "obj-1", Ctime: 100, SourceSeq: 1, CreatedAt: time.Now().Unix()},
	}}
	_, err := srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestSyncFileVersions_NoPeerIdentityReturnsError(t *testing.T) {
	srv, _ := newTestCatalogServer(t)
	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}}}

	_, err := srv.SyncFileVersions(context.Background(), req)
	assert.Error(t, err)
}

func TestSyncFileVersions_DuplicateBatchIsIdempotent(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")
	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1", CreatedAt: time.Now().Unix()}}}

	_, err := srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)
	_, err = srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestSyncFileVersions_GRPCRoundTripWithoutTLSIsRejected(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterCatalogServiceServer(grpcSrv, srv)
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

	client := pb.NewCatalogServiceClient(conn)
	_, err = client.SyncFileVersions(context.Background(), &pb.SyncRequest{
		Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}},
	})
	// bufconn + insecure transport carries no peer certificate, so
	// PeerHostname fails and the RPC is rejected — proving identity is
	// enforced end to end, not just when a fake context is handed in.
	assert.Error(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestSyncFileVersions_RealMTLSRoundTrip uses the actual connection.StartServer/
// connection.Connect helpers production code uses, and the project's real
// testdata certs (whose client.crt SAN is "bwfs.internal" — see
// common/mtls/peer_test.go), to prove SourceNode extraction works against a
// genuine mTLS handshake, not just a fabricated context.
func TestSyncFileVersions_RealMTLSRoundTrip(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close()) // release the port; connection.StartServer re-binds it

	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(ctx, logger, port, fixtureCertsDir, func(s *grpc.Server) {
			pb.RegisterCatalogServiceServer(s, srv)
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "server did not start listening")

	conn, err := connection.Connect("localhost", port, 5, fixtureCertsDir)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewCatalogServiceClient(conn)
	_, err = client.SyncFileVersions(context.Background(), &pb.SyncRequest{
		Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}},
	})
	require.NoError(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: FAIL — `catalogServer`/`NewCatalogServer` undefined (compile error).

- [ ] **Step 3: Implement the server**

Create `src/cmd/catalog/server.go`:

```go
package main

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	catalogstore "github.com/alex-sviridov/miniprotector/storage/catalog"
)

type catalogServer struct {
	pb.UnimplementedCatalogServiceServer
	store  *catalogstore.Store
	logger *slog.Logger
}

func NewCatalogServer(store *catalogstore.Store, logger *slog.Logger) *catalogServer {
	return &catalogServer{store: store, logger: logger}
}

func (s *catalogServer) SyncFileVersions(ctx context.Context, req *pb.SyncRequest) (*pb.SyncResponse, error) {
	sourceNode, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("SyncFileVersions: could not determine peer identity", "error", err)
		return nil, err
	}

	entries := req.GetEntries()
	batch := make([]catalogstore.Entry, len(entries))
	for i, e := range entries {
		batch[i] = catalogstore.Entry{
			SourceNode:      sourceNode,
			JobID:           e.GetJobId(),
			ObjectID:        e.GetObjectId(),
			Metadata:        e.GetMetadata(),
			Ctime:           e.GetCtime(),
			SourceSeq:       e.GetSourceSeq(),
			SourceCreatedAt: time.Unix(e.GetCreatedAt(), 0).UTC(),
		}
	}

	if err := s.store.EnsureEntries(batch); err != nil {
		s.logger.Error("SyncFileVersions: persist failed", "error", err, "count", len(batch))
		return nil, err
	}

	s.logger.Info("SyncFileVersions: batch persisted", "source_node", sourceNode, "count", len(batch))
	return &pb.SyncResponse{}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS, all five tests (including the real mTLS round trip).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalog/server.go src/cmd/catalog/server_test.go
git commit -m "feat(catalog): implement SyncFileVersions gRPC handler"
```

---

### Task 6: `catalog` binary — CLI, main, Makefile target

**Files:**
- Create: `src/cmd/catalog/arguments.go`
- Create: `src/cmd/catalog/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `NewCatalogServer` (Task 5), `catalogstore.New` (Task 3), `pb.RegisterCatalogServiceServer` (Task 4), `config.CatalogPort`/`config.ResolveConfigPath`/`config.ResolveCertsDir`/`config.ParseConfig` (existing + Task 1), `connection.StartServer` (existing, `src/common/connection/server.go`), `logging.NewLogger` (existing), `common.ValidatePort` (existing, `src/common/args.go`).
- Produces: working `catalog <storage_path> [--port N] [--debug]` binary via `make catalog`.

- [ ] **Step 1: Write `arguments.go`**

Create `src/cmd/catalog/arguments.go`:

```go
package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	StoragePath string
	Port        int
	Debug       bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "catalog <storage_path>",
		Short: "Receive and persist replicated bwfs file versions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.StoragePath = cliArgs[0]
			return nil
		},
	}
	cmd.Flags().IntVar(&args.Port, "port", conf.CatalogPort, "Port to listen on")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	if err := common.ValidatePort(args.Port); err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}

	return args, nil
}
```

- [ ] **Step 2: Write `main.go`**

Create `src/cmd/catalog/main.go`:

```go
// catalog receives replicated bwfs file versions over gRPC and persists
// them idempotently to its own SQLite database.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	catalogstore "github.com/alex-sviridov/miniprotector/storage/catalog"
	"google.golang.org/grpc"
)

func main() {
	const appName = "catalog"

	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	arguments, err := parseArguments(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	store, err := catalogstore.New(arguments.StoragePath)
	if err != nil {
		logger.Error("failed to open catalog store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	srv := NewCatalogServer(store, logger)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("catalog started", "storage_path", arguments.StoragePath, "port", arguments.Port)

	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
		pb.RegisterCatalogServiceServer(s, srv)
	}); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Add the Makefile target**

In `Makefile`, add `CATALOG_CMD := cmd/catalog` next to `CATALOGSYNC_CMD`, add `catalog` to the `.PHONY` line, and add a target mirroring `catalogsync`'s exactly:

```makefile
catalog: $(BINARY_DIR) ## Build catalog binary
	@printf "$(BLUE)Building catalog...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/catalog ./$(CATALOG_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/catalog"
```

- [ ] **Step 4: Verify it builds and runs**

Run: `make catalog`
Expected: `Built successfully:bin/catalog`.

Run:
```bash
mkdir -p /tmp/catalog-smoke/certs /tmp/catalog-smoke/storage
cp src/common/testdata/certs/* /tmp/catalog-smoke/certs/
cat > /tmp/catalog-smoke/local.conf <<'EOF'
default_port=15722
default_streams=4
logfolder=/tmp/catalog-smoke/log
catalog_port=15723
EOF
MP_CONFIG_PATH=/tmp/catalog-smoke ./bin/catalog /tmp/catalog-smoke/storage --debug &
sleep 1
ls /tmp/catalog-smoke/storage/catalog.db
kill %1
```
Expected: `catalog.db` exists; the process logged `"catalog started"` and `"Server ready, accepting connections"` before being killed.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalog/arguments.go src/cmd/catalog/main.go Makefile
git commit -m "feat(catalog): add CLI, main, and Makefile target"
```

---

### Task 7: `catalogsync` — `GrpcSender`

**Files:**
- Create: `src/cmd/catalogsync/grpcsender.go`
- Test: `src/cmd/catalogsync/grpcsender_test.go`

**Interfaces:**
- Consumes: `pb.*` types (Task 4), `connection.Connect(host string, port, timeout int, certsDir string) (*grpc.ClientConn, error)` (existing), `wfs.FileVersionRecord` (existing, `src/storage/filesystem/models.go`), `Sender` interface (existing, `src/cmd/catalogsync/sender.go`: `Send(batch []wfs.FileVersionRecord) error`).
- Produces: `type GrpcSender struct{...}` implementing `Sender`; `NewGrpcSender(host string, port, timeoutSec int, certsDir string) (*GrpcSender, error)`; `(*GrpcSender) Close() error`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/catalogsync/grpcsender_test.go`:

```go
package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type fakeCatalogServer struct {
	pb.UnimplementedCatalogServiceServer
	lastReq *pb.SyncRequest
	err     error
}

func (f *fakeCatalogServer) SyncFileVersions(ctx context.Context, req *pb.SyncRequest) (*pb.SyncResponse, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return &pb.SyncResponse{}, nil
}

func newTestGrpcSender(t *testing.T, fake *fakeCatalogServer) *GrpcSender {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterCatalogServiceServer(grpcSrv, fake)
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.GracefulStop)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return &GrpcSender{conn: conn, client: pb.NewCatalogServiceClient(conn), timeoutSec: 5}
}

func TestGrpcSender_Send_ConvertsBatchToSingleRequest(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)

	now := time.Now()
	batch := []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1", Ctime: 100, CreatedAt: now},
		{Seq: 2, JobID: "job-1", ObjectID: "obj-2", Ctime: 200, CreatedAt: now},
	}

	require.NoError(t, sender.Send(batch))

	require.NotNil(t, fake.lastReq)
	require.Len(t, fake.lastReq.Entries, 2)
	assert.Equal(t, "obj-1", fake.lastReq.Entries[0].ObjectId)
	assert.Equal(t, "job-1", fake.lastReq.Entries[0].JobId)
	assert.Equal(t, int64(1), fake.lastReq.Entries[0].SourceSeq)
	assert.Equal(t, now.Unix(), fake.lastReq.Entries[0].CreatedAt)
}

func TestGrpcSender_Send_EmptyBatchSendsEmptyRequest(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)

	require.NoError(t, sender.Send(nil))
	require.NotNil(t, fake.lastReq)
	assert.Empty(t, fake.lastReq.Entries)
}

func TestGrpcSender_Send_RPCErrorPropagates(t *testing.T) {
	fake := &fakeCatalogServer{err: errors.New("boom")}
	sender := newTestGrpcSender(t, fake)

	err := sender.Send([]wfs.FileVersionRecord{{JobID: "job-1", ObjectID: "obj-1"}})
	assert.Error(t, err)
}

var _ Sender = (*GrpcSender)(nil)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/catalogsync/... -run TestGrpcSender -v`
Expected: FAIL — `GrpcSender` undefined (compile error).

- [ ] **Step 3: Implement `GrpcSender`**

Create `src/cmd/catalogsync/grpcsender.go`:

```go
package main

import (
	"context"
	"fmt"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"google.golang.org/grpc"
)

// GrpcSender delivers a batch to a real catalog service over gRPC — the
// production Sender, used once catalog_host is configured.
type GrpcSender struct {
	conn       *grpc.ClientConn
	client     pb.CatalogServiceClient
	timeoutSec int
}

// NewGrpcSender dials host:port with mTLS credentials loaded from certsDir.
// The connection is held open and reused for every subsequent Send call.
func NewGrpcSender(host string, port, timeoutSec int, certsDir string) (*GrpcSender, error) {
	conn, err := connection.Connect(host, port, timeoutSec, certsDir)
	if err != nil {
		return nil, fmt.Errorf("connect to catalog: %w", err)
	}
	return &GrpcSender{conn: conn, client: pb.NewCatalogServiceClient(conn), timeoutSec: timeoutSec}, nil
}

func (s *GrpcSender) Send(batch []wfs.FileVersionRecord) error {
	entries := make([]*pb.FileVersionEntry, len(batch))
	for i, r := range batch {
		entries[i] = &pb.FileVersionEntry{
			JobId:     r.JobID,
			ObjectId:  r.ObjectID,
			Metadata:  r.Metadata,
			Ctime:     r.Ctime,
			SourceSeq: r.Seq,
			CreatedAt: r.CreatedAt.Unix(),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.timeoutSec)*time.Second)
	defer cancel()

	if _, err := s.client.SyncFileVersions(ctx, &pb.SyncRequest{Entries: entries}); err != nil {
		return fmt.Errorf("SyncFileVersions: %w", err)
	}
	return nil
}

func (s *GrpcSender) Close() error {
	return s.conn.Close()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/catalogsync/... -v`
Expected: PASS, all tests (existing `catalogsync` tests plus the four new ones).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalogsync/grpcsender.go src/cmd/catalogsync/grpcsender_test.go
git commit -m "feat(catalogsync): add GrpcSender, a real Sender against catalog"
```

---

### Task 8: `catalogsync` — wire sender selection into `main.go`

**Files:**
- Create: `src/cmd/catalogsync/sender_select.go`
- Test: `src/cmd/catalogsync/sender_select_test.go`
- Modify: `src/cmd/catalogsync/main.go`

**Interfaces:**
- Consumes: `NewGrpcSender`/`GrpcSender` (Task 7), `NewLoggingSender` (existing, `src/cmd/catalogsync/sender.go`), `config.CatalogHost`/`config.CatalogPort`/`config.ConnectionTimeOutSec` (Task 1 + existing), `config.ResolveCertsDir` (existing).
- Produces: `selectSender(conf *config.Config, logger *slog.Logger, certsDir string) Sender`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/catalogsync/sender_select_test.go`:

```go
package main

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/config"
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestSelectSender_NoCatalogHostReturnsLoggingSender(t *testing.T) {
	conf := &config.Config{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sender := selectSender(conf, logger, "unused")

	_, ok := sender.(*LoggingSender)
	assert.True(t, ok)
}

func TestSelectSender_UnreachableCatalogFallsBackToLoggingSender(t *testing.T) {
	conf := &config.Config{
		CatalogHost:          "127.0.0.1",
		CatalogPort:          freeTCPPort(t), // nothing listening
		ConnectionTimeOutSec: 1,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sender := selectSender(conf, logger, "../../common/testdata/certs")

	_, ok := sender.(*LoggingSender)
	assert.True(t, ok)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/catalogsync/... -run TestSelectSender -v`
Expected: FAIL — `selectSender` undefined (compile error).

- [ ] **Step 3: Implement `selectSender` and wire it into `main.go`**

Create `src/cmd/catalogsync/sender_select.go`:

```go
package main

import (
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/config"
)

// selectSender chooses catalogsync's Sender based on configuration: a real
// GrpcSender if catalog_host is set and reachable at startup, LoggingSender
// otherwise — catalog_host unset, or the catalog being temporarily down,
// must never block catalogsync from starting and running.
func selectSender(conf *config.Config, logger *slog.Logger, certsDir string) Sender {
	if conf.CatalogHost == "" {
		return NewLoggingSender(logger)
	}
	grpcSender, err := NewGrpcSender(conf.CatalogHost, conf.CatalogPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Warn("could not connect to catalog at startup, falling back to LoggingSender until next restart",
			"catalog_host", conf.CatalogHost, "catalog_port", conf.CatalogPort, "error", err)
		return NewLoggingSender(logger)
	}
	logger.Info("catalogsync sending to catalog", "catalog_host", conf.CatalogHost, "catalog_port", conf.CatalogPort)
	return grpcSender
}
```

In `src/cmd/catalogsync/main.go`, replace:

```go
	sender := NewLoggingSender(logger)
```

with:

```go
	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}
	sender := selectSender(conf, logger, certsDir)
	if closer, ok := sender.(interface{ Close() error }); ok {
		defer closer.Close()
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/catalogsync/... -v`
Expected: PASS, all tests.

- [ ] **Step 5: Run the full test suite and lint**

Run: `cd src && go build ./... && go vet ./... && go test ./...`
Expected: all pass, no vet warnings.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/catalogsync/sender_select.go src/cmd/catalogsync/sender_select_test.go src/cmd/catalogsync/main.go
git commit -m "feat(catalogsync): select GrpcSender when catalog_host is configured"
```

---

### Task 9: `catalog/` docker-compose deployment

**Files:**
- Create: `catalog/Dockerfile`
- Create: `catalog/entrypoint.sh`
- Create: `catalog/docker-compose.yml`
- Create: `catalog/local.conf`
- Create: `catalog/README.md`

**Interfaces:**
- Consumes: `catalog` and `certclient` binaries (Task 6 + existing `src/cmd/certclient`), `Makefile`'s `catalog`/`certclient` targets.

- [ ] **Step 1: Write `catalog/Dockerfile`**

```dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make catalog certclient

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/catalog /build/bin/certclient ./
COPY catalog/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

(No `protoc`/`protoc-gen-go` stage needed here — unlike `src/e2e/Dockerfile`, `catalog`'s generated `.pb.go` files are already committed to the repo from Task 4, so `make catalog certclient` only needs `go build`.)

- [ ] **Step 2: Write `catalog/entrypoint.sh`**

```sh
#!/bin/sh
set -e

mkdir -p "$STORAGE_PATH"

# Bootstraps a new mTLS identity on first run (requires MP_CERT_TOKEN), or
# renews the existing one on every subsequent container restart — no
# expiry check, so certclient always renews when an identity is already
# present. Picking up a renewal made independently while the container
# keeps running (e.g. a scheduled certclient run against the same
# MP_CONFIG_PATH) doesn't require this step to run again — see the
# certificate hot-reload fix in common/mtls.
./certclient

exec ./catalog "$STORAGE_PATH" --debug="${DEBUG:-false}"
```

Run: `chmod +x catalog/entrypoint.sh`

- [ ] **Step 3: Write `catalog/local.conf`**

```
# default_port/default_streams/logfolder are required by every miniprotector
# binary's shared config parser, even though catalog itself only uses
# catalog_port and ca_host below. Harmless placeholders.
default_port=15722
default_streams=4
logfolder=/data/log

# The port catalog listens on, and the port bwfs nodes' catalogsync dials
# (paired with catalog_host, set in each bwfs node's own local.conf).
catalog_port=15723

# Set to this deployment's CA host:port before first boot.
ca_host=ca.backup.internal:9000
```

- [ ] **Step 4: Write `catalog/docker-compose.yml`**

```yaml
services:
  catalog:
    build:
      context: ..
      dockerfile: catalog/Dockerfile
    volumes:
      - ./data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - STORAGE_PATH=/data/storage
    ports:
      - "15723:15723"
    restart: unless-stopped
```

- [ ] **Step 5: Write `catalog/README.md`**

```markdown
# Backup Catalog

Receives replicated `bwfs` file-version batches over mTLS gRPC and persists them to its own
SQLite database (`catalog.db`). Control-plane component — see
[Architecture](../docs/ARCHITECTURE.md).

## First-time setup

Enroll this node with the CA before the first `docker compose up` (same flow any other node
uses — see [`ca/README.md`](../ca/README.md#enrolling-a-node)):

```bash
certrequest catalog-01 --san catalog.backup.internal --ca-url https://<ca-host>:9000
```

Relay the printed token out-of-band, then set it for the first run:

```bash
MP_CERT_TOKEN=<token> docker compose up -d
```

Edit `catalog_port`/`ca_host` in `local.conf` first if the defaults don't match your deployment.

## Running

```bash
docker compose up -d
```

Restarting after the first run renews the node's certificate automatically (`certclient` always
renews when an identity already exists — no token needed). This does not itself keep a
long-running container's certificate fresh on its own schedule; re-run `certclient` inside the
container (`docker compose exec catalog ./certclient`) or restart it periodically to trigger a
renewal. `catalog` picks up a renewed certificate on its next new incoming connection without
needing a restart.

## Configuring catalogsync to send here

On each `bwfs` node running `catalogsync`, set in `local.conf`:

```
catalog_host=catalog.backup.internal
catalog_port=15723
```

## See Also

- [catalog component](../docs/components/catalog.md)
- [catalogsync component](../docs/components/catalogsync.md)
- [certclient](../docs/components/certclient.md)
- [Architecture](../docs/ARCHITECTURE.md)
```

- [ ] **Step 6: Verify the image builds and the compose file is valid**

Run (from repo root):
```bash
docker build -f catalog/Dockerfile -t catalog-build-check .
docker compose -f catalog/docker-compose.yml config
```
Expected: image builds successfully; `docker compose config` prints the resolved config with no errors.

- [ ] **Step 7: Clean up the test image and commit**

```bash
docker rmi catalog-build-check
git add catalog/
git commit -m "feat(catalog): add docker-compose deployment"
```

---

### Task 10: Documentation

**Files:**
- Create: `docs/components/catalog.md`
- Create: `docs/protocols/catalog-sync.md`
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/components/catalogsync.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Write `docs/components/catalog.md`**

```markdown
# catalog

Receives `catalogsync`'s replicated `bwfs` file-version batches over gRPC and persists them
idempotently to its own SQLite database. **Control-plane component** — runs centrally, not
colocated with any single `bwfs` node. Receive-and-store only today; no query/report API yet.

## Usage

```
catalog <storage_path> [--port N] [--debug]
```

`storage_path` is where `catalog.db` lives. `--port` defaults to `catalog_port` from
`local.conf` (15723 if unset).

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `catalog_port` config value | Port to listen on |
| `--debug` | false | Enable debug logging |

## How It Works

`SyncFileVersions` is the sole RPC: one call per batch `catalogsync` sends. Each entry is
persisted keyed by `(source_node, job_id, object_id)`:

- `source_node` is the CA-verified hostname from the caller's mTLS client certificate
  (`mtls.PeerHostname`), never taken from the RPC payload. `job_id`/`object_id` alone are only
  unique within a single `bwfs` node; `source_node` disambiguates across a fleet of nodes
  replicating to the same catalog.
- A batch containing an entry already stored for its `(source_node, job_id, object_id)` is a
  no-op for that entry (`ON CONFLICT DO NOTHING`) — safe for `catalogsync` to resend a batch it
  isn't sure was received.

## Configuration Keys

- `catalog_port` — port `catalog` listens on *(default: 15723)*

## Certificates

Same mTLS pattern as `bwfs`/`brfs`/`rwfs`: identity bootstrapped/renewed via the **`certclient`**
binary against `MP_CONFIG_PATH/certs`. `catalog` itself never talks to the CA directly. A
certificate renewed on disk while `catalog` is running is picked up automatically on the next new
incoming connection — no restart required.

## Deployment

Ships as its own `docker compose` stack — see [`catalog/README.md`](../../catalog/README.md).

## Building

```bash
make catalog
```

## See Also

- [catalogsync](./catalogsync.md) — the component that sends batches here
- [Catalog Sync Protocol](../protocols/catalog-sync.md)
- [certclient](./certclient.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 2: Write `docs/protocols/catalog-sync.md`**

```markdown
# Catalog Sync Protocol

`catalogsync` → `catalog`, over mTLS gRPC. Defined in [`src/api/catalog.proto`](../../src/api/catalog.proto).

## Service

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
}
```

One unary call per batch — `catalogsync` already batches client-side (`CatalogSyncBatchSize`), so
a `SyncRequest` carries a whole batch in a single round trip. Any RPC failure fails the batch as a
whole; there is no partial-batch success/failure reporting, matching `catalogsync`'s existing
all-or-nothing `Sender.Send(batch) error` contract.

## Messages

```protobuf
message FileVersionEntry {
  string job_id     = 1;
  string object_id  = 2;
  bytes  metadata   = 3;
  int64  ctime      = 4;
  int64  source_seq = 5; // bwfs's local file_versions.seq — informational only
  int64  created_at = 6; // unix seconds; bwfs's original recording time
}

message SyncRequest {
  repeated FileVersionEntry entries = 1;
}

message SyncResponse {} // empty ack
```

## Identity

`catalog` does not trust any node identifier carried in the request payload. The persisted
`source_node` for every entry in a batch comes from the CA-verified hostname on the caller's mTLS
client certificate (first SAN, falling back to CommonName — see `common/mtls.PeerHostname`). This
is what lets `(source_node, job_id, object_id)` serve as a safe idempotency key across a fleet of
`bwfs` nodes whose `job_id`/`object_id` values are otherwise only unique per-node.

## See Also

- [catalog](../components/catalog.md)
- [catalogsync](../components/catalogsync.md)
```

- [ ] **Step 3: Update `README.md`**

In the Components list, add after `catalogsync`:

```markdown
- **[catalog](docs/components/catalog.md)** - Backup Catalog — receives `catalogsync`'s replicated file versions over gRPC and persists them centrally; control-plane component
```

In the Documentation section, add after the Restore Protocol line:

```markdown
- **[Catalog Sync Protocol](docs/protocols/catalog-sync.md)** - catalogsync → catalog replication protocol
```

- [ ] **Step 4: Update `docs/ARCHITECTURE.md`**

In the Components table, add a row after `catalogsync`:

```markdown
| catalog | Backup Catalog — receives catalogsync's replicated file_versions over gRPC | Implemented |
```

In the Control Plane vs. Agents table, update the `Components` and `Runs where` cells to include `catalog`:

```markdown
| Components | `ca/` (step-ca container), `certrequest`, `catalog` | `bwfs`, `brfs`, `rwfs`, `certclient` |
| Runs where | On/near the CA host (`certrequest`); `catalog` runs centrally, wherever the catalog deployment lives — see below | Dial `ca_host:9000` outbound only, for enrollment/renewal; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
```

Immediately below that table, add:

```markdown
`catalog` is control plane by role (a fleet-wide central service, not colocated with any single
backup node) but bootstraps its own mTLS identity the same way agents do, via `certclient` — it
doesn't fit either row cleanly. It listens on its own port (`catalog_port`, default 15723) for
`catalogsync` connections from every `bwfs` node's agent host.
```

In the mermaid diagram, replace the `Catalog (planned)` subgraph and its dashed edge:

```mermaid
    subgraph "Catalog (planned)"
        Catalog[(Backup Catalog)]
    end
```
```mermaid
    catalogsync -.->|replicate batches<br/>planned| Catalog
```

with:

```mermaid
    subgraph "Catalog"
        Catalog[(Backup Catalog)]
    end
```
```mermaid
    catalogsync -->|SyncFileVersions<br/>gRPC, mTLS| Catalog
```

and remove `Catalog` from the `classDef planned` class list (it's no longer planned), adding it
to `classDef component` instead:

```markdown
    class SrcFS,BackupFS,DstFS filesystem
    class brfs,bwfs,catalogsync,Catalog component
    class rwfs component
    class DB database
```

- [ ] **Step 5: Update `docs/components/catalogsync.md`**

Replace the top paragraph's second sentence (`The catalog service itself does not exist yet...`)
and the `Sender` description under "How It Works" to describe the now-real `GrpcSender`:

```markdown
`catalogsync` selects its `Sender` at startup based on configuration: if `catalog_host` is set in
`local.conf`, it uses `GrpcSender`, a real mTLS gRPC client against the [catalog](./catalog.md)
service. If `catalog_host` is unset, or the catalog is unreachable at startup, it falls back to
`LoggingSender`, which logs each batch and always succeeds — this keeps `catalogsync` runnable
without a `catalog` deployment, and never blocks it from starting just because the catalog is
temporarily down.
```

Add to the Configuration Keys section:

```markdown
- `catalog_host` — hostname of the `catalog` service to send batches to; unset means `catalogsync`
  falls back to `LoggingSender`
- `catalog_port` — port to dial on `catalog_host` *(default: 15723)*
```

Add to See Also:

```markdown
- [catalog](./catalog.md) — the service `catalogsync` replicates to
- [Catalog Sync Protocol](../protocols/catalog-sync.md)
```

- [ ] **Step 6: Add a `CHANGELOG.md` entry**

Add at the top of `CHANGELOG.md`, above the existing most-recent entry:

```markdown
## 2026-07-03 — Backup catalog service (catalog)

Added `catalog`, the receiving end of `catalogsync`'s replication pipeline: a standalone gRPC
service that persists replicated `bwfs` file-version batches to its own SQLite database, keyed by
`(source_node, job_id, object_id)` — `source_node` comes from the CA-verified mTLS client
certificate, never the payload, so a single catalog can safely receive from a fleet of `bwfs`
nodes. `catalogsync` gained a real `GrpcSender` (config-gated by `catalog_host`/`catalog_port`),
replacing the `LoggingSender` stand-in whenever a catalog is configured and reachable. `catalog`
ships its own `docker compose` deployment (`catalog/`), using the same `certclient`-bootstrapped
mTLS identity every other node uses. Also fixed a pre-existing gap in `common/mtls`: server and
client identity certificates are now re-read from disk on every new connection instead of once at
startup, so a certificate renewed by a scheduled `certclient` run is picked up without restarting
the long-running process — this benefits `bwfs`/`brfs`/`rwfs` too, not just this new pair.
```

- [ ] **Step 7: Commit**

```bash
git add docs/components/catalog.md docs/protocols/catalog-sync.md README.md docs/ARCHITECTURE.md docs/components/catalogsync.md CHANGELOG.md
git commit -m "docs: document catalog, its protocol, and update architecture"
```

---

### Task 11: e2e integration test

**Files:**
- Modify: `src/e2e/Dockerfile`
- Modify: `src/e2e/config.conf`
- Modify: `src/e2e/docker.go`
- Create: `src/e2e/catalog_validate.go`
- Create: `src/e2e/catalog_test.go`

**Interfaces:**
- Consumes: `catalog`/`catalogsync` binaries (Tasks 6, existing), `createNetwork`, `startBwfsContainer`, `runBrfsContainer`, `newDockerClient`, `freePort` (existing `src/e2e/docker.go` helpers), `generateTestData` (existing `src/e2e/testdata.go`).

- [ ] **Step 1: Add `catalog` and `catalogsync` to the e2e image**

In `src/e2e/Dockerfile`, change:
```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs rwfs
```
to:
```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs rwfs catalog catalogsync
```
and change:
```dockerfile
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs ./
```
to:
```dockerfile
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs /build/bin/catalog /build/bin/catalogsync ./
```

- [ ] **Step 2: Point the baked-in e2e config at a fixed catalog network alias**

In `src/e2e/config.conf`, add two lines:
```
catalog_host=catalog.internal
catalog_port=15723
```

- [ ] **Step 3: Add container helpers to `src/e2e/docker.go`**

Add after `startBwfsContainer`:

```go
// startCatalogContainer starts catalog and returns the host port it's
// mapped to. storageDir on the host is bind-mounted to /storage so the
// test can open catalog.db directly afterward. It joins networkID under
// the "catalog.internal" alias, matching e2e's baked-in config.conf
// (catalog_host=catalog.internal).
func startCatalogContainer(ctx context.Context, t testingT, imageID, networkID, storageDir string) string {
	t.Helper()
	cli := newDockerClient(t)

	hostPort, err := freePort()
	require.NoError(t, err)

	containerPort := nat.Port("15723/tcp")
	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image:        imageID,
			Cmd:          []string{"/app/catalog", "/storage", "--port", "15723", "--debug"},
			User:         fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			ExposedPorts: nat.PortSet{containerPort: struct{}{}},
		},
		&container.HostConfig{
			Binds: []string{storageDir + ":/storage"},
			PortBindings: nat.PortMap{
				containerPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: hostPort}},
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkID: {NetworkID: networkID, Aliases: []string{"catalog.internal"}},
			},
		},
		nil,
		fmt.Sprintf("catalog-server-%d", time.Now().UnixNano()),
	)
	require.NoError(t, err)
	require.NoError(t, cli.ContainerStart(ctx, resp.ID, container.StartOptions{}))

	t.Cleanup(func() {
		stopCtx := context.Background()
		timeout := 5
		_ = cli.ContainerStop(stopCtx, resp.ID, container.StopOptions{Timeout: &timeout})
		_ = cli.ContainerRemove(stopCtx, resp.ID, container.RemoveOptions{Force: true})
		cli.Close()
	})

	return hostPort
}

// runCatalogsyncContainer starts catalogsync as a long-running background
// container, reading bwfsStorageDir (the same bind mount bwfs's own
// container uses) and sending to whatever catalog_host/catalog_port are
// baked into e2e's config.conf. It joins networkID so DNS resolution of
// catalog.internal works.
func runCatalogsyncContainer(ctx context.Context, t testingT, imageID, networkID, bwfsStorageDir string) {
	t.Helper()
	cli := newDockerClient(t)

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: imageID,
			Cmd:   []string{"/app/catalogsync", "/storage", "--debug"},
			User:  fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		},
		&container.HostConfig{
			Binds: []string{bwfsStorageDir + ":/storage"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkID: {NetworkID: networkID},
			},
		},
		nil,
		fmt.Sprintf("catalogsync-%d", time.Now().UnixNano()),
	)
	require.NoError(t, err)
	require.NoError(t, cli.ContainerStart(ctx, resp.ID, container.StartOptions{}))

	t.Cleanup(func() {
		stopCtx := context.Background()
		timeout := 5
		_ = cli.ContainerStop(stopCtx, resp.ID, container.StopOptions{Timeout: &timeout})
		_ = cli.ContainerRemove(stopCtx, resp.ID, container.RemoveOptions{Force: true})
		cli.Close()
	})
}
```

- [ ] **Step 4: Add a catalog.db reader for assertions**

Create `src/e2e/catalog_validate.go`:

```go
//go:build e2e

package e2e

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

type catalogEntryRow struct {
	SourceNode string
	JobID      string
	ObjectID   string
}

// waitForCatalogEntryCount polls catalogStorageDir/catalog.db until it
// contains at least wantCount rows or the timeout expires, then returns the
// rows found. Polling (rather than a single read) accounts for
// catalogsync's poll/replicate loop and its own PollIntervalSec cadence.
func waitForCatalogEntryCount(t testingT, catalogStorageDir string, wantCount int) []catalogEntryRow {
	dbPath := filepath.Join(catalogStorageDir, "catalog.db")
	dsn := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", dbPath)

	deadline := time.Now().Add(30 * time.Second)
	var rows []catalogEntryRow
	for time.Now().Before(deadline) {
		sqlDB, err := sql.Open("sqlite", dsn)
		if err == nil {
			db, gormErr := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
				Logger: logger.Default.LogMode(logger.Silent),
			})
			if gormErr == nil {
				var got []catalogEntryRow
				if err := db.Table("entry_records").
					Select("source_node, job_id, object_id").
					Find(&got).Error; err == nil && len(got) >= wantCount {
					sqlDB.Close()
					return got
				}
			}
			sqlDB.Close()
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("catalog.db at %s did not reach %d entries within 30s", dbPath, wantCount)
	return nil
}
```

- [ ] **Step 5: Write the e2e test**

Create `src/e2e/catalog_test.go`:

```go
//go:build e2e

package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_CatalogReceivesReplicatedFileVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir := t.TempDir()
	records := generateTestData(t, dataDir)
	require.NotEmpty(t, records)

	networkID := createNetwork(ctx, t)

	bwfsStorageDir := t.TempDir()
	bwfsHostPort := startBwfsContainer(ctx, t, testImageID, networkID, bwfsStorageDir)
	require.NoError(t, waitForBwfs(ctx, bwfsHostPort))

	catalogStorageDir := t.TempDir()
	startCatalogContainer(ctx, t, testImageID, networkID, catalogStorageDir)

	runCatalogsyncContainer(ctx, t, testImageID, networkID, bwfsStorageDir)

	// Find the bwfs container's own network alias — startBwfsContainer
	// registers it as "bwfs.internal".
	exitCode := runBrfsContainer(ctx, t, testImageID, networkID, dataDir, "bwfs.internal", 4, "e2e-src-host")
	require.Equal(t, 0, exitCode)

	rows := waitForCatalogEntryCount(t, catalogStorageDir, len(records))
	assert.Len(t, rows, len(records))
	for _, row := range rows {
		assert.Equal(t, "bwfs.internal", row.SourceNode)
		assert.NotEmpty(t, row.JobID)
		assert.NotEmpty(t, row.ObjectID)
	}
}
```

`bwfs` writes `metadata.db` under `/storage` inside its container (per `startBwfsContainer`), and
`catalogsync` reads `/storage/metadata.db` inside its own container via the same bind mount — so
`bwfsStorageDir` (the host path) must be passed to both `startBwfsContainer` and
`runCatalogsyncContainer`, as above. Every e2e container (`bwfs`, `catalogsync`, `catalog`) is
built from the same image with the same baked-in `common/testdata/certs` (there's no per-node
`certclient` enrollment in e2e, unlike the real `catalog/` deployment) — so every mTLS connection
in this test presents the identical fixture identity, whose SAN is `bwfs.internal`. That's why
`row.SourceNode` is asserted as `"bwfs.internal"` regardless of which container actually sent the
batch; this test proves the pipeline delivers and persists correctly end-to-end, not multi-node
`SourceNode` disambiguation (already covered by Task 3's unit tests).

- [ ] **Step 6: Run the e2e test**

Run: `cd src && go test -tags=e2e -timeout=300s ./e2e/... -run TestE2E_CatalogReceivesReplicatedFileVersions -v`
Expected: PASS. (Requires a running Docker daemon.)

- [ ] **Step 7: Run the full e2e suite to confirm no regressions**

Run: `make test-e2e`
Expected: all e2e tests, including the existing ones, PASS.

- [ ] **Step 8: Commit**

```bash
git add src/e2e/Dockerfile src/e2e/config.conf src/e2e/docker.go src/e2e/catalog_validate.go src/e2e/catalog_test.go
git commit -m "test(e2e): add brfs->bwfs->catalogsync->catalog round-trip test"
```

---

## Post-Implementation

Before merging to `main`: confirm `CHANGELOG.md` entry (Task 10, Step 6) reads correctly against
whatever else lands in the same branch, per `.claude/CLAUDE.md`'s merge rule.
