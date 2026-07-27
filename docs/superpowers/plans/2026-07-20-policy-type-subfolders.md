# Policy Type Subfolders Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `policy-server`'s on-disk policies a `type`, derived from the immediate subfolder they live in (`policies/backup/*.json` today), and make `agent`'s backup-task derivation filter on `type == "backup"`, so future non-backup policy types can coexist without being misinterpreted.

**Architecture:** `policy-server` walks one level of subdirectories under its policies directory instead of globbing flat files, tagging every loaded policy with its parent subfolder's name. That `Type` flows through the proto `Policy` message, `policyclient`'s on-disk cache, and `api-server`'s REST DTO as plain passthrough data; `agent` is the one consumer that actually branches on it, skipping any cached policy whose type isn't `"backup"` before deriving backup tasks.

**Tech Stack:** Go, protobuf/gRPC (`protoc` + `protoc-gen-go`/`protoc-gen-go-grpc`, already installed), `testify` (`assert`/`require`).

## Global Constraints

- No back-compat and no migration code for the old flat `policies/*.json` layout — this is a breaking on-disk layout change (per `docs/superpowers/specs/2026-07-20-policy-type-subfolders-design.md`).
- Type is derived from the subfolder name only — never read from or written to the policy JSON file itself.
- `CreatePolicy`/`UpdatePolicy` (proto, `api-server`, web UI) get **no** `type` parameter in this change — the write path stays hardcoded to `"backup"`.
- Every step that changes Go code ends with `go build ./...` and the relevant `go test ./...` passing before moving on.

---

## Task 1: Add `type` field to the policy-server proto and regenerate

**Files:**
- Modify: `src/api/policyserver.proto`
- Generated (do not hand-edit): `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go`

**Interfaces:**
- Produces: `pb.Policy.GetType() string` and `pb.Policy{Type: ...}`, consumed by Task 3 (`server.go`).

- [ ] **Step 1: Add the field to the `Policy` message**

In `src/api/policyserver.proto`, the `Policy` message currently ends at `client_filters = 9`:

```proto
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
  string destination = 7;
  // policy-server-computed, deterministic from the policy file's name --
  // stable across reloads, changes only if the file is renamed. Not
  // present in the on-disk policy JSON schema.
  string id = 8;
  // Only ever populated by ListPolicies/CreatePolicy/UpdatePolicy -- omitted
  // by GetPolicies so a node never learns another node's targeting rules
  // from a policy that already matched its own identity.
  ClientFilters client_filters = 9;
}
```

Add a `type` field after `client_filters`:

```proto
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
  string destination = 7;
  // policy-server-computed, deterministic from the policy file's name --
  // stable across reloads, changes only if the file is renamed. Not
  // present in the on-disk policy JSON schema.
  string id = 8;
  // Only ever populated by ListPolicies/CreatePolicy/UpdatePolicy -- omitted
  // by GetPolicies so a node never learns another node's targeting rules
  // from a policy that already matched its own identity.
  ClientFilters client_filters = 9;
  // Derived from the name of the subfolder the policy file was loaded from
  // (e.g. "backup" for policies/backup/*.json) -- never read from or
  // written to the on-disk policy JSON. Populated by both GetPolicies and
  // ListPolicies.
  string type = 10;
}
```

- [ ] **Step 2: Regenerate**

Run:
```bash
cd /home/alex/miniprotector && make proto
```
Expected: `Protobuf code generated in src/api/` with no errors.

- [ ] **Step 3: Verify it compiles**

Run:
```bash
cd /home/alex/miniprotector/src && go build ./...
```
Expected: exits 0, no output.

- [ ] **Step 4: Verify the generated field exists**

Run:
```bash
grep -n "GetType\|Type string" /home/alex/miniprotector/src/api/policyserver.pb.go | head -5
```
Expected: shows `Type string` on the `Policy` struct and a generated `GetType()` method.

- [ ] **Step 5: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go
git commit -m "feat(api): add type field to policyserver Policy message"
```

---

## Task 2: policy-server — derive policy Type from subfolder, subfolder-based Reload

**Files:**
- Modify: `src/cmd/policy-server/policy.go`
- Modify: `src/cmd/policy-server/cache.go`
- Modify: `src/cmd/policy-server/write.go`
- Modify: `src/cmd/policy-server/main.go` (doc comment only)
- Modify (full rewrite): `src/cmd/policy-server/policy_test.go`
- Modify (full rewrite): `src/cmd/policy-server/cache_test.go`
- Modify (full rewrite): `src/cmd/policy-server/server_test.go`
- Modify (full rewrite): `src/cmd/policy-server/write_test.go`
- Modify (full rewrite): `src/cmd/policy-server/watch_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 (this task is entirely internal to `policy-server`'s own `Policy` struct, independent of the proto `Policy` message).
- Produces: `Policy.Type string` (internal struct field, `json:"-"`), `parsePolicyFile(filePath, policyType string) (Policy, error)` (signature change from the old single-arg form) — consumed by Task 3 (`server.go`'s `toProtoPolicy`).

This is a wide-reaching but single, coherent behavioral change: `Cache.Reload`'s contract changes from "every `*.json` directly under `dir`" to "every `*.json` one level under `dir`, tagged by its immediate parent folder's name." Every existing test that exercises `Reload` (directly, or transitively via `newTestWriteServer`/`newTestServerWithPolicies`) has its fixtures relocated into a `backup/` subfolder in the same commit — splitting that relocation into a separate task would leave an intermediate commit with failing tests, which we don't want.

- [ ] **Step 1: Write the new/changed tests first**

Replace the full contents of `src/cmd/policy-server/policy_test.go` with:

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
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestParsePolicyFile_ValidPolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-10T00:00:00Z"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *", "0 20 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)

	p, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	assert.Equal(t, "nightly-web-backup", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.Metadata.CreatedAt)
	assert.Equal(t, []string{"web-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, p.ClientFilters.Labels)
	require.Len(t, p.ObjectFilters, 1)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, []string{"*.html", "*.css"}, p.ObjectFilters[0].Include)
	assert.Equal(t, []string{"*.tmp"}, p.ObjectFilters[0].Exclude)
	assert.NotEmpty(t, p.ObjectFilters[0].ID)
	assert.Equal(t, "24h", p.RPO)
	assert.Equal(t, []string{"0 2 * * *", "0 20 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_SetsTypeFromArgument(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{"metadata": {"name": "nightly"}}`)

	p, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	assert.Equal(t, "backup", p.Type)
}

func TestParsePolicyFile_ComputesDeterministicPolicyID(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup"},
		"object_filters": [{"path": "/var/www"}]
	}`)

	p1, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p2, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)

	assert.NotEmpty(t, p1.Metadata.ID)
	assert.Equal(t, p1.Metadata.ID, p2.Metadata.ID, "same filename must yield the same policy ID every parse")
}

func TestParsePolicyFile_DifferentFilenamesYieldDifferentPolicyIDs(t *testing.T) {
	dir := t.TempDir()
	pathA := writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "same-name"}}`)
	pathB := writePolicyFile(t, dir, "b.json", `{"metadata": {"name": "same-name"}}`)

	pa, err := parsePolicyFile(pathA, "backup")
	require.NoError(t, err)
	pb, err := parsePolicyFile(pathB, "backup")
	require.NoError(t, err)

	assert.NotEqual(t, pa.Metadata.ID, pb.Metadata.ID, "identical metadata.name in different files must not collide")
}

func TestParsePolicyFile_ObjectFiltersAtDifferentIndicesGetDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "multi.json", `{
		"metadata": {"name": "multi"},
		"object_filters": [{"path": "/a"}, {"path": "/b"}]
	}`)

	p, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	require.Len(t, p.ObjectFilters, 2)
	assert.NotEmpty(t, p.ObjectFilters[0].ID)
	assert.NotEmpty(t, p.ObjectFilters[1].ID)
	assert.NotEqual(t, p.ObjectFilters[0].ID, p.ObjectFilters[1].ID)
}

func TestParsePolicyFile_ObjectFiltersWithIdenticalPathGetDistinctIDs(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "duplicate-path.json", `{
		"metadata": {"name": "duplicate-path"},
		"object_filters": [
			{"path": "/var/www", "include": ["*.html"]},
			{"path": "/var/www", "exclude": ["*.log"]}
		]
	}`)

	p, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	require.Len(t, p.ObjectFilters, 2)
	assert.NotEqual(t, p.ObjectFilters[0].ID, p.ObjectFilters[1].ID, "two object filters sharing a path must still get distinct IDs")
}

func TestParsePolicyFile_ObjectFilterOmitsIncludeExclude(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "minimal.json", `{
		"metadata": {"name": "minimal"},
		"object_filters": [{"path": "/data"}]
	}`)

	p, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	require.Len(t, p.ObjectFilters, 1)
	assert.Equal(t, "/data", p.ObjectFilters[0].Path)
	assert.Empty(t, p.ObjectFilters[0].Include)
	assert.Empty(t, p.ObjectFilters[0].Exclude)
}

func TestParsePolicyFile_InvalidIncludePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"object_filters": [{"path": "/data", "include": ["["]}]
	}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidExcludePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"object_filters": [{"path": "/data", "exclude": ["["]}]
	}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_MissingNameFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{"metadata": {"name": ""}}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidJSONFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `not json`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidHostnamePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"client_filters": {"hostnames": ["["]}
	}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_MissingFileFails(t *testing.T) {
	_, err := parsePolicyFile(filepath.Join(t.TempDir(), "does-not-exist.json"), "backup")
	assert.Error(t, err)
}

func TestValidatePolicy_ValidPolicyReturnsNil(t *testing.T) {
	p := Policy{
		Metadata:      Metadata{Name: "ok"},
		ClientFilters: ClientFilters{Hostnames: []string{"web-*"}},
		ObjectFilters: []ObjectFilter{{Path: "/data", Include: []string{"*.sql"}, Exclude: []string{"*.tmp"}}},
	}
	assert.NoError(t, validatePolicy(p))
}

