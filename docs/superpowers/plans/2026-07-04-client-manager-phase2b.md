# Client Manager Phase 2b: Issuer Service Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `issuer` binary — the CA-host-local service that mints short-lived, attribute-carrying operating certificates and refuses to do so for a revoked hostname — as a standalone, fully-tested component. This plan does **not** yet wire `agent` to call it (that's phase 2c, a separate follow-up plan); it produces a working, independently-testable `issuer serve` you can call directly with a real CSR and get back a real, attribute-bearing certificate signed by a real CA.

**Architecture:** `issuer` shares `storage/clientmanager` with `client-manager` (same SQLite file, same host — no sync protocol) and reuses `common/certmint` for token minting. Its one RPC, `RequestOperatingCert`, derives the caller's hostname from its verified mTLS identity (never a caller-supplied field), checks `revoked` and current `attribute` values from the shared database, and — if not revoked — mints a token via `certmint.Mint` and signs the caller's own already-built CSR against `step-ca` directly via `(*ca.Client).Sign`, embedding attributes through `api.SignRequest.TemplateData` (confirmed against source: any OTT holder may set this field, no extra permission gate; step-ca's own `CustomTemplateOptions` merges it into `.Insecure.User` for a custom template to read — no custom JWT claims, since `ca.Provisioner`'s signing key is unexported and inaccessible outside its own package).

**Tech Stack:** Go, gRPC + protobuf (new `issuer` proto), gorm + `modernc.org/sqlite` (existing `storage/clientmanager`, one schema addition), `smallstep/certificates/{ca,api}` (the same pinned v0.30.2 already used by `certmint`/`certclient`), `common/mtls`/`common/connection` (existing, unmodified).

## Global Constraints

