# Client Manager Phase 2a: SAN Management & Certrequest Retirement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `client-manager` the ability to add/remove a client's SAN aliases after enrollment, and retire `certrequest` entirely by having `client-manager` mint enrollment tokens directly, in-process — the foundational half of phase 2 (see `docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md`), independent of the listening-service/enforcement work that follows in a later plan.

**Architecture:** `client-manager` now holds the CA's provisioner password directly (via the existing `common/certmint` package) instead of dialing `certrequest serve` over gRPC — `certrequest` (both its one-shot CLI and `serve` mode) is deleted outright, along with the `enrollment_broker` proto it existed to serve. SAN aliases join `description`/`attribute` as another piece of per-client state, stored on the same `ClientRecord` phase 1 already added a `SANs` field to.

**Tech Stack:** Go, cobra (CLI), gorm + `modernc.org/sqlite` (existing store, no schema migration needed — `SANs` column already exists), `common/certmint` (existing token-minting package, now called in-process instead of over gRPC).

## Global Constraints

- No network interface on `client-manager` — everything in this plan is local CLI + local DB. (Confirmed requirement from the phase-2 design's core decision; the listening service that *does* need a network interface is out of scope for this plan.)
- SAN changes are a stored fact only in this plan — they don't yet get baked into any certificate automatically. That requires the listening service (a later plan); `re-enroll` (already existing, unchanged) is today's only way to actually mint a token reflecting a client's current SAN list.
- `certmint.Mint`'s existing signature (`func Mint(hostname string, sans []string, opts Options) (string, error)`) is not changed — `client-manager` calls it as-is; no wrapper needed since it already matches the `minter` type this plan introduces.
- No source code outside `src/cmd/clientmanager`, `src/storage/clientmanager`, `src/common/config`, `src/common/certmint`, and the deletion of `src/cmd/certrequest`/`src/api/enrollment_broker*` is touched by this plan.

---

## File Structure

| File | Responsibility |
|---|---|
| `src/storage/clientmanager/store.go` (modify) | `AddSAN`/`RemoveSAN` methods |
| `src/storage/clientmanager/store_test.go` (modify) | Tests for the above |
| `src/cmd/clientmanager/san.go` (new) | `runSanAdd`/`runSanRemove` |
| `src/cmd/clientmanager/san_test.go` (new) | Tests for the above |
| `src/cmd/clientmanager/list.go` (modify) | `show` also displays a client's current SAN list |
| `src/cmd/clientmanager/arguments.go` (modify) | `san add`/`san remove` subcommands (Task 2); provisioner-credential flags on `add`/`re-enroll` (Task 3) |
| `src/cmd/clientmanager/mint.go` (new, replaces `broker_client.go`) | `minter` type, now matching `certmint.Mint`'s own signature directly |
| `src/cmd/clientmanager/broker_client.go` (deleted) | Superseded by `mint.go` — no more network broker |
| `src/cmd/clientmanager/add.go` (modify) | `runAdd`/`runReEnroll` take `certmint.Options` instead of `conf, certsDir` |
| `src/cmd/clientmanager/add_test.go` (modify) | Updated stub signatures, one new test for the threaded-through options |
| `src/cmd/clientmanager/main.go` (modify) | Drop `certsDir` (client-manager makes no network calls at all now); wire `certmint.Mint` directly |
| `src/cmd/certrequest/` (deleted entirely) | Retired — `client-manager` absorbs its role |
| `src/api/enrollment_broker.proto`, `.pb.go`, `_grpc.pb.go` (deleted) | No longer served by anything |
| `src/common/config/config.go`, `config_test.go` (modify) | Remove `ClientManagerHost`/`CertrequestHost`/`CertrequestPort` |
| `src/common/certmint/certmint.go` (modify) | Doc comment update — no longer "shared by certrequest's two modes" |
| `Makefile` (modify) | Remove the `certrequest` build target/variable and its `test-e2e` reference |
| `docs/components/certrequest.md`, `docs/protocols/enrollment-broker.md` (deleted) | No longer exist |
| `docs/components/client-manager.md`, `docs/ARCHITECTURE.md`, `README.md`, `deploy/control-plane/README.md` (modify) | Reflect the retirement and new SAN commands |

---

### Task 1: `storage/clientmanager` — `AddSAN`/`RemoveSAN`

**Files:**
- Modify: `src/storage/clientmanager/store.go`
- Test: `src/storage/clientmanager/store_test.go`

**Interfaces:**
- Consumes: `ClientRecord.SANsList() []string` (existing, `models.go`), `ErrClientNotFound` (existing).
- Produces: `(*Store).AddSAN(hostname, alias string) error`, `(*Store).RemoveSAN(hostname, alias string) error` — both idempotent (adding an already-present alias, or removing an absent one, is a no-op, not an error) and both return `ErrClientNotFound` for an untracked hostname.

- [ ] **Step 1: Write the failing tests**

Append to `src/storage/clientmanager/store_test.go`:

```go
func TestAddSAN_AppendsToEmptyList(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	require.NoError(t, store.AddSAN("node-1", "node-1.internal"))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"node-1.internal"}, got.SANsList())
}

func TestAddSAN_DuplicateIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"a.internal"}, time.Now()))

	require.NoError(t, store.AddSAN("node-1", "a.internal"))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.internal"}, got.SANsList())
}

func TestAddSAN_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.AddSAN("ghost", "a.internal")
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestRemoveSAN_RemovesExistingAlias(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"a.internal", "b.internal"}, time.Now()))

	require.NoError(t, store.RemoveSAN("node-1", "a.internal"))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"b.internal"}, got.SANsList())
}

func TestRemoveSAN_NonExistentAliasIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"a.internal"}, time.Now()))

	require.NoError(t, store.RemoveSAN("node-1", "z.internal"))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.internal"}, got.SANsList())
}

func TestRemoveSAN_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.RemoveSAN("ghost", "a.internal")
	assert.ErrorIs(t, err, ErrClientNotFound)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./storage/clientmanager/... -run 'TestAddSAN|TestRemoveSAN' -v`
Expected: FAIL — `store.AddSAN`/`store.RemoveSAN` undefined (compile error).

- [ ] **Step 3: Implement**

Append to `src/storage/clientmanager/store.go` (it already imports `encoding/json`, used by `AddClient` — no new imports needed):

```go
// AddSAN appends alias to hostname's SAN list if not already present -- a
// no-op, not an error, if it's already there. Returns ErrClientNotFound if
// hostname isn't tracked.
func (s *Store) AddSAN(hostname, alias string) error {
	rec, err := s.GetClient(hostname)
	if err != nil {
		return err
	}
	sans := rec.SANsList()
	for _, existing := range sans {
		if existing == alias {
			return nil
		}
	}
	return s.setSANs(hostname, append(sans, alias))
}

// RemoveSAN removes alias from hostname's SAN list if present -- a no-op,
// not an error, if it isn't there. Returns ErrClientNotFound if hostname
// isn't tracked.
func (s *Store) RemoveSAN(hostname, alias string) error {
	rec, err := s.GetClient(hostname)
	if err != nil {
		return err
	}
	sans := rec.SANsList()
	filtered := make([]string, 0, len(sans))
	for _, existing := range sans {
		if existing != alias {
			filtered = append(filtered, existing)
		}
	}
	return s.setSANs(hostname, filtered)
}

func (s *Store) setSANs(hostname string, sans []string) error {
	sansJSON, err := json.Marshal(sans)
	if err != nil {
		return fmt.Errorf("marshal sans: %w", err)
	}
	return s.db.Model(&ClientRecord{}).Where("hostname = ?", hostname).Update("sans", string(sansJSON)).Error
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./storage/clientmanager/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/storage/clientmanager/store.go src/storage/clientmanager/store_test.go
git commit -m "feat(clientmanager): add AddSAN/RemoveSAN store methods"
```

---

### Task 2: `client-manager` CLI — `san add`/`san remove`

**Files:**
- Create: `src/cmd/clientmanager/san.go`
- Create: `src/cmd/clientmanager/san_test.go`
- Modify: `src/cmd/clientmanager/arguments.go`
- Modify: `src/cmd/clientmanager/main.go`
- Modify: `src/cmd/clientmanager/list.go`

**Interfaces:**
- Consumes: `Store.AddSAN`/`RemoveSAN` (Task 1), `newTestManagerStore(t)` (existing helper, `add_test.go`).
- Produces: `runSanAdd(store *clientmanagerstore.Store, args *Arguments) error`, `runSanRemove(store *clientmanagerstore.Store, args *Arguments) error`. New `Arguments.SanAlias string` field.

- [ ] **Step 1: Write `san.go`**

`src/cmd/clientmanager/san.go`:

```go
package main

import (
	"fmt"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func runSanAdd(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.AddSAN(args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("add san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}

func runSanRemove(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.RemoveSAN(args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("remove san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}
```

- [ ] **Step 2: Extend `Arguments` and add the `san` command tree**

In `src/cmd/clientmanager/arguments.go`, first fix the stale comment on `SANs` (it currently reads `// "key=value" strings, for description/attribute set (Task 9)`, left over from a copy-paste — it has nothing to do with description/attribute) and add the new `SanAlias` field:

```go
type Arguments struct {
	Action   string // "add" | "re-enroll" | "list" | "show" | "revoke" | "unrevoke" |
	                 // "description-set" | "description-unset" | "attribute-set" | "attribute-unset" |
	                 // "san-add" | "san-remove"
	Hostname string
	SANs     []string // Additional SAN aliases for add/re-enroll
	KVPairs  []string // "key=value" strings, for description/attribute set
	Key      string   // for description/attribute unset
	SanAlias string   // for san add/remove
}
```

Then, immediately before the final `rootCmd.AddCommand(...)` line, add:

```go
	sanCmd := &cobra.Command{Use: "san", Short: "Manage a client's SAN aliases"}
	sanCmd.AddCommand(
		&cobra.Command{
			Use:   "add <hostname> <alias>",
			Short: "Add a SAN alias (applied on the client's next credential refresh)",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "san-add"
				args.Hostname = cliArgs[0]
				args.SanAlias = cliArgs[1]
				return nil
			},
		},
		&cobra.Command{
			Use:   "remove <hostname> <alias>",
			Short: "Remove a SAN alias (applied on the client's next credential refresh)",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "san-remove"
				args.Hostname = cliArgs[0]
				args.SanAlias = cliArgs[1]
				return nil
			},
		},
	)
```

Change the existing:

```go
	rootCmd.AddCommand(addCmd, reEnrollCmd, listCmd, showCmd, revokeCmd, unrevokeCmd, descriptionCmd, attributeCmd)
```

to:

```go
	rootCmd.AddCommand(addCmd, reEnrollCmd, listCmd, showCmd, revokeCmd, unrevokeCmd, descriptionCmd, attributeCmd, sanCmd)
```

And update the "subcommand is required" error message:

```go
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: add, re-enroll, list, show, revoke, unrevoke, description, attribute, san")
	}
```

- [ ] **Step 3: Wire into `run()`**

In `src/cmd/clientmanager/main.go`, add two cases to the `switch` in `run(...)`:

```go
	case "san-add":
		return runSanAdd(store, args)
	case "san-remove":
		return runSanRemove(store, args)
```

- [ ] **Step 4: `show` displays the current SAN list**

In `src/cmd/clientmanager/list.go`, in `runShow`, add a line right after the `added_at:` line:

```go
	fmt.Fprintf(out, "added_at:   %s\n", client.AddedAt.Format(timeLayout))
	sans := client.SANsList()
	if len(sans) == 0 {
		fmt.Fprintln(out, "sans:       (none)")
	} else {
		fmt.Fprintf(out, "sans:       %s\n", strings.Join(sans, ", "))
	}
```

Add `"strings"` to `list.go`'s import block.

- [ ] **Step 5: Write the failing tests, then confirm they pass**

`src/cmd/clientmanager/san_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func TestRunSanAdd_AddsAlias(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	err := runSanAdd(store, &Arguments{Hostname: "node-1", SanAlias: "node-1.internal"})
	require.NoError(t, err)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"node-1.internal"}, got.SANsList())
}

func TestRunSanAdd_UnknownHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	err := runSanAdd(store, &Arguments{Hostname: "ghost", SanAlias: "x.internal"})
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunSanRemove_RemovesAlias(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"a.internal"}, time.Now()))

	err := runSanRemove(store, &Arguments{Hostname: "node-1", SanAlias: "a.internal"})
	require.NoError(t, err)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Empty(t, got.SANsList())
}

func TestRunSanRemove_UnknownHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	err := runSanRemove(store, &Arguments{Hostname: "ghost", SanAlias: "x.internal"})
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}
```

Run: `cd src && go test ./cmd/clientmanager/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/clientmanager/san.go src/cmd/clientmanager/san_test.go src/cmd/clientmanager/arguments.go src/cmd/clientmanager/main.go src/cmd/clientmanager/list.go
git commit -m "feat(clientmanager): add san add/remove commands"
```

---

### Task 3: `client-manager` mints tokens directly (retires the broker dependency)

**Files:**
- Create: `src/cmd/clientmanager/mint.go`
- Delete: `src/cmd/clientmanager/broker_client.go`
- Modify: `src/cmd/clientmanager/add.go`
- Modify: `src/cmd/clientmanager/add_test.go`
- Modify: `src/cmd/clientmanager/arguments.go`
- Modify: `src/cmd/clientmanager/main.go`

**Interfaces:**
- Consumes: `certmint.Mint(hostname string, sans []string, opts certmint.Options) (string, error)` and `certmint.Options{CAURL, RootFile, Provisioner, PasswordFile string}` (existing, `common/certmint`).
- Produces: `type minter func(hostname string, sans []string, opts certmint.Options) (string, error)`; `runAdd(mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error`; `runReEnroll(mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error`; `run(mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, out io.Writer) error`.

- [ ] **Step 1: Delete the broker client, add the `minter` type**

Delete `src/cmd/clientmanager/broker_client.go` entirely.

Create `src/cmd/clientmanager/mint.go`:

```go
// client-manager mints enrollment tokens directly, in-process, using the
// same common/certmint package certrequest used to. This replaces the
// certrequest-serve broker from phase 1 -- see
// docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md for
// why that's safe now that client-manager runs on the CA host directly,
// rather than a separate, less-trusted one.
package main

import "github.com/alex-sviridov/miniprotector/common/certmint"

// minter mints an enrollment token for hostname/sans using the given
// provisioner credentials. certmint.Mint's own signature already matches
// this exactly, so production code passes it directly with no wrapper;
// tests inject a stub.
type minter func(hostname string, sans []string, opts certmint.Options) (string, error)
```

- [ ] **Step 2: Update `add.go` to the new signature**

Replace `src/cmd/clientmanager/add.go` in full:

```go
package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/alex-sviridov/miniprotector/common/certmint"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func runAdd(mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	if _, err := store.GetClient(args.Hostname); err == nil {
		return fmt.Errorf("client %q already exists; use re-enroll or description/attribute set instead", args.Hostname)
	} else if !errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return fmt.Errorf("check existing client: %w", err)
	}

	token, err := mint(args.Hostname, args.SANs, mintOpts)
	if err != nil {
		return fmt.Errorf("add %s: %w", args.Hostname, err)
	}

	if err := store.AddClient(args.Hostname, args.SANs, time.Now()); err != nil {
		return fmt.Errorf("record client %s: %w", args.Hostname, err)
	}

	fmt.Fprintln(out, token)
	return nil
}

func runReEnroll(mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	client, err := store.GetClient(args.Hostname)
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

	fmt.Fprintln(out, token)
	return nil
}
```

- [ ] **Step 3: Update `add_test.go`'s stub signatures**

Replace `src/cmd/clientmanager/add_test.go` in full:

```go
package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/certmint"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func newTestManagerStore(t *testing.T) *clientmanagerstore.Store {
	t.Helper()
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRunAdd_MintsAndRecordsClient(t *testing.T) {
	store := newTestManagerStore(t)
	var out bytes.Buffer
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		assert.Equal(t, "node-1", hostname)
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(certmint.Options{}, store, args, stubMint, &out)
	require.NoError(t, err)
	assert.Equal(t, "tok-abc\n", out.String())

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", got.Hostname)
}

func TestRunAdd_DuplicateHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	called := false
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		called = true
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)
	assert.False(t, called, "mint must not be called for a duplicate add")
}

func TestRunAdd_MintFailureDoesNotRecordClient(t *testing.T) {
	store := newTestManagerStore(t)
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		return "", assert.AnError
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)

	_, err = store.GetClient("node-1")
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunAdd_PassesMintOptsThrough(t *testing.T) {
	store := newTestManagerStore(t)
	wantOpts := certmint.Options{CAURL: "https://ca.internal:9000", Provisioner: "admin@backup.internal"}
	var gotOpts certmint.Options
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		gotOpts = opts
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(wantOpts, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, wantOpts, gotOpts)
}

func TestRunReEnroll_UnknownHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		t.Fatal("mint must not be called for an unknown hostname")
		return "", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "ghost"}
	err := runReEnroll(certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)
}

func TestRunReEnroll_MintsFreshToken(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	var out bytes.Buffer
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1"}
	err := runReEnroll(certmint.Options{}, store, args, stubMint, &out)
	require.NoError(t, err)
	assert.Equal(t, "tok-fresh\n", out.String())
}

func TestRunReEnroll_NoSANOverride_ReusesStoredSANsFromAdd(t *testing.T) {
	store := newTestManagerStore(t)
	addSANs := []string{"alias1", "alias2"}
	require.NoError(t, store.AddClient("node-1", addSANs, time.Now()))

	var gotSANs []string
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		gotSANs = sans
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1"}
	err := runReEnroll(certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, addSANs, gotSANs)
}

func TestRunReEnroll_WithSANOverride_UsesOverrideNotStoredSANs(t *testing.T) {
	store := newTestManagerStore(t)
	addSANs := []string{"alias1", "alias2"}
	require.NoError(t, store.AddClient("node-1", addSANs, time.Now()))

	overrideSANs := []string{"override1"}
	var gotSANs []string
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		gotSANs = sans
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1", SANs: overrideSANs}
	err := runReEnroll(certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, overrideSANs, gotSANs)
}
```

- [ ] **Step 4: Add provisioner-credential flags to `add`/`re-enroll`**

In `src/cmd/clientmanager/arguments.go`, add four fields to `Arguments` (alongside the existing ones):

```go
	// Provisioner credentials for add/re-enroll -- client-manager holds
	// the CA's provisioner password directly, replacing certrequest.
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
```

At the top of `parseArguments()`, declare two local vars alongside `args := &Arguments{}`:

```go
	var caURLFlag, defaultsFile string
```

Add flags to `addCmd` (after its existing `--san` flag registration):

```go
	addCmd.Flags().StringVar(&caURLFlag, "ca-url", "", "CA URL, e.g. https://localhost:9000 (default: read from --defaults-file)")
	addCmd.Flags().StringVar(&defaultsFile, "defaults-file", "deploy/control-plane/ca/data/config/defaults.json", "Path to step-ca's defaults.json, used to default --ca-url")
	addCmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	addCmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	addCmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")
```

Add the identical five flags to `reEnrollCmd` (same flag names/defaults, after its existing `--san` flag registration) — copy the block above verbatim, replacing `addCmd.Flags()` with `reEnrollCmd.Flags()`.

After `rootCmd.Execute()` succeeds (right before the existing `if args.Action == "" { ... }` check), add:

```go
	if args.Action == "add" || args.Action == "re-enroll" {
		args.CAURL = caURLFlag
		if args.CAURL == "" {
			defaultURL, err := readDefaultCAURL(defaultsFile)
			if err != nil {
				return nil, fmt.Errorf("--ca-url not given and could not be read from %s: %w", defaultsFile, err)
			}
			args.CAURL = defaultURL
		}
	}
```

Add this helper function at the end of the file (identical in behavior to `certrequest`'s own, which is being deleted in Task 4 — this small helper is duplicated rather than shared, since certrequest is going away entirely and there'd be nothing left to share with):

```go
// readDefaultCAURL reads the "ca-url" field out of step-ca's defaults.json.
func readDefaultCAURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var defaults struct {
		CAURL string `json:"ca-url"`
	}
	if err := json.Unmarshal(data, &defaults); err != nil {
		return "", err
	}
	if defaults.CAURL == "" {
		return "", fmt.Errorf("%s has no ca-url field", path)
	}
	return defaults.CAURL, nil
}
```

Add `"encoding/json"` and `"os"` to `arguments.go`'s import block.

- [ ] **Step 5: Rewire `main.go`**

Replace `src/cmd/clientmanager/main.go` in full:

```go
// client-manager owns the persistent list of enrolled clients: when they
// were added, their annotations and RBAC attributes, their SAN aliases,
// and whether they've been revoked. It holds the CA's provisioner
// password directly and mints enrollment tokens in-process -- see
// docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md for
// why this replaced the separate certrequest/certrequest-serve broker.
// See docs/components/client-manager.md.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/config"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func main() {
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

	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
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

	mintOpts := certmint.Options{
		CAURL:        args.CAURL,
		RootFile:     args.RootFile,
		Provisioner:  args.Provisioner,
		PasswordFile: args.PasswordFile,
	}

	if err := run(mintOpts, store, args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// run dispatches on args.Action. Broken out from main so tests can drive
// it directly against a temp-dir store without touching os.Exit.
func run(mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, out io.Writer) error {
	switch args.Action {
	case "add":
		return runAdd(mintOpts, store, args, certmint.Mint, out)
	case "re-enroll":
		return runReEnroll(mintOpts, store, args, certmint.Mint, out)
	case "list":
		return runList(store, out)
	case "show":
		return runShow(store, args, out)
	case "revoke":
		return runRevoke(store, args)
	case "unrevoke":
		return runUnrevoke(store, args)
	case "description-set":
		return runKVSet(store, clientmanagerstore.KindDescription, args)
	case "description-unset":
		return runKVUnset(store, clientmanagerstore.KindDescription, args)
	case "attribute-set":
		return runKVSet(store, clientmanagerstore.KindAttribute, args)
	case "attribute-unset":
		return runKVUnset(store, clientmanagerstore.KindAttribute, args)
	case "san-add":
		return runSanAdd(store, args)
	case "san-remove":
		return runSanRemove(store, args)
	default:
		return fmt.Errorf("unknown action %q", args.Action)
	}
}
```

- [ ] **Step 6: Run tests to verify everything passes**

Run: `cd src && go build ./cmd/clientmanager/... && go test ./cmd/clientmanager/... -v`
Expected: PASS (all tests across `add_test.go`, `list_test.go`, `label_test.go`, `san_test.go`).

- [ ] **Step 7: Commit**

```bash
git add src/cmd/clientmanager/
git commit -m "feat(clientmanager): mint tokens directly, retiring the certrequest-serve broker dependency"
```

---

### Task 4: Retire `certrequest` and the `enrollment_broker` proto

**Files:**
- Delete: `src/cmd/certrequest/` (entire directory: `main.go`, `arguments.go`, `arguments_test.go`, `broker_server.go`, `broker_server_test.go`, `broker_server_realmtls_test.go`, `e2e_test.go`, `serve_e2e_test.go`)
- Delete: `src/api/enrollment_broker.proto`, `src/api/enrollment_broker.pb.go`, `src/api/enrollment_broker_grpc.pb.go`
- Modify: `src/common/config/config.go`, `src/common/config/config_test.go`
- Modify: `src/common/certmint/certmint.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing new — this task only removes now-unused code. No other task depends on anything here.

- [ ] **Step 1: Delete `certrequest` and the proto it existed to serve**

```bash
git rm -r src/cmd/certrequest
git rm src/api/enrollment_broker.proto src/api/enrollment_broker.pb.go src/api/enrollment_broker_grpc.pb.go
```

- [ ] **Step 2: Remove the now-unused config fields**

In `src/common/config/config.go`, remove these three fields from the `Config` struct:

```go
	ClientManagerHost          string
	CertrequestHost            string
	CertrequestPort            int
```

Remove `CertrequestPort: 9100,` from the defaults literal in `ParseConfig`.

Remove these three `case`s from the `switch key` block:

```go
	case "client_manager_host":
		config.ClientManagerHost = value
		foundFields["client_manager_host"] = true
	case "certrequest_host":
		config.CertrequestHost = value
		foundFields["certrequest_host"] = true
	case "certrequest_port":
		port, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("invalid certrequest_port value at line %d: %s", lineNum, value)
		}
		config.CertrequestPort = port
		foundFields["certrequest_port"] = true
