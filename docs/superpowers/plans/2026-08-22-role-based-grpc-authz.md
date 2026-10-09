# Role-Based gRPC Authorization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the gap where any operating-tier certificate can call any RPC on any control-plane
service it can reach, by assigning every node one of three closed roles (`control-plane`, `store`,
`client`) at enrollment and enforcing a per-RPC allowed-role list on every gRPC server.

**Architecture:** A node's role is stored as a reserved `authz-role` attribute
(`client-manager`'s existing generic `attribute` key/value table), which already flows unmodified
through `issuer`'s existing certificate-embedding pipeline into the `attribute` X.509 extension on
every operating certificate. A new `common/mtls.RequireRoles` gRPC interceptor reads that
extension off the verified peer certificate (via the existing `PeerAttributes`) and enforces a
per-method allowed-role map, wired into every server via `common/connection.StartServer`.

**Tech Stack:** Go, gRPC (`google.golang.org/grpc`), GORM/SQLite (`storage/clientmanager`),
protoc/protoc-gen-go-grpc, Cobra CLI (`client-manager`), testify (`assert`/`require`).

## Global Constraints

- The reserved attribute key is `authz-role`, **not** `role` — `demo/policy-server/policies/backup/webserver-backup.json`
  already uses a free-form `role` attribute as a policy-targeting label, unrelated to
  authorization. Never use the bare key `role` anywhere in this plan's code or docs.
- Valid role tokens are exactly `control-plane`, `store`, `client` (see
  `docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md`). No other value is ever
  valid.
- No backward-compatibility or migration path is being built. A node without a matching
  `authz-role` attribute is denied every role-gated RPC once these changes ship — this is
  explicit, accepted, and out of scope to soften.
- `issuer` and `api-server`'s REST bearer-token layer are untouched by this plan — both are
  explicitly out of scope per the design.
- Every new/changed Go file must compile and its package's tests must pass before moving to the
  next task (`go build ./...` and `go test ./...`, scoped to the changed package(s) at minimum).

---

### Task 1: Role validation in `storage/clientmanager`

**Files:**
- Create: `src/storage/clientmanager/role.go`
- Create: `src/storage/clientmanager/role_test.go`
- Modify: `src/storage/clientmanager/store.go:157-167` (`SetKV`)
- Modify: `src/storage/clientmanager/store_test.go` (append tests)

**Interfaces:**
- Produces: `clientmanagerstore.RoleAttributeKey` (`const string = "authz-role"`),
  `clientmanagerstore.DefaultRole` (`const string = "client"`), `clientmanagerstore.ValidRoles`
  (`[]string`), `clientmanagerstore.ValidateRole(value string) error`.
- Consumes: nothing new — builds on the existing `Store.SetKV`/`KV`/`AddClient` already in
  `store.go`.

- [ ] **Step 1: Write the failing tests for `ValidateRole`**

Create `src/storage/clientmanager/role_test.go`:

```go
package clientmanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateRole_AcceptsEachValidToken(t *testing.T) {
	for _, role := range ValidRoles {
		assert.NoError(t, ValidateRole(role))
	}
}

func TestValidateRole_AcceptsMultiRoleCommaList(t *testing.T) {
	assert.NoError(t, ValidateRole("store,client"))
}

func TestValidateRole_RejectsUnknownToken(t *testing.T) {
	assert.Error(t, ValidateRole("web"))
}

func TestValidateRole_RejectsEmptyValue(t *testing.T) {
	assert.Error(t, ValidateRole(""))
}

func TestValidateRole_RejectsOneBadTokenInCommaList(t *testing.T) {
	assert.Error(t, ValidateRole("client,bogus"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./storage/clientmanager/... -run TestValidateRole -v`
Expected: FAIL — `ValidateRole` / `ValidRoles` undefined.

- [ ] **Step 3: Implement `role.go`**

Create `src/storage/clientmanager/role.go`:

```go
// role.go defines the closed authorization-role enum a node is assigned
// at enrollment and carries in its operating certificate's attribute
// extension under the "authz-role" key. See
// docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md.
package clientmanager

import (
	"fmt"
	"strings"
)

// RoleAttributeKey is the reserved ClientKVRecord (KindAttribute) key
// carrying a node's authorization role(s). Deliberately not the bare
// "role" -- that key is already used as a free-form policy-targeting
// label (see demo/policy-server/policies/backup/webserver-backup.json).
const RoleAttributeKey = "authz-role"

// DefaultRole is assigned when client-manager add / AddClient is not
// given an explicit role -- the common case, since most enrolled nodes
// are ordinary backup-agent hosts.
const DefaultRole = "client"

// ValidRoles is the closed set of roles a node may be assigned.
var ValidRoles = []string{"control-plane", "store", "client"}

// ValidateRole checks that value is a non-empty comma-separated list of
// ValidRoles tokens. Called wherever a RoleAttributeKey value is
// written: Store.SetKV, and the CLI/clientmanager-admin-api call sites
// that set it alongside AddClient/ReEnrollClient.
func ValidateRole(value string) error {
	if value == "" {
		return fmt.Errorf("role must not be empty")
	}
	for _, token := range strings.Split(value, ",") {
		if !isValidRoleToken(token) {
			return fmt.Errorf("invalid role %q: must be one of %v", token, ValidRoles)
		}
	}
	return nil
}

func isValidRoleToken(token string) bool {
	for _, r := range ValidRoles {
		if token == r {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./storage/clientmanager/... -run TestValidateRole -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Write failing tests for `SetKV`'s role validation**

Append to `src/storage/clientmanager/store_test.go`:

```go
func TestSetKV_RoleAttribute_AcceptsValidRole(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))

	require.NoError(t, store.SetKV(t.Context(), "node-1", KindAttribute, RoleAttributeKey, "control-plane"))

	attrs, err := store.KV(t.Context(), "node-1", KindAttribute)
	require.NoError(t, err)
	require.Len(t, attrs, 1)
	assert.Equal(t, "control-plane", attrs[0].Value)
}

func TestSetKV_RoleAttribute_RejectsInvalidRole(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))

	err := store.SetKV(t.Context(), "node-1", KindAttribute, RoleAttributeKey, "web")
	assert.Error(t, err)

	attrs, err := store.KV(t.Context(), "node-1", KindAttribute)
	require.NoError(t, err)
	assert.Empty(t, attrs)
}

func TestSetKV_NonRoleAttributeKeyIsUnvalidated(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))

	// "role" (not "authz-role") is the pre-existing free-form
	// policy-targeting label -- SetKV must not validate it.
	require.NoError(t, store.SetKV(t.Context(), "node-1", KindAttribute, "role", "web"))
}

