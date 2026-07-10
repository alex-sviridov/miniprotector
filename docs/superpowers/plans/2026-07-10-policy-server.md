# Policy Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `policy-server`, a new standalone control-plane binary that serves backup policies — static, operator-authored JSON files under `$MP_CONFIG_PATH/policies/` — filtered to exactly the policies whose `client_filters` match a requesting client's verified hostname and certificate-embedded attribute labels.

**Architecture:** `policy-server` follows the same small, narrow gRPC-service shape as `catalog`/`issuer`. It has no database and calls no other service: a client's labels are read directly off its presented mTLS certificate (`issuer` already embeds a hostname's `attribute` key/value pairs as a custom X.509 extension on every operating certificate it mints), and policies are held in an in-memory cache rebuilt from disk on startup and on every write to a `policies/.changed` sentinel file (watched via `fsnotify`).

**Tech Stack:** Go, gRPC + protobuf (new `policyserver.proto`), `github.com/fsnotify/fsnotify` (new direct dependency), `common/mtls`/`common/connection`/`common/config` (existing, additively extended), cobra (already used for every CLI in this repo), stdlib `path.Match` for hostname glob filters (no new dependency).

## Global Constraints

- `GetPoliciesRequest` carries no fields — the caller's hostname and attribute labels are always derived from the verified mTLS peer certificate, never a request field, same trust model as every other authenticated RPC in this project.
- `GetPoliciesResponse`'s `Policy` message never includes `client_filters` — a returned policy has already matched; the filter that selected it carries no further meaning to the caller.
- `policy-server`'s listener uses the default `connection.StartServer` (operating-tier peer certs) — no tier special-casing, unlike `issuer`'s own listener.
- `policy-server` never parses, validates, or evaluates `rpo` or `backup_window` — both are opaque pass-through strings.
- A single malformed `*.json` file under `policies/` is skipped (logged loudly), never blocks the rest of the directory from loading. If every file in a reload attempt fails, the previous good in-memory cache is kept, not replaced with an empty list.
- No changes to `client-manager`, `issuer`, or `agent` — this is a purely additive, independently-deployable component. No client-side consumer of `GetPolicies` is built in this plan.

---

## File Structure

| File | Responsibility |
|---|---|
| `src/common/mtls/peer.go` (modify) | `PeerAttributes(ctx) (map[string]string, error)` — extracts `issuer`'s attribute extension from the peer cert |
| `src/common/mtls/peer_test.go` (modify) | Tests for the above |
| `src/common/config/config.go`, `config_test.go` (modify) | `PolicyServerHost`/`PolicyServerPort` (default `9300`), `ResolvePoliciesDir()` |
| `src/api/policyserver.proto` (new) + generated | `PolicyService.GetPolicies` RPC schema |
| `src/cmd/policy-server/policy.go` (new) | `Policy`/`Metadata`/`ClientFilters`/`ObjectFilter` schema types, `parsePolicyFile` |
| `src/cmd/policy-server/policy_test.go` (new) | Tests for the above |
| `src/cmd/policy-server/filter.go` (new) | `Policy.Matches`, hostname glob + label matching |
| `src/cmd/policy-server/filter_test.go` (new) | Tests for the above |
| `src/cmd/policy-server/cache.go` (new) | `Cache`: concurrency-safe in-memory policy list, directory reload logic |
| `src/cmd/policy-server/cache_test.go` (new) | Tests for the above |
| `src/cmd/policy-server/watch.go` (new) | `watchForReload`: fsnotify watcher on `policies/.changed` |
| `src/cmd/policy-server/watch_test.go` (new) | Tests for the above |
| `src/cmd/policy-server/server.go` (new) | `policyServerServer`: `GetPolicies` handler |
| `src/cmd/policy-server/server_test.go` (new) | Tests for the above |
| `src/cmd/policy-server/arguments.go` (new) | `policy-server` CLI flags |
| `src/cmd/policy-server/main.go` (new) | Wiring: config, cache, watcher, `connection.StartServer` |
| `Makefile` (modify) | `policy-server` build target |
| `docs/components/policy-server.md` (new), `docs/protocols/policy-server.md` (new), `docs/components/agent.md`, `README.md`, `docs/ARCHITECTURE.md`, `CHANGELOG.md` (modify) | Documentation |

---

### Task 1: `common/mtls` — `PeerAttributes`

**Files:**
- Modify: `src/common/mtls/peer.go`
- Modify: `src/common/mtls/peer_test.go`

**Interfaces:**
- Produces: `PeerAttributes(ctx context.Context) (map[string]string, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `src/common/mtls/peer_test.go` (add `"encoding/asn1"` and `"encoding/json"` to the existing import block):

```go
func selfSignedCertWithAttributes(t *testing.T, cn string, attrs map[string]string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	value, err := json.Marshal(attrs)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		ExtraExtensions: []pkix.Extension{
			{Id: attributeExtensionOID, Critical: false, Value: value},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

func contextWithPeerCert(cert *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}

func TestPeerAttributes_ReturnsParsedAttributes(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"role": "prod-db", "env": "prod"})
	ctx := contextWithPeerCert(cert)

	got, err := PeerAttributes(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"role": "prod-db", "env": "prod"}, got)
}