```

- [ ] **Step 3: Remove the now-obsolete config tests**

In `src/common/config/config_test.go`, delete these four test functions in full: `TestParseConfig_ClientManagerHostParsesCorrectly`, `TestParseConfig_CertrequestHostParsesCorrectly`, `TestParseConfig_CertrequestPortDefaultsTo9100`, `TestParseConfig_CertrequestPortParsesCorrectly`.

- [ ] **Step 4: Update `certmint`'s doc comment**

In `src/common/certmint/certmint.go`, change the package doc comment from:

```go
// Package certmint mints one-time CA enrollment tokens using a
// provisioner's password-protected key. Shared by certrequest's one-shot
// CLI and its serve mode -- the only two callers that need CA-admin-
// equivalent access to a provisioner's key.
package certmint
```

to:

```go
// Package certmint mints one-time CA enrollment tokens using a
// provisioner's password-protected key. Called directly by client-manager
// -- the only thing in this system that needs CA-admin-equivalent access
// to a provisioner's key.
package certmint
```

- [ ] **Step 5: Update the Makefile**

Remove the line `CERTREQUEST_CMD := cmd/certrequest`.

Remove `certrequest` from the `.PHONY` line:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certrequest certclient catalogsync catalog agent clientmanager test test-e2e lint control-plane-up
```