func TestValidatePolicy_MissingNameFails(t *testing.T) {
	assert.Error(t, validatePolicy(Policy{}))
}

func TestValidatePolicy_InvalidHostnamePatternFails(t *testing.T) {
	p := Policy{Metadata: Metadata{Name: "x"}, ClientFilters: ClientFilters{Hostnames: []string{"["}}}
	assert.Error(t, validatePolicy(p))
}

func TestValidatePolicy_InvalidIncludePatternFails(t *testing.T) {
	p := Policy{Metadata: Metadata{Name: "x"}, ObjectFilters: []ObjectFilter{{Path: "/data", Include: []string{"["}}}}
	assert.Error(t, validatePolicy(p))
}

func TestValidatePolicy_InvalidExcludePatternFails(t *testing.T) {
	p := Policy{Metadata: Metadata{Name: "x"}, ObjectFilters: []ObjectFilter{{Path: "/data", Exclude: []string{"["}}}}
	assert.Error(t, validatePolicy(p))
}
```

Replace the full contents of `src/cmd/policy-server/cache_test.go` with:

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

func TestCache_ReloadLoadsValidPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "b.json", `{"metadata": {"name": "policy-b"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	assert.Len(t, got, 2)
}

func TestCache_ReloadSkipsMalformedFileKeepsGoodOnes(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "good.json", `{"metadata": {"name": "policy-good"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "bad.json", `not json`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1)
	assert.Equal(t, "policy-good", got[0].Metadata.Name)
}

func TestCache_ReloadAllFilesFailKeepsPreviousCache(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "good.json", `{"metadata": {"name": "policy-good"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	require.Len(t, c.Policies(), 1)

	require.NoError(t, os.Remove(filepath.Join(dir, "backup", "good.json")))
	writePolicyFile(t, filepath.Join(dir, "backup"), "bad.json", `not json`)

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

func TestCache_ReloadTagsPoliciesWithSubfolderNameAsType(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "other"), "b.json", `{"metadata": {"name": "policy-b"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	types := map[string]string{}
	for _, p := range c.Policies() {
		types[p.Metadata.Name] = p.Type
	}
	assert.Equal(t, "backup", types["policy-a"])
	assert.Equal(t, "other", types["policy-b"], "an unrecognized subfolder name is still loaded and tagged with its literal name")
}

func TestCache_ReloadSkipsFileDirectlyUnderPoliciesDir(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "stray.json", `{"metadata": {"name": "stray"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1, "a *.json file with no type subfolder must not be loaded")
	assert.Equal(t, "policy-a", got[0].Metadata.Name)
}

func TestCache_PoliciesReturnsSnapshotCopy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{
		"metadata": {"name": "policy-a"},
		"client_filters": {
			"hostnames": ["host1", "host2"],
			"labels": {"env": "prod", "team": "platform"}
		},
		"object_filters": [{"path": "/data/*", "include": ["*.sql"], "exclude": ["*.tmp"]}],
		"rpo": "1h",
		"backup_window": ["08:00", "12:00"]
	}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	// Test mutation of plain value field (should not affect cache)
	got := c.Policies()
	got[0].Metadata.Name = "mutated-name"

	// Test mutation of nested slice field
	got[0].ClientFilters.Hostnames[0] = "mutated-host"

	// Test mutation of nested map field
	got[0].ClientFilters.Labels["env"] = "dev"

	// Test mutation of ObjectFilters slice
	got[0].ObjectFilters[0].Path = "/mutated/*"
	got[0].ObjectFilters[0].Include[0] = "mutated"
	got[0].ObjectFilters[0].Exclude[0] = "mutated"

	// Test mutation of BackupWindow slice
	got[0].BackupWindow[0] = "23:00"

	// Verify that a fresh call to Policies() returns the original values
	got2 := c.Policies()
	assert.Equal(t, "policy-a", got2[0].Metadata.Name, "mutating Metadata.Name in returned snapshot must not affect cache")
	assert.Equal(t, "host1", got2[0].ClientFilters.Hostnames[0], "mutating Hostnames in returned snapshot must not affect cache")
	assert.Equal(t, "prod", got2[0].ClientFilters.Labels["env"], "mutating Labels in returned snapshot must not affect cache")
	assert.Equal(t, "/data/*", got2[0].ObjectFilters[0].Path, "mutating ObjectFilters in returned snapshot must not affect cache")
	assert.Equal(t, "*.sql", got2[0].ObjectFilters[0].Include[0], "mutating ObjectFilters[].Include in returned snapshot must not affect cache")
	assert.Equal(t, "*.tmp", got2[0].ObjectFilters[0].Exclude[0], "mutating ObjectFilters[].Exclude in returned snapshot must not affect cache")
	assert.Equal(t, "08:00", got2[0].BackupWindow[0], "mutating BackupWindow in returned snapshot must not affect cache")
	assert.NotEmpty(t, got2[0].ObjectFilters[0].ID, "ObjectFilter.ID must survive the snapshot copy")
	assert.Equal(t, "backup", got2[0].Type, "Type must survive the snapshot copy")
}

func TestCache_FindByIDReturnsMatchingPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	want := c.Policies()[0]
	got, ok := c.FindByID(want.Metadata.ID)
	require.True(t, ok)
	assert.Equal(t, "policy-a", got.Metadata.Name)
	assert.Equal(t, filepath.Join(dir, "backup", "a.json"), got.SourcePath)
}

func TestCache_FindByIDUnknownIDReturnsFalse(t *testing.T) {
	c := NewCache()
	_, ok := c.FindByID("does-not-exist")
	assert.False(t, ok)
}

func TestCache_FindBySourcePathReturnsMatchingPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got, ok := c.FindBySourcePath(filepath.Join(dir, "backup", "a.json"))
	require.True(t, ok)
	assert.Equal(t, "policy-a", got.Metadata.Name)
}

func TestCache_FindBySourcePathUnknownPathReturnsFalse(t *testing.T) {
	c := NewCache()
	_, ok := c.FindBySourcePath("/does/not/exist.json")
	assert.False(t, ok)
}
```

Replace the full contents of `src/cmd/policy-server/server_test.go` with (identical to today except every `writePolicyFile(t, dir, "...json", ...)` now targets `filepath.Join(dir, "backup")`):

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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// attributeExtensionOID mirrors cmd/issuer/e2e_test.go's own copy -- the
// same private-use OID issuer embeds attributes under; small OID constants
// like this are duplicated per test file in this codebase rather than
// exported from common/mtls.
var attributeExtensionOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}

// peerCertContext builds a context carrying only a verified mTLS peer
// certificate (with attrs as its embedded extension) for hostname, with no
// gRPC metadata attached. fakeAuthContext (below) layers job-id metadata
// on top for the common case; TestGetPolicies_MissingJobIDRejected uses
// this directly to exercise the "no job-id metadata at all" path.
func peerCertContext(t *testing.T, hostname string, attrs map[string]string) context.Context {
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

// fakeAuthContext mirrors cmd/catalog/server_test.go's helper of the same
// name, plus job-id metadata every GetPolicies test needs by default now
// that it's required.
func fakeAuthContext(t *testing.T, hostname string, attrs map[string]string) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(peerCertContext(t, hostname, attrs), metadata.Pairs("job-id", "test-job-id"))
}

func newTestServerWithPolicies(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, dir, testLogger())
}

func TestGetPolicies_ReturnsOnlyMatchingPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "db.json", `{
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
	writePolicyFile(t, filepath.Join(dir, "backup"), "all.json", `{"metadata": {"name": "everyone"}}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "anything", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "everyone", resp.Policies[0].Name)
}

func TestGetPolicies_MatchesOnPeerCertLabels(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "db.json", `{
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

func TestGetPolicies_MissingJobIDRejected(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	_, err := srv.GetPolicies(peerCertContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	assert.Error(t, err)
}

func TestGetPolicies_ResponseFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "full.json", `{
		"metadata": {"name": "full-policy", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
		"object_filters": [{"path": "/var/www", "include": ["*.html"], "exclude": ["*.tmp"]}, {"path": "/etc"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	p := resp.Policies[0]
	assert.Equal(t, "full-policy", p.Name)
	assert.Equal(t, "24h", p.Rpo)
	assert.Equal(t, []string{"0 2 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
	require.Len(t, p.ObjectFilters, 2)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, []string{"*.html"}, p.ObjectFilters[0].Include)
	assert.Equal(t, []string{"*.tmp"}, p.ObjectFilters[0].Exclude)
	assert.Empty(t, p.ObjectFilters[1].Include)
	assert.Empty(t, p.ObjectFilters[1].Exclude)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.CreatedAt.AsTime())
	assert.Equal(t, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), p.UpdatedAt.AsTime())
	assert.NotEmpty(t, p.Id)
	assert.NotEmpty(t, p.ObjectFilters[0].Id)
	assert.NotEmpty(t, p.ObjectFilters[1].Id)
	assert.NotEqual(t, p.ObjectFilters[0].Id, p.ObjectFilters[1].Id)
}

func TestListPolicies_ReturnsAllPoliciesRegardlessOfIdentity(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "db.json", `{
		"metadata": {"name": "db-policy"},
		"client_filters": {"labels": {"role": "db"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Policies, 2)
}

func TestListPolicies_IncludesClientFilters(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, []string{"web-*"}, resp.Policies[0].ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, resp.Policies[0].ClientFilters.Labels)
}

func TestGetPolicies_StillOmitsClientFilters(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Nil(t, resp.Policies[0].ClientFilters)
}
```

Replace the full contents of `src/cmd/policy-server/write_test.go` with (the two `TestUniqueFilename_*` tests deliberately keep writing flat under `dir`, since they exercise `uniqueFilename` directly and not `Reload`; every other test's fixtures/assertions move under `backup/`):

```go
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func TestSlugify(t *testing.T) {
	assert.Equal(t, "nightly-db-backup", slugify("Nightly DB Backup!"))
	assert.Equal(t, "a-b-c", slugify("  a__b--c  "))
	assert.Equal(t, "", slugify("!!!"))
}

func TestUniqueFilename_ReturnsBaseWhenFree(t *testing.T) {
	dir := t.TempDir()
	got, err := uniqueFilename(dir, "nightly-db-backup")
	require.NoError(t, err)
	assert.Equal(t, "nightly-db-backup.json", got)
}

func TestUniqueFilename_AppendsSuffixOnCollision(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "nightly-db-backup.json", `{}`)
	got, err := uniqueFilename(dir, "nightly-db-backup")
	require.NoError(t, err)
	assert.Equal(t, "nightly-db-backup-2.json", got)
}

func TestUniqueFilename_SkipsMultipleCollisions(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "x.json", `{}`)
	writePolicyFile(t, dir, "x-2.json", `{}`)
	got, err := uniqueFilename(dir, "x")
	require.NoError(t, err)
	assert.Equal(t, "x-3.json", got)
}

func newTestWriteServer(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, dir, testLogger())
}

func TestCreatePolicy_WritesFileAndReturnsPolicyWithID(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "Nightly DB Backup",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/var/lib/postgres"}},
		Rpo:           "24h",
		BackupWindow:  []string{"0 2 * * *"},
		Destination:   "bwfs:8080",
	})

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Id)
	assert.Equal(t, "Nightly DB Backup", resp.Name)
	require.Len(t, resp.ObjectFilters, 1)
	assert.NotEmpty(t, resp.ObjectFilters[0].Id)

	data, err := os.ReadFile(filepath.Join(dir, "backup", "nightly-db-backup.json"))
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(data, &onDisk))
	assert.Equal(t, "Nightly DB Backup", onDisk["metadata"].(map[string]any)["name"])
}