func TestPeerAttributes_NoExtensionReturnsEmptyMap(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "node-1")
	ctx := contextWithPeerCert(cert)

	got, err := PeerAttributes(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestPeerAttributes_MalformedExtensionValueFails(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "node-1"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		ExtraExtensions: []pkix.Extension{
			{Id: attributeExtensionOID, Critical: false, Value: []byte("not json")},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	_, err = PeerAttributes(contextWithPeerCert(cert))
	assert.Error(t, err)
}

func TestPeerAttributes_NoPeerInContext(t *testing.T) {
	_, err := PeerAttributes(context.Background())
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/mtls/... -run 'TestPeerAttributes' -v`
Expected: FAIL — `PeerAttributes`/`attributeExtensionOID` undefined (compile error).

- [ ] **Step 3: Implement**

In `src/common/mtls/peer.go`, add `"encoding/asn1"` and `"encoding/json"` to the imports, then append:

```go
// attributeExtensionOID identifies the custom X.509 extension issuer embeds
// on every operating certificate it mints, carrying the hostname's current
// attribute key/value pairs (deploy/control-plane/ca/templates/leaf.tpl).
// Non-critical; present only when the hostname has at least one attribute
// set.
var attributeExtensionOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}

// PeerAttributes extracts and JSON-decodes the attribute extension from the
// client certificate presented on ctx's gRPC peer connection, as embedded by
// issuer (see attributeExtensionOID). Returns an empty, non-nil map -- not
// an error -- when the peer certificate carries no such extension, since
// that's the normal case for a hostname with no attributes set.
func PeerAttributes(ctx context.Context) (map[string]string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no peer information in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, fmt.Errorf("peer connection is not authenticated via TLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no peer certificate presented")
	}
	cert := tlsInfo.State.PeerCertificates[0]
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(attributeExtensionOID) {
			continue
		}
		attrs := make(map[string]string)
		if err := json.Unmarshal(ext.Value, &attrs); err != nil {
			return nil, fmt.Errorf("parse attribute extension: %w", err)
		}
		return attrs, nil
	}
	return map[string]string{}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/mtls/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/common/mtls/peer.go src/common/mtls/peer_test.go
git commit -m "feat(mtls): add PeerAttributes, reading issuer's attribute extension off the peer cert"
```

---

### Task 2: `policy-server` — policy schema and file parsing

**Files:**
- Create: `src/cmd/policy-server/policy.go`
- Create: `src/cmd/policy-server/policy_test.go`

**Interfaces:**
- Produces: `type Metadata struct{Name string; CreatedAt, UpdatedAt time.Time}`, `type ClientFilters struct{Hostnames []string; Labels map[string]string}`, `type ObjectFilter struct{Path string}`, `type Policy struct{Metadata Metadata; ClientFilters ClientFilters; ObjectFilters []ObjectFilter; RPO string; BackupWindow []string}`, `parsePolicyFile(filePath string) (Policy, error)`.

- [ ] **Step 1: Write the failing tests**

`src/cmd/policy-server/policy_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePolicyFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestParsePolicyFile_ValidPolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-10T00:00:00Z"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *", "0 20 * * *"]
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
	assert.Equal(t, "nightly-web-backup", p.Metadata.Name)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.Metadata.CreatedAt)
	assert.Equal(t, []string{"web-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, p.ClientFilters.Labels)
	assert.Equal(t, []ObjectFilter{{Path: "/var/www"}}, p.ObjectFilters)
	assert.Equal(t, "24h", p.RPO)
	assert.Equal(t, []string{"0 2 * * *", "0 20 * * *"}, p.BackupWindow)
}

func TestParsePolicyFile_MissingNameFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{"metadata": {"name": ""}}`)

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidJSONFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `not json`)

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidHostnamePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"client_filters": {"hostnames": ["["]}
	}`)

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}

func TestParsePolicyFile_MissingFileFails(t *testing.T) {
	_, err := parsePolicyFile(filepath.Join(t.TempDir(), "does-not-exist.json"))
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: FAIL — package `main` in `cmd/policy-server` doesn't exist yet (compile error; no non-test files present).

- [ ] **Step 3: Implement**

`src/cmd/policy-server/policy.go`:

```go
// policy-server's on-disk policy schema: one JSON file per policy under
// $MP_CONFIG_PATH/policies/. See
// docs/superpowers/specs/2026-07-10-policy-server-design.md.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"time"
)

type Metadata struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ClientFilters struct {
	Hostnames []string          `json:"hostnames"`
	Labels    map[string]string `json:"labels"`
}

type ObjectFilter struct {
	Path string `json:"path"`
}

type Policy struct {
	Metadata      Metadata       `json:"metadata"`
	ClientFilters ClientFilters  `json:"client_filters"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
}

// parsePolicyFile reads and validates a single policy JSON file. A policy
// must have a non-empty metadata.name, and every client_filters.hostnames
// entry must be a syntactically valid glob pattern (path.Match's syntax) --
// both are treated as load errors, causing the caller to skip this file
// rather than serve a policy no client could ever legitimately match.
func parsePolicyFile(filePath string) (Policy, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Policy{}, fmt.Errorf("read %s: %w", filePath, err)
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("parse %s: %w", filePath, err)
	}
	if p.Metadata.Name == "" {
		return Policy{}, fmt.Errorf("%s: metadata.name is required", filePath)
	}
	for _, pattern := range p.ClientFilters.Hostnames {
		if _, err := path.Match(pattern, ""); err != nil {
			return Policy{}, fmt.Errorf("%s: invalid hostname pattern %q: %w", filePath, pattern, err)
		}
	}
	return p, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS (all 5 tests).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/policy.go src/cmd/policy-server/policy_test.go
git commit -m "feat(policy-server): add policy schema and file parsing"
```

---

### Task 3: `policy-server` — client filter matching

**Files:**
- Create: `src/cmd/policy-server/filter.go`
- Create: `src/cmd/policy-server/filter_test.go`

**Interfaces:**
- Consumes: `Policy`, `ClientFilters` (Task 2).
- Produces: `(Policy) Matches(hostname string, labels map[string]string) bool`.

- [ ] **Step 1: Write the failing tests**

`src/cmd/policy-server/filter_test.go`:

```go
package main