- `issuer` and `client-manager` must be configured with the **same** `var_path` (or otherwise resolve to the same `clientmanager.sqlite` file) — they are two processes sharing one database file, not two databases kept in sync. This is a deployment requirement to document clearly, not something either binary's code enforces.
- The caller's hostname is **always** derived from `mtls.PeerHostname(ctx)` (the verified peer certificate) — never from a request field. This is the same trust model already used by `catalog`'s `source_node` and phase 1's `certrequest serve`.
- A revoked hostname's request must be refused outright — no certificate issued, no partial success.
- `step-ca` itself is not modified by this plan — `issuer` only calls its stock, already-used `(*ca.Client).Sign` and reuses `certmint.Mint`'s existing token-minting call. The custom X.509 template needed to actually *read* `.Insecure.User` in an issued certificate's extensions is deployment configuration (`ca.json`), not source code, and is out of scope for this plan (documented as a deployment note in Task 6, not implemented here — this plan proves attributes reach `TemplateData` correctly; wiring a specific certificate extension format is the CA-operator's template-authoring choice, deliberately not prescribed here).
- A failure to record `last_seen` must never fail an otherwise-successful certificate issuance — it's best-effort telemetry, logged on failure, not propagated as an RPC error.
- No changes to `agent`, `certclient`, or `common/mtls` in this plan — those are phase 2c's territory (agent needs the dual bootstrap/operating credential handling this plan's design assumes exists, but doesn't itself require).

---

## File Structure

| File | Responsibility |
|---|---|
| `src/storage/clientmanager/models.go` (modify) | `ClientRecord` gains `LastSeenAt *time.Time` |
| `src/storage/clientmanager/store.go` (modify) | `Store.UpdateLastSeen(hostname string, at time.Time) error` |
| `src/storage/clientmanager/store_test.go` (modify) | Tests for the above |
| `src/cmd/clientmanager/list.go` (modify) | `list`/`show` display real `last_seen` instead of hardcoded `"unknown"` |
| `src/cmd/clientmanager/list_test.go` (modify) | Updated expectations |
| `src/api/issuer.proto` (new) + generated | `RequestOperatingCert` RPC schema |
| `src/common/config/config.go`, `config_test.go` (modify) | `IssuerHost`/`IssuerPort` (default `9200`), `OperatingCertTTLSec` (default `3600`) |
| `src/cmd/issuer/server.go` (new) | `issuerServer`: auth, revoked/attribute lookup, dispatches to `mintAndSign` |
| `src/cmd/issuer/server_test.go` (new) | Unit tests, fabricated peer identity + stubbed `mintAndSign`, mirrors phase 1's `broker_server_test.go` pattern |
| `src/cmd/issuer/mintsign.go` (new) | Real `mintAndSign`: `certmint.Mint` + `(*ca.Client).Sign` with the caller's CSR and attribute `TemplateData` |
| `src/cmd/issuer/arguments.go` (new) | `issuer serve` CLI flags (mirrors `certrequest serve`'s provisioner-credential flags) |
| `src/cmd/issuer/main.go` (new) | Wiring: config, store, real `mintAndSign`, `connection.StartServer` |
| `src/cmd/issuer/e2e_test.go` (new) | Real step-ca integration test: a genuine CSR signed end-to-end, confirming the attribute makes it into `TemplateData`/the resulting cert per a test template |
| `Makefile` (modify) | `issuer` build target |
| `docs/components/issuer.md` (new), `docs/protocols/issuer.md` (new), `docs/components/client-manager.md`, `docs/ARCHITECTURE.md` (modify) | Documentation |

---

### Task 1: `storage/clientmanager` — `last_seen` tracking

**Files:**
- Modify: `src/storage/clientmanager/models.go`
- Modify: `src/storage/clientmanager/store.go`
- Modify: `src/storage/clientmanager/store_test.go`

**Interfaces:**
- Produces: `ClientRecord.LastSeenAt *time.Time` (nil until first set), `(*Store).UpdateLastSeen(hostname string, at time.Time) error` (returns `ErrClientNotFound` for an untracked hostname).

- [ ] **Step 1: Write the failing tests**

Append to `src/storage/clientmanager/store_test.go`:

```go
func TestUpdateLastSeen_SetsTimestamp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	seenAt := time.Now().Truncate(time.Second)

	require.NoError(t, store.UpdateLastSeen("node-1", seenAt))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	require.NotNil(t, got.LastSeenAt)
	assert.True(t, seenAt.Equal(*got.LastSeenAt))
}

func TestUpdateLastSeen_OverwritesPreviousValue(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.UpdateLastSeen("node-1", time.Now().Add(-time.Hour)))

	newSeenAt := time.Now().Truncate(time.Second)
	require.NoError(t, store.UpdateLastSeen("node-1", newSeenAt))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.True(t, newSeenAt.Equal(*got.LastSeenAt))
}

func TestUpdateLastSeen_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateLastSeen("ghost", time.Now())
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestGetClient_NewClientHasNilLastSeenAt(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Nil(t, got.LastSeenAt)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./storage/clientmanager/... -run 'TestUpdateLastSeen|TestGetClient_NewClientHasNilLastSeenAt' -v`
Expected: FAIL — `store.UpdateLastSeen`/`got.LastSeenAt` undefined (compile error).

- [ ] **Step 3: Add the field**

In `src/storage/clientmanager/models.go`, add one field to `ClientRecord`:

```go
type ClientRecord struct {
	Hostname   string `gorm:"primaryKey"`
	AddedAt    time.Time
	SANs       string // JSON-encoded []string; "" or "[]" means no extra SANs
	Revoked    bool
	RevokedAt  *time.Time
	LastSeenAt *time.Time
}
```

- [ ] **Step 4: Implement `UpdateLastSeen`**

Append to `src/storage/clientmanager/store.go`:

```go
// UpdateLastSeen records the most recent time hostname successfully
// obtained an operating certificate. Best-effort telemetry -- callers
// should log rather than fail a request on this returning an error.
// Returns ErrClientNotFound if hostname isn't tracked.
func (s *Store) UpdateLastSeen(hostname string, at time.Time) error {
	res := s.db.Model(&ClientRecord{}).Where("hostname = ?", hostname).Update("last_seen_at", at)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrClientNotFound
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./storage/clientmanager/... -v`
Expected: PASS (all tests, including every pre-existing one — gorm's `AutoMigrate`, already called in `openDB`, adds the new nullable column automatically, no migration script needed).

- [ ] **Step 6: Commit**

```bash
git add src/storage/clientmanager/
git commit -m "feat(clientmanager): track last_seen_at on ClientRecord"
```

---

### Task 2: `client-manager` displays real `last_seen`

**Files:**
- Modify: `src/cmd/clientmanager/list.go`
- Modify: `src/cmd/clientmanager/list_test.go`

**Interfaces:**
- Consumes: `ClientRecord.LastSeenAt *time.Time` (Task 1).

- [ ] **Step 1: Update `runList`/`runShow`**

In `src/cmd/clientmanager/list.go`, replace the hardcoded `"unknown"` in `runList`:

```go
	for _, c := range clients {
		revoked := "no"
		if c.Revoked {
			revoked = "yes"
		}
		lastSeen := "never"
		if c.LastSeenAt != nil {
			lastSeen = c.LastSeenAt.Format(timeLayout)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Hostname, c.AddedAt.Format(timeLayout), revoked, lastSeen)
	}
```

(Remove the old comment about `LAST_SEEN` always being `"unknown"` above this block — it's no longer true.)

In `runShow`, replace:

```go
	fmt.Fprintln(out, "last_seen:  unknown")
```

with:

```go
	if client.LastSeenAt != nil {
		fmt.Fprintf(out, "last_seen:  %s\n", client.LastSeenAt.Format(timeLayout))
	} else {
		fmt.Fprintln(out, "last_seen:  never")
	}
```

- [ ] **Step 2: Update the existing tests**

In `src/cmd/clientmanager/list_test.go`, find `TestRunList_ShowsAddedClients` — it currently asserts `assert.Contains(t, out.String(), "unknown")`. Change that assertion to `assert.Contains(t, out.String(), "never")` (a freshly-added client has never been seen).

Add two new tests:

```go
func TestRunList_ShowsRealLastSeenTimestamp(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	seenAt := time.Now().Truncate(time.Second)
	require.NoError(t, store.UpdateLastSeen("node-1", seenAt))

	var out bytes.Buffer
	require.NoError(t, runList(store, &out))
	assert.Contains(t, out.String(), seenAt.Format(timeLayout))
}

func TestRunShow_ShowsRealLastSeenTimestamp(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	seenAt := time.Now().Truncate(time.Second)
	require.NoError(t, store.UpdateLastSeen("node-1", seenAt))

	var out bytes.Buffer
	require.NoError(t, runShow(store, &Arguments{Hostname: "node-1"}, &out))
	assert.Contains(t, out.String(), "last_seen:  "+seenAt.Format(timeLayout))
}
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `cd src && go test ./cmd/clientmanager/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 4: Commit**

```bash
git add src/cmd/clientmanager/list.go src/cmd/clientmanager/list_test.go
git commit -m "feat(clientmanager): display real last_seen instead of a placeholder"
```

---

### Task 3: `issuer.proto`

**Files:**
- Create: `src/api/issuer.proto`
- Generated (via `make proto`): `src/api/issuer.pb.go`, `src/api/issuer_grpc.pb.go`

**Interfaces:**
- Produces: `pb.IssuerServiceServer`, `pb.RegisterIssuerServiceServer`, `pb.NewIssuerServiceClient`, `pb.RequestOperatingCertRequest{CsrDer []byte}`, `pb.RequestOperatingCertResponse{CertChainPem []byte}`, `pb.UnimplementedIssuerServiceServer`.

- [ ] **Step 1: Write the proto file**

`src/api/issuer.proto`:

```proto
syntax = "proto3";

package issuerservice;

option go_package = "./proto";

// IssuerService is the sole RPC surface a bootstrapped node calls to keep
// its operating certificate fresh. The caller's hostname is never a field
// on this message -- it is always derived from the verified mTLS peer
// identity, exactly like every other authenticated RPC in this project.
service IssuerService {
  rpc RequestOperatingCert(RequestOperatingCertRequest) returns (RequestOperatingCertResponse);
}

message RequestOperatingCertRequest {
  // DER-encoded PKCS#10 certificate signing request. The caller's private
  // key never leaves the caller -- only the CSR crosses the wire.
  bytes csr_der = 1;
}

message RequestOperatingCertResponse {
  // Full certificate chain (leaf + any intermediates), PEM-encoded,
  // concatenated in order -- ready to write directly to client.crt.
  bytes cert_chain_pem = 1;
}
```

- [ ] **Step 2: Generate the Go code**

Run: `make proto`
Expected output: `Protobuf code generated in src/api/` and new files `src/api/issuer.pb.go`, `src/api/issuer_grpc.pb.go` present.

- [ ] **Step 3: Confirm it compiles**

Run: `cd src && go build ./api/...`
Expected: no output, exit code 0.

- [ ] **Step 4: Commit**

```bash
git add src/api/issuer.proto src/api/issuer.pb.go src/api/issuer_grpc.pb.go
git commit -m "feat(api): add issuer proto (RequestOperatingCert RPC)"
```

---

### Task 4: `common/config` — new keys

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `Config.IssuerHost string`, `Config.IssuerPort int` (default `9200`), `Config.OperatingCertTTLSec int` (default `3600`), parsed from `issuer_host`, `issuer_port`, `OperatingCertTTLSec` keys.

- [ ] **Step 1: Write the failing tests**

Append to `src/common/config/config_test.go`:

```go
func TestParseConfig_IssuerHostParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nissuer_host=ca.backup.internal\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "ca.backup.internal", conf.IssuerHost)
}

func TestParseConfig_IssuerPortDefaultsTo9200(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9200, conf.IssuerPort)
}

func TestParseConfig_IssuerPortParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nissuer_port=9300\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9300, conf.IssuerPort)
}

func TestParseConfig_OperatingCertTTLSecDefaultsTo3600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 3600, conf.OperatingCertTTLSec)
}

func TestParseConfig_OperatingCertTTLSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nOperatingCertTTLSec=1800\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 1800, conf.OperatingCertTTLSec)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/config/... -run 'TestParseConfig_IssuerHost|TestParseConfig_IssuerPort|TestParseConfig_OperatingCertTTLSec' -v`
Expected: FAIL — fields undefined (compile error).

- [ ] **Step 3: Implement**

In `src/common/config/config.go`, add three fields to the `Config` struct:

```go
	IssuerHost          string
	IssuerPort          int
	OperatingCertTTLSec int
```

Add two defaults to the literal in `ParseConfig`:

```go
	config := &Config{
		JobTimeoutSec:              30,
		CatalogSyncBatchSize:       500,
		CatalogSyncPollIntervalSec: 5,
		CatalogSyncMaxBackoffSec:   60,
		CatalogPort:                15723,
		ReconcileIntervalSec:       30,
		IssuerPort:                 9200,
		OperatingCertTTLSec:        3600,
	}
```

Add three `case`s to the `switch key` block:

```go
		case "issuer_host":
			config.IssuerHost = value
			foundFields["issuer_host"] = true
		case "issuer_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid issuer_port value at line %d: %s", lineNum, value)
			}
			config.IssuerPort = port
			foundFields["issuer_port"] = true
		case "OperatingCertTTLSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid OperatingCertTTLSec value at line %d: %s", lineNum, value)
			}
			config.OperatingCertTTLSec = number
			foundFields["OperatingCertTTLSec"] = true
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add issuer_host, issuer_port, OperatingCertTTLSec"
```

---

### Task 5: `issuer`'s server logic (auth + revoked/attribute lookup)

**Files:**
- Create: `src/cmd/issuer/server.go`
- Create: `src/cmd/issuer/server_test.go`

**Interfaces:**
- Consumes: `pb.IssuerServiceServer`/`pb.RequestOperatingCertRequest`/`Response` (Task 3), `mtls.PeerHostname(ctx) (string, error)` (existing), `clientmanagerstore.Store.GetClient`/`KV`/`UpdateLastSeen` (existing + Task 1), `clientmanagerstore.KindAttribute` (existing).
- Produces: `type mintAndSignFunc func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error)`, `newIssuerServer(store *clientmanagerstore.Store, mintSign mintAndSignFunc, logger *slog.Logger) *issuerServer`.

- [ ] **Step 1: Write the failing tests**

`src/cmd/issuer/server_test.go`:

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
	"log/slog"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	pb "github.com/alex-sviridov/miniprotector/api"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestIssuerStore(t *testing.T) *clientmanagerstore.Store {
	t.Helper()
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

// fakeAuthContext mirrors cmd/catalog/server_test.go's and cmd/certrequest/
// broker_server_test.go's helper of the same name: a self-signed cert with
// the given hostname as its SAN, simulating a verified mTLS peer identity
// without a real handshake.
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

// testCSR builds a minimal, validly-signed CSR for use as request payload
// -- the server never inspects its subject/SANs (those come from the
// database, keyed by the verified peer hostname), only forwards it.
func testCSR(t *testing.T) *x509.CertificateRequest {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(der)
	require.NoError(t, err)
	return csr
}

func TestRequestOperatingCert_KnownNotRevokedHostSucceeds(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"node-1.internal"}, time.Now()))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindAttribute, "role", "prod-db"))

	var gotHostname string
	var gotSANs []string
	var gotAttrs map[string]string
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		gotHostname = hostname
		gotSANs = sans
		gotAttrs = attributes
		return []byte("fake-cert-chain"), nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	resp, err := srv.RequestOperatingCert(fakeAuthContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-cert-chain"), resp.CertChainPem)
	assert.Equal(t, "node-1", gotHostname)
	assert.Equal(t, []string{"node-1.internal"}, gotSANs)
	assert.Equal(t, map[string]string{"role": "prod-db"}, gotAttrs)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	require.NotNil(t, got.LastSeenAt, "last_seen should be stamped on success")
}

func TestRequestOperatingCert_RevokedHostRejectedWithoutMinting(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.SetRevoked("node-1", true, time.Now()))

	called := false
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		called = true
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(fakeAuthContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)
	assert.False(t, called, "mintSign must not be called for a revoked host")

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Nil(t, got.LastSeenAt, "last_seen must not be stamped when the request was refused")
}