func TestCreatePolicy_SecondCallWithSameNameGetsDistinctFile(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	req := &pb.CreatePolicyRequest{Name: "dup", Destination: "bwfs:8080"}
	first, err := srv.CreatePolicy(context.Background(), req)
	require.NoError(t, err)
	second, err := srv.CreatePolicy(context.Background(), req)
	require.NoError(t, err)

	assert.NotEqual(t, first.Id, second.Id)
	_, err = os.Stat(filepath.Join(dir, "backup", "dup.json"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "backup", "dup-2.json"))
	require.NoError(t, err)
}

func TestCreatePolicy_MissingNameReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_InvalidGlobPatternReturnsInvalidArgumentAndWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "broken",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/data", Include: []string{"["}}},
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no backup/ directory (and no file) should be written when validation fails")
}

// TestCreatePolicy_ConcurrentCreatesForDifferentNamesBothSurvive guards
// against a stale-reload race: gRPC dispatches each unary RPC to its own
// goroutine, so two CreatePolicy calls for two different policies can run
// concurrently against the same server. Without serializing the write RPCs
// against each other, one RPC's Reload could glob+parse a stale snapshot of
// the directory before the other RPC's write lands on disk, then win the
// lock-and-swap race and silently overwrite the cache with that stale
// snapshot -- reverting the other RPC's just-created policy from the
// in-memory cache even though its file is correctly on disk. This doesn't
// reliably reproduce the race mid-flight (that's inherently
// timing-dependent); it proves that with writeMu in place, two concurrent
// creates can no longer both succeed without both being visible in the
// final cache state.
func TestCreatePolicy_ConcurrentCreatesForDifferentNamesBothSurvive(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	names := []string{"policy-one", "policy-two"}
	var wg sync.WaitGroup
	errs := make([]error, len(names))
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
				Name:        name,
				Destination: "bwfs:8080",
			})
			errs[i] = err
		}(i, name)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "CreatePolicy for %q should succeed", names[i])
	}

	got := map[string]bool{}
	for _, p := range srv.cache.Policies() {
		got[p.Metadata.Name] = true
	}
	for _, name := range names {
		assert.True(t, got[name], "policy %q must be visible in cache after both concurrent creates complete", name)
	}
}

func TestCreatePolicy_ClientFiltersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "web",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
	})

	require.NoError(t, err)
	require.NotNil(t, resp.ClientFilters)
	assert.Equal(t, []string{"web-*"}, resp.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, resp.ClientFilters.Labels)
}

func TestUpdatePolicy_OverwritesFileKeepsIDAndCreatedAt(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{
		"metadata": {"name": "nightly", "created_at": "2026-07-01T00:00:00Z", "updated_at": "2026-07-01T00:00:00Z"},
		"object_filters": [{"path": "/old"}],
		"destination": "bwfs:8080"
	}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	resp, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:            original.Metadata.ID,
		Name:          "nightly-renamed",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/new"}},
		Destination:   "bwfs:9090",
	})

	require.NoError(t, err)
	assert.Equal(t, original.Metadata.ID, resp.Id, "id must stay stable across an update")
	assert.Equal(t, "nightly-renamed", resp.Name)
	assert.Equal(t, "bwfs:9090", resp.Destination)
	assert.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), resp.CreatedAt.AsTime())
	assert.NotEqual(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), resp.UpdatedAt.AsTime())
}

func TestUpdatePolicy_UnknownIDReturnsNotFound(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{Id: "does-not-exist", Name: "x"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestUpdatePolicy_InvalidInputReturnsInvalidArgumentLeavesFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	before, err := os.ReadFile(filepath.Join(dir, "backup", "nightly.json"))
	require.NoError(t, err)

	_, err = srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{Id: original.Metadata.ID, Name: ""})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	after, err := os.ReadFile(filepath.Join(dir, "backup", "nightly.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "file must be unchanged when validation fails")
}

func TestDeletePolicy_RemovesFileAndReloads(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: original.Metadata.ID})

	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "backup", "nightly.json"))
	assert.True(t, os.IsNotExist(err))
	assert.Empty(t, srv.cache.Policies())
}

func TestDeletePolicy_UnknownIDReturnsNotFound(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: "does-not-exist"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestDeletePolicy_LeavesOtherPoliciesIntact(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "b.json", `{"metadata": {"name": "policy-b"}}`)
	srv := newTestWriteServer(t, dir)
	var target Policy
	for _, p := range srv.cache.Policies() {
		if p.Metadata.Name == "policy-a" {
			target = p
		}
	}

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: target.Metadata.ID})

	require.NoError(t, err)
	remaining := srv.cache.Policies()
	require.Len(t, remaining, 1)
	assert.Equal(t, "policy-b", remaining[0].Metadata.Name)
}
```

Replace the full contents of `src/cmd/policy-server/watch_test.go` with:

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
	time.Sleep(50 * time.Millisecond)

	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".changed"), []byte("1"), 0o644))

	require.Eventually(t, func() bool {
		return len(c.Policies()) == 1
	}, 2*time.Second, 10*time.Millisecond, "cache should reload after .changed is written")
}

func TestWatchForReload_ReloadsOnTouchOfExistingChangedFile(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	require.Empty(t, c.Policies())

	changedPath := filepath.Join(dir, ".changed")
	require.NoError(t, os.WriteFile(changedPath, []byte("1"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchForReload(ctx, dir, c, testLogger())
	time.Sleep(50 * time.Millisecond)

	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	// Simulate `touch` on an already-existing file: an mtime-only update,
	// which Linux inotify reports as IN_ATTRIB (fsnotify's Chmod op), not
	// Write or Create.
	now := time.Now()
	require.NoError(t, os.Chtimes(changedPath, now, now))

	require.Eventually(t, func() bool {
		return len(c.Policies()) == 1
	}, 2*time.Second, 10*time.Millisecond, "cache should reload after touch of an already-existing .changed file")
}

func TestWatchForReload_IgnoresOtherFileWrites(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchForReload(ctx, dir, c, testLogger())

	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, c.Policies(), "reload must not fire without a write to .changed")
}
```

- [ ] **Step 2: Run the package tests to confirm they fail (implementation not changed yet)**