import "testing"

func TestPolicy_Matches(t *testing.T) {
	cases := []struct {
		name     string
		filters  ClientFilters
		hostname string
		labels   map[string]string
		want     bool
	}{
		{
			name:     "empty filters match everyone",
			filters:  ClientFilters{},
			hostname: "anything",
			labels:   nil,
			want:     true,
		},
		{
			name:     "hostname glob matches",
			filters:  ClientFilters{Hostnames: []string{"web-*"}},
			hostname: "web-01",
			labels:   nil,
			want:     true,
		},
		{
			name:     "hostname glob does not match",
			filters:  ClientFilters{Hostnames: []string{"web-*"}},
			hostname: "db-01",
			labels:   nil,
			want:     false,
		},
		{
			name:     "all required labels present",
			filters:  ClientFilters{Labels: map[string]string{"env": "prod", "role": "db"}},
			hostname: "any",
			labels:   map[string]string{"env": "prod", "role": "db", "extra": "ignored"},
			want:     true,
		},
		{
			name:     "missing one required label",
			filters:  ClientFilters{Labels: map[string]string{"env": "prod", "role": "db"}},
			hostname: "any",
			labels:   map[string]string{"env": "prod"},
			want:     false,
		},
		{
			name:     "hostname matches but label missing -- AND fails",
			filters:  ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
			hostname: "web-01",
			labels:   map[string]string{},
			want:     false,
		},
		{
			name:     "hostname and label both match -- AND succeeds",
			filters:  ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
			hostname: "web-01",
			labels:   map[string]string{"env": "prod"},
			want:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Policy{ClientFilters: tc.filters}
			got := p.Matches(tc.hostname, tc.labels)
			if got != tc.want {
				t.Errorf("Matches(%q, %v) = %v, want %v", tc.hostname, tc.labels, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestPolicy_Matches -v`
Expected: FAIL — `Matches` undefined (compile error).

- [ ] **Step 3: Implement**

`src/cmd/policy-server/filter.go`:

```go
package main

import "path"

// Matches reports whether a client with the given hostname and attribute
// labels satisfies this policy's client_filters: an empty hostname pattern
// list matches any hostname; a non-empty list requires at least one glob
// match. Every key/value pair in client_filters.labels must be present in
// labels -- extra labels the client has beyond what's listed don't
// disqualify a match. Both conditions must hold (AND); there is no
// either-hostname-or-labels mode.
func (p Policy) Matches(hostname string, labels map[string]string) bool {
	if !hostnameMatches(p.ClientFilters.Hostnames, hostname) {
		return false
	}
	return labelsMatch(p.ClientFilters.Labels, labels)
}

func hostnameMatches(patterns []string, hostname string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, hostname); ok {
			return true
		}
	}
	return false
}

func labelsMatch(required, actual map[string]string) bool {
	for k, v := range required {
		if actual[k] != v {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS (all tests, including Task 2's).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/filter.go src/cmd/policy-server/filter_test.go
git commit -m "feat(policy-server): add client filter matching (hostname glob + label AND)"
```

---

### Task 4: `policy-server` — in-memory policy cache

**Files:**
- Create: `src/cmd/policy-server/cache.go`
- Create: `src/cmd/policy-server/cache_test.go`

**Interfaces:**
- Consumes: `parsePolicyFile` (Task 2).
- Produces: `type Cache struct{...}`, `NewCache() *Cache`, `(*Cache) Policies() []Policy`, `(*Cache) Reload(dir string, logger *slog.Logger) error`.

- [ ] **Step 1: Write the failing tests**

`src/cmd/policy-server/cache_test.go`:

```go
package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

const validPolicyJSON = `{"metadata": {"name": "%s"}}`

func TestCache_ReloadLoadsValidPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, dir, "b.json", `{"metadata": {"name": "policy-b"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	assert.Len(t, got, 2)
}

func TestCache_ReloadSkipsMalformedFileKeepsGoodOnes(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "good.json", `{"metadata": {"name": "policy-good"}}`)
	writePolicyFile(t, dir, "bad.json", `not json`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1)
	assert.Equal(t, "policy-good", got[0].Metadata.Name)
}

func TestCache_ReloadAllFilesFailKeepsPreviousCache(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "good.json", `{"metadata": {"name": "policy-good"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	require.Len(t, c.Policies(), 1)

	require.NoError(t, os.Remove(filepath.Join(dir, "good.json")))
	writePolicyFile(t, dir, "bad.json", `not json`)

	err := c.Reload(dir, testLogger())
	assert.Error(t, err)
	got := c.Policies()
	require.Len(t, got, 1, "previous good cache must be kept")
	assert.Equal(t, "policy-good", got[0].Metadata.Name)
}

func TestCache_ReloadEmptyDirectoryYieldsEmptyPolicies(t *testing.T) {
	dir := t.TempDir()

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	assert.Empty(t, c.Policies())
}

func TestCache_PoliciesReturnsSnapshotCopy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	got[0].Metadata.Name = "mutated"

	got2 := c.Policies()
	assert.Equal(t, "policy-a", got2[0].Metadata.Name, "mutating a returned snapshot must not affect the cache")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestCache -v`
Expected: FAIL — `Cache`/`NewCache` undefined (compile error).

- [ ] **Step 3: Implement**

`src/cmd/policy-server/cache.go`:

```go
package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
)

// Cache holds the current, atomically-swapped set of policies loaded from
// disk. Safe for concurrent use: GetPolicies handlers call Policies()
// concurrently with a background reload triggered by the fsnotify watcher.
type Cache struct {
	mu       sync.RWMutex
	policies []Policy
}

func NewCache() *Cache {
	return &Cache{}
}

// Policies returns a snapshot of the currently-loaded policy list; mutating
// the returned slice/elements never affects the cache.
func (c *Cache) Policies() []Policy {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Policy, len(c.policies))
	copy(out, c.policies)
	return out
}

// Reload re-reads every *.json file directly under dir, replacing the
// cached policy list with whatever parsed successfully. A file that fails
// to parse is logged and skipped -- it doesn't block the rest of the
// directory from loading. If dir contains at least one *.json file and
// every single one failed to parse, the previous good cache is left in
// place (an error is returned) rather than swapped to an empty list -- an
// empty policies/ directory is a valid "no policies" state, but a reload
// that produced zero successes out of one-or-more attempts is treated as a
// failed reload, not an intentional empty state.
func (c *Cache) Reload(dir string, logger *slog.Logger) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return fmt.Errorf("list policy files in %s: %w", dir, err)
	}

	loaded := make([]Policy, 0, len(matches))
	for _, filePath := range matches {
		p, err := parsePolicyFile(filePath)
		if err != nil {
			logger.Error("skipping malformed policy file", "path", filePath, "error", err)
			continue
		}
		loaded = append(loaded, p)
	}

	if len(matches) > 0 && len(loaded) == 0 {
		return fmt.Errorf("reload of %s: all %d policy files failed to parse, keeping previous cache", dir, len(matches))
	}

	c.mu.Lock()
	c.policies = loaded
	c.mu.Unlock()
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS (all tests, including Tasks 2-3's).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/cache.go src/cmd/policy-server/cache_test.go
git commit -m "feat(policy-server): add in-memory policy cache with directory reload"
```

---

### Task 5: `policy-server` — fsnotify-triggered reload

**Files:**
- Modify: `src/go.mod`, `src/go.sum`
- Create: `src/cmd/policy-server/watch.go`
- Create: `src/cmd/policy-server/watch_test.go`

**Interfaces:**
- Consumes: `Cache.Reload` (Task 4).
- Produces: `watchForReload(ctx context.Context, dir string, cache *Cache, logger *slog.Logger) error`.

- [ ] **Step 1: Add the `fsnotify` dependency**

Run:
```bash
cd src && go get github.com/fsnotify/fsnotify@latest && go mod tidy
```
Expected: `src/go.mod` gains a direct `require github.com/fsnotify/fsnotify vX.Y.Z` line.

- [ ] **Step 2: Write the failing tests**

`src/cmd/policy-server/watch_test.go`:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatchForReload_ReloadsOnChangedFileWrite(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	require.Empty(t, c.Policies())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchForReload(ctx, dir, c, testLogger())

	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".changed"), []byte("1"), 0o644))

	require.Eventually(t, func() bool {
		return len(c.Policies()) == 1
	}, 2*time.Second, 10*time.Millisecond, "cache should reload after .changed is written")
}

func TestWatchForReload_IgnoresOtherFileWrites(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchForReload(ctx, dir, c, testLogger())

	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, c.Policies(), "reload must not fire without a write to .changed")
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestWatchForReload -v`
Expected: FAIL — `watchForReload` undefined (compile error).

- [ ] **Step 4: Implement**

`src/cmd/policy-server/watch.go`:

```go
package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// watchForReload watches dir for writes to dir/.changed -- the sentinel
// file an operator touches after finishing a (possibly multi-file) policy
// edit -- and triggers a full Cache.Reload on each write. Watching this one
// sentinel, rather than every *.json file individually, means a batch edit
// across several policy files produces exactly one atomic reload instead of
// one reload per file mid-edit. Blocks until ctx is cancelled.
func watchForReload(ctx context.Context, dir string, cache *Cache, logger *slog.Logger) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.Add(dir); err != nil {
		return err
	}
	changedPath := filepath.Join(dir, ".changed")

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Name != changedPath {
				continue
			}
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}
			if err := cache.Reload(dir, logger); err != nil {
				logger.Error("policy reload failed", "error", err)
				continue
			}
			logger.Info("policies reloaded")
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logger.Error("policy watcher error", "error", err)
		}
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS (all tests, including Tasks 2-4's).

- [ ] **Step 6: Commit**

```bash
git add src/go.mod src/go.sum src/cmd/policy-server/watch.go src/cmd/policy-server/watch_test.go
git commit -m "feat(policy-server): reload cache on policies/.changed via fsnotify"
```

---

### Task 6: `policyserver.proto`

**Files:**
- Create: `src/api/policyserver.proto`
- Generated (via `make proto`): `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go`

**Interfaces:**
- Produces: `pb.PolicyServiceServer`, `pb.RegisterPolicyServiceServer`, `pb.NewPolicyServiceClient`, `pb.UnimplementedPolicyServiceServer`, `pb.GetPoliciesRequest{}`, `pb.GetPoliciesResponse{Policies []*pb.Policy}`, `pb.Policy{Name string, CreatedAt, UpdatedAt *timestamppb.Timestamp, ObjectFilters []*pb.ObjectFilter, Rpo string, BackupWindow []string}`, `pb.ObjectFilter{Path string}`.

- [ ] **Step 1: Write the proto file**

`src/api/policyserver.proto`:

```proto
syntax = "proto3";

package policyserverservice;

option go_package = "./proto";

import "google/protobuf/timestamp.proto";

// PolicyService is policy-server's sole RPC surface: a running node asks
// "which policies apply to me?" and gets back exactly the policies whose
// client_filters match its own verified identity. The caller's hostname and
// attribute labels are never fields on this message -- both are derived
// entirely from the mTLS peer certificate presented on the connection, the
// same trust model every other authenticated RPC in this project uses.
service PolicyService {
  rpc GetPolicies(GetPoliciesRequest) returns (GetPoliciesResponse);
}

message GetPoliciesRequest {}

message GetPoliciesResponse {
  repeated Policy policies = 1;
}

message ObjectFilter {
  string path = 1;
}

message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  // Duration string, e.g. "24h" (time.ParseDuration format). policy-server
  // never parses or evaluates this -- opaque pass-through data.
  string rpo = 5;
  // List of cron expressions (5-field). policy-server never parses or
  // evaluates these -- opaque pass-through data.
  repeated string backup_window = 6;
}
```

- [ ] **Step 2: Generate the Go code**

Run: `make proto`
Expected output: `Protobuf code generated in src/api/` and new files `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go` present.

- [ ] **Step 3: Confirm it compiles**

Run: `cd src && go build ./api/...`
Expected: no output, exit code 0.

- [ ] **Step 4: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go
git commit -m "feat(api): add policyserver proto (GetPolicies RPC)"
```

---

### Task 7: `policy-server`'s gRPC handler

**Files:**
- Create: `src/cmd/policy-server/server.go`
- Create: `src/cmd/policy-server/server_test.go`

**Interfaces:**
- Consumes: `pb.PolicyServiceServer`/`pb.GetPoliciesRequest`/`Response`/`pb.Policy`/`pb.ObjectFilter` (Task 6), `mtls.PeerHostname(ctx)`/`mtls.PeerAttributes(ctx)` (existing + Task 1), `Cache.Policies()` (Task 4), `Policy.Matches` (Task 3).
- Produces: `type policyServerServer struct{...}`, `NewPolicyServerServer(cache *Cache, logger *slog.Logger) *policyServerServer`.

- [ ] **Step 1: Write the failing tests**

`src/cmd/policy-server/server_test.go`:

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
	"encoding/asn1"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// attributeExtensionOID mirrors cmd/issuer/e2e_test.go's own copy -- the
// same private-use OID issuer embeds attributes under; small OID constants
// like this are duplicated per test file in this codebase rather than
// exported from common/mtls.
var attributeExtensionOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}

// fakeAuthContext mirrors cmd/catalog/server_test.go's and cmd/issuer/
// server_test.go's helper of the same name: a self-signed cert with the
// given hostname as its SAN and attributes as its embedded extension,
// simulating a verified mTLS peer identity without a real handshake.
func fakeAuthContext(t *testing.T, hostname string, attrs map[string]string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	var extensions []pkix.Extension
	if attrs != nil {
		value, err := json.Marshal(attrs)
		require.NoError(t, err)
		extensions = []pkix.Extension{{Id: attributeExtensionOID, Critical: false, Value: value}}
	}

	template := &x509.Certificate{
		SerialNumber:    big.NewInt(1),
		Subject:         pkix.Name{CommonName: hostname},
		DNSNames:        []string{hostname},
		NotBefore:       time.Now(),
		NotAfter:        time.Now().Add(time.Hour),
		ExtraExtensions: extensions,
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

func newTestServerWithPolicies(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, testLogger())
}

func TestGetPolicies_ReturnsOnlyMatchingPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	writePolicyFile(t, dir, "db.json", `{
		"metadata": {"name": "db-policy"},
		"client_filters": {"labels": {"role": "db"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "web-policy", resp.Policies[0].Name)
}

func TestGetPolicies_EmptyFiltersMatchEveryone(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "all.json", `{"metadata": {"name": "everyone"}}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "anything", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "everyone", resp.Policies[0].Name)
}

func TestGetPolicies_MatchesOnPeerCertLabels(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "db.json", `{
		"metadata": {"name": "db-policy"},
		"client_filters": {"labels": {"role": "db"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "node-1", map[string]string{"role": "db"}), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "db-policy", resp.Policies[0].Name)
}

func TestGetPolicies_NoPeerIdentityRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerWithPolicies(t, dir)

	_, err := srv.GetPolicies(context.Background(), &pb.GetPoliciesRequest{})
	assert.Error(t, err)
}

func TestGetPolicies_ResponseFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "full.json", `{
		"metadata": {"name": "full-policy", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
		"object_filters": [{"path": "/var/www"}, {"path": "/etc"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"]
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	p := resp.Policies[0]
	assert.Equal(t, "full-policy", p.Name)
	assert.Equal(t, "24h", p.Rpo)
	assert.Equal(t, []string{"0 2 * * *"}, p.BackupWindow)
	require.Len(t, p.ObjectFilters, 2)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.CreatedAt.AsTime())
	assert.Equal(t, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), p.UpdatedAt.AsTime())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestGetPolicies -v`
Expected: FAIL — `policyServerServer`/`NewPolicyServerServer` undefined (compile error).

- [ ] **Step 3: Implement**

`src/cmd/policy-server/server.go`:

```go
package main

import (
	"context"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// policyServerServer implements PolicyService: the sole RPC any node calls
// to learn which backup policies target it. The caller's identity (hostname
// and attribute labels) is always derived from the verified mTLS peer
// certificate -- never a request field -- and matched against the current
// in-memory policy cache. No database, no other service is consulted.
type policyServerServer struct {
	pb.UnimplementedPolicyServiceServer
	cache  *Cache
	logger *slog.Logger
}

func NewPolicyServerServer(cache *Cache, logger *slog.Logger) *policyServerServer {
	return &policyServerServer{cache: cache, logger: logger}
}

func (s *policyServerServer) GetPolicies(ctx context.Context, _ *pb.GetPoliciesRequest) (*pb.GetPoliciesResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not determine peer identity", "error", err)
		return nil, err
	}
	labels, err := mtls.PeerAttributes(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not read peer attributes", "hostname", hostname, "error", err)
		return nil, err
	}

	var matched []*pb.Policy
	for _, p := range s.cache.Policies() {
		if !p.Matches(hostname, labels) {
			continue
		}
		matched = append(matched, toProtoPolicy(p))
	}

	s.logger.Info("GetPolicies", "hostname", hostname, "matched", len(matched))
	return &pb.GetPoliciesResponse{Policies: matched}, nil
}

func toProtoPolicy(p Policy) *pb.Policy {
	objectFilters := make([]*pb.ObjectFilter, len(p.ObjectFilters))
	for i, f := range p.ObjectFilters {
		objectFilters[i] = &pb.ObjectFilter{Path: f.Path}
	}
	return &pb.Policy{
		Name:          p.Metadata.Name,
		CreatedAt:     timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:     timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters: objectFilters,
		Rpo:           p.RPO,
		BackupWindow:  p.BackupWindow,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS (all tests, including Tasks 2-5's).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go
git commit -m "feat(policy-server): add GetPolicies gRPC handler"
```

---

### Task 8: config keys, CLI, `main.go`, and the build target

**Files:**
- Modify: `src/common/config/config.go`, `src/common/config/config_test.go`
- Create: `src/cmd/policy-server/arguments.go`
- Create: `src/cmd/policy-server/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `Cache`/`NewCache`/`Cache.Reload` (Task 4), `watchForReload` (Task 5), `pb.RegisterPolicyServiceServer` (Task 6), `NewPolicyServerServer` (Task 7), `connection.StartServer`/`config.ResolveConfigPath`/`ResolveCertsDir`/`ContextKey` (existing), `logging.NewLogger` (existing).
- Produces: `Config.PolicyServerHost string`, `Config.PolicyServerPort int` (default `9300`), `config.ResolvePoliciesDir() (string, error)`, `type Arguments struct{Port int; Debug bool}`, `parseArguments(conf *config.Config) (*Arguments, error)`.

- [ ] **Step 1: Write the failing config tests**

Append to `src/common/config/config_test.go`:

```go
func TestParseConfig_PolicyServerHostParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\npolicy_server_host=policy.backup.internal\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "policy.backup.internal", conf.PolicyServerHost)
}

func TestParseConfig_PolicyServerPortDefaultsTo9300(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9300, conf.PolicyServerPort)
}

func TestParseConfig_PolicyServerPortParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\npolicy_server_port=9301\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9301, conf.PolicyServerPort)
}

func TestResolvePoliciesDir_JoinsBaseDirWithPolicies(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigPathEnvVar, dir)

	got, err := ResolvePoliciesDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "policies"), got)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/config/... -run 'TestParseConfig_PolicyServer|TestResolvePoliciesDir' -v`
Expected: FAIL — fields/function undefined (compile error).

- [ ] **Step 3: Implement the config changes**

In `src/common/config/config.go`, add two fields to the `Config` struct (after `IssuerSelfCertRefreshIntervalSec`):

```go
	PolicyServerHost                 string
	PolicyServerPort                 int
```

Add the default to the literal in `ParseConfig` (alongside `IssuerPort: 9200,`):

```go
		PolicyServerPort:                 9300,
```

Add two `case`s to the `switch key` block (alongside `case "issuer_port":`):

```go
		case "policy_server_host":
			config.PolicyServerHost = value
			foundFields["policy_server_host"] = true
		case "policy_server_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid policy_server_port value at line %d: %s", lineNum, value)
			}
			config.PolicyServerPort = port
			foundFields["policy_server_port"] = true
```

Append `ResolvePoliciesDir` after `ResolveCertsDir`:

```go
// ResolvePoliciesDir determines the policy-server policy directory:
// <base>/policies, where base comes from ResolveBaseDir.
func ResolvePoliciesDir() (string, error) {
	baseDir, err := ResolveBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "policies"), nil
}
```

- [ ] **Step 4: Run config tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit the config changes**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add policy_server_host, policy_server_port, ResolvePoliciesDir"
```

- [ ] **Step 6: CLI arguments**

`src/cmd/policy-server/arguments.go` (mirrors `catalog`'s `arguments.go`):

```go
package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Port  int
	Debug bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "policy-server",
		Short: "Serve backup policies filtered by a requesting client's hostname and attribute labels",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().IntVar(&args.Port, "port", conf.PolicyServerPort, "Port to listen on")
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

- [ ] **Step 7: `main.go`**

`src/cmd/policy-server/main.go`:

```go
// policy-server serves backup policies -- static, operator-maintained JSON
// files under $MP_CONFIG_PATH/policies/ -- filtered to whatever the
// requesting client's verified hostname and certificate-embedded attribute
// labels match. It is bootstrapped and certificate-managed exactly like any
// other node in the mesh (client-manager add, agent + issuer refresh); it
// holds no database and calls no other service. See
// docs/components/policy-server.md and
// docs/superpowers/specs/2026-07-10-policy-server-design.md.
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
	"google.golang.org/grpc"
)

func main() {
	const appName = "policy-server"

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

	policiesDir, err := config.ResolvePoliciesDir()
	if err != nil {
		logger.Error("policies directory resolution failed", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(policiesDir, 0o755); err != nil {
		logger.Error("failed to create policies directory", "path", policiesDir, "error", err)
		os.Exit(1)
	}

	cache := NewCache()
	if err := cache.Reload(policiesDir, logger); err != nil {
		logger.Error("initial policy load failed", "error", err)
		os.Exit(1)
	}

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	srv := NewPolicyServerServer(cache, logger)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := watchForReload(signalCtx, policiesDir, cache, logger); err != nil {
			logger.Error("policy watcher stopped", "error", err)
		}
	}()

	logger.Info("policy-server started", "port", arguments.Port, "policies_dir", policiesDir)

	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
		pb.RegisterPolicyServiceServer(s, srv)
	}); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 8: Confirm it builds**

Run: `cd src && go build ./cmd/policy-server/...`
Expected: no output, exit code 0.

- [ ] **Step 9: Add the Makefile target**

In `Makefile`, add `POLICY_SERVER_CMD := cmd/policy-server` alongside the other `*_CMD` variables, add `policy-server` to the `.PHONY` line, and add:

```makefile
policy-server: $(BINARY_DIR) ## Build policy-server binary
	@printf "$(BLUE)Building policy-server...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/policy-server ./$(POLICY_SERVER_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/policy-server"
```

- [ ] **Step 10: Full verification**

Run: `make policy-server && cd src && go build ./... && go test ./... 2>&1 | tail -30`
Expected: `policy-server` builds successfully; every package shows `ok` (the pre-existing, unrelated `cmd/brfs` vet warning, if checked, remains the only `go vet` output — not introduced by this task).

- [ ] **Step 11: Commit**

```bash
git add src/cmd/policy-server/arguments.go src/cmd/policy-server/main.go Makefile
git commit -m "feat(policy-server): add CLI, main wiring, and build target"
```

---

### Task 9: Documentation

**Files:**
- Create: `docs/components/policy-server.md`
- Create: `docs/protocols/policy-server.md`
- Modify: `docs/components/agent.md`, `README.md`, `docs/ARCHITECTURE.md`, `CHANGELOG.md`

- [ ] **Step 1: Write `docs/components/policy-server.md`**

```markdown
# policy-server

Serves backup policies — static, operator-authored JSON files under `$MP_CONFIG_PATH/policies/` —
filtered to exactly the policies whose `client_filters` match a requesting client's verified
hostname and certificate-embedded attribute labels. See
[Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md).

`policy-server` is bootstrapped and certificate-managed exactly like any other node in the mesh
(`client-manager add`, `agent` + `issuer` refresh) — it holds no database and calls no other
service. A client's attribute labels are read directly off its presented mTLS certificate: `issuer`
already embeds a hostname's current `attribute` key/value pairs as a custom X.509 extension on
every operating certificate it mints.

## Usage

```bash
policy-server --port 9300
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `9300` (or `policy_server_port` from `local.conf`) | Port to listen on |
| `--debug` | false | Enable debug logging |

## Behavior

`GetPolicies` (see [protocol](../protocols/policy-server.md)) is `policy-server`'s only RPC. The
caller's hostname is always the verified mTLS peer identity; the caller's attribute labels are
parsed from the same peer certificate's embedded extension (`mtls.PeerAttributes`) — neither is
ever a field on the request.

A policy matches a client when: (`client_filters.hostnames` is empty, or the client's hostname
matches at least one glob pattern in it) **and** (every key/value pair in `client_filters.labels`
is present among the client's attribute labels). Both conditions must hold. The response never
includes `client_filters` — a returned policy has already matched, so the filter that selected it
carries no further meaning to the caller.

`policy-server` never parses, validates, or evaluates a policy's `rpo` or `backup_window` — both
are opaque strings, stored and returned verbatim for a future consumer to interpret.

### Policy files and hot reload

Each `$MP_CONFIG_PATH/policies/*.json` file is one policy: `metadata` (`name` plus operator-set
`created_at`/`updated_at`), `client_filters` (`hostnames` glob list, `labels` map), `object_filters`
(a list of `{"path": "..."}` entries), `rpo` (a duration string, e.g. `"24h"`), and `backup_window`
(a list of cron expressions, e.g. `["0 2 * * *", "0 20 * * *"]`).

All policies are loaded into memory at startup. To pick up edits, touch
`$MP_CONFIG_PATH/policies/.changed` after finishing your edit(s) — `policy-server` watches that one
sentinel file via `fsnotify` and reloads the entire directory as a single atomic swap on each
write. This lets you edit several policy files as a batch and trigger exactly one reload, rather
than reloading (potentially mid-edit) on every individual file write.

A single malformed policy file is skipped, logged loudly, and does not block the rest of the
directory from loading. If every file in a reload attempt fails to parse, the previous good
in-memory cache is kept rather than replaced with an empty list.

## Configuration Keys

- `policy_server_host` / `policy_server_port` — where `policy-server` listens *(default port:
  9300)*

## Building

```bash
make policy-server
```

## See Also

- [issuer](./issuer.md) — mints the operating certificates whose embedded attribute extension
  `policy-server` reads
- [Policy Server Protocol](../protocols/policy-server.md)
- [Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 2: Write `docs/protocols/policy-server.md`**

```markdown
# Policy Server Protocol

Any enrolled node (authenticated with its operating credential, `client.crt`/`client.key`) →
`policy-server`'s `GetPolicies` RPC, mTLS (`common/mtls`, same transport every other gRPC call in
this project uses).

## RPC

```proto
service PolicyService {
  rpc GetPolicies(GetPoliciesRequest) returns (GetPoliciesResponse);
}

message GetPoliciesRequest {}

message GetPoliciesResponse {
  repeated Policy policies = 1;
}

message ObjectFilter {
  string path = 1;
}

message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
}
```

## Authorization

The caller's hostname is always derived from its verified mTLS peer identity
(`mtls.PeerHostname`); the caller's attribute labels are always derived from the same peer
certificate's embedded attribute extension (`mtls.PeerAttributes`, reading the custom X.509
extension `issuer` bakes into every operating certificate it mints). Neither is ever a field on
`GetPoliciesRequest`. `policy-server`'s listener requires the default operating-tier peer
certificate — the same requirement every server except `issuer`'s own listener enforces.

## Behavior

- `GetPoliciesRequest` is empty — no fields to set.
- `GetPoliciesResponse.policies` contains every policy whose `client_filters` match the caller:
  hostname glob match (or no hostname restriction) **and** every required label present — both
  conditions must hold. `client_filters` itself is never echoed back.
- `rpo` and `backup_window` are opaque, pass-through strings — `policy-server` never parses or
  evaluates either.

## See Also

- [policy-server](../components/policy-server.md)
- [issuer](../components/issuer.md) — embeds the attribute extension this protocol's authorization
  depends on
- [Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md)
```

- [ ] **Step 3: Update `docs/components/agent.md`**

Find the existing forward-looking note about "policy-server-fetched work" (referenced from the
research done during this plan's design phase) and update it to reflect that `policy-server` now
exists as a component, while making clear `agent` itself does not yet call it:

```markdown
`policy-server` (see [policy-server](./policy-server.md)) now exists as a standalone component
serving backup policies, but `agent` does not yet fetch from it — no policy-driven scheduling is
wired into `agent`'s reconcile loop. That integration remains separate, later work.
```

- [ ] **Step 4: Update `README.md`**

Add to the Components list (after `catalog`):

```markdown
- **[policy-server](docs/components/policy-server.md)** - Serves backup policies filtered by a requesting client's hostname and attribute labels (control-plane component)
```

Add to the Documentation list (after the Issuer Protocol line):

```markdown
- **[Policy Server Protocol](docs/protocols/policy-server.md)** - policy-server's GetPolicies protocol
```

- [ ] **Step 5: Update `docs/ARCHITECTURE.md`**

Add a new components-table row after `issuer`:

```markdown
| policy-server | Serves backup policies filtered by a requesting client's hostname and attribute labels; no database, reads labels from the peer cert | Implemented (no client-side consumer yet — agent integration is separate, later work) |
```

- [ ] **Step 6: Add the `CHANGELOG.md` entry**

Add to the top of `CHANGELOG.md` (most recent first), following the existing entry format:

```markdown
## 2026-07-10 — Backup policy serving (policy-server)

Added `policy-server`, a new control-plane binary that serves backup policies — static,
operator-authored JSON files under `$MP_CONFIG_PATH/policies/` — filtered to whatever a requesting
client's hostname and attribute labels match. It holds no database and calls no other service: a
client's attribute labels are read directly off its own mTLS certificate, since `issuer` already
embeds them there as a custom X.509 extension on every operating certificate it mints. Policies are
cached in memory and hot-reloaded as a single atomic swap whenever an operator touches a
`policies/.changed` sentinel file after editing one or more policy files. A client-side consumer
(`agent` fetching and acting on policies) is deliberately deferred to later, separate work.
```

- [ ] **Step 7: Final verification**

Run: `cd src && go test ./... 2>&1 | tail -30` and `go vet ./...`
Expected: `ok` for every package; `go vet` shows only the pre-existing `cmd/brfs` warning.

- [ ] **Step 8: Commit**

```bash
git add docs/components/policy-server.md docs/protocols/policy-server.md docs/components/agent.md README.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document policy-server and its protocol"
```

---

## Self-Review

**Spec coverage:**
- Policies as one-JSON-file-per-policy under `$MP_CONFIG_PATH/policies/` → Task 2.
- `metadata` (name + timestamps), `client_filters` (hostname globs + labels), `object_filters`
  (structured, path-only), `rpo` (duration string), `backup_window` (list of cron strings) → Task 2.
- Label source is the client's own mTLS cert (no database) → Task 1, consumed by Task 7.
- Filter AND semantics (hostname glob OR-within, labels AND-within, both AND-across) → Task 3.
- In-memory cache, malformed-file skip-and-log, zero-success-keeps-previous → Task 4.
- `.changed`-sentinel-triggered atomic reload via fsnotify → Task 5.
- `GetPolicies` RPC: empty request, `client_filters`-stripped response → Task 6 (schema), Task 7
  (handler).
- Default operating-tier listener (no tier special-casing) → Task 8 (`connection.StartServer`,
  same as `catalog`).
- `policy_server_host`/`policy_server_port` config, `$MP_CONFIG_PATH/policies` resolution → Task 8.
- Documentation impact (component doc, protocol doc, README, ARCHITECTURE, CHANGELOG, this
  project's own documentation rule for gRPC protocol changes) → Task 9.
- Explicitly out of scope (correctly not covered by any task above): a client-side consumer of
  `GetPolicies` (`agent` or `brfs` calling it), RPO/backup_window enforcement, policy CRUD/admin
  UI, cross-file validation, HA.

**Placeholder scan:** no "TBD"/"TODO" strings; every code block is complete and directly usable —
re-read Task 5's `watch_test.go` and Task 7's `server_test.go` in particular, since both fabricate
non-trivial peer certificates, and confirmed both build a fully valid `x509.Certificate` template
with real `ExtraExtensions`, not a placeholder cert.

**Type consistency:** `Cache.Reload(dir string, logger *slog.Logger) error` (Task 4) is called
identically by `watchForReload` (Task 5) and `main.go` (Task 8). `Policy`/`Metadata`/
`ClientFilters`/`ObjectFilter` (Task 2) are used identically by `filter.go` (Task 3), `cache.go`
(Task 4), and `server.go`'s `toProtoPolicy` (Task 7). `pb.Policy`'s field names (`Name`,
`CreatedAt`, `UpdatedAt`, `ObjectFilters`, `Rpo`, `BackupWindow`) match exactly between the proto
definition (Task 6) and `toProtoPolicy`/the response-shape test assertions (Task 7) — in
particular, proto's lowercase `rpo` field generates Go field `Rpo` (single-word, no underscore),
confirmed against `pb.Policy{..., Rpo: p.RPO, ...}` usage.

No gaps found.