func TestRequestOperatingCert_UnknownHostRejectedWithoutMinting(t *testing.T) {
	store := newTestIssuerStore(t)

	called := false
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		called = true
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(fakeAuthContext(t, "ghost"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)
	assert.False(t, called, "mintSign must not be called for a hostname not tracked at all")
}

func TestRequestOperatingCert_NoPeerIdentityRejected(t *testing.T) {
	store := newTestIssuerStore(t)
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		t.Fatal("mintSign must not be called without a peer identity")
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(context.Background(), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)
}

func TestRequestOperatingCert_MalformedCSRRejected(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		t.Fatal("mintSign must not be called for an unparseable CSR")
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(fakeAuthContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: []byte("not a csr"),
	})
	assert.Error(t, err)
}

func TestRequestOperatingCert_MintSignFailurePropagatesAndSkipsLastSeen(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return nil, assert.AnError
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(fakeAuthContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Nil(t, got.LastSeenAt)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/issuer/... -v`
Expected: FAIL — package `main` in `cmd/issuer` doesn't exist yet (compile error; no non-test files present).

- [ ] **Step 3: Implement**

`src/cmd/issuer/server.go`:

```go
package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

// mintAndSignFunc mints a token for hostname/sans and signs csr against the
// CA, embedding attributes via the sign request's TemplateData, returning
// the full PEM-encoded certificate chain. Production wires this to
// mintAndSign (mintsign.go); tests inject a stub so this file's unit tests
// never touch a real CA.
type mintAndSignFunc func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error)

// issuerServer implements IssuerService: the sole RPC an already-
// bootstrapped node calls to obtain a fresh operating certificate. The
// caller's identity is always the verified mTLS peer hostname -- never a
// request field -- looked up against the client-manager database this
// binary shares. A revoked hostname is refused outright, regardless of
// whether its bootstrap credential is otherwise perfectly valid.
type issuerServer struct {
	pb.UnimplementedIssuerServiceServer
	store    *clientmanagerstore.Store
	mintSign mintAndSignFunc
	logger   *slog.Logger
}

func newIssuerServer(store *clientmanagerstore.Store, mintSign mintAndSignFunc, logger *slog.Logger) *issuerServer {
	return &issuerServer{store: store, mintSign: mintSign, logger: logger}
}

func (s *issuerServer) RequestOperatingCert(ctx context.Context, req *pb.RequestOperatingCertRequest) (*pb.RequestOperatingCertResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		return nil, fmt.Errorf("determine caller identity: %w", err)
	}

	client, err := s.store.GetClient(hostname)
	if err != nil {
		return nil, fmt.Errorf("hostname %s not tracked: %w", hostname, err)
	}
	if client.Revoked {
		return nil, fmt.Errorf("hostname %s is revoked", hostname)
	}

	attrRecords, err := s.store.KV(hostname, clientmanagerstore.KindAttribute)
	if err != nil {
		return nil, fmt.Errorf("load attributes for %s: %w", hostname, err)
	}
	attributes := make(map[string]string, len(attrRecords))
	for _, a := range attrRecords {
		attributes[a.Key] = a.Value
	}

	csr, err := x509.ParseCertificateRequest(req.GetCsrDer())
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}

	chainPEM, err := s.mintSign(hostname, client.SANsList(), attributes, csr)
	if err != nil {
		return nil, fmt.Errorf("issue certificate for %s: %w", hostname, err)
	}

	if err := s.store.UpdateLastSeen(hostname, time.Now()); err != nil {
		s.logger.Error("failed to update last_seen", "hostname", hostname, "error", err)
	}

	return &pb.RequestOperatingCertResponse{CertChainPem: chainPEM}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/issuer/... -v`
Expected: PASS (all 6 tests).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/issuer/server.go src/cmd/issuer/server_test.go
git commit -m "feat(issuer): add server logic for RequestOperatingCert"
```