Run:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/policy-server/... 2>&1 | head -40
```
Expected: compile errors (`parsePolicyFile(path)` — not enough arguments; `p.Type`/`got.Type` — undefined field) — confirms the tests are exercising code that doesn't exist yet.

- [ ] **Step 3: Update `policy.go`**

In `src/cmd/policy-server/policy.go`, change the top-of-file comment and the `Policy` struct:

Old:
```go
// policy-server's on-disk policy schema: one JSON file per policy under
// $MP_CONFIG_PATH/policies/. See
// docs/superpowers/specs/2026-07-10-policy-server-design.md.
package main
```
New:
```go
// policy-server's on-disk policy schema: one JSON file per policy under
// $MP_CONFIG_PATH/policies/<type>/ (e.g. policies/backup/). See
// docs/superpowers/specs/2026-07-10-policy-server-design.md and
// docs/superpowers/specs/2026-07-20-policy-type-subfolders-design.md.
package main
```

Old:
```go
type Policy struct {
	Metadata      Metadata       `json:"metadata"`
	ClientFilters ClientFilters  `json:"client_filters"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	SourcePath    string         `json:"-"`
}
```
New:
```go
type Policy struct {
	Metadata      Metadata       `json:"metadata"`
	ClientFilters ClientFilters  `json:"client_filters"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	SourcePath    string         `json:"-"`
	// Type is derived from the name of the immediate subfolder the policy
	// file was loaded from (e.g. "backup" for policies/backup/*.json) --
	// never read from or written to the on-disk policy JSON. Set by
	// parsePolicyFile.
	Type string `json:"-"`
}
```

Old:
```go
// parsePolicyFile reads and validates a single policy JSON file -- see
// validatePolicy for the validation rules applied.
func parsePolicyFile(filePath string) (Policy, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Policy{}, fmt.Errorf("read %s: %w", filePath, err)
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("parse %s: %w", filePath, err)
	}
	if err := validatePolicy(p); err != nil {
		return Policy{}, fmt.Errorf("%s: %w", filePath, err)
	}

	policyUUID := uuid.NewSHA1(policyIDNamespace, []byte(filepath.Base(filePath)))
	p.Metadata.ID = policyUUID.String()
	p.SourcePath = filePath
	for i := range p.ObjectFilters {
		p.ObjectFilters[i].ID = uuid.NewSHA1(policyUUID, []byte(strconv.Itoa(i))).String()
	}

	return p, nil
}
```
New:
```go
// parsePolicyFile reads and validates a single policy JSON file, tagging it
// with policyType -- the caller's own knowledge of which type subfolder
// filePath was found in (see Cache.Reload) -- see validatePolicy for the
// validation rules applied.
func parsePolicyFile(filePath, policyType string) (Policy, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Policy{}, fmt.Errorf("read %s: %w", filePath, err)
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("parse %s: %w", filePath, err)
	}
	if err := validatePolicy(p); err != nil {
		return Policy{}, fmt.Errorf("%s: %w", filePath, err)
	}

	policyUUID := uuid.NewSHA1(policyIDNamespace, []byte(filepath.Base(filePath)))
	p.Metadata.ID = policyUUID.String()
	p.SourcePath = filePath
	p.Type = policyType
	for i := range p.ObjectFilters {
		p.ObjectFilters[i].ID = uuid.NewSHA1(policyUUID, []byte(strconv.Itoa(i))).String()
	}

	return p, nil
}
```

- [ ] **Step 4: Rewrite `Cache.Reload` and `Cache.Policies` in `cache.go`**

Old imports:
```go
import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
)
```
New imports:
```go
import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)
```

Old `Reload`:
```go
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
New `Reload`:
```go
// Reload re-reads every *.json file found one level under dir -- i.e.
// dir/<type>/*.json for every immediate subdirectory <type> of dir --
// tagging each loaded policy with that subdirectory's name as its Type. A
// *.json file sitting directly under dir, outside any type subfolder, is
// logged and skipped, the same as a malformed file -- it doesn't block the
// rest of the directory from loading. Reload does not validate subfolder
// names against a whitelist of known types; an unrecognized subfolder is
// still loaded and tagged with its literal name -- deciding what an
// unrecognized type means is left to downstream consumers (agent today).
//
// If dir contains at least one *.json file (anywhere: stray or in a type
// subfolder) and every loadable one failed to parse, the previous good
// cache is left in place (an error is returned) rather than swapped to an
// empty list -- an empty, or entirely subfolder-less, policies/ directory
// is a valid "no policies" state, but a reload that produced zero
// successes out of one-or-more attempts is treated as a failed reload, not
// an intentional empty state.
func (c *Cache) Reload(dir string, logger *slog.Logger) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}

	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			logger.Warn("skipping policy file with no type subfolder", "path", filepath.Join(dir, e.Name()))
		}
	}

	type candidate struct {
		path       string
		policyType string
	}
	var candidates []candidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subdir := filepath.Join(dir, e.Name())
		matches, err := filepath.Glob(filepath.Join(subdir, "*.json"))
		if err != nil {
			return fmt.Errorf("list policy files in %s: %w", subdir, err)
		}
		for _, m := range matches {
			candidates = append(candidates, candidate{path: m, policyType: e.Name()})
		}
	}

	loaded := make([]Policy, 0, len(candidates))
	for _, cd := range candidates {
		p, err := parsePolicyFile(cd.path, cd.policyType)
		if err != nil {
			logger.Error("skipping malformed policy file", "path", cd.path, "error", err)
			continue
		}
		loaded = append(loaded, p)
	}

	if len(candidates) > 0 && len(loaded) == 0 {
		return fmt.Errorf("reload of %s: all %d policy files failed to parse, keeping previous cache", dir, len(candidates))
	}

	c.mu.Lock()
	c.policies = loaded
	c.mu.Unlock()
	return nil
}
```

Old `Policies` struct-literal (inside the existing loop, unchanged surrounding code):
```go
			out[i] = Policy{
				Metadata: p.Metadata, // plain types: string, time.Time, time.Time
				ClientFilters: ClientFilters{
					Hostnames: make([]string, len(p.ClientFilters.Hostnames)),
					Labels:    make(map[string]string, len(p.ClientFilters.Labels)),
				},
				ObjectFilters: make([]ObjectFilter, len(p.ObjectFilters)),
				RPO:           p.RPO, // plain string
				BackupWindow:  make([]string, len(p.BackupWindow)),
				Destination:   p.Destination, // plain string
				SourcePath:    p.SourcePath,  // plain string
			}
```
New:
```go
			out[i] = Policy{
				Metadata: p.Metadata, // plain types: string, time.Time, time.Time
				ClientFilters: ClientFilters{
					Hostnames: make([]string, len(p.ClientFilters.Hostnames)),
					Labels:    make(map[string]string, len(p.ClientFilters.Labels)),
				},
				ObjectFilters: make([]ObjectFilter, len(p.ObjectFilters)),
				RPO:           p.RPO, // plain string
				BackupWindow:  make([]string, len(p.BackupWindow)),
				Destination:   p.Destination, // plain string
				SourcePath:    p.SourcePath,  // plain string
				Type:          p.Type,        // plain string
			}
```

- [ ] **Step 5: Update `CreatePolicy` in `write.go` to write into `policies/backup/`**

Old:
```go
	slug := slugify(p.Metadata.Name)
	if slug == "" {
		return nil, status.Error(codes.InvalidArgument, "name must contain at least one alphanumeric character")
	}
	filename, err := uniqueFilename(s.policiesDir, slug)
	if err != nil {
		s.logger.Error("CreatePolicy: filename allocation failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to allocate a policy filename")
	}
	filePath := filepath.Join(s.policiesDir, filename)
```
New:
```go
	slug := slugify(p.Metadata.Name)
	if slug == "" {
		return nil, status.Error(codes.InvalidArgument, "name must contain at least one alphanumeric character")
	}

	// Every policy created through this RPC is type "backup" -- the only
	// type that exists today. A future second type needs its own creation
	// path once it exists; see
	// docs/superpowers/specs/2026-07-20-policy-type-subfolders-design.md.
	backupDir := filepath.Join(s.policiesDir, "backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		s.logger.Error("CreatePolicy: failed to create backup policies directory", "path", backupDir, "error", err)
		return nil, status.Error(codes.Internal, "failed to create policy type directory")
	}
	filename, err := uniqueFilename(backupDir, slug)
	if err != nil {
		s.logger.Error("CreatePolicy: filename allocation failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to allocate a policy filename")
	}
	filePath := filepath.Join(backupDir, filename)
```

Also update the doc comment immediately above `CreatePolicy`:

Old:
```go
// CreatePolicy validates req, allocates a filename from a slug of the
// policy's name (appending "-2", "-3", ... on collision), and atomically
// writes the new policy file before reloading the cache. The filename it
// picks is permanent for that policy's lifetime -- it's what the policy's
// id derives from.
```
New:
```go
// CreatePolicy validates req, allocates a filename from a slug of the
// policy's name (appending "-2", "-3", ... on collision), and atomically
// writes the new policy file into policies/backup/ (the only policy type
// this RPC creates today) before reloading the cache. The filename it
// picks is permanent for that policy's lifetime -- it's what the policy's
// id derives from.
```

- [ ] **Step 6: Update the top-of-file doc comment in `main.go`**

Old:
```go
// policy-server serves backup policies -- static, operator-maintained JSON
// files under $MP_CONFIG_PATH/policies/ -- filtered to whatever the
// requesting client's verified hostname and certificate-embedded attribute
// labels match. It is bootstrapped and certificate-managed exactly like any
```
New:
```go
// policy-server serves backup policies -- static, operator-maintained JSON
// files under $MP_CONFIG_PATH/policies/backup/ -- filtered to whatever the
// requesting client's verified hostname and certificate-embedded attribute
// labels match. It is bootstrapped and certificate-managed exactly like any
```

- [ ] **Step 7: Run the full policy-server package test suite**

Run:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/policy-server/... -v 2>&1 | tail -80
```
Expected: `ok` — every test (old, relocated, and the two new `TestCache_Reload...` tests) passes.

- [ ] **Step 8: Build the whole module**

Run:
```bash
cd /home/alex/miniprotector/src && go build ./...
```
Expected: exits 0, no output.

- [ ] **Step 9: Commit**

```bash
git add src/cmd/policy-server/policy.go src/cmd/policy-server/cache.go src/cmd/policy-server/write.go src/cmd/policy-server/main.go src/cmd/policy-server/policy_test.go src/cmd/policy-server/cache_test.go src/cmd/policy-server/server_test.go src/cmd/policy-server/write_test.go src/cmd/policy-server/watch_test.go
git commit -m "feat(policy-server): derive policy type from subfolder, load from policies/<type>/"
```

---

## Task 3: policy-server — propagate Type into gRPC responses

**Files:**
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/server_test.go`
- Modify: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: `pb.Policy.Type` (Task 1), `Policy.Type` (Task 2).
- Produces: `pb.Policy.GetType()` populated on every `GetPolicies`/`ListPolicies`/`CreatePolicy`/`UpdatePolicy` response — consumed by Task 4 (`policyclient`, via the wire, not by import).

- [ ] **Step 1: Write the failing tests**

In `src/cmd/policy-server/server_test.go`, append:

```go
func TestGetPolicies_ResponseIncludesType(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "backup", resp.Policies[0].Type)
}

func TestListPolicies_ResponseIncludesType(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "backup", resp.Policies[0].Type)
}
```