becomes:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certclient catalogsync catalog agent clientmanager test test-e2e lint control-plane-up
```

Remove the entire `certrequest:` target block:

```makefile
certrequest: $(BINARY_DIR) ## Build certrequest binary
	@printf "$(BLUE)Building certrequest...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/certrequest ./$(CERTREQUEST_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/certrequest"
```

In the `test-e2e` target, remove the now-nonexistent path:

```makefile
	cd src && go test -tags=e2e -timeout=300s ./e2e/... ./cmd/certrequest/...
```

becomes:

```makefile
	cd src && go test -tags=e2e -timeout=300s ./e2e/...
```

- [ ] **Step 6: Verify the whole module still builds and tests clean**

Run: `cd src && go build ./... && go vet ./... && go test ./... 2>&1 | tail -30`
Expected: no build errors, no vet output, `ok` for every package (the pre-existing, unrelated `cmd/brfs` vet warning from earlier work is the only thing `go vet` may still report — everything else must be clean).

Run: `grep -rn "certrequest\|EnrollmentBroker\|ClientManagerHost\|CertrequestHost\|CertrequestPort" src/ 2>/dev/null` — confirm this returns nothing (no leftover references anywhere in source).

- [ ] **Step 7: Commit**

```bash
git add -A src/common/config/config.go src/common/config/config_test.go src/common/certmint/certmint.go Makefile
git commit -m "chore: retire certrequest and the enrollment_broker proto"
```

---

### Task 5: Documentation

**Files:**
- Delete: `docs/components/certrequest.md`, `docs/protocols/enrollment-broker.md`
- Modify: `docs/components/client-manager.md`, `docs/ARCHITECTURE.md`, `README.md`, `deploy/control-plane/README.md`

- [ ] **Step 1: Delete the retired docs**

```bash
git rm docs/components/certrequest.md docs/protocols/enrollment-broker.md
```

- [ ] **Step 2: Rewrite `docs/components/client-manager.md`**

```markdown
# client-manager