---

### Task 6: `issuer`'s real minting/signing, CLI, and a real-CA e2e test

**Files:**
- Create: `src/cmd/issuer/mintsign.go`
- Create: `src/cmd/issuer/arguments.go`
- Create: `src/cmd/issuer/main.go`
- Create: `src/cmd/issuer/e2e_test.go`

**Interfaces:**
- Consumes: `certmint.Mint`/`certmint.Options` (existing), `mintAndSignFunc` (Task 5), `connection.StartServer` (existing), `config.Config.IssuerPort`/`OperatingCertTTLSec` (Task 4).
- Produces: `mintAndSign(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest, opts certmint.Options) ([]byte, error)`.

- [ ] **Step 1: Implement the real mint+sign**

`src/cmd/issuer/mintsign.go`:

```go
// issuer's real certificate issuance: mint a one-time token via the same
// certmint package client-manager uses, then sign the caller's own CSR
// directly against the CA -- never generating a keypair here, since the
// private key must never leave the node that requested it.
package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"

	"github.com/smallstep/certificates/api"
	"github.com/smallstep/certificates/ca"

	"github.com/alex-sviridov/miniprotector/common/certmint"
)

func mintAndSign(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest, opts certmint.Options) ([]byte, error) {
	token, err := certmint.Mint(hostname, sans, opts)
	if err != nil {
		return nil, fmt.Errorf("mint token: %w", err)
	}

	templateData, err := json.Marshal(attributes)
	if err != nil {
		return nil, fmt.Errorf("marshal attributes: %w", err)
	}

	client, err := ca.NewClient(opts.CAURL, ca.WithRootFile(opts.RootFile))
	if err != nil {
		return nil, fmt.Errorf("create CA client: %w", err)
	}

	signResp, err := client.Sign(&api.SignRequest{
		CsrPEM:       api.NewCertificateRequest(csr),
		OTT:          token,
		TemplateData: templateData,
	})
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}

	var chainPEM []byte
	chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: signResp.ServerPEM.Raw})...)
	for _, c := range signResp.CertChainPEM {
		chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	return chainPEM, nil
}
```