In `src/cmd/policy-server/write_test.go`, append:

```go
func TestCreatePolicy_ResponseIncludesBackupType(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{Name: "nightly"})

	require.NoError(t, err)
	assert.Equal(t, "backup", resp.Type)
}
```

- [ ] **Step 2: Run to verify failure**

Run:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/policy-server/... -run 'TestGetPolicies_ResponseIncludesType|TestListPolicies_ResponseIncludesType|TestCreatePolicy_ResponseIncludesBackupType' -v
```
Expected: FAIL — `resp.Policies[0].Type`/`resp.Type` is `""`, not `"backup"`.

- [ ] **Step 3: Wire `Type` into `toProtoPolicy`**

In `src/cmd/policy-server/server.go`:

Old:
```go
func toProtoPolicy(p Policy) *pb.Policy {
	objectFilters := make([]*pb.ObjectFilter, len(p.ObjectFilters))
	for i, f := range p.ObjectFilters {
		objectFilters[i] = &pb.ObjectFilter{Id: f.ID, Path: f.Path, Include: f.Include, Exclude: f.Exclude}
	}
	return &pb.Policy{
		Id:            p.Metadata.ID,
		Name:          p.Metadata.Name,
		CreatedAt:     timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:     timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters: objectFilters,
		Rpo:           p.RPO,
		BackupWindow:  p.BackupWindow,
		Destination:   p.Destination,
	}
}
```
New:
```go
func toProtoPolicy(p Policy) *pb.Policy {
	objectFilters := make([]*pb.ObjectFilter, len(p.ObjectFilters))
	for i, f := range p.ObjectFilters {
		objectFilters[i] = &pb.ObjectFilter{Id: f.ID, Path: f.Path, Include: f.Include, Exclude: f.Exclude}
	}
	return &pb.Policy{
		Id:            p.Metadata.ID,
		Name:          p.Metadata.Name,
		CreatedAt:     timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:     timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters: objectFilters,
		Rpo:           p.RPO,
		BackupWindow:  p.BackupWindow,
		Destination:   p.Destination,
		Type:          p.Type,
	}
}
```

`toProtoPolicyAdmin` calls `toProtoPolicy` and only adds `client_filters` on top, so `CreatePolicy`/`UpdatePolicy`/`ListPolicies` (all three go through `toProtoPolicyAdmin`) pick up `Type` automatically — no separate change needed there.

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/policy-server/... -v 2>&1 | tail -30
```
Expected: `ok` — all tests pass, including the three new ones.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go src/cmd/policy-server/write_test.go
git commit -m "feat(policy-server): include type in GetPolicies/ListPolicies/CreatePolicy/UpdatePolicy responses"
```

---

## Task 4: policyclient — cache Type passthrough

**Files:**
- Modify: `src/cmd/policyclient/fetch.go`
- Modify: `src/cmd/policyclient/fetch_test.go`

**Interfaces:**
- Consumes: `pb.Policy.GetType()` (Task 3).
- Produces: `CachedPolicy.Type string` (json field `"type"` in `policies-cache.json`) — consumed by Task 5 (`agent`).

- [ ] **Step 1: Write the failing test**

In `src/cmd/policyclient/fetch_test.go`, modify `TestRunFetch_Success_WritesCacheFile`'s fake response and assertions:

Old:
```go
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Id:        "policy-uuid-123",
				Name:      "daily-db-backup",
				CreatedAt: created,
				UpdatedAt: updated,
				ObjectFilters: []*pb.ObjectFilter{
					{Id: "filter-uuid-1", Path: "/var/lib/postgres", Include: []string{"*.sql"}},
					{Id: "filter-uuid-2", Path: "/etc/postgres", Exclude: []string{"*.bak"}},
				},
				Rpo:          "24h",
				BackupWindow: []string{"0 2 * * *"},
				Destination:  "bwfs-east.internal:8080",
			},
		},
	}}
```
New:
```go
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Id:        "policy-uuid-123",
				Name:      "daily-db-backup",
				CreatedAt: created,
				UpdatedAt: updated,
				ObjectFilters: []*pb.ObjectFilter{
					{Id: "filter-uuid-1", Path: "/var/lib/postgres", Include: []string{"*.sql"}},
					{Id: "filter-uuid-2", Path: "/etc/postgres", Exclude: []string{"*.bak"}},
				},
				Rpo:          "24h",
				BackupWindow: []string{"0 2 * * *"},
				Destination:  "bwfs-east.internal:8080",
				Type:         "backup",
			},
		},
	}}
```

Old assertions block end:
```go
	assert.Equal(t, "24h", got[0].RPO)
	assert.Equal(t, []string{"0 2 * * *"}, got[0].BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", got[0].Destination)
}
```
New:
```go
	assert.Equal(t, "24h", got[0].RPO)
	assert.Equal(t, []string{"0 2 * * *"}, got[0].BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", got[0].Destination)
	assert.Equal(t, "backup", got[0].Type)
}
```

- [ ] **Step 2: Run to verify failure**

Run:
```bash
cd /home/alex/miniprotector/src && go build ./... 2>&1 | head -20
```
Expected: compile error — `pb.Policy` has no field `Type`... wait, Task 1 already added it, so this should instead be a `CachedPolicy` compile error:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/policyclient/... -run TestRunFetch_Success_WritesCacheFile -v
```
Expected: FAIL — `got[0].Type` undefined (field doesn't exist on `CachedPolicy` yet).

- [ ] **Step 3: Add `Type` to `CachedPolicy` and `toCachedPolicies`**

In `src/cmd/policyclient/fetch.go`:

Old:
```go
// CachedPolicy is the on-disk representation of one policy-server Policy --
// the same fields the GetPolicies RPC response already defines, converted
// directly from the protobuf message.
type CachedPolicy struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
}
```
New:
```go
// CachedPolicy is the on-disk representation of one policy-server Policy --
// the same fields the GetPolicies RPC response already defines, converted
// directly from the protobuf message.
type CachedPolicy struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	// Derived by policy-server from the subfolder the policy file was
	// loaded from (e.g. "backup"). Pure passthrough here -- policyclient
	// itself never branches on it; agent does (see
	// cmd/agent/backup.go's backupTasks).
	Type string `json:"type"`
}
```

Old (inside `toCachedPolicies`):
```go
		out = append(out, CachedPolicy{
			ID:            p.GetId(),
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
			Destination:   p.GetDestination(),
		})
```
New:
```go
		out = append(out, CachedPolicy{
			ID:            p.GetId(),
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
			Destination:   p.GetDestination(),
			Type:          p.GetType(),
		})
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/policyclient/... -v 2>&1 | tail -30
```
Expected: `ok`.

- [ ] **Step 5: Build the whole module**

Run:
```bash
cd /home/alex/miniprotector/src && go build ./...
```
Expected: exits 0.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go
git commit -m "feat(policyclient): pass policy type through into policies-cache.json"
```

---

## Task 5: agent — filter backup tasks to type "backup"

**Files:**
- Modify: `src/cmd/agent/backup.go`
- Modify (full rewrite): `src/cmd/agent/backup_test.go`

**Interfaces:**
- Consumes: `CachedPolicy.Type` on-disk field (Task 4) — `agent` reads its own duplicated `cachedPolicy` struct from `policies-cache.json`, not `policyclient`'s Go type (per the existing "agent can't import another command's main package" constraint already documented in `backup.go`).
- Produces: `backupTasks()` now skips any cached policy whose `Type != "backup"`.

Every existing `backupTasks` test writes a JSON fixture (or, in one case, a `cachedPolicy{}` struct literal) with no `"type"` field. Once the skip is added, an absent/empty `Type` no longer equals `"backup"`, so **every existing fixture must gain `"type": "backup"`** in the same commit — otherwise every existing test would start failing (or, worse, start passing for the wrong reason: "zero tasks" becomes ambiguous between "type mismatch" and whatever behavior the test actually intended to exercise, e.g. an unparseable `rpo`). This is why the whole file is rewritten in one step rather than patched incrementally.

- [ ] **Step 1: Write the new/changed tests**

Replace the full contents of `src/cmd/agent/backup_test.go` with:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCachedPolicies(t *testing.T, dir, json string) string {
	t.Helper()
	path := filepath.Join(dir, "policies-cache.json")
	require.NoError(t, os.WriteFile(path, []byte(json), 0o644))
	return path
}

func TestShortID_TruncatesToEightHexCharsAfterStrippingDashes(t *testing.T) {
	assert.Equal(t, "aaaaaaaa", shortID("aaaaaaaa-1111-1111-1111-111111111111"))
}

func TestShortID_EmptyInputReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", shortID(""))
}

func TestShortID_ShorterThanEightCharsReturnedUnchanged(t *testing.T) {
	assert.Equal(t, "abcd", shortID("ab-cd"))
}

func TestWindowOpen_TriggerJustInsideGraceReportsOpen(t *testing.T) {
	sched, err := cron.ParseStandard("0 2 * * *") // fires 02:00 daily
	require.NoError(t, err)
	now := time.Date(2026, 7, 4, 2, 30, 0, 0, time.UTC) // 30 min after trigger
	assert.True(t, windowOpen([]cron.Schedule{sched}, now, time.Hour))
}

func TestWindowOpen_TriggerJustOutsideGraceReportsClosed(t *testing.T) {
	sched, err := cron.ParseStandard("0 2 * * *")
	require.NoError(t, err)
	now := time.Date(2026, 7, 4, 3, 30, 0, 0, time.UTC) // 90 min after trigger
	assert.False(t, windowOpen([]cron.Schedule{sched}, now, time.Hour))
}

func TestWindowOpen_OneOfMultipleSchedulesRecentlyTriggeredStillOpen(t *testing.T) {
	morning, err := cron.ParseStandard("0 2 * * *")
	require.NoError(t, err)
	evening, err := cron.ParseStandard("0 20 * * *")
	require.NoError(t, err)
	now := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC) // just after the morning slot only
	assert.True(t, windowOpen([]cron.Schedule{morning, evening}, now, time.Hour))
}

func TestRpoElapsed_NeverSucceededIsElapsed(t *testing.T) {
	assert.True(t, rpoElapsed(PolicyState{}, time.Now(), time.Hour))
}

func TestRpoElapsed_RecentSuccessIsNotElapsed(t *testing.T) {
	now := time.Now()
	last := now.Add(-10 * time.Minute)
	assert.False(t, rpoElapsed(PolicyState{LastSuccessAt: &last}, now, time.Hour))
}

func TestRpoElapsed_OldSuccessIsElapsed(t *testing.T) {
	now := time.Now()
	last := now.Add(-2 * time.Hour)
	assert.True(t, rpoElapsed(PolicyState{LastSuccessAt: &last}, now, time.Hour))
}

func TestReadCachedPolicies_MissingFileReturnsOkFalse(t *testing.T) {
	dir := t.TempDir()
	policies, ok := readCachedPolicies(filepath.Join(dir, "does-not-exist.json"))
	assert.False(t, ok)
	assert.Empty(t, policies)
}

func TestReadCachedPolicies_CorruptFileReturnsOkFalse(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `not json`)
	policies, ok := readCachedPolicies(path)
	assert.False(t, ok)
	assert.Empty(t, policies)
}

func TestReadCachedPolicies_ValidEmptyListReturnsOkTrue(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[]`)
	policies, ok := readCachedPolicies(path)
	assert.True(t, ok)
	assert.Empty(t, policies)
}