func TestSetKV_RoleKeyOnDescriptionKindIsUnvalidated(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))

	require.NoError(t, store.SetKV(t.Context(), "node-1", KindDescription, RoleAttributeKey, "anything"))
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd src && go test ./storage/clientmanager/... -run TestSetKV_Role -v`
Expected: FAIL — `TestSetKV_RoleAttribute_RejectsInvalidRole` fails because `SetKV` doesn't
validate yet (no error returned, one attribute row exists).

- [ ] **Step 7: Add the validation branch to `SetKV`**

In `src/storage/clientmanager/store.go`, change:

```go
func (s *Store) SetKV(ctx context.Context, hostname string, kind KVKind, key, value string) error {
	if _, err := s.GetClient(ctx, hostname); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hostname"}, {Name: "kind"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&ClientKVRecord{Hostname: hostname, Kind: kind, Key: key, Value: value}).Error
}
```

to:

```go
func (s *Store) SetKV(ctx context.Context, hostname string, kind KVKind, key, value string) error {
	if kind == KindAttribute && key == RoleAttributeKey {
		if err := ValidateRole(value); err != nil {
			return err
		}
	}
	if _, err := s.GetClient(ctx, hostname); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hostname"}, {Name: "kind"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&ClientKVRecord{Hostname: hostname, Kind: kind, Key: key, Value: value}).Error
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `cd src && go test ./storage/clientmanager/... -v`
Expected: PASS, entire package (all pre-existing tests plus the new ones).

- [ ] **Step 9: Commit**

```bash
git add src/storage/clientmanager/role.go src/storage/clientmanager/role_test.go src/storage/clientmanager/store.go src/storage/clientmanager/store_test.go
git commit -m "feat(clientmanager): add authz-role attribute validation"
```

---

### Task 2: `client-manager add`/`re-enroll --role` CLI flag

**Files:**
- Modify: `src/cmd/clientmanager/arguments.go:13-29,40-72` (`Arguments` struct, `addCmd`/`reEnrollCmd` flags)
- Modify: `src/cmd/clientmanager/add.go` (`runAdd`, `runReEnroll`)
- Modify: `src/cmd/clientmanager/add_test.go` (append tests)

**Interfaces:**
- Consumes: `clientmanagerstore.DefaultRole`, `clientmanagerstore.ValidateRole`,
  `clientmanagerstore.RoleAttributeKey`, `clientmanagerstore.KindAttribute` (Task 1).
- Produces: `Arguments.Role string` field; no other package depends on this directly.

- [ ] **Step 1: Write failing CLI tests**

Append to `src/cmd/clientmanager/add_test.go`:

```go
func TestRunAdd_NoRoleFlag_DefaultsToClient(t *testing.T) {
	store := newTestManagerStore(t)
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)

	view, err := store.LoadClientView(t.Context(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, "client", view.Attributes[clientmanagerstore.RoleAttributeKey])
}

func TestRunAdd_WithRoleFlag_StoresGivenRole(t *testing.T) {
	store := newTestManagerStore(t)
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1", Role: "control-plane"}
	err := runAdd(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)

	view, err := store.LoadClientView(t.Context(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, "control-plane", view.Attributes[clientmanagerstore.RoleAttributeKey])
}

func TestRunAdd_InvalidRole_ErrorsBeforeMinting(t *testing.T) {
	store := newTestManagerStore(t)
	called := false
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		called = true
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1", Role: "web"}
	err := runAdd(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)
	assert.False(t, called, "mint must not be called for an invalid role")

	_, err = store.GetClient(t.Context(), "node-1")
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunReEnroll_NoRoleFlag_LeavesExistingRoleUnchanged(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))
	require.NoError(t, store.SetKV(t.Context(), "node-1", clientmanagerstore.KindAttribute, clientmanagerstore.RoleAttributeKey, "store"))
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1"}
	err := runReEnroll(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)

	view, err := store.LoadClientView(t.Context(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, "store", view.Attributes[clientmanagerstore.RoleAttributeKey])
}

func TestRunReEnroll_WithRoleFlag_OverwritesStoredRole(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))
	require.NoError(t, store.SetKV(t.Context(), "node-1", clientmanagerstore.KindAttribute, clientmanagerstore.RoleAttributeKey, "client"))
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1", Role: "control-plane"}
	err := runReEnroll(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)

	view, err := store.LoadClientView(t.Context(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, "control-plane", view.Attributes[clientmanagerstore.RoleAttributeKey])
}

func TestRunReEnroll_InvalidRole_ErrorsBeforeMinting(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))
	called := false
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		called = true
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1", Role: "web"}
	err := runReEnroll(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)
	assert.False(t, called, "mint must not be called for an invalid role")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go build ./cmd/clientmanager/...`
Expected: FAIL — `Arguments` has no field `Role`.

- [ ] **Step 3: Add the `Role` field and `--role` flags**

In `src/cmd/clientmanager/arguments.go`, add to the `Arguments` struct (after `SANs`):

```go
	Role     string   // Authorization role for add/re-enroll: control-plane, store, or client
```

Add, immediately after the existing `addCmd.Flags().StringArrayVar(&args.SANs, "san", ...)` line:

```go
	addCmd.Flags().StringVar(&args.Role, "role", "", "Authorization role for this node: control-plane, store, or client (default: client)")
```

Add, immediately after the existing `reEnrollCmd.Flags().StringArrayVar(&args.SANs, "san", ...)` line:

```go
	reEnrollCmd.Flags().StringVar(&args.Role, "role", "", "Authorization role for this node: control-plane, store, or client (empty keeps the currently stored role)")
```

- [ ] **Step 4: Implement role resolution in `runAdd`/`runReEnroll`**

Replace the full contents of `src/cmd/clientmanager/add.go` with:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/alex-sviridov/miniprotector/common/certmint"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func runAdd(ctx context.Context, mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	role := args.Role
	if role == "" {
		role = clientmanagerstore.DefaultRole
	}
	if err := clientmanagerstore.ValidateRole(role); err != nil {
		return fmt.Errorf("add %s: %w", args.Hostname, err)
	}

	if _, err := store.GetClient(ctx, args.Hostname); err == nil {
		return fmt.Errorf("client %q already exists; use re-enroll or description/attribute set instead", args.Hostname)
	} else if !errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return fmt.Errorf("check existing client: %w", err)
	}

	token, err := mint(args.Hostname, args.SANs, mintOpts)
	if err != nil {
		return fmt.Errorf("add %s: %w", args.Hostname, err)
	}

	if err := store.AddClient(ctx, args.Hostname, args.SANs, time.Now()); err != nil {
		return fmt.Errorf("record client %s: %w", args.Hostname, err)
	}
	if err := store.SetKV(ctx, args.Hostname, clientmanagerstore.KindAttribute, clientmanagerstore.RoleAttributeKey, role); err != nil {
		return fmt.Errorf("set role for %s: %w", args.Hostname, err)
	}

	fmt.Fprintln(out, token)
	return nil
}