- [ ] **Step 2: CLI arguments**

`src/cmd/issuer/arguments.go` (mirrors `certrequest`'s `serve` mode flags exactly — same provisioner-credential defaults, since `issuer` needs the identical CA/provisioner access to mint tokens):

```go
package main

import (
	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
	Debug        bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "issuer",
		Short: "Mint short-lived, attribute-bearing operating certificates for already-enrolled nodes",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&args.CAURL, "ca-url", "https://localhost:9000", "CA URL, e.g. https://localhost:9000")
	cmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	cmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	cmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	return args, nil
}
```

- [ ] **Step 3: `main.go`**

`src/cmd/issuer/main.go`:

```go
// issuer mints short-lived operating certificates for already-enrolled
// nodes, refusing to do so for a revoked hostname. It shares its database
// with client-manager (same var_path, same clientmanager.sqlite file --
// not synced, the same file) and reuses common/certmint for token minting.
// See docs/components/issuer.md and
// docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md.
package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
	"google.golang.org/grpc"
)

func main() {
	const appName = "issuer"

	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

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

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Var directory resolution failed: %v\n", err)
		os.Exit(1)
	}
	store, err := clientmanagerstore.New(varDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open client-manager store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	mintOpts := certmint.Options{
		CAURL:        args.CAURL,
		RootFile:     args.RootFile,
		Provisioner:  args.Provisioner,
		PasswordFile: args.PasswordFile,
	}
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return mintAndSign(hostname, sans, attributes, csr, mintOpts)
	}
	srv := newIssuerServer(store, mintSign, logger)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("issuer started", "port", conf.IssuerPort)

	if err := connection.StartServer(signalCtx, logger, conf.IssuerPort, certsDir, func(s *grpc.Server) {
		pb.RegisterIssuerServiceServer(s, srv)
	}); err != nil {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Confirm it builds**

Run: `cd src && go build ./cmd/issuer/...`
Expected: no output, exit code 0.

- [ ] **Step 5: Write the real-CA e2e test**

`src/cmd/issuer/e2e_test.go` (mirrors `certrequest`'s existing e2e test pattern exactly — a real, throwaway `step-ca` via the repo's own `deploy/control-plane/docker-compose.yml`, its `step-ca` service only):

```go
//go:build e2e

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/certmint"
)