Owns the persistent list of enrolled clients: when they were added, free-form annotations
(`description`), attributes intended for baking into a client's certificate (`attribute`), SAN
aliases (`san`), and a revoked flag. Holds the CA's provisioner password directly and mints
enrollment tokens in-process — see
[Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
for why this is safe: `client-manager` runs directly on the CA host, as a single-operator CLI tool
with no network interface of its own, rather than a separate, less-trusted host (phase 1's
original placement — see [Design: Client Manager](../superpowers/specs/2026-07-04-client-manager-design.md)
for that earlier reasoning and why it changed).

## Usage

```
client-manager add <hostname> [--san alias]... [--ca-url url] [--defaults-file path] [--root path] [--provisioner name] [--password-file path]
client-manager re-enroll <hostname> [--san alias]... [--ca-url url] [--defaults-file path] [--root path] [--provisioner name] [--password-file path]
client-manager list
client-manager show <hostname>
client-manager revoke <hostname>
client-manager unrevoke <hostname>

client-manager description set <hostname> k=v [k=v...]
client-manager description unset <hostname> k
client-manager attribute set <hostname> k=v [k=v...]
client-manager attribute unset <hostname> k
client-manager san add <hostname> <alias>
client-manager san remove <hostname> <alias>
```

`add`/`re-enroll` mint a one-time enrollment token directly (the same mechanism `certrequest` used
to provide, now built in) and print it to stdout for the operator to relay out-of-band to the
target node, same as before. Everything else is local SQLite CRUD — `client-manager` has no
network interface at all.

| Flag | Default | Description |
|------|---------|-------------|
| `--san` | | Additional SAN alias for the token (repeatable) |
| `--ca-url` | read from `--defaults-file` | CA URL, e.g. `https://localhost:9000` |
| `--defaults-file` | `deploy/control-plane/ca/data/config/defaults.json` | Path to step-ca's `defaults.json`, used to default `--ca-url` when it isn't given explicitly |
| `--root` | `deploy/control-plane/ca/data/certs/root_ca.crt` | Path to the CA's root certificate |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `deploy/control-plane/ca/data/secrets/password` | Path to the provisioner password file |

## Behavior

- `add` errors if `hostname` is already tracked (use `re-enroll` or `description|attribute|san`
  instead) and records nothing locally unless minting actually succeeded.
- `revoke`/`unrevoke` only set a flag in `client-manager`'s own database in this plan — nothing yet
  blocks a renewal. See the phase-2 design's architecture for the listening service that will
  enforce this, not yet built.
- `attribute`/`san` values are stored only; a client's next `re-enroll` is currently the only way
  to mint a token reflecting a client's current attributes/SANs. Automatic refresh on an ordinary
  credential renewal is what the phase-2 listening service (not yet built) provides.
- `list`'s `LAST_SEEN` column always reads `unknown` — `client-manager` has no visibility into
  renewals, which happen directly between `certclient` and the CA.

## Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `var_path` | binary's own directory | Where `clientmanager.sqlite` lives |

## Building

```bash
make clientmanager
```

## See Also

- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
- [Design: Client Manager (phase 1)](../superpowers/specs/2026-07-04-client-manager-design.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 3: Update `docs/ARCHITECTURE.md`**

Read the file first — this step touches five separate spots, listed in file order, each given as an
exact old → new so there's no ambiguity about which occurrence is meant.

**3a. Components table row** (currently `Implemented (phase 1: no enforcement yet)`):

Change:
```markdown
| client-manager | Owns the enrolled-client list: descriptions, RBAC-bound attributes, revoked status | Implemented (phase 1: no enforcement yet) |
```
to:
```markdown
| client-manager | Owns the enrolled-client list: descriptions, RBAC-bound attributes, SAN aliases, revoked status; mints enrollment tokens directly | Implemented (enforcement — the phase-2 listening service — not yet built) |
```

**3b. "Control Plane vs. Agents" table — `Components` and `Runs where` rows** (`certrequest` no
longer exists as a separate binary):

Change:
```markdown
| Components | `deploy/control-plane/ca/` (step-ca container), `certrequest` (one-shot CLI and `serve` mode), `catalog`, `client-manager` | `bwfs`, `brfs`, `rwfs`, `certclient`, `agent` |
| Runs where | On/near the CA host (`certrequest`); `catalog` runs centrally, wherever the catalog deployment lives — see below | Dial `ca_host:9000` outbound only, for enrollment/renewal; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
```
to:
```markdown
| Components | `deploy/control-plane/ca/` (step-ca container), `catalog`, `client-manager` | `bwfs`, `brfs`, `rwfs`, `certclient`, `agent` |
| Runs where | On the CA host (`client-manager`); `catalog` runs centrally, wherever the catalog deployment lives — see below | Dial `ca_host:9000` outbound only, for enrollment/renewal; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
```

**3c. "Docker/e2e images" row** (`certrequest`'s specific claim no longer applies to anything):

Change:
```markdown
| Docker/e2e images | `certrequest` never ships onto an agent host or into an agent image | Agent images bundle `certclient` only |
```
to:
```markdown
| Docker/e2e images | Control-plane-only binaries (`client-manager`) never ship onto an agent host or into an agent image | Agent images bundle `certclient` only |
```

**3d. The `certclient`-identity sentence:**

Change:
```markdown
A node's mTLS identity (`ca.crt`, `client.crt`, `client.key`, consumed by `common/mtls`) is
obtained via `certclient`, using a token minted by `certrequest`. See
[certrequest](components/certrequest.md) and [certclient](components/certclient.md).
```
to:
```markdown
A node's mTLS identity (`ca.crt`, `client.crt`, `client.key`, consumed by `common/mtls`) is
obtained via `certclient`, using a token minted by `client-manager`. See
[client-manager](components/client-manager.md) and [certclient](components/certclient.md).
```

**3e. The `client-manager` control-plane paragraph** (rewritten in full — it no longer has an mTLS
identity or network role at all, unlike `catalog`, which this paragraph previously compared it to):

Change:
```markdown
`client-manager` is control plane by role (an admin-facing service tracking the enrolled-client
fleet) but, like `catalog`, bootstraps its own mTLS identity the same way agents do, via
`certclient`. Its only network role is calling `certrequest serve`'s `MintEnrollmentToken` RPC —
see [Design: Client Manager](superpowers/specs/2026-07-04-client-manager-design.md) for why
token-minting is routed through a narrow broker rather than giving `client-manager` the CA's
provisioner password directly.
```
to:
```markdown
`client-manager` is control plane by role (an admin-facing tool tracking the enrolled-client
fleet) but, unlike every other component in this table, has no mTLS identity and no network
interface of its own at all — it runs directly on the CA host as a single-operator CLI, holding
the CA's provisioner password directly. See
[Design: Client Manager Phase 2](superpowers/specs/2026-07-04-client-manager-phase2-design.md)
for why that's safe now, having originally been placed on a separate host in phase 1 specifically
to avoid this.
```

- [ ] **Step 4: Update `README.md`**

Remove the `certrequest` bullet from the Components list entirely:

```markdown
- **[certrequest](docs/components/certrequest.md)** - Mints one-time enrollment tokens for nodes (control-plane, run on/near the CA)
```

Update the `client-manager` bullet:

```markdown
- **[client-manager](docs/components/client-manager.md)** - Owns the enrolled-client list and mints enrollment tokens directly: descriptions, RBAC-bound attributes, SAN aliases, revoked status (control-plane component, runs on the CA host)
```

- [ ] **Step 5: Update `deploy/control-plane/README.md`**

Replace every `certrequest` reference with the equivalent `client-manager` command. Specifically:

Change:
```bash
cd deploy/control-plane
docker run --rm --network control-plane_default \
  -v "$(pwd)/../..:/repo" -w /repo/src \
  golang:1.26 \
  go run ./cmd/certrequest catalog --ca-url https://step-ca:9000 \
    --defaults-file /repo/deploy/control-plane/ca/data/config/defaults.json \
    --root /repo/deploy/control-plane/ca/data/certs/root_ca.crt \
    --password-file /repo/deploy/control-plane/ca/data/secrets/password
```
to:
```bash
cd deploy/control-plane
docker run --rm --network control-plane_default \
  -v "$(pwd)/../..:/repo" -w /repo/src \
  golang:1.26 \
  go run ./cmd/clientmanager add catalog --ca-url https://step-ca:9000 \
    --defaults-file /repo/deploy/control-plane/ca/data/config/defaults.json \
    --root /repo/deploy/control-plane/ca/data/certs/root_ca.crt \
    --password-file /repo/deploy/control-plane/ca/data/secrets/password
```

Change:
```bash
certrequest node-east-01 --san node-east-01.internal --ca-url https://localhost:9000
```
to:
```bash
client-manager add node-east-01 --san node-east-01.internal --ca-url https://localhost:9000
```

Change:
```bash
certrequest catalog-01 --san catalog.backup.internal --ca-url https://localhost:9000
```
to:
```bash
client-manager add catalog-01 --san catalog.backup.internal --ca-url https://localhost:9000
```

Update the "See Also" list entry `- [certrequest](../../docs/components/certrequest.md)` to
`- [client-manager](../../docs/components/client-manager.md)`.

- [ ] **Step 6: Final full verification**

Run: `make build 2>&1 | tail -20`
Expected: every binary (including `clientmanager`) reports `Built successfully`; no `certrequest` target exists anymore (confirm with `make certrequest 2>&1` — expect `make: *** No rule to make target 'certrequest'`).

Run: `cd src && go test ./... 2>&1 | tail -30`
Expected: `ok` for every package.

Run: `cd src && go vet ./...`
Expected: only the pre-existing, unrelated `cmd/brfs` warning (confirmed out of scope earlier in this project's history) — no new output.

- [ ] **Step 7: Commit**

```bash
git add docs/components/client-manager.md docs/ARCHITECTURE.md README.md deploy/control-plane/README.md
git add -A docs/components/certrequest.md docs/protocols/enrollment-broker.md
git commit -m "docs: retire certrequest docs, document client-manager's expanded role"
```

---

## Self-Review

**Spec coverage:**
- SAN add/remove after enrollment → Tasks 1–2.
- `client-manager` holds the provisioner password directly, no broker → Task 3.
- `certrequest` retired entirely → Task 4.
- Docs/deployment scripts reflect the new shape → Task 5.
- Explicitly *not* covered (correctly, per the phase-2 design's own sequencing): the listening
  service, enforcement of `revoked`/`attribute`/`san` on an actual certificate, `agent`'s new
  policies, the `common/mtls` alternate-credential-file helper — all belong to the next plan.

**Placeholder scan:** no `TBD`/`TODO` strings; every code block is complete; every test has real
assertions.

**Type consistency:** `minter`'s signature (Task 3) matches `certmint.Mint`'s real signature
exactly, checked against `src/common/certmint/certmint.go`'s actual current code (read directly,
not assumed) before writing this plan. `runAdd`/`runReEnroll`'s new signatures are used
consistently across `add.go`, `add_test.go`, and `main.go`'s `run()`. `Arguments.SanAlias` is
introduced once (Task 2) and never renamed. `Store.AddSAN`/`RemoveSAN` (Task 1) and
`runSanAdd`/`runSanRemove` (Task 2) are the only two places SAN mutation logic lives — no
duplication between them.

No gaps found.