func TestBackupTasks_OnePolicyWithTwoPathsYieldsTwoTasksWithStableDistinctIDs(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "daily-db-backup",
		"type": "backup",
		"object_filters": [
			{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/var/lib/postgres"},
			{"id": "bbbbbbbb-2222-2222-2222-222222222222", "path": "/etc/postgres"}
		],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 2)
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "backup:daily-db-backup:/var/lib/postgres:aaaaaaaa")
	assert.Contains(t, ids, "backup:daily-db-backup:/etc/postgres:bbbbbbbb")
	assert.NotEqual(t, tasks[0].ID, tasks[1].ID)
}

func TestBackupTasks_ObjectFiltersSharingPathGetDistinctTaskIDs(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "web-policy",
		"type": "backup",
		"object_filters": [
			{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/var/www", "include": ["*.html"]},
			{"id": "bbbbbbbb-2222-2222-2222-222222222222", "path": "/var/www", "exclude": ["*.log"]}
		],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 2)
	assert.NotEqual(t, tasks[0].ID, tasks[1].ID, "two object filters sharing a path must get distinct task IDs")
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "backup:web-policy:/var/www:aaaaaaaa")
	assert.Contains(t, ids, "backup:web-policy:/var/www:bbbbbbbb")
}

func TestBackupTasks_TaskArgsMatchBrfsShape(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "daily-db-backup",
		"type": "backup",
		"object_filters": [{"path": "/var/lib/postgres"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 1)
	task := tasks[0]
	assert.Equal(t, "brfs", task.Binary)
	require.Len(t, task.Args, 5)
	assert.Equal(t, "/var/lib/postgres", task.Args[0])
	assert.Equal(t, "--destination", task.Args[1])
	assert.Equal(t, "bwfs-east:8080", task.Args[2])
	assert.Equal(t, "--job-id", task.Args[3])
	assert.Contains(t, task.Args[4], "backup:daily-db-backup:var-lib-postgres:")
	assert.True(t, task.Background)
}

func TestBackupTasks_DueRequiresBothWindowOpenAndRpoElapsed(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "backup",
		"object_filters": [{"path": "/data"}],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	require.True(t, ok)
	require.Len(t, tasks, 1)
	task := tasks[0]

	windowOpenTime := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC)
	windowClosedTime := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	recent := windowOpenTime.Add(-10 * time.Minute)
	old := windowOpenTime.Add(-2 * time.Hour)

	assert.False(t, task.Due(PolicyState{LastSuccessAt: &recent}, windowOpenTime), "window open but RPO not elapsed: not due")
	assert.False(t, task.Due(PolicyState{LastSuccessAt: &old}, windowClosedTime), "RPO elapsed but window closed: not due")
	assert.True(t, task.Due(PolicyState{LastSuccessAt: &old}, windowOpenTime), "both true: due")
	assert.True(t, task.Due(PolicyState{}, windowOpenTime), "never run and window open: due")
}

func TestBackupTasks_PerPathIndependence(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "backup",
		"object_filters": [
			{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/a"},
			{"id": "bbbbbbbb-2222-2222-2222-222222222222", "path": "/b"}
		],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	require.True(t, ok)
	require.Len(t, tasks, 2)

	windowOpenTime := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC)
	recent := windowOpenTime.Add(-10 * time.Minute)

	var taskA, taskB Policy
	for _, task := range tasks {
		if task.ID == "backup:p:/a:aaaaaaaa" {
			taskA = task
		} else {
			taskB = task
		}
	}
	// /a recently succeeded (not due); /b never ran (due) -- proves one
	// path's state has no effect on its sibling's due-check.
	assert.False(t, taskA.Due(PolicyState{LastSuccessAt: &recent}, windowOpenTime))
	assert.True(t, taskB.Due(PolicyState{}, windowOpenTime))
}

func TestBackupTasks_UnparseableRpoSkipsPolicyEntirely(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "backup",
		"object_filters": [{"path": "/data"}],
		"rpo": "not-a-duration",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	assert.True(t, ok, "the file itself was still validly read")
	assert.Empty(t, tasks)
}