func runReEnroll(ctx context.Context, mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	if args.Role != "" {
		if err := clientmanagerstore.ValidateRole(args.Role); err != nil {
			return fmt.Errorf("re-enroll %s: %w", args.Hostname, err)
		}
	}

	client, err := store.GetClient(ctx, args.Hostname)
	if err != nil {
		return fmt.Errorf("re-enroll %s: %w", args.Hostname, err)
	}

	sans := args.SANs
	if len(sans) == 0 {
		sans = client.SANsList()
	}

	token, err := mint(args.Hostname, sans, mintOpts)
	if err != nil {
		return fmt.Errorf("re-enroll %s: %w", args.Hostname, err)
	}

	if args.Role != "" {
		if err := store.SetKV(ctx, args.Hostname, clientmanagerstore.KindAttribute, clientmanagerstore.RoleAttributeKey, args.Role); err != nil {
			return fmt.Errorf("set role for %s: %w", args.Hostname, err)
		}
	}

	fmt.Fprintln(out, token)
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd src && go test ./cmd/clientmanager/... -v`
Expected: PASS, entire package (including the pre-existing `TestRunAdd_*`/`TestRunReEnroll_*` tests
— `TestRunAdd_MintsAndRecordsClient` etc. must still pass unchanged since they don't set `Role`).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/clientmanager/arguments.go src/cmd/clientmanager/add.go src/cmd/clientmanager/add_test.go
git commit -m "feat(clientmanager): add --role flag to add/re-enroll"
```

---

### Task 3: Add `role` field to the `clientmanageradmin` proto

**Files:**
- Modify: `src/api/clientmanageradmin.proto`
- Regenerate: `src/api/clientmanageradmin.pb.go`, `src/api/clientmanageradmin_grpc.pb.go` (via `make proto`)

**Interfaces:**
- Produces: `pb.AddClientRequest.Role string` / `GetRole() string`,
  `pb.ReEnrollClientRequest.Role string` / `GetRole() string` (used by Task 4).

- [ ] **Step 1: Edit the proto messages**

In `src/api/clientmanageradmin.proto`, change:

```proto
message AddClientRequest {
  string hostname = 1;
  repeated string sans = 2;
}
```

to:

```proto
message AddClientRequest {
  string hostname = 1;
  repeated string sans = 2;
  // Authorization role to assign at enrollment: "control-plane", "store",
  // or "client". Empty resolves to "client". See
  // docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md.
  string role = 3;
}
```

and change:

```proto
message ReEnrollClientRequest {
  string hostname = 1;
  // Empty means keep the hostname's currently stored SANs.
  repeated string sans = 2;
}
```

to:

```proto
message ReEnrollClientRequest {
  string hostname = 1;
  // Empty means keep the hostname's currently stored SANs.
  repeated string sans = 2;
  // Authorization role to assign. Empty means keep the hostname's
  // currently stored role, unchanged.
  string role = 3;
}
```

- [ ] **Step 2: Regenerate the Go protobuf code**

Run: `make proto` (from the repo root)
Expected: `src/api/clientmanageradmin.pb.go` is rewritten; `git status` shows it modified.

- [ ] **Step 3: Verify the build still compiles**

Run: `cd src && go build ./...`
Expected: PASS — adding an optional proto field doesn't break any existing caller.

- [ ] **Step 4: Commit**

```bash
git add src/api/clientmanageradmin.proto src/api/clientmanageradmin.pb.go
git commit -m "feat(api): add role field to AddClientRequest/ReEnrollClientRequest"
```

---

### Task 4: `clientmanager-admin-api` role handling

**Files:**
- Modify: `src/cmd/clientmanager-admin-api/server.go:35-85` (`AddClient`, `ReEnrollClient`)
- Modify: `src/cmd/clientmanager-admin-api/server_test.go` (append tests)

**Interfaces:**
- Consumes: `pb.AddClientRequest.GetRole()`, `pb.ReEnrollClientRequest.GetRole()` (Task 3);
  `clientmanagerstore.DefaultRole`, `clientmanagerstore.ValidateRole`,
  `clientmanagerstore.RoleAttributeKey`, `clientmanagerstore.KindAttribute` (Task 1).

- [ ] **Step 1: Write failing tests**

Append to `src/cmd/clientmanager-admin-api/server_test.go`:

```go
func TestAddClient_NoRole_DefaultsToClient(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)

	_, err := srv.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "node-1"})
	require.NoError(t, err)

	view, err := store.LoadClientView(t.Context(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, "client", view.Attributes[clientmanagerstore.RoleAttributeKey])
}

func TestAddClient_WithRole_StoresGivenRole(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)

	_, err := srv.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "node-1", Role: "store"})
	require.NoError(t, err)

	view, err := store.LoadClientView(t.Context(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, "store", view.Attributes[clientmanagerstore.RoleAttributeKey])
}

func TestAddClient_InvalidRole_RejectsBeforeMinting(t *testing.T) {
	srv, _, rec := newTestAdminServer(t)

	_, err := srv.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "node-1", Role: "web"})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, 0, rec.calls, "mint must not be called for an invalid role")
}

func TestReEnrollClient_NoRole_LeavesStoredRoleUnchanged(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))
	require.NoError(t, store.SetKV(t.Context(), "node-1", clientmanagerstore.KindAttribute, clientmanagerstore.RoleAttributeKey, "store"))

	_, err := srv.ReEnrollClient(context.Background(), &pb.ReEnrollClientRequest{Hostname: "node-1"})
	require.NoError(t, err)

	view, err := store.LoadClientView(t.Context(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, "store", view.Attributes[clientmanagerstore.RoleAttributeKey])
}

func TestReEnrollClient_WithRole_OverwritesStoredRole(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))
	require.NoError(t, store.SetKV(t.Context(), "node-1", clientmanagerstore.KindAttribute, clientmanagerstore.RoleAttributeKey, "client"))

	_, err := srv.ReEnrollClient(context.Background(), &pb.ReEnrollClientRequest{Hostname: "node-1", Role: "control-plane"})
	require.NoError(t, err)

	view, err := store.LoadClientView(t.Context(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, "control-plane", view.Attributes[clientmanagerstore.RoleAttributeKey])
}

func TestReEnrollClient_InvalidRole_RejectsBeforeMinting(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))

	_, err := srv.ReEnrollClient(context.Background(), &pb.ReEnrollClientRequest{Hostname: "node-1", Role: "web"})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, 0, rec.calls, "mint must not be called for an invalid role")
}
```

This test file needs one new import: add `clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"` to its import block if not already present (it already is — `server_test.go` imports it for `newTestAdminServer`).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go build ./cmd/clientmanager-admin-api/...`
Expected: FAIL — `pb.AddClientRequest`/`pb.ReEnrollClientRequest` have no `Role` field until Task 3
lands (Task 3 must be done first; if done, this instead fails at `go test` because `Role` isn't
persisted yet).

- [ ] **Step 3: Implement role handling in `server.go`**

In `src/cmd/clientmanager-admin-api/server.go`, replace the `AddClient` method with:

```go
func (s *clientManagerAdminServer) AddClient(ctx context.Context, req *pb.AddClientRequest) (*pb.AddClientResponse, error) {
	hostname := req.GetHostname()
	if hostname == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname is required")
	}
	role := req.GetRole()
	if role == "" {
		role = clientmanagerstore.DefaultRole
	}
	if err := clientmanagerstore.ValidateRole(role); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid role: %v", err)
	}

	if _, err := s.store.GetClient(ctx, hostname); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "client %s already enrolled", hostname)
	} else if !errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		s.logger.Error("AddClient: check existing failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "check existing client: %v", err)
	}

	token, err := s.mint(hostname, req.GetSans(), s.mintOpts)
	if err != nil {
		s.logger.Error("AddClient: mint failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "mint token: %v", err)
	}

	if err := s.store.AddClient(ctx, hostname, req.GetSans(), time.Now()); err != nil {
		s.logger.Error("AddClient: record failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "record client: %v", err)
	}
	if err := s.store.SetKV(ctx, hostname, clientmanagerstore.KindAttribute, clientmanagerstore.RoleAttributeKey, role); err != nil {
		s.logger.Error("AddClient: set role failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "set role: %v", err)
	}

	return &pb.AddClientResponse{Token: token}, nil
}
```

Replace the `ReEnrollClient` method with:

```go
func (s *clientManagerAdminServer) ReEnrollClient(ctx context.Context, req *pb.ReEnrollClientRequest) (*pb.ReEnrollClientResponse, error) {
	hostname := req.GetHostname()
	if role := req.GetRole(); role != "" {
		if err := clientmanagerstore.ValidateRole(role); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid role: %v", err)
		}
	}

	rec, err := s.store.GetClient(ctx, hostname)
	if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
	}
	if err != nil {
		s.logger.Error("ReEnrollClient: query failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "get client: %v", err)
	}

	sans := req.GetSans()
	if len(sans) == 0 {
		sans = rec.SANsList()
	}

	token, err := s.mint(hostname, sans, s.mintOpts)
	if err != nil {
		s.logger.Error("ReEnrollClient: mint failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "mint token: %v", err)
	}

	if role := req.GetRole(); role != "" {
		if err := s.store.SetKV(ctx, hostname, clientmanagerstore.KindAttribute, clientmanagerstore.RoleAttributeKey, role); err != nil {
			s.logger.Error("ReEnrollClient: set role failed", "hostname", hostname, "error", err)
			return nil, status.Errorf(codes.Internal, "set role: %v", err)
		}
	}

	return &pb.ReEnrollClientResponse{Token: token}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -v`
Expected: PASS, entire package (including pre-existing `TestAddClient_MintsAndRecordsClient` etc.).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/clientmanager-admin-api/server.go src/cmd/clientmanager-admin-api/server_test.go
git commit -m "feat(clientmanager-admin-api): resolve and persist role on AddClient/ReEnrollClient"
```

---

### Task 5: `common/mtls.RequireRoles` interceptor

**Files:**
- Create: `src/common/mtls/authz.go`
- Create: `src/common/mtls/authz_test.go`

**Interfaces:**
- Consumes: `PeerAttributes(ctx context.Context) (map[string]string, error)` (already exists in
  `src/common/mtls/peer.go`); test helpers `selfSignedCertWithAttributes`, `selfSignedCertNoSAN`,
  `contextWithPeerCert` (already exist in `src/common/mtls/peer_test.go`, same package).
- Produces: `mtls.RequireRoles(requirements map[string][]string) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor)`
  — used by Task 6.

- [ ] **Step 1: Write the failing tests**

Create `src/common/mtls/authz_test.go`:

```go
package mtls

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func handlerRecorder() (grpc.UnaryHandler, *bool) {
	called := false
	return func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}, &called
}

func TestRequireRoles_AllowsMatchingRole(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "control-plane"})
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Method": {"control-plane"}})
	handler, called := handlerRecorder()

	resp, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, *called)
}

func TestRequireRoles_DeniesMismatchedRole(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "client"})
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Method": {"control-plane"}})
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.False(t, *called)
}

func TestRequireRoles_DeniesMissingRoleAttribute(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "node-1")
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Method": {"control-plane"}})
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.False(t, *called)
}

func TestRequireRoles_MethodNotInMapIsOpen(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "node-1")
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Restricted": {"control-plane"}})
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Open"}, handler)
	require.NoError(t, err)
	assert.True(t, *called)
}

func TestRequireRoles_MultiRoleCommaListMatchesAny(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "store,client"})
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Method": {"client"}})
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.NoError(t, err)
	assert.True(t, *called)
}

func TestRequireRoles_NilRequirementsIsFullyOpen(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "node-1")
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(nil)
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.NoError(t, err)
	assert.True(t, *called)
}

// fakeServerStream is a minimal grpc.ServerStream: RequireRoles's stream
// path only ever calls Context().
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

func TestRequireRoles_StreamDeniesMismatchedRole(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "client"})
	ctx := contextWithPeerCert(cert)
	_, stream := RequireRoles(map[string][]string{"/svc/Stream": {"control-plane"}})
	called := false
	handler := func(srv any, ss grpc.ServerStream) error {
		called = true
		return nil
	}

	err := stream(nil, &fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/svc/Stream"}, handler)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.False(t, called)
}

func TestRequireRoles_StreamAllowsMatchingRole(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "client"})
	ctx := contextWithPeerCert(cert)
	_, stream := RequireRoles(map[string][]string{"/svc/Stream": {"client"}})
	called := false
	handler := func(srv any, ss grpc.ServerStream) error {
		called = true
		return nil
	}

	err := stream(nil, &fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/svc/Stream"}, handler)
	require.NoError(t, err)
	assert.True(t, called)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./common/mtls/... -run TestRequireRoles -v`
Expected: FAIL — `RequireRoles` undefined.

- [ ] **Step 3: Implement `authz.go`**

Create `src/common/mtls/authz.go`:

```go
package mtls

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RequireRoles builds a paired gRPC unary/stream interceptor enforcing a
// per-method allowed-role list, read from the caller's peer certificate's
// attribute extension (the "authz-role" key, comma-separated) via
// PeerAttributes. requirements maps a full gRPC method name (e.g.
// "/catalogservice.CatalogService/SyncFileVersions", matching
// grpc.UnaryServerInfo.FullMethod / grpc.StreamServerInfo.FullMethod
// exactly) to the roles allowed to call it. A method absent from
// requirements is unrestricted -- open to any caller regardless of role,
// including one with no role attribute at all (e.g. policy-server's
// GetPolicies, which every enrolled node must be able to call). See
// docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md.
func RequireRoles(requirements map[string][]string) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := checkRole(ctx, requirements, info.FullMethod); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
	stream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := checkRole(ss.Context(), requirements, info.FullMethod); err != nil {
			return err
		}
		return handler(srv, ss)
	}
	return unary, stream
}

func checkRole(ctx context.Context, requirements map[string][]string, fullMethod string) error {
	allowed, restricted := requirements[fullMethod]
	if !restricted {
		return nil
	}
	attrs, err := PeerAttributes(ctx)
	if err != nil {
		return status.Errorf(codes.PermissionDenied, "role check failed: %v", err)
	}
	for _, callerRole := range splitRoles(attrs["authz-role"]) {
		for _, a := range allowed {
			if callerRole == a {
				return nil
			}
		}
	}
	return status.Errorf(codes.PermissionDenied, "method %s requires role in %v", fullMethod, allowed)
}

func splitRoles(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./common/mtls/... -v`
Expected: PASS, entire package (including every pre-existing `mtls_test.go`/`peer_test.go` test).

- [ ] **Step 5: Commit**

```bash
git add src/common/mtls/authz.go src/common/mtls/authz_test.go
git commit -m "feat(mtls): add RequireRoles per-RPC authorization interceptor"
```

---

### Task 6: Wire `roleRequirements` through `common/connection.StartServer`

**Files:**
- Modify: `src/common/connection/server.go` (full rewrite of both functions)
- Modify: `src/common/connection/mtls_wiring_test.go` (5 call sites)
- Modify: `src/cmd/issuer/main.go:128` (`StartServerWithCredentials` call)
- Modify: `src/cmd/catalog/main.go:67` (`StartServer` call)
- Modify: `src/cmd/clientmanager-admin-api/main.go:85` (`StartServer` call)
- Modify: `src/cmd/policy-server/main.go:104` (`StartServer` call)
- Modify: `src/cmd/bwfs/main.go:122` (`StartServer` call)
- Modify: `src/cmd/clientmanager-api/main.go:76` (`StartServer` call)
- Modify: `src/cmd/catalog/server_test.go:198` (`StartServer` call in `TestSyncFileVersions_RealMTLSRoundTrip`)

**Interfaces:**
- Consumes: `mtls.RequireRoles` (Task 5).
- Produces: `connection.StartServer(ctx, logger, port, certsDir, roleRequirements map[string][]string, register func(*grpc.Server)) error`
  and `connection.StartServerWithCredentials(ctx, logger, port, creds, roleRequirements map[string][]string, register func(*grpc.Server)) error`
  — every later task passes a real map here instead of `nil`.

This task is deliberately regression-safe: every call site gets `nil` (no behavior change) so all
existing tests keep passing; Tasks 7-11 replace `nil` with each service's real matrix.

- [ ] **Step 1: Update `connection/server.go`**

Replace the full contents of `src/common/connection/server.go` with:

```go
package connection

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// StartServer creates and starts a gRPC server on the specified port,
// requiring mutual TLS using the certs in certsDir (ca.crt, client.crt,
// client.key). roleRequirements, if non-nil, is wired as a per-method
// role-authorization interceptor via mtls.RequireRoles -- nil means no
// role restriction on any RPC this server registers. The register
// callback receives the bare *grpc.Server so callers can register any
// service (backup, restore, …) without this package importing
// service-specific proto packages.
func StartServer(ctx context.Context, logger *slog.Logger, port int, certsDir string, roleRequirements map[string][]string, register func(*grpc.Server)) error {
	creds, err := mtls.LoadServerCredentials(certsDir)
	if err != nil {
		return fmt.Errorf("failed to load server credentials: %w", err)
	}
	return StartServerWithCredentials(ctx, logger, port, creds, roleRequirements, register)
}

// StartServerWithCredentials is StartServer, parameterized on already-built
// transport credentials instead of loading the default certsDir/client.crt
// identity -- used by callers presenting a different credential requirement
// (issuer, which requires bootstrap/issuer-caller peer certs rather than the
// default operating-tier check; see mtls.LoadIssuerServerCredentials).
func StartServerWithCredentials(ctx context.Context, logger *slog.Logger, port int, creds credentials.TransportCredentials, roleRequirements map[string][]string, register func(*grpc.Server)) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	logger.Info("Server starting", "port", port)

	unaryInterceptor, streamInterceptor := mtls.RequireRoles(roleRequirements)
	grpcServer := grpc.NewServer(
		grpc.Creds(creds),
		grpc.ChainUnaryInterceptor(unaryInterceptor),
		grpc.ChainStreamInterceptor(streamInterceptor),
	)
	register(grpcServer)

	logger.Info("Server ready, accepting connections")

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down server...")
		grpcServer.GracefulStop()
	}()

	return grpcServer.Serve(listener)
}
```

- [ ] **Step 2: Update `mtls_wiring_test.go`'s five call sites**

In `src/common/connection/mtls_wiring_test.go`, change each of the four:

```go
		errCh <- StartServer(ctx, testLogger(), port, fixtureCertsDir, func(s *grpc.Server) {})
```

to:

```go
		errCh <- StartServer(ctx, testLogger(), port, fixtureCertsDir, nil, func(s *grpc.Server) {})
```

(appears in `TestStartServerConnect_RoundTripSucceeds`, `TestStartServerConnect_UntrustedClientCertRejected`,
`TestConnectWithIdentity_RoundTripSucceeds` — three occurrences of exactly this line — plus
`TestStartServer_MissingCertsDirFailsFast`'s single-line form:)

```go
	err := StartServer(ctx, testLogger(), port, "does-not-exist", func(s *grpc.Server) {})
```

to:

```go
	err := StartServer(ctx, testLogger(), port, "does-not-exist", nil, func(s *grpc.Server) {})
```

And in `TestStartServerWithCredentials_RoundTripSucceeds`, change:

```go
		errCh <- StartServerWithCredentials(ctx, testLogger(), port, creds, func(s *grpc.Server) {})
```

to:

```go
		errCh <- StartServerWithCredentials(ctx, testLogger(), port, creds, nil, func(s *grpc.Server) {})
```

- [ ] **Step 3: Update every production call site**

In `src/cmd/issuer/main.go`, change:

```go
	if err := connection.StartServerWithCredentials(signalCtx, logger, conf.IssuerPort, creds, func(s *grpc.Server) {
```

to:

```go
	if err := connection.StartServerWithCredentials(signalCtx, logger, conf.IssuerPort, creds, nil, func(s *grpc.Server) {
```

In `src/cmd/catalog/main.go`, `src/cmd/clientmanager-admin-api/main.go`, `src/cmd/policy-server/main.go`,
and `src/cmd/clientmanager-api/main.go`, each has exactly one call of the shape:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
```

Change each to:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, nil, func(s *grpc.Server) {
```

In `src/cmd/bwfs/main.go`, change:

```go
		if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
```

to:

```go
		if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, nil, func(s *grpc.Server) {
```

- [ ] **Step 4: Update the existing real-round-trip test in `catalog/server_test.go`**

In `src/cmd/catalog/server_test.go`, change:

```go
		errCh <- connection.StartServer(ctx, logger, port, fixtureCertsDir, func(s *grpc.Server) {
			pb.RegisterCatalogServiceServer(s, srv)
		})
```

to:

```go
		errCh <- connection.StartServer(ctx, logger, port, fixtureCertsDir, nil, func(s *grpc.Server) {
			pb.RegisterCatalogServiceServer(s, srv)
		})
```

- [ ] **Step 5: Run the full build and test suite**

Run: `cd src && go build ./... && go test ./... -count=1`
Expected: PASS — every package compiles and every existing test still passes unchanged (all
`nil` requirements mean the new interceptors are fully open, matching prior behavior exactly).

- [ ] **Step 6: Commit**

```bash
git add src/common/connection/server.go src/common/connection/mtls_wiring_test.go src/cmd/issuer/main.go src/cmd/catalog/main.go src/cmd/catalog/server_test.go src/cmd/clientmanager-admin-api/main.go src/cmd/policy-server/main.go src/cmd/clientmanager-api/main.go src/cmd/bwfs/main.go
git commit -m "feat(connection): wire role-requirements interceptor into StartServer"
```

---

### Task 7: `clientmanager-api` role matrix

**Files:**
- Create: `src/cmd/clientmanager-api/authz.go`
- Create: `src/cmd/clientmanager-api/authz_test.go`
- Modify: `src/cmd/clientmanager-api/main.go:76` (pass `roleRequirements()` instead of `nil`)

**Interfaces:**
- Consumes: `pb.ClientManagerService_ServiceDesc.ServiceName` (existing generated constant).
- Produces: `roleRequirements() map[string][]string` (package-private, this binary only).

- [ ] **Step 1: Write the failing test**

Create `src/cmd/clientmanager-api/authz_test.go`:

```go
package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	svc := pb.ClientManagerService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + svc + "/ListClients": {"control-plane"},
		"/" + svc + "/GetClient":   {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./cmd/clientmanager-api/... -run TestRoleRequirements -v`
Expected: FAIL — `roleRequirements` undefined.

- [ ] **Step 3: Implement `authz.go`**

Create `src/cmd/clientmanager-api/authz.go`:

```go
package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is clientmanager-api's per-RPC authorization matrix:
// every RPC is restricted to control-plane callers -- api-server is the
// only intended caller. See
// docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md.
func roleRequirements() map[string][]string {
	svc := pb.ClientManagerService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + svc + "/ListClients": {"control-plane"},
		"/" + svc + "/GetClient":   {"control-plane"},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./cmd/clientmanager-api/... -v`
Expected: PASS, entire package.

- [ ] **Step 5: Wire it into `main.go`**

In `src/cmd/clientmanager-api/main.go`, change:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, nil, func(s *grpc.Server) {
```

to:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, roleRequirements(), func(s *grpc.Server) {
```

- [ ] **Step 6: Verify the build**

Run: `cd src && go build ./cmd/clientmanager-api/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/clientmanager-api/authz.go src/cmd/clientmanager-api/authz_test.go src/cmd/clientmanager-api/main.go
git commit -m "feat(clientmanager-api): enforce control-plane role on every RPC"
```

---

### Task 8: `clientmanager-admin-api` role matrix + real round-trip proof

This is the centerpiece proof of the whole design: an operating certificate without the
`control-plane` role must be rejected calling `AddClient` — the exact scenario (a compromised
`bwfs`/`brfs`/`rwfs` node minting enrollment tokens) that motivated this plan.

**Files:**
- Create: `src/cmd/clientmanager-admin-api/authz.go`
- Create: `src/cmd/clientmanager-admin-api/authz_test.go`
- Create: `src/cmd/clientmanager-admin-api/authz_e2e_test.go`
- Modify: `src/cmd/clientmanager-admin-api/main.go:85` (pass `roleRequirements()` instead of `nil`)

**Interfaces:**
- Consumes: `pb.ClientManagerAdminService_ServiceDesc.ServiceName`; `connection.StartServer`,
  `connection.Connect` (Task 6, existing); `mtls`'s `fixtureCertsDir` fixture pattern (existing,
  `src/common/testdata/certs`, whose `client.crt` carries no `authz-role` attribute at all).
- Produces: `roleRequirements() map[string][]string`.

- [ ] **Step 1: Write the failing unit test**

Create `src/cmd/clientmanager-admin-api/authz_test.go`:

```go
package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	svc := pb.ClientManagerAdminService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + svc + "/AddClient":         {"control-plane"},
		"/" + svc + "/ReEnrollClient":    {"control-plane"},
		"/" + svc + "/RevokeClient":      {"control-plane"},
		"/" + svc + "/UnrevokeClient":    {"control-plane"},
		"/" + svc + "/UpdateDescription": {"control-plane"},
		"/" + svc + "/UpdateAttributes":  {"control-plane"},
		"/" + svc + "/UpdateSANs":        {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -run TestRoleRequirements -v`
Expected: FAIL — `roleRequirements` undefined.

- [ ] **Step 3: Implement `authz.go`**

Create `src/cmd/clientmanager-admin-api/authz.go`:

```go
package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is clientmanager-admin-api's per-RPC authorization
// matrix: every RPC is CA-admin-equivalent and restricted to
// control-plane callers -- api-server is the only intended caller. This
// is the centerpiece fix of
// docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md: an
// operating certificate that isn't control-plane-role (e.g. a
// compromised bwfs/brfs/rwfs node's) is rejected here before any handler
// runs.
func roleRequirements() map[string][]string {
	svc := pb.ClientManagerAdminService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + svc + "/AddClient":         {"control-plane"},
		"/" + svc + "/ReEnrollClient":    {"control-plane"},
		"/" + svc + "/RevokeClient":      {"control-plane"},
		"/" + svc + "/UnrevokeClient":    {"control-plane"},
		"/" + svc + "/UpdateDescription": {"control-plane"},
		"/" + svc + "/UpdateAttributes":  {"control-plane"},
		"/" + svc + "/UpdateSANs":        {"control-plane"},
	}
}
```

- [ ] **Step 4: Run test to verify the unit test passes**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -run TestRoleRequirements -v`
Expected: PASS.

- [ ] **Step 5: Write the failing real-round-trip E2E test**

Create `src/cmd/clientmanager-admin-api/authz_e2e_test.go`:

```go
// authz_e2e_test.go proves role enforcement over a real mTLS + gRPC round
// trip -- not just a fabricated context -- using the same
// connection.StartServer/connection.Connect helpers production code uses
// and the project's real testdata certs (see common/mtls/peer_test.go).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/connection"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

const fixtureCertsDir = "../../common/testdata/certs"

func TestAddClient_RealMTLSRoundTrip_NonControlPlaneRoleDenied(t *testing.T) {
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewClientManagerAdminServer(store, (&stubMinter{token: "tok-abc"}).mint, certmint.Options{}, logger)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close()) // release the port; connection.StartServer re-binds it

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(ctx, logger, port, fixtureCertsDir, roleRequirements(), func(s *grpc.Server) {
			pb.RegisterClientManagerAdminServiceServer(s, srv)
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

	// fixtureCertsDir's client.crt carries no authz-role attribute at
	// all -- exactly the state a real, already-enrolled bwfs/brfs/rwfs
	// node is in. It must be denied.
	conn, err := connection.Connect("localhost", port, 5, fixtureCertsDir)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewClientManagerAdminServiceClient(conn)
	_, err = client.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "attacker-controlled"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	_, getErr := store.GetClient(t.Context(), "attacker-controlled")
	assert.ErrorIs(t, getErr, clientmanagerstore.ErrClientNotFound, "the denied AddClient must not have recorded a client")
}
```

- [ ] **Step 6: Run test to verify it passes**

This test doesn't follow the usual red/green cycle: it builds its own server directly from
`roleRequirements()` (Step 3, already implemented) and `connection.StartServer`'s interceptor
wiring (Task 6, already implemented) rather than going through `main()` — so nothing new needs to
be implemented for it to pass, and it should go green as soon as it's added. Its value is as a
regression proof of the real, motivating scenario (a `bwfs`/`brfs`/`rwfs`-like operating
certificate with no role attribute must be denied `AddClient` over a genuine mTLS + gRPC round
trip), not as a driver for new code.

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -v`
Expected: PASS, entire package (including the new `TestAddClient_RealMTLSRoundTrip_NonControlPlaneRoleDenied`).

- [ ] **Step 7: Wire the real matrix into `main.go`**

In `src/cmd/clientmanager-admin-api/main.go`, change:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, nil, func(s *grpc.Server) {
```

to:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, roleRequirements(), func(s *grpc.Server) {
```

- [ ] **Step 8: Verify the build**

Run: `cd src && go build ./cmd/clientmanager-admin-api/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add src/cmd/clientmanager-admin-api/authz.go src/cmd/clientmanager-admin-api/authz_test.go src/cmd/clientmanager-admin-api/authz_e2e_test.go src/cmd/clientmanager-admin-api/main.go
git commit -m "feat(clientmanager-admin-api): enforce control-plane role on every RPC"
```

---

### Task 9: `catalog` role matrix + real round-trip proof

**Files:**
- Create: `src/cmd/catalog/authz.go`
- Create: `src/cmd/catalog/authz_test.go`
- Modify: `src/cmd/catalog/server_test.go` (add one new real-round-trip test after the existing one)
- Modify: `src/cmd/catalog/main.go:67` (pass `roleRequirements()` instead of `nil`)

**Interfaces:**
- Consumes: `pb.CatalogService_ServiceDesc.ServiceName`; `connection.StartServer`,
  `connection.Connect`, `fixtureCertsDir` (already declared in `catalog/server_test.go` as
  `"../../common/testdata/certs"`), `newTestCatalogServer` (already exists in `server_test.go`).
- Produces: `roleRequirements() map[string][]string`.

- [ ] **Step 1: Write the failing unit test**

Create `src/cmd/catalog/authz_test.go`:

```go
package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	svc := pb.CatalogService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + svc + "/SyncFileVersions":      {"store"},
		"/" + svc + "/ListEntries":           {"control-plane"},
		"/" + svc + "/ListClientFacets":      {"control-plane"},
		"/" + svc + "/ListJobFacets":         {"control-plane"},
		"/" + svc + "/ListDirectoryFacets":   {"control-plane"},
		"/" + svc + "/ListStoreFacets":       {"control-plane"},
		"/" + svc + "/ListDirectoryChildren": {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./cmd/catalog/... -run TestRoleRequirements -v`
Expected: FAIL — `roleRequirements` undefined.

- [ ] **Step 3: Implement `authz.go`**

Create `src/cmd/catalog/authz.go`:

```go
package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is catalog's per-RPC authorization matrix:
// SyncFileVersions is called only by catalogsync, which always runs on
// a store-role bwfs host; the six List* query RPCs back api-server's
// catalog views and are restricted to control-plane callers.
func roleRequirements() map[string][]string {
	svc := pb.CatalogService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + svc + "/SyncFileVersions":      {"store"},
		"/" + svc + "/ListEntries":           {"control-plane"},
		"/" + svc + "/ListClientFacets":      {"control-plane"},
		"/" + svc + "/ListJobFacets":         {"control-plane"},
		"/" + svc + "/ListDirectoryFacets":   {"control-plane"},
		"/" + svc + "/ListStoreFacets":       {"control-plane"},
		"/" + svc + "/ListDirectoryChildren": {"control-plane"},
	}
}
```

- [ ] **Step 4: Run test to verify the unit test passes**

Run: `cd src && go test ./cmd/catalog/... -run TestRoleRequirements -v`
Expected: PASS.

- [ ] **Step 5: Write the failing real-round-trip denial test**

In `src/cmd/catalog/server_test.go`, add a new test immediately after
`TestSyncFileVersions_RealMTLSRoundTrip` (reusing that test's setup pattern):

```go
// TestSyncFileVersions_RealMTLSRoundTrip_NonStoreRoleDenied proves role
// enforcement over the same real mTLS + gRPC round trip
// TestSyncFileVersions_RealMTLSRoundTrip uses, but with roleRequirements()
// wired in (production behavior) instead of nil.
func TestSyncFileVersions_RealMTLSRoundTrip_NonStoreRoleDenied(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(ctx, logger, port, fixtureCertsDir, roleRequirements(), func(s *grpc.Server) {
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

	// fixtureCertsDir's client.crt carries no authz-role attribute --
	// must be denied "store"-only SyncFileVersions.
	conn, err := connection.Connect("localhost", port, 5, fixtureCertsDir)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewCatalogServiceClient(conn)
	_, err = client.SyncFileVersions(context.Background(), &pb.SyncRequest{
		Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}},
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	count, err := store.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "the denied SyncFileVersions must not have written anything")
}
```

This reuses imports (`net`, `context`, `slog`, `os`, `time`, `fmt`, `grpc`, `pb`, `connection`,
`codes`, `status`, `require`, `assert`) that `TestSyncFileVersions_RealMTLSRoundTrip` already
brings into this file — no new imports needed.

- [ ] **Step 6: Run test to verify it passes**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS, entire package (including the pre-existing `TestSyncFileVersions_RealMTLSRoundTrip`,
now passing `nil`, which must still succeed unchanged).

- [ ] **Step 7: Wire the real matrix into `main.go`**

In `src/cmd/catalog/main.go`, change:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, nil, func(s *grpc.Server) {
```

to:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, roleRequirements(), func(s *grpc.Server) {
```

- [ ] **Step 8: Verify the build**

Run: `cd src && go build ./cmd/catalog/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add src/cmd/catalog/authz.go src/cmd/catalog/authz_test.go src/cmd/catalog/server_test.go src/cmd/catalog/main.go
git commit -m "feat(catalog): enforce store/control-plane roles on every RPC"
```

---

### Task 10: `policy-server` role matrix

**Files:**
- Create: `src/cmd/policy-server/authz.go`
- Create: `src/cmd/policy-server/authz_test.go`
- Modify: `src/cmd/policy-server/main.go:104` (pass `roleRequirements()` instead of `nil`)

**Interfaces:**
- Consumes: `pb.PolicyService_ServiceDesc.ServiceName`.
- Produces: `roleRequirements() map[string][]string`.

- [ ] **Step 1: Write the failing test**

Create `src/cmd/policy-server/authz_test.go`:

```go
package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + svc + "/ListPolicies":      {"control-plane"},
		"/" + svc + "/CreatePolicy":      {"control-plane"},
		"/" + svc + "/UpdatePolicy":      {"control-plane"},
		"/" + svc + "/DeletePolicy":      {"control-plane"},
		"/" + svc + "/GetNodeCertStatus": {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}

func TestRoleRequirements_GetPoliciesIsUnrestricted(t *testing.T) {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	_, restricted := roleRequirements()["/"+svc+"/GetPolicies"]
	assert.False(t, restricted, "GetPolicies must stay open to every role")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./cmd/policy-server/... -run TestRoleRequirements -v`
Expected: FAIL — `roleRequirements` undefined.

- [ ] **Step 3: Implement `authz.go`**

Create `src/cmd/policy-server/authz.go`:

```go
package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is policy-server's per-RPC authorization matrix.
// GetPolicies is deliberately absent -- every enrolled node, of every
// role, calls it on a schedule to fetch its own policies and report
// check-ins, including control-plane nodes managing their own agent
// lifecycle. The remaining RPCs are the admin surface api-server proxies
// and are restricted to control-plane callers.
func roleRequirements() map[string][]string {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + svc + "/ListPolicies":      {"control-plane"},
		"/" + svc + "/CreatePolicy":      {"control-plane"},
		"/" + svc + "/UpdatePolicy":      {"control-plane"},
		"/" + svc + "/DeletePolicy":      {"control-plane"},
		"/" + svc + "/GetNodeCertStatus": {"control-plane"},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS, entire package.

- [ ] **Step 5: Wire it into `main.go`**

In `src/cmd/policy-server/main.go`, change:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, nil, func(s *grpc.Server) {
```

to:

```go
	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, roleRequirements(), func(s *grpc.Server) {
```

- [ ] **Step 6: Verify the build**

Run: `cd src && go build ./cmd/policy-server/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/policy-server/authz.go src/cmd/policy-server/authz_test.go src/cmd/policy-server/main.go
git commit -m "feat(policy-server): enforce control-plane role on admin RPCs"
```

---

### Task 11: `bwfs` role matrix

**Files:**
- Create: `src/cmd/bwfs/authz.go`
- Create: `src/cmd/bwfs/authz_test.go`
- Modify: `src/cmd/bwfs/main.go:122` (pass `roleRequirements()` instead of `nil`)

**Interfaces:**
- Consumes: `pb.BackupService_ServiceDesc.ServiceName`, `pb.ListService_ServiceDesc.ServiceName`,
  `pb.RestoreService_ServiceDesc.ServiceName`.
- Produces: `roleRequirements() map[string][]string`.

- [ ] **Step 1: Write the failing test**

Create `src/cmd/bwfs/authz_test.go`:

```go
package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	backupSvc := pb.BackupService_ServiceDesc.ServiceName
	listSvc := pb.ListService_ServiceDesc.ServiceName
	restoreSvc := pb.RestoreService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + backupSvc + "/ProcessBackupStream": {"client"},
		"/" + backupSvc + "/BackupCommit":        {"client"},
		"/" + listSvc + "/ListFiles":             {"client"},
		"/" + listSvc + "/ResolveRestoreFiles":   {"client"},
		"/" + restoreSvc + "/RestoreFile":        {"client"},
	}
	assert.Equal(t, want, roleRequirements())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./cmd/bwfs/... -run TestRoleRequirements -v`
Expected: FAIL — `roleRequirements` undefined.

- [ ] **Step 3: Implement `authz.go`**

Create `src/cmd/bwfs/authz.go`:

```go
package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is bwfs's per-RPC authorization matrix, spanning all
// three protocols it serves on one listener (backup, list, restore).
// Every RPC is restricted to client-role callers -- brfs and rwfs are
// the only legitimate callers; catalogsync reads bwfs's local SQLite
// directly rather than over gRPC, so it never appears here.
func roleRequirements() map[string][]string {
	backupSvc := pb.BackupService_ServiceDesc.ServiceName
	listSvc := pb.ListService_ServiceDesc.ServiceName
	restoreSvc := pb.RestoreService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + backupSvc + "/ProcessBackupStream": {"client"},
		"/" + backupSvc + "/BackupCommit":        {"client"},
		"/" + listSvc + "/ListFiles":             {"client"},
		"/" + listSvc + "/ResolveRestoreFiles":   {"client"},
		"/" + restoreSvc + "/RestoreFile":        {"client"},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./cmd/bwfs/... -v`
Expected: PASS (this package's existing test suite may take longer — it includes bwfs's storage
integration tests; scope with `-run TestRoleRequirements` first if a full run is slow, then run
the full package once before committing).

- [ ] **Step 5: Wire it into `main.go`**

In `src/cmd/bwfs/main.go`, change:

```go
		if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, nil, func(s *grpc.Server) {
```

to:

```go
		if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, roleRequirements(), func(s *grpc.Server) {
```

- [ ] **Step 6: Verify the build**

Run: `cd src && go build ./cmd/bwfs/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/bwfs/authz.go src/cmd/bwfs/authz_test.go src/cmd/bwfs/main.go
git commit -m "feat(bwfs): enforce client role on backup/list/restore RPCs"
```

---

### Task 12: Documentation, demo enrollment, and changelog

Per `.claude/CLAUDE.md`'s documentation rules: this is both a gRPC protocol change (Task 3's
`role` field, plus new authorization behavior on every RPC in Tasks 7-11) and a feature change
(new `--role` flag, new enforcement behavior), so protocol docs, component docs, and the
architecture/security docs must all be updated before this is considered mergeable, plus a
changelog entry.

**Files:**
- Modify: `docs/protocols/clientmanager-admin.md`
- Modify: `docs/protocols/catalog-sync.md`
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/protocols/backup.md`
- Modify: `docs/protocols/list.md`
- Modify: `docs/protocols/restore.md`
- Modify: `docs/components/client-manager.md`
- Modify: `docs/components/clientmanager-admin-api.md`
- Modify: `docs/components/clientmanager-api.md`
- Modify: `docs/components/catalog.md`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/bwfs.md`
- Modify: `docs/SECURITY.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `demo/up.sh`
- Modify: `CHANGELOG.md`

No test steps — this task is documentation and a shell script, not Go code (`demo/up.sh` cannot be
exercised without the full Docker Compose stack, which is out of scope here; a plain `sh -n
demo/up.sh` syntax check is the only feasible verification).

- [ ] **Step 1: Update `docs/protocols/clientmanager-admin.md`**

Replace:

```
mTLS (`common/mtls`, same transport every other gRPC call in this project uses). `api-server`
is the sole intended caller — see [Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
for why this isn't enforced at the transport layer (the existing mesh-wide "any operating-tier cert
may call any RPC it can reach" convention applies here too, deliberately).
```

with:

```
mTLS (`common/mtls`, same transport every other gRPC call in this project uses). `api-server`
is the sole intended caller, and every RPC on this service now requires the caller's operating
certificate to carry the `control-plane` authorization role — enforced by a
`common/mtls.RequireRoles` gRPC interceptor, rejecting any other role (e.g. an ordinary
`bwfs`/`brfs`/`rwfs` node's `client` role) with `codes.PermissionDenied` before any handler runs.
See [Design: Role-Based gRPC Authorization](../superpowers/specs/2026-08-22-role-based-grpc-authz-design.md)
and [Security Model](../SECURITY.md#role-based-rpc-authorization).
```

In the same file's `## RPC` code block, update `AddClientRequest`/`ReEnrollClientRequest` to match
Task 3's proto (add the `role` field to each). Add a new bullet to `## Behavior`:

```
- **`role`** (both `AddClient`/`ReEnrollClient`): the node's authorization role — `control-plane`,
  `store`, or `client`. Empty resolves to `client` on `AddClient` (the same default
  `client-manager add` uses); empty means "keep the currently stored role" on `ReEnrollClient`. An
  unrecognized value returns `codes.InvalidArgument` before minting a token.
```

- [ ] **Step 2: Update `docs/protocols/catalog-sync.md`**

Add a new section after `## Identity`, before `## ListEntries`:

```
## Authorization

`SyncFileVersions` requires the caller's operating certificate to carry the `store` role —
`catalogsync` always runs on the same host as the `bwfs` node it replicates from, enrolled with
`authz-role=store`. `ListEntries`/`ListClientFacets`/`ListJobFacets`/`ListDirectoryFacets`/
`ListStoreFacets`/`ListDirectoryChildren` require `control-plane` (`api-server`'s role). See
[Design: Role-Based gRPC Authorization](../superpowers/specs/2026-08-22-role-based-grpc-authz-design.md).
```

- [ ] **Step 3: Update `docs/protocols/policy-server.md`**

Append a new paragraph to the end of the existing `## Authorization` section:

```

Beyond peer identity, every RPC except `GetPolicies` now also requires the caller's operating
certificate to carry the `control-plane` role — `GetPolicies` deliberately stays open to every
role, since every enrolled node (`client`, `store`, and `control-plane` alike) calls it on a
schedule to fetch its own policies. See
[Design: Role-Based gRPC Authorization](../superpowers/specs/2026-08-22-role-based-grpc-authz-design.md).
```

- [ ] **Step 4: Update `docs/protocols/backup.md`, `docs/protocols/list.md`, `docs/protocols/restore.md`**

Add to each, immediately after its `## Protocol Definition`/`## Protocol Flow` intro (before the
first detailed section):

`backup.md`:
```
## Authorization

`ProcessBackupStream` and `BackupCommit` require the caller's operating certificate to carry the
`client` role — `brfs` is the only legitimate caller. See
[Design: Role-Based gRPC Authorization](../superpowers/specs/2026-08-22-role-based-grpc-authz-design.md).
```

`list.md`:
```
## Authorization

`ListFiles` and `ResolveRestoreFiles` require the caller's operating certificate to carry the
`client` role — `rwfs` is the only legitimate caller. See
[Design: Role-Based gRPC Authorization](../superpowers/specs/2026-08-22-role-based-grpc-authz-design.md).
```

`restore.md`:
```
## Authorization

`RestoreFile` requires the caller's operating certificate to carry the `client` role — `rwfs` is
the only legitimate caller. See
[Design: Role-Based gRPC Authorization](../superpowers/specs/2026-08-22-role-based-grpc-authz-design.md).
```

- [ ] **Step 5: Update `docs/components/client-manager.md`**

Add a row to the flags table (the shared `add`/`re-enroll` table):

```
| `--role` | `client` on `add`; keep existing on `re-enroll` | Authorization role: `control-plane`, `store`, or `client` |
```

Add a bullet to `## Behavior`:

```
- `add`'s `--role` (default `client`) and `re-enroll`'s `--role` (default: keep the currently
  stored role) set the node's authorization role, stored as the reserved `authz-role` attribute
  and enforced by every role-gated RPC in the mesh — see
  [Security Model](../SECURITY.md#role-based-rpc-authorization).
```

- [ ] **Step 6: Update `docs/components/clientmanager-admin-api.md`, `docs/components/clientmanager-api.md`**

Add to each, after `## How It Works`:

`clientmanager-admin-api.md`:
```
## Authorization

Every RPC requires the caller to hold the `control-plane` role — enforced by a
`common/mtls.RequireRoles` interceptor wired into this service's listener. `api-server` is
enrolled with `authz-role=control-plane`; an ordinary fleet node's operating certificate (`client`
or `store` role) is rejected with `codes.PermissionDenied` before any handler runs. See
[Security Model](../SECURITY.md#role-based-rpc-authorization).
```

`clientmanager-api.md`:
```
## Authorization

Both RPCs require the caller to hold the `control-plane` role, enforced the same way as
[clientmanager-admin-api](./clientmanager-admin-api.md). See
[Security Model](../SECURITY.md#role-based-rpc-authorization).
```

- [ ] **Step 7: Update `docs/components/catalog.md`, `docs/components/policy-server.md`, `docs/components/bwfs.md`**

Add to each, after its intro paragraph and before `## Usage`:

`catalog.md`:
```
## Authorization

`SyncFileVersions` requires the `store` role; the six read-only query RPCs require
`control-plane`. See [Security Model](../SECURITY.md#role-based-rpc-authorization).
```

`policy-server.md`:
```
## Authorization

`GetPolicies` is open to every role. `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy`
require the `control-plane` role. See [Security Model](../SECURITY.md#role-based-rpc-authorization).
```

`bwfs.md`:
```
## Authorization

Every RPC across `BackupService`, `ListService`, and `RestoreService` requires the `client` role.
See [Security Model](../SECURITY.md#role-based-rpc-authorization).
```

- [ ] **Step 8: Add a new section to `docs/SECURITY.md`**

Add, after the `## The two-tier credential model` section and before `## Revocation and its
trust-model costs`:

```
## Role-based RPC authorization

The two-tier credential model above governs *which stage* of a node's lifecycle a certificate is
valid for (bootstrap vs. operating); it says nothing about *which RPCs* an operating certificate
may call. Historically, every operating-tier certificate was interchangeable: any enrolled node —
including the least-privileged thing in the fleet, an ordinary `bwfs`/`brfs`/`rwfs` backup-agent
host — could call any RPC on any control-plane service it could reach, including
`clientmanager-admin-api`'s CA-admin-equivalent writes (mint enrollment tokens, revoke arbitrary
nodes, rewrite SAN/attribute data).

Every node is now additionally assigned one of three closed roles at enrollment —
`control-plane`, `store`, or `client` — stored as the reserved `authz-role` attribute
(`client-manager attribute set <hostname> authz-role=...`, or the `--role` flag on
`add`/`re-enroll`) and carried in every issued operating certificate via the existing `attribute`
X.509 extension described above. Every gRPC server (`clientmanager-api`,
`clientmanager-admin-api`, `catalog`, `policy-server`, `bwfs`) enforces a per-RPC allowed-role
list via a `common/mtls.RequireRoles` gRPC interceptor, reading the caller's role off its
already-verified peer certificate — never a request field. A caller whose role isn't in an RPC's
allow-list is rejected with `codes.PermissionDenied` before any handler logic runs. `issuer`'s own
listener is untouched — it's already gated by the orthogonal EKU bootstrap/operating tier check
above.

No backward-compatibility path exists for this: a node without a matching `authz-role` attribute
is denied every role-gated RPC (though `GetPolicies` stays open to every role, so its own
certificate lifecycle keeps functioning) until it's backfilled or re-enrolled. See
[Design: Role-Based gRPC Authorization](superpowers/specs/2026-08-22-role-based-grpc-authz-design.md)
for the full per-RPC matrix.
```

- [ ] **Step 9: Update `docs/ARCHITECTURE.md`**

Add, immediately after the existing "Control Plane vs. Agents" table:

```

Each control-plane component above is enrolled with the `control-plane` authorization role;
`bwfs` (and `catalogsync`, which shares its host) is enrolled with `store`; every other agent node
defaults to `client`. Every gRPC server enforces this per RPC — see
[Security Model](SECURITY.md#role-based-rpc-authorization).
```

- [ ] **Step 10: Update `demo/up.sh`**

Change each of these five `enroll` calls (only the `attrs` argument changes; `enroll`'s own
function body and every other call are untouched):

```sh
enroll clientmanager-api
enroll catalog
enroll api-server
enroll policy-server
enroll database
enroll webserver "role=web"
enroll store
```

to:

```sh
enroll clientmanager-api "authz-role=control-plane"
enroll catalog "authz-role=control-plane"
enroll api-server "authz-role=control-plane"
enroll policy-server "authz-role=control-plane"
enroll database
enroll webserver "role=web"
enroll store "authz-role=store"
```

(`database`, `webserver`, and `log-gateway` are left unchanged — they default to `authz-role=client`
via `client-manager add`'s own default, which is correct for ordinary backup-agent hosts.)

- [ ] **Step 11: Syntax-check the shell script**

Run: `sh -n demo/up.sh`
Expected: no output, exit code 0.

- [ ] **Step 12: Add a CHANGELOG entry**

In `CHANGELOG.md`, insert a new entry at the top, immediately after the `# Changelog` header and
its intro line, before the existing `## 2026-08-22 — Fix demo reset instructions...` entry:

```markdown
## 2026-08-22 — Role-based gRPC authorization

Any node holding a valid operating certificate — including the least-privileged thing in the
fleet, an ordinary backup-agent host — could previously call any RPC on any control-plane service
it could reach, including `clientmanager-admin-api`'s CA-admin-equivalent writes (mint enrollment
tokens, revoke arbitrary nodes). Every node is now assigned one of three closed roles
(`control-plane`, `store`, `client`) at enrollment, carried in its operating certificate via the
existing `attribute` extension and enforced per RPC by a new gRPC interceptor across
`clientmanager-api`, `clientmanager-admin-api`, `catalog`, `policy-server`, and `bwfs`. See
`docs/SECURITY.md#role-based-rpc-authorization`.

```

- [ ] **Step 13: Commit**

```bash
git add docs/protocols/clientmanager-admin.md docs/protocols/catalog-sync.md docs/protocols/policy-server.md docs/protocols/backup.md docs/protocols/list.md docs/protocols/restore.md docs/components/client-manager.md docs/components/clientmanager-admin-api.md docs/components/clientmanager-api.md docs/components/catalog.md docs/components/policy-server.md docs/components/bwfs.md docs/SECURITY.md docs/ARCHITECTURE.md demo/up.sh CHANGELOG.md
git commit -m "docs: document role-based gRPC authorization"
```

---

## Final verification

- [ ] Run `cd src && go build ./... && go vet ./... && go test ./... -count=1` from the repo root
  and confirm everything passes.
- [ ] Run `sh -n demo/up.sh` and confirm it's still syntactically valid.
- [ ] Skim `git log --oneline` for this branch and confirm every task's commit is present in
  order (Tasks 1-12).