// TestE2E_MintAndSignEmbedsAttributesInTemplateData proves the real
// mintAndSign call, against a genuine throwaway step-ca, produces a
// signable certificate and that the attributes reach the sign request's
// TemplateData -- this is the specific, previously-unverified mechanism
// this whole design depends on (see the phase-2 design spec's corrected
// "step-ca" component description). It does not configure a custom x509
// template (that's deployment configuration, out of scope for this test);
// it confirms the plumbing that makes such a template possible, by
// checking the request-level artifact directly.
func TestE2E_MintAndSignEmbedsAttributesInTemplateData(t *testing.T) {
	requireDocker(t)

	repoRoot := repoRootDir(t)
	tempDir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "ca"), 0o755))
	copyComposeFileWithEphemeralPort(t, filepath.Join(repoRoot, "deploy", "control-plane", "docker-compose.yml"), filepath.Join(tempDir, "docker-compose.yml"))
	copyFile(t, filepath.Join(repoRoot, "deploy", "control-plane", "ca", "entrypoint.sh"), filepath.Join(tempDir, "ca", "entrypoint.sh"))
	require.NoError(t, os.Chmod(filepath.Join(tempDir, "ca", "entrypoint.sh"), 0o755))

	secretsDir := filepath.Join(tempDir, "ca", "data", "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0o700))
	password := randomPassword(t)
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "password"), []byte(password), 0o600))

	projectName := fmt.Sprintf("issuer-e2e-%d", time.Now().UnixNano())
	compose := func(args ...string) *exec.Cmd {
		cmd := exec.Command("docker", append([]string{"compose", "-p", projectName}, args...)...)
		cmd.Dir = tempDir
		return cmd
	}
	t.Cleanup(func() {
		downCmd := compose("down", "--volumes", "--remove-orphans")
		if out, err := downCmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})
	upCmd := compose("up", "-d", "step-ca")
	out, err := upCmd.CombinedOutput()
	require.NoError(t, err, "docker compose up failed: %s", out)

	hostPort := discoverHostPort(t, compose)
	caURL := fmt.Sprintf("https://localhost:%s", hostPort)
	rootPath := filepath.Join(tempDir, "ca", "data", "certs", "root_ca.crt")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, waitForCA(ctx, caURL, rootPath), "step-ca never became ready")

	opts := certmint.Options{
		CAURL:        caURL,
		RootFile:     rootPath,
		Provisioner:  "admin@backup.internal",
		PasswordFile: filepath.Join(secretsDir, "password"),
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "e2e-issuer-host"},
	}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(csrDER)
	require.NoError(t, err)

	chainPEM, err := mintAndSign("e2e-issuer-host", nil, map[string]string{"role": "prod-db"}, csr, opts)
	require.NoError(t, err, "mintAndSign")
	require.NotEmpty(t, chainPEM)

	block, _ := pem.Decode(chainPEM)
	require.NotNil(t, block, "expected at least one PEM block in the chain")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.Equal(t, "e2e-issuer-host", leaf.Subject.CommonName)
}

// requireDocker skips the test (loudly, with a clear reason) if Docker isn't
// usable in this environment, rather than silently passing.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not found in PATH, skipping e2e test: %v", err)
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker daemon not reachable, skipping e2e test: %v\n%s", err, out)
	}
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.Create(dst)
	require.NoError(t, err)
	defer out.Close()
	_, err = io.Copy(out, in)
	require.NoError(t, err)
}

func copyComposeFileWithEphemeralPort(t *testing.T, src, dst string) {
	t.Helper()
	contents, err := os.ReadFile(src)
	require.NoError(t, err)
	rewritten := strings.Replace(string(contents), `"9000:9000"`, `"0:9000"`, 1)
	require.NotEqual(t, string(contents), rewritten, "expected to find literal \"9000:9000\" port mapping in %s", src)
	require.NoError(t, os.WriteFile(dst, []byte(rewritten), 0o644))
}