func TestBackupTasks_NoValidBackupWindowSkipsPolicyEntirely(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "backup",
		"object_filters": [{"path": "/data"}],
		"rpo": "1h",
		"backup_window": ["not a cron expression"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	assert.True(t, ok)
	assert.Empty(t, tasks)
}

func TestBackupTasks_NonBackupTypeSkipsPolicyEntirely(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "restore",
		"object_filters": [{"path": "/data"}],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	assert.True(t, ok, "the file itself was still validly read")
	assert.Empty(t, tasks, "a cached policy whose type isn't \"backup\" must contribute zero tasks")
}

func TestBackupTasks_MixedTypesOnlyBackupTypeProducesTasks(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[
		{
			"name": "backup-policy",
			"type": "backup",
			"object_filters": [{"path": "/data"}],
			"rpo": "1h",
			"backup_window": ["0 2 * * *"],
			"destination": "bwfs:8080"
		},
		{
			"name": "other-policy",
			"type": "restore",
			"object_filters": [{"path": "/other"}],
			"rpo": "1h",
			"backup_window": ["0 2 * * *"],
			"destination": "bwfs:8080"
		}
	]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Contains(t, tasks[0].ID, "backup-policy")
}

func TestBackupTasks_MissingCacheFileReturnsOkFalseWithNoTasks(t *testing.T) {
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(filepath.Join(t.TempDir(), "does-not-exist.json"), conf)
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestBackupTasks_CorruptCacheFileReturnsOkFalseWithNoTasks(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `not json`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestBackupTasks_JobIDFieldMatchesArgsFlag(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	cached := []cachedPolicy{{
		Name:          "web-policy",
		Type:          "backup",
		ObjectFilters: []ObjectFilter{{Path: "/srv/web"}},
		RPO:           "1h",
		BackupWindow:  []string{"* * * * *"},
		Destination:   "bwfs:9000",
	}}
	data, err := json.Marshal(cached)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cachePath, data, 0o644))

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(cachePath, conf)
	require.True(t, ok)
	require.Len(t, tasks, 1)

	task := tasks[0]
	assert.NotEmpty(t, task.JobID)
	assert.Contains(t, task.Args, "--job-id")
	assert.Contains(t, task.Args, task.JobID)
}

func TestBackupTasks_RemovedPolicyStopsBeingDerived(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	conf := &config.Config{BackupWindowGraceSec: 3600}

	require.NoError(t, os.WriteFile(cachePath, []byte(`[{
		"name": "p", "type": "backup", "object_filters": [{"path": "/data"}], "rpo": "1h",
		"backup_window": ["0 2 * * *"], "destination": "bwfs:8080"
	}]`), 0o644))
	tasks, ok := backupTasks(cachePath, conf)
	require.True(t, ok)
	require.Len(t, tasks, 1)

	require.NoError(t, os.WriteFile(cachePath, []byte(`[]`), 0o644))
	tasks, ok = backupTasks(cachePath, conf)
	assert.True(t, ok, "an empty-but-valid file is still a confirmed-good read")
	assert.Empty(t, tasks)
}

func TestBackupTasks_TaskArgsIncludeIncludeExcludeFlagsWhenPresent(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "web-policy",
		"type": "backup",
		"object_filters": [{"path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 1)
	task := tasks[0]
	require.Len(t, task.Args, 9)
	assert.Equal(t, "/var/www", task.Args[0])
	assert.Equal(t, "--destination", task.Args[1])
	assert.Equal(t, "bwfs:8080", task.Args[2])
	assert.Equal(t, "--job-id", task.Args[3])
	assert.Equal(t, "--include", task.Args[5])
	assert.Equal(t, "*.html,*.css", task.Args[6])
	assert.Equal(t, "--exclude", task.Args[7])
	assert.Equal(t, "*.tmp", task.Args[8])
}
```

- [ ] **Step 2: Run to verify failure**

Run:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/agent/... -run TestBackupTasks -v 2>&1 | tail -40
```
Expected: compile error (`cachedPolicy{... Type: "backup" ...}` — unknown field `Type`) — confirms the test file exercises a field that doesn't exist yet.

- [ ] **Step 3: Add `Type` to `cachedPolicy` and the skip check**

In `src/cmd/agent/backup.go`:

Old:
```go
// cachedPolicy mirrors the subset of policyclient's on-disk CachedPolicy
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type cachedPolicy struct {
	Name          string         `json:"name"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
}
```
New:
```go
// cachedPolicy mirrors the subset of policyclient's on-disk CachedPolicy
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type cachedPolicy struct {
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
}
```

Old (inside `backupTasks`'s loop):
```go
	var tasks []Policy
	for _, p := range cachedPolicies {
		rpo, err := time.ParseDuration(p.RPO)
		if err != nil {
			continue
		}
		schedules := parseSchedules(p.BackupWindow)
		if len(schedules) == 0 {
			continue
		}
```
New:
```go
	var tasks []Policy
	for _, p := range cachedPolicies {
		// Only type "backup" policies become backup tasks -- a future
		// non-backup type simply contributes zero tasks here, the same
		// fail-safe direction as the unparseable-rpo/no-backup_window
		// skips below: no sound backup task can be built for a policy
		// this loop doesn't understand how to interpret.
		if p.Type != "backup" {
			continue
		}
		rpo, err := time.ParseDuration(p.RPO)
		if err != nil {
			continue
		}
		schedules := parseSchedules(p.BackupWindow)
		if len(schedules) == 0 {
			continue
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/agent/... -v 2>&1 | tail -60
```
Expected: `ok` — all `backup_test.go` tests pass, including the two new type-filtering tests.

- [ ] **Step 5: Build the whole module**

Run:
```bash
cd /home/alex/miniprotector/src && go build ./...
```
Expected: exits 0.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/agent/backup.go src/cmd/agent/backup_test.go
git commit -m "feat(agent): only derive backup tasks from cached policies of type backup"
```

---

## Task 6: api-server — pass Type through to the REST DTO

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/policies_test.go`

**Interfaces:**
- Consumes: `pb.Policy.GetType()` (Task 3).
- Produces: `policyDTO.Type` (JSON field `"type"` in `/api/v1/policies` responses).

- [ ] **Step 1: Write the failing test**

In `src/cmd/api-server/policies_test.go`, modify `TestToPolicyDTO_ConvertsTimestampsToUnixSecondsAndClientFilters`:

Old:
```go
func TestToPolicyDTO_ConvertsTimestampsToUnixSecondsAndClientFilters(t *testing.T) {
	p := &pb.Policy{
		Id:            "p1",
		Name:          "nightly",
		CreatedAt:     timestamppb.New(time.Unix(1752400000, 0)),
		UpdatedAt:     timestamppb.New(time.Unix(1752400010, 0)),
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
		ObjectFilters: []*pb.ObjectFilter{{Id: "f1", Path: "/data", Include: []string{"*.sql"}}},
		Rpo:           "24h",
		BackupWindow:  []string{"0 2 * * *"},
		Destination:   "bwfs:8080",
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, int64(1752400000), dto.CreatedAt)
	assert.Equal(t, int64(1752400010), dto.UpdatedAt)
	assert.Equal(t, []string{"web-*"}, dto.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, dto.ClientFilters.Labels)
	require.Len(t, dto.ObjectFilters, 1)
	assert.Equal(t, "f1", dto.ObjectFilters[0].ID)
	assert.Equal(t, "/data", dto.ObjectFilters[0].Path)
}
```
New:
```go
func TestToPolicyDTO_ConvertsTimestampsToUnixSecondsAndClientFilters(t *testing.T) {
	p := &pb.Policy{
		Id:            "p1",
		Name:          "nightly",
		CreatedAt:     timestamppb.New(time.Unix(1752400000, 0)),
		UpdatedAt:     timestamppb.New(time.Unix(1752400010, 0)),
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
		ObjectFilters: []*pb.ObjectFilter{{Id: "f1", Path: "/data", Include: []string{"*.sql"}}},
		Rpo:           "24h",
		BackupWindow:  []string{"0 2 * * *"},
		Destination:   "bwfs:8080",
		Type:          "backup",
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, int64(1752400000), dto.CreatedAt)
	assert.Equal(t, int64(1752400010), dto.UpdatedAt)
	assert.Equal(t, []string{"web-*"}, dto.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, dto.ClientFilters.Labels)
	require.Len(t, dto.ObjectFilters, 1)
	assert.Equal(t, "f1", dto.ObjectFilters[0].ID)
	assert.Equal(t, "/data", dto.ObjectFilters[0].Path)
	assert.Equal(t, "backup", dto.Type)
}
```

- [ ] **Step 2: Run to verify failure**

Run:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/api-server/... -run TestToPolicyDTO -v
```
Expected: FAIL — `dto.Type` undefined (field doesn't exist on `policyDTO` yet).

- [ ] **Step 3: Add `Type` to `policyDTO` and `toPolicyDTO`**

In `src/cmd/api-server/policies.go`:

Old:
```go
type policyDTO struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
	ClientFilters clientFiltersDTO  `json:"client_filters"`
	ObjectFilters []objectFilterDTO `json:"object_filters"`
	RPO           string            `json:"rpo"`
	BackupWindow  []string          `json:"backup_window"`
	Destination   string            `json:"destination"`
}
```
New:
```go
type policyDTO struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
	ClientFilters clientFiltersDTO  `json:"client_filters"`
	ObjectFilters []objectFilterDTO `json:"object_filters"`
	RPO           string            `json:"rpo"`
	BackupWindow  []string          `json:"backup_window"`
	Destination   string            `json:"destination"`
	Type          string            `json:"type"`
}
```

Old (inside `toPolicyDTO`):
```go
	return policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters: objectFilters,
		RPO:           p.GetRpo(),
		BackupWindow:  p.GetBackupWindow(),
		Destination:   p.GetDestination(),
	}
```
New:
```go
	return policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters: objectFilters,
		RPO:           p.GetRpo(),
		BackupWindow:  p.GetBackupWindow(),
		Destination:   p.GetDestination(),
		Type:          p.GetType(),
	}
```

Note: `policyInput` (the `POST`/`PUT` request body struct) is deliberately left unchanged — no `type` field, matching the write-path decision that `CreatePolicy`/`UpdatePolicy` stay hardcoded to `"backup"` for now.

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
cd /home/alex/miniprotector/src && go test ./cmd/api-server/... -v 2>&1 | tail -40
```
Expected: `ok`.

- [ ] **Step 5: Build the whole module**

Run:
```bash
cd /home/alex/miniprotector/src && go build ./...
```
Expected: exits 0.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "feat(api-server): pass policy type through to the REST policy DTO"
```

---

## Task 7: Move demo policy files into `policies/backup/`

**Files:**
- Move: `demo/policy-server/policies/audit-logs.json` → `demo/policy-server/policies/backup/audit-logs.json`
- Move: `demo/policy-server/policies/database-backup.json` → `demo/policy-server/policies/backup/database-backup.json`
- Move: `demo/policy-server/policies/webserver-backup.json` → `demo/policy-server/policies/backup/webserver-backup.json`
- Modify: `demo/README.md`

**Interfaces:**
- Consumes: nothing (no code dependency — this is data + docs).
- Produces: nothing consumed by a later task.

`demo/docker-compose.yml` mounts the whole `./policy-server/policies` host directory as `/data/policies` inside the container (`- ./policy-server/policies:/data/policies`), so moving files into a subfolder underneath that mount needs no `docker-compose.yml` change — the container will simply see `/data/policies/backup/*.json`, exactly what Task 2's `Cache.Reload` now expects.

- [ ] **Step 1: Move the three demo policy files**

```bash
mkdir -p /home/alex/miniprotector/demo/policy-server/policies/backup
git -C /home/alex/miniprotector mv demo/policy-server/policies/audit-logs.json demo/policy-server/policies/backup/audit-logs.json
git -C /home/alex/miniprotector mv demo/policy-server/policies/database-backup.json demo/policy-server/policies/backup/database-backup.json
git -C /home/alex/miniprotector mv demo/policy-server/policies/webserver-backup.json demo/policy-server/policies/backup/webserver-backup.json
```

- [ ] **Step 2: Verify the move and that file contents are untouched**

```bash
ls /home/alex/miniprotector/demo/policy-server/policies/backup/
git -C /home/alex/miniprotector diff --cached --stat
```
Expected: three files listed in `backup/`; `git diff --cached --stat` shows three renames (`100% similarity`), no content changes.

- [ ] **Step 3: Update `demo/README.md`'s path references**

Old:
```markdown
touches your host filesystem beyond this directory (except `demo/policy-server/policies/`, which
you're meant to edit — see "Backup policies" below): every secret and every byte of state lives in
```
New:
```markdown
touches your host filesystem beyond this directory (except `demo/policy-server/policies/backup/`,
which you're meant to edit — see "Backup policies" below): every secret and every byte of state
lives in
```

Old:
```markdown
`policy-server` ships with three example policies (`demo/policy-server/policies/`), each
demonstrating a different way `client_filters` can select clients:
```
New:
```markdown
`policy-server` ships with three example policies (`demo/policy-server/policies/backup/`), each
demonstrating a different way `client_filters` can select clients:
```

Old:
```markdown
Edit a policy and watch it reload live. `policy-server` watches one sentinel file,
`policies/.changed` — any write to it triggers a full reload of every `*.json` file under
`policies/`, so a multi-file edit reloads atomically instead of file-by-file:

```bash
docker compose -f demo/docker-compose.yml exec policy-server sh -c \
  "sed -i 's/1h/30m/' /data/policies/database-backup.json && touch /data/policies/.changed"
docker compose -f demo/docker-compose.yml logs policy-server | tail -5   # confirm the reload log line
```
```
New:
```markdown
Edit a policy and watch it reload live. `policy-server` watches one sentinel file,
`policies/.changed` — any write to it triggers a full reload of every `*.json` file found one level
under `policies/` (i.e. under a type subfolder such as `policies/backup/`), so a multi-file edit
reloads atomically instead of file-by-file:

```bash
docker compose -f demo/docker-compose.yml exec policy-server sh -c \
  "sed -i 's/1h/30m/' /data/policies/backup/database-backup.json && touch /data/policies/.changed"
docker compose -f demo/docker-compose.yml logs policy-server | tail -5   # confirm the reload log line
```
```

- [ ] **Step 4: Commit**

```bash
git add demo/README.md
git commit -m "chore(demo): move example policies into policies/backup/"
```

---

## Task 8: Documentation and changelog

**Files:**
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/policyclient.md`
- Modify: `docs/components/agent.md`
- Modify: `docs/protocols/policy-server.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing (docs only).
- Produces: nothing (terminal task).

- [ ] **Step 1: Update `docs/components/policy-server.md`**

Old:
```markdown
# policy-server

Serves backup policies — JSON files under `$MP_CONFIG_PATH/policies/`, one per policy — filtered to
exactly the policies whose `client_filters` match a requesting client's verified hostname and
certificate-embedded attribute labels. Also exposes an admin write API
```
New:
```markdown
# policy-server

Serves backup policies — JSON files under `$MP_CONFIG_PATH/policies/backup/`, one per policy —
filtered to exactly the policies whose `client_filters` match a requesting client's verified
hostname and certificate-embedded attribute labels. Also exposes an admin write API
```

Old:
```markdown
### Policy files and hot reload

Each `$MP_CONFIG_PATH/policies/*.json` file is one policy: `metadata` (`name` plus operator-set
`created_at`/`updated_at`), `client_filters` (`hostnames` glob list, `labels` map), `object_filters`
```
New:
```markdown
### Policy types and directory layout

A policy's type is derived from the name of the immediate subfolder its file lives in under
`$MP_CONFIG_PATH/policies/` — `policies/backup/*.json` are type `"backup"`, the only type that
exists today. Type is never read from or written to the on-disk policy JSON itself; it's purely a
function of file location, computed at load time the same way `policy-server` already computes each
policy's `id` and never reads it from the file. A `*.json` sitting directly under `policies/`,
outside any type subfolder, is skipped and logged — the same "loud skip, don't block the rest"
treatment already applied to a malformed file (see below). An unrecognized subfolder name is still
loaded and tagged with its literal name; `policy-server` does not validate against a whitelist of
known types — that's left to whichever downstream consumer (`agent` today) knows what to do with a
given type. `CreatePolicy` always writes into `policies/backup/`, creating that subdirectory if
missing — there is currently no way to create a policy of any other type through this RPC. See
[Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md).

### Policy files and hot reload

Each policy type subfolder's `*.json` file is one policy: `metadata` (`name` plus operator-set
`created_at`/`updated_at`), `client_filters` (`hostnames` glob list, `labels` map), `object_filters`
```

Old (later in the same "Policy files and hot reload" section):
```markdown
All policies are loaded into memory at startup. To pick up edits, touch
`$MP_CONFIG_PATH/policies/.changed` after finishing your edit(s) — `policy-server` watches that one
sentinel file via `fsnotify` and reloads the entire directory as a single atomic swap on each
write. This lets you edit several policy files as a batch and trigger exactly one reload, rather
than reloading (potentially mid-edit) on every individual file write.
```
New:
```markdown
All policies are loaded into memory at startup. To pick up edits, touch
`$MP_CONFIG_PATH/policies/.changed` after finishing your edit(s) — `policy-server` watches that one
top-level sentinel file via `fsnotify` and reloads every type subfolder as a single atomic swap on
each write. This lets you edit several policy files (in one or more type subfolders) as a batch and
trigger exactly one reload, rather than reloading (potentially mid-edit) on every individual file
write.
```

- [ ] **Step 2: Update `docs/components/policyclient.md`**

Old:
```markdown
```json
[
  {
    "id": "b1f2c3d4-...",
    "name": "daily-db-backup",
    "created_at": "2026-07-01T00:00:00Z",
    "updated_at": "2026-07-05T00:00:00Z",
    "object_filters": [
      {"id": "a9e8d7c6-...", "path": "/var/lib/postgres", "include": ["*.sql"]},
      {"id": "f0e1d2c3-...", "path": "/etc/postgres"}
    ],
    "rpo": "24h",
    "backup_window": ["0 2 * * *"],
    "destination": "bwfs-east.internal:8080"
  }
]
```
```
New:
```markdown
```json
[
  {
    "id": "b1f2c3d4-...",
    "name": "daily-db-backup",
    "created_at": "2026-07-01T00:00:00Z",
    "updated_at": "2026-07-05T00:00:00Z",
    "object_filters": [
      {"id": "a9e8d7c6-...", "path": "/var/lib/postgres", "include": ["*.sql"]},
      {"id": "f0e1d2c3-...", "path": "/etc/postgres"}
    ],
    "rpo": "24h",
    "backup_window": ["0 2 * * *"],
    "destination": "bwfs-east.internal:8080",
    "type": "backup"
  }
]
```

`type` is derived by `policy-server` from the subfolder the policy file lives in (`policies/backup/`
today) — pure passthrough data as far as `policyclient` is concerned; see
[Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md).
`agent` is the consumer that actually branches on it (see
[agent](./agent.md#policy-driven-backup-execution)).
```

- [ ] **Step 3: Update `docs/components/agent.md`**

Old:
```markdown
## Policy-driven backup execution

Every reconcile tick, `agent` re-reads `policies-cache.json` fresh (so it notices `policy-update`
refreshing the cache without needing a restart) and derives one backup task per
`(policy, object_filters path)` pair.
```
New:
```markdown
## Policy-driven backup execution

Every reconcile tick, `agent` re-reads `policies-cache.json` fresh (so it notices `policy-update`
refreshing the cache without needing a restart) and derives one backup task per
`(policy, object_filters path)` pair, considering only cached policies whose `type` is `"backup"` —
the only type that exists today. A cached policy of any other type is silently skipped, the same
fail-safe direction already used for an unparseable `rpo` or missing `backup_window` below; see
[Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md).
```

- [ ] **Step 4: Update `docs/protocols/policy-server.md`**

Old:
```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string id = 8;
  ClientFilters client_filters = 9;
}
```
New:
```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string id = 8;
  ClientFilters client_filters = 9;
  string type = 10;
}
```

Old (in "Behavior"):
```markdown
- Both `Policy.id` and each `ObjectFilter.id` are computed by `policy-server` itself --
  deterministically, from the policy file's name (and each object filter's position within it) --
  never read from or written to the on-disk policy JSON. They exist so two policies, or two object
  filters within one policy, can never be confused with each other downstream even when their
  human-facing `name`/`path` happen to collide.
```
New:
```markdown
- Both `Policy.id` and each `ObjectFilter.id` are computed by `policy-server` itself --
  deterministically, from the policy file's name (and each object filter's position within it) --
  never read from or written to the on-disk policy JSON. They exist so two policies, or two object
  filters within one policy, can never be confused with each other downstream even when their
  human-facing `name`/`path` happen to collide.
- `Policy.type` is likewise computed, not read from the file -- derived from the name of the
  immediate subfolder the policy file lives in under `$MP_CONFIG_PATH/policies/` (`"backup"` for
  `policies/backup/*.json`, the only type today). Populated by both `GetPolicies` and
  `ListPolicies`. `CreatePolicy`/`UpdatePolicyRequest` carry no `type` field -- `CreatePolicy`
  always writes into `policies/backup/`. See
  [Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md).
```

Also update the "See Also" section:

Old:
```markdown
## See Also

- [policy-server](../components/policy-server.md)
- [issuer](../components/issuer.md) — embeds the attribute extension this protocol's authorization
  depends on
- [Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md)
```
New:
```markdown
## See Also

- [policy-server](../components/policy-server.md)
- [issuer](../components/issuer.md) — embeds the attribute extension this protocol's authorization
  depends on
- [Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md)
- [Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md)
```

- [ ] **Step 5: Add a `CHANGELOG.md` entry**

At the top of `CHANGELOG.md`, immediately after the `# Changelog` preamble (before the existing most-recent entry), insert:

```markdown
## 2026-07-20 — policy-server: policy type subfolders

Policies now live under a per-type subfolder — `$MP_CONFIG_PATH/policies/backup/*.json` today,
tagged `type: "backup"` — instead of flat under `policies/`. A policy's type is derived purely from
the name of the subfolder it's loaded from, never read from or written to the file itself, so a
future second policy type is just a new subfolder name with no schema migration for existing files.
`agent`'s backup-task derivation now skips any cached policy whose type isn't `"backup"`, laying the
groundwork for a future non-backup policy type to coexist without being misinterpreted as one. This
is a breaking on-disk layout change with no migration path: existing flat `policies/*.json` files
must be moved into `policies/backup/` before upgrading. `CreatePolicy`/`UpdatePolicy` are unchanged
otherwise — no `type` parameter yet, since there's nothing to choose between until a second type
exists.

```

- [ ] **Step 6: Commit**

```bash
git add docs/components/policy-server.md docs/components/policyclient.md docs/components/agent.md docs/protocols/policy-server.md CHANGELOG.md
git commit -m "docs: document policy type subfolders"
```

---

## Final verification

- [ ] **Run the full test suite**

```bash
cd /home/alex/miniprotector/src && go build ./... && go test ./...
```
Expected: build succeeds, all packages report `ok`.

- [ ] **Sanity-check the demo policy fixtures still parse as valid JSON**

```bash
for f in /home/alex/miniprotector/demo/policy-server/policies/backup/*.json; do
  python3 -m json.tool "$f" > /dev/null && echo "OK: $f" || echo "INVALID: $f"
done
```
Expected: `OK:` for all three files.