func discoverHostPort(t *testing.T, compose func(args ...string) *exec.Cmd) string {
	t.Helper()
	portCmd := compose("port", "step-ca", "9000")
	out, err := portCmd.CombinedOutput()
	require.NoError(t, err, "docker compose port failed: %s", out)
	addr := strings.TrimSpace(string(out))
	idx := strings.LastIndex(addr, ":")
	require.GreaterOrEqual(t, idx, 0, "unexpected `docker compose port` output: %q", addr)
	portStr := addr[idx+1:]
	_, err = strconv.Atoi(portStr)
	require.NoError(t, err, "failed to parse port from `docker compose port` output: %q", addr)
	return portStr
}

func randomPassword(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func waitForCA(ctx context.Context, caURL, rootPath string) error {
	httpClient := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // throwaway test CA, cert not yet trusted at poll time
		},
	}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s/health to be ready: %w (last error: %v)", caURL, ctx.Err(), lastErr)
		case <-ticker.C:
			if _, err := os.Stat(rootPath); err != nil {
				lastErr = fmt.Errorf("root cert not yet written: %w", err)
				continue
			}
			resp, err := httpClient.Get(caURL + "/health")
			if err != nil {
				lastErr = err
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
		}
	}
}
```

- [ ] **Step 6: Run the e2e test**

Run: `cd src && go test -tags=e2e -timeout=120s ./cmd/issuer/... -run TestE2E_MintAndSignEmbedsAttributesInTemplateData -v`
Expected: PASS (or a clear Docker-unavailable skip message).

- [ ] **Step 7: Add the Makefile target**

In `Makefile`, add `ISSUER_CMD := cmd/issuer` alongside the other `*_CMD` variables, add `issuer` to the `.PHONY` line, and add:

```makefile
issuer: $(BINARY_DIR) ## Build issuer binary
	@printf "$(BLUE)Building issuer...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/issuer ./$(ISSUER_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/issuer"
```

- [ ] **Step 8: Full verification**

Run: `make issuer && cd src && go build ./... && go test ./... 2>&1 | tail -30`
Expected: `issuer` builds successfully; every package shows `ok` (the pre-existing, unrelated `cmd/brfs` vet warning, if checked, remains the only `go vet` output — not introduced by this task).

- [ ] **Step 9: Commit**

```bash
git add src/cmd/issuer/ Makefile
git commit -m "feat(issuer): real minting/signing, CLI, e2e coverage, build target"
```

---

### Task 7: Documentation

**Files:**
- Create: `docs/components/issuer.md`
- Create: `docs/protocols/issuer.md`
- Modify: `docs/components/client-manager.md`, `docs/ARCHITECTURE.md`

- [ ] **Step 1: Write `docs/components/issuer.md`**

```markdown
# issuer

Mints short-lived, attribute-bearing **operating certificates** for already-enrolled nodes,
refusing to do so for a revoked hostname — the enforcement half of
[Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md).
Runs on the CA host, sharing `client-manager`'s SQLite database directly (same file, same host —
not synchronized over a network) and reusing `common/certmint` for token minting.

## Usage

```bash
issuer --ca-url https://localhost:9000 --root <path> --provisioner <name> --password-file <path>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--ca-url` | `https://localhost:9000` | CA URL |
| `--root` | `deploy/control-plane/ca/data/certs/root_ca.crt` | Path to the CA's root certificate |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `deploy/control-plane/ca/data/secrets/password` | Path to the provisioner password file |
| `--debug` | false | Enable debug logging |

## Behavior

`RequestOperatingCert` (see [protocol](../protocols/issuer.md)) is `issuer`'s only RPC. The
caller's hostname is always the verified mTLS peer identity, never a request field. For a known,
not-revoked hostname: mints a token via the same mechanism `client-manager` uses, signs the
caller's own submitted CSR against the CA directly (the caller's private key never reaches
`issuer`), embeds the hostname's current `attribute` values via the sign request's `TemplateData`,
and records `last_seen`. For a revoked or untracked hostname: refuses outright, no certificate
issued, `last_seen` untouched. A `last_seen` write failure is logged but never fails an otherwise-
successful request.

**Deployment note:** `issuer` and `client-manager` must be configured with the same `var_path` (or
otherwise resolve to the same `clientmanager.sqlite` file) — they share one database, not two kept
in sync.

**Not yet in this phase:** actually baking `attribute` values into a certificate's extensions
requires a custom X.509 template (`options.x509.templateFile` in the CA's `ca.json`) that reads
`.Insecure.User.<field>` — that template is deployment configuration for a CA operator to author,
not something this binary's code prescribes. This phase proves the data reaches `TemplateData`
correctly (see the e2e test); it does not ship a specific template.

## Building

```bash
make issuer
```

## See Also

- [client-manager](./client-manager.md) — owns the database `issuer` reads
- [Enrollment Broker Protocol](../protocols/issuer.md)
- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 2: Write `docs/protocols/issuer.md`**

```markdown
# Issuer Protocol

Already-bootstrapped node → `issuer`'s `RequestOperatingCert` RPC, mTLS (`common/mtls`, same
transport every other gRPC call in this project uses).

## RPC

```proto
service IssuerService {
  rpc RequestOperatingCert(RequestOperatingCertRequest) returns (RequestOperatingCertResponse);
}

message RequestOperatingCertRequest {
  bytes csr_der = 1;
}

message RequestOperatingCertResponse {
  bytes cert_chain_pem = 1;
}
```

## Authorization

The caller's hostname is always derived from its verified mTLS peer identity (`mtls.PeerHostname`)
— never a field on the request. `issuer` looks that hostname up in the same database
`client-manager` writes to: unknown or revoked hostnames are refused outright.

## Behavior

- `csr_der` is a DER-encoded PKCS#10 certificate signing request the caller builds itself — its
  private key never leaves the caller.
- `cert_chain_pem` is the full certificate chain (leaf + any intermediates), PEM-encoded and
  concatenated in order, ready to write directly to `client.crt`.
- The issued certificate's validity is requested per `OperatingCertTTLSec` (`local.conf`), bounded
  by the provisioner's own claims on the CA side.
- Current `attribute` key/value pairs for the hostname are passed as the sign request's
  `TemplateData` — any OTT holder may set this field on step-ca's stock `/1.0/sign` (no extra
  permission gate), and it is merged into `.Insecure.User` for a custom certificate template to
  read, if one is configured.

## See Also

- [issuer](../components/issuer.md)
- [client-manager](../components/client-manager.md)
- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
```

- [ ] **Step 3: Update `docs/components/client-manager.md`**

Add to the "Behavior" section (after the `attribute`/`san` line about re-enroll):

```markdown
- `revoke` now has a real enforcement path: [`issuer`](./issuer.md), sharing this binary's
  database, refuses to issue a fresh operating certificate to a revoked hostname. `attribute`/`san`
  changes are read by `issuer` on the client's next operating-certificate request. (`agent`'s own
  side of requesting that refresh is a separate, later piece of work — not yet built.)
```

Update the `LAST_SEEN` bullet (it currently says "always reads unknown"):

```markdown
- `list`'s `LAST_SEEN` column now reflects real data once `issuer` has served at least one request
  for that hostname; `never` until then.
```

Add to "See Also": `- [issuer](./issuer.md) — enforces revoke/attribute, shares this binary's database`.

- [ ] **Step 4: Update `docs/ARCHITECTURE.md`**

Add a new components-table row after `client-manager`:

```markdown
| issuer | Mints short-lived operating certificates, enforcing revoke and embedding current attributes; shares client-manager's database | Implemented (agent integration and a CA-side custom template are separate, later work) |
```

- [ ] **Step 5: Final verification**

Run: `cd src && go test ./... 2>&1 | tail -30` and `go vet ./...`
Expected: `ok` for every package; `go vet` shows only the pre-existing `cmd/brfs` warning.

- [ ] **Step 6: Commit**

```bash
git add docs/components/issuer.md docs/protocols/issuer.md docs/components/client-manager.md docs/ARCHITECTURE.md
git commit -m "docs: document issuer and its protocol"
```

---

## Self-Review

**Spec coverage:**
- `last_seen` becomes real data → Tasks 1–2.
- Revocation enforcement (refuse the next request) → Task 5.
- Attributes reach the issued certificate's template data → Tasks 5–6, proven by the e2e test.
- SAN changes take effect on next refresh → Task 5 (`client.SANsList()` passed to `mintSign` unconditionally, same mechanism as attributes).
- `step-ca` stays stock → confirmed throughout; only `ca.Client.Sign`/`certmint.Mint` (both pre-existing, already-used calls) are exercised.
- Explicitly *not* covered here (correctly, per this plan's own stated scope): `agent` integration, `common/mtls`'s dual-credential helper, the actual CA-side custom template file, HA for `issuer` — all deferred to a follow-up plan (phase 2c) or explicitly out of scope per the design's Non-Goals.

**Placeholder scan:** an earlier draft of Task 6's e2e test had a broken, self-referential PEM-decode helper (`decodePEMBlock`/`decodeViaStdlibPEM`, the latter never defined) — caught during self-review and replaced with a direct, ordinary `pem.Decode(chainPEM)` call; the import block was corrected to match (added `crypto/tls`/`encoding/pem`, removed an unused `encoding/json`). No other placeholders found; every remaining code block is complete and runnable.

**Type consistency:** `mintAndSignFunc`'s signature (Task 5) matches the real `mintAndSign`'s signature (Task 6) exactly except for the trailing `opts certmint.Options` parameter, which `main.go`'s closure supplies — consistent with the same pattern already used for `certrequest serve`'s `mintFunc`/`certmint.Mint` split in phase 1. `pb.RequestOperatingCertRequest.CsrDer`/`pb.RequestOperatingCertResponse.CertChainPem` (Task 3) are used identically in `server.go` (Task 5) and `mintsign.go`/`main.go` (Task 6).

No gaps found.
