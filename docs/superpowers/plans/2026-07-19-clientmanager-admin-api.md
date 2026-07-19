# clientmanager-admin-api Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add network-reachable client enrollment/revocation/description/attribute/SAN-alias writes to `api-server`'s REST surface, backed by a new gRPC daemon (`clientmanager-admin-api`) that holds the CA provisioner password directly.

**Architecture:** A new binary, `clientmanager-admin-api`, ships in the same container as the existing (untouched, still read-only) `clientmanager-api`, sharing one mesh identity/`agent` process. It exposes seven RPCs (`AddClient`, `ReEnrollClient`, `RevokeClient`, `UnrevokeClient`, `UpdateDescription`, `UpdateAttributes`, `UpdateSANs`) that call the same `storage/clientmanager` store methods and `common/certmint.Mint` function `client-manager`'s CLI already uses — no new business logic, just a new caller. `api-server` gets a second outbound gRPC connection and seven new REST endpoints under `/api/v1/clients`, each a thin 1:1 proxy to one RPC.

**Tech Stack:** Go 1.26, gRPC + protobuf (protoc, protoc-gen-go, protoc-gen-go-grpc — all present at `/home/alex/.local/bin`), GORM + `modernc.org/sqlite`, cobra for CLI flags, `testify` for assertions, Docker Compose for deployment.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-19-clientmanager-admin-api-design.md` — every task below implements one piece of it; re-read it if a task's rationale is unclear.
- No new business logic: every write operation reuses `storage/clientmanager.Store` methods and `common/certmint.Mint`, exactly as `client-manager`'s CLI (`src/cmd/clientmanager/`) already does.
- `clientmanager-api` (the existing read-only service) stays untouched except for the `LoadClientView` refactor in Task 1, which must not change its observable behavior — its existing tests are the regression check.
- REST JSON field stays `attributes`, not `labels` (matches the existing `GET /api/v1/clients` response shape).
- Per this repo's `.claude/CLAUDE.md` documentation rules: any new/changed `.proto` requires a `docs/protocols/` entry (Task 2); any feature change requires `docs/components/` + `README.md` updates (Task 13); any merge to `main` requires a `CHANGELOG.md` entry (Task 13).
- Run `cd src && go build ./... && go vet ./...` and the relevant `go test` package(s) before every commit in this plan.

---

### Task 1: Extract `Store.LoadClientView` and refactor `clientmanager-api` onto it

**Files:**
- Modify: `src/storage/clientmanager/models.go`
- Modify: `src/storage/clientmanager/store.go`
- Modify: `src/storage/clientmanager/store_test.go`
- Modify: `src/cmd/clientmanager-api/server.go`

**Interfaces:**
- Produces: `clientmanagerstore.ClientView` struct (`Hostname string`, `Revoked bool`, `RevokedAt *time.Time`, `LastSeenAt *time.Time`, `SANs []string`, `Descriptions map[string]string`, `Attributes map[string]string`) and `(*Store) LoadClientView(hostname string) (*ClientView, error)` — returns `ErrClientNotFound` for an untracked hostname. Both are consumed by Task 3 onward (`clientmanager-admin-api`) and by this task's own refactor of `clientmanager-api`.

- [ ] **Step 1: Write the failing test**

Append to `src/storage/clientmanager/store_test.go`:

```go
func TestLoadClientView_ReturnsFullRecordWithKVAndSANs(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"alias.internal"}, time.Now()))
	require.NoError(t, store.SetKV("node-1", KindAttribute, "role", "db"))
	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "alice"))

	view, err := store.LoadClientView("node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", view.Hostname)
	assert.False(t, view.Revoked)
	assert.Equal(t, []string{"alias.internal"}, view.SANs)
	assert.Equal(t, "db", view.Attributes["role"])
	assert.Equal(t, "alice", view.Descriptions["owner"])
}

func TestLoadClientView_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.LoadClientView("ghost")
	assert.ErrorIs(t, err, ErrClientNotFound)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./storage/clientmanager/... -run TestLoadClientView -v`
Expected: FAIL — `store.LoadClientView undefined`

- [ ] **Step 3: Add the `ClientView` type**

Append to `src/storage/clientmanager/models.go`:

```go
// ClientView is a client record plus its resolved description/attribute
// key/value pairs -- the full shape both clientmanager-api and
// clientmanager-admin-api expose over gRPC.
type ClientView struct {
	Hostname     string
	Revoked      bool
	RevokedAt    *time.Time
	LastSeenAt   *time.Time
	SANs         []string
	Descriptions map[string]string
	Attributes   map[string]string
}
```

- [ ] **Step 4: Implement `LoadClientView`**

In `src/storage/clientmanager/store.go`, add this method immediately after the existing `GetClient` method:

```go
// LoadClientView returns hostname's full record: base fields plus resolved
// description and attribute key/value pairs. Returns ErrClientNotFound if
// hostname isn't tracked.
func (s *Store) LoadClientView(hostname string) (*ClientView, error) {
	rec, err := s.GetClient(hostname)
	if err != nil {
		return nil, err
	}

	view := &ClientView{
		Hostname:   rec.Hostname,
		Revoked:    rec.Revoked,
		RevokedAt:  rec.RevokedAt,
		LastSeenAt: rec.LastSeenAt,
		SANs:       rec.SANsList(),
	}

	descs, err := s.KV(hostname, KindDescription)
	if err != nil {
		return nil, err
	}
	view.Descriptions = make(map[string]string, len(descs))
	for _, d := range descs {
		view.Descriptions[d.Key] = d.Value
	}

	attrs, err := s.KV(hostname, KindAttribute)
	if err != nil {
		return nil, err
	}
	view.Attributes = make(map[string]string, len(attrs))
	for _, a := range attrs {
		view.Attributes[a.Key] = a.Value
	}

	return view, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd src && go test ./storage/clientmanager/... -run TestLoadClientView -v`
Expected: PASS

- [ ] **Step 6: Refactor `clientmanager-api` to use `LoadClientView`**

In `src/cmd/clientmanager-api/server.go`, replace the `ListClients`, `GetClient`, and `toProtoClient` methods (the whole block from `func (s *clientManagerAPIServer) ListClients` through the end of `toProtoClient`) with:

```go
func (s *clientManagerAPIServer) ListClients(ctx context.Context, _ *pb.ListClientsRequest) (*pb.ListClientsResponse, error) {
	recs, err := s.store.ListClients()
	if err != nil {
		s.logger.Error("ListClients: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list clients: %v", err)
	}

	clients := make([]*pb.Client, len(recs))
	for i, rec := range recs {
		view, err := s.store.LoadClientView(rec.Hostname)
		if err != nil {
			s.logger.Error("ListClients: load view failed", "hostname", rec.Hostname, "error", err)
			return nil, status.Errorf(codes.Internal, "list clients: %v", err)
		}
		clients[i] = toProtoClient(view)
	}
	return &pb.ListClientsResponse{Clients: clients}, nil
}

func (s *clientManagerAPIServer) GetClient(ctx context.Context, req *pb.GetClientRequest) (*pb.Client, error) {
	view, err := s.store.LoadClientView(req.GetHostname())
	if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return nil, status.Errorf(codes.NotFound, "client %s not found", req.GetHostname())
	}
	if err != nil {
		s.logger.Error("GetClient: query failed", "hostname", req.GetHostname(), "error", err)
		return nil, status.Errorf(codes.Internal, "get client: %v", err)
	}
	return toProtoClient(view), nil
}

// toProtoClient converts a resolved client view into its wire
// representation. clientmanager-admin-api has its own local copy of this
// same conversion -- storage/clientmanager can't import the generated pb
// package without an import cycle, so each gRPC-facing binary does its
// own trivial field mapping from the shared ClientView.
func toProtoClient(v *clientmanagerstore.ClientView) *pb.Client {
	client := &pb.Client{
		Hostname:     v.Hostname,
		Revoked:      v.Revoked,
		Sans:         v.SANs,
		Descriptions: v.Descriptions,
		Attributes:   v.Attributes,
	}
	if v.RevokedAt != nil {
		client.RevokedAt = v.RevokedAt.Unix()
	}
	if v.LastSeenAt != nil {
		client.LastSeenAt = v.LastSeenAt.Unix()
	}
	return client
}
```

- [ ] **Step 7: Run the full existing clientmanager-api and storage/clientmanager test suites to confirm no regression**

Run: `cd src && go test ./storage/clientmanager/... ./cmd/clientmanager-api/... -v`
Expected: PASS — all existing tests (`TestListClients_ReturnsAllClientsWithAttributesAndDescriptions`, `TestGetClient_UnknownHostnameReturnsNotFound`, `TestGetClient_RevokedAndLastSeenTimestampsRoundTrip`, etc.) still pass unchanged, proving the refactor is behavior-preserving.

- [ ] **Step 8: Build and vet**

Run: `cd src && go build ./... && go vet ./...`
Expected: no errors

- [ ] **Step 9: Commit**

```bash
git add src/storage/clientmanager/models.go src/storage/clientmanager/store.go src/storage/clientmanager/store_test.go src/cmd/clientmanager-api/server.go
git commit -m "refactor(clientmanager): extract Store.LoadClientView, shared by clientmanager-api and the upcoming clientmanager-admin-api"
```

---

### Task 2: Add the `clientmanageradmin.proto` service definition and generate its Go bindings

**Files:**
- Create: `src/api/clientmanageradmin.proto`
- Create (generated): `src/api/clientmanageradmin.pb.go`, `src/api/clientmanageradmin_grpc.pb.go`

**Interfaces:**
- Consumes: `clientmanagerapiservice.Client` (defined in `src/api/clientmanager.proto`, already generated as Go type `Client` in package `proto`) — reused as every mutating RPC's response type instead of duplicating the client-record shape.
- Produces: Go types `pb.AddClientRequest`, `pb.AddClientResponse`, `pb.ReEnrollClientRequest`, `pb.ReEnrollClientResponse`, `pb.RevokeClientRequest`, `pb.UnrevokeClientRequest`, `pb.UpdateClientKVRequest`, `pb.UpdateClientSANsRequest`, the `pb.ClientManagerAdminServiceClient`/`pb.ClientManagerAdminServiceServer` interfaces, `pb.RegisterClientManagerAdminServiceServer`, and `pb.UnimplementedClientManagerAdminServiceServer` — consumed by every task from Task 3 onward.

- [ ] **Step 1: Write the proto file**

Create `src/api/clientmanageradmin.proto`:

```proto
// src/api/clientmanageradmin.proto
syntax = "proto3";

package clientmanageradminservice;

import "api/clientmanager.proto";

option go_package = "./proto";

// ClientManagerAdminService holds the CA provisioner password directly
// (CA-admin-equivalent access) -- deliberately isolated from
// clientmanager-api's general-purpose, password-free read surface. See
// docs/superpowers/specs/2026-07-19-clientmanager-admin-api-design.md.
service ClientManagerAdminService {
  rpc AddClient(AddClientRequest) returns (AddClientResponse);
  rpc ReEnrollClient(ReEnrollClientRequest) returns (ReEnrollClientResponse);
  rpc RevokeClient(RevokeClientRequest) returns (clientmanagerapiservice.Client);
  rpc UnrevokeClient(UnrevokeClientRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateDescription(UpdateClientKVRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateAttributes(UpdateClientKVRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateSANs(UpdateClientSANsRequest) returns (clientmanagerapiservice.Client);
}

message AddClientRequest {
  string hostname = 1;
  repeated string sans = 2;
}

message AddClientResponse {
  string token = 1;
}

message ReEnrollClientRequest {
  string hostname = 1;
  // Empty means keep the hostname's currently stored SANs.
  repeated string sans = 2;
}

message ReEnrollClientResponse {
  string token = 1;
}

message RevokeClientRequest {
  string hostname = 1;
}

message UnrevokeClientRequest {
  string hostname = 1;
}

message UpdateClientKVRequest {
  string hostname = 1;
  map<string, string> set = 2;
  repeated string unset = 3;
}

message UpdateClientSANsRequest {
  string hostname = 1;
  repeated string add = 2;
  repeated string remove = 3;
}
```

- [ ] **Step 2: Generate the Go bindings**

Run: `cd /home/alex/miniprotector && make proto`
Expected: `Protobuf code generated in src/api/` with no errors, and `src/api/clientmanageradmin.pb.go` / `src/api/clientmanageradmin_grpc.pb.go` now exist.

- [ ] **Step 3: Confirm the whole module still builds**

Run: `cd src && go build ./...`
Expected: no errors — in particular, `RevokeClient`/`UnrevokeClient`/`UpdateDescription`/`UpdateAttributes`/`UpdateSANs` in the generated `clientmanageradmin_grpc.pb.go` should return `(*Client, error)` (the same `Client` type `clientmanager.pb.go` defines, unqualified, since both files generate into the same Go package).

- [ ] **Step 4: Commit**

```bash
git add src/api/clientmanageradmin.proto src/api/clientmanageradmin.pb.go src/api/clientmanageradmin_grpc.pb.go
git commit -m "feat(api): add ClientManagerAdminService proto for network-reachable client writes"
```

---

### Task 3: `clientmanager-admin-api` skeleton binary with `AddClient`

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `Makefile`
- Create: `src/cmd/clientmanager-admin-api/arguments.go`
- Create: `src/cmd/clientmanager-admin-api/main.go`
- Create: `src/cmd/clientmanager-admin-api/server.go`
- Create: `src/cmd/clientmanager-admin-api/server_test.go`

**Interfaces:**
- Consumes: `clientmanagerstore.New`, `clientmanagerstore.ErrClientNotFound`, `clientmanagerstore.ClientView` (Task 1); `certmint.Options`, `certmint.Mint(hostname string, sans []string, opts certmint.Options) (string, error)`; `connection.StartServer`; `pb.RegisterClientManagerAdminServiceServer`.
- Produces: `Arguments{Port, CAURL, RootFile, Provisioner, PasswordFile, Debug}` / `parseArguments(conf *config.Config) (*Arguments, error)`; `type minter func(hostname string, sans []string, opts certmint.Options) (string, error)`; `clientManagerAdminServer` struct with `store *clientmanagerstore.Store`, `mint minter`, `mintOpts certmint.Options`, `logger *slog.Logger` fields; `NewClientManagerAdminServer(store *clientmanagerstore.Store, mint minter, mintOpts certmint.Options, logger *slog.Logger) *clientManagerAdminServer`; method `(*clientManagerAdminServer) AddClient(ctx, *pb.AddClientRequest) (*pb.AddClientResponse, error)`; local function `toProtoClient(v *clientmanagerstore.ClientView) *pb.Client` (used by later tasks in this same package). `config.Config` gains `ClientManagerAdminAPIPort int` (default `9501`) and `ClientManagerAdminAPIHost string` — consumed by `api-server` starting Task 8.

- [ ] **Step 1: Add config fields**

In `src/common/config/config.go`, add two fields to the `Config` struct, immediately after `APIServerToken string`:

```go
	APIServerToken                   string
	ClientManagerAdminAPIPort        int
	ClientManagerAdminAPIHost        string
```

Add the port default to the `ParseConfig` struct literal, immediately after `LogGatewayPort: 9400,`:

```go
		LogGatewayPort:                   9400,
		ClientManagerAdminAPIPort:        9501,
```

Add parsing cases, immediately after the existing `case "clientmanager_api_host":` block:

```go
		case "clientmanager_api_host":
			config.ClientManagerAPIHost = value
			foundFields["clientmanager_api_host"] = true
		case "clientmanager_admin_api_host":
			config.ClientManagerAdminAPIHost = value
			foundFields["clientmanager_admin_api_host"] = true
		case "clientmanager_admin_api_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid clientmanager_admin_api_port value at line %d: %s", lineNum, value)
			}
			config.ClientManagerAdminAPIPort = port
			foundFields["clientmanager_admin_api_port"] = true
```

(The `case "clientmanager_api_host":` line already exists — only the two new `case` blocks after it are additions.)

- [ ] **Step 2: Verify config.go still builds**

Run: `cd src && go build ./common/config/...`
Expected: no errors

- [ ] **Step 3: Add Makefile build target**

In `Makefile`, add to the `.PHONY` line (after `clientmanager-api`):

```
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certclient catalogsync catalog agent clientmanager issuer policy-server policyclient log-gateway clientmanager-api clientmanager-admin-api api-server test test-e2e lint control-plane-up demo-up demo-down
```

Add the command variable, after `CLIENTMANAGER_API_CMD := cmd/clientmanager-api`:

```makefile
CLIENTMANAGER_API_CMD := cmd/clientmanager-api
CLIENTMANAGER_ADMIN_API_CMD := cmd/clientmanager-admin-api
API_SERVER_CMD := cmd/api-server
```

Add the build target, immediately after the `clientmanager-api:` target block and before `api-server:`:

```makefile
clientmanager-api: $(BINARY_DIR) ## Build clientmanager-api binary
	@printf "$(BLUE)Building clientmanager-api...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/clientmanager-api ./$(CLIENTMANAGER_API_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/clientmanager-api"

clientmanager-admin-api: $(BINARY_DIR) ## Build clientmanager-admin-api binary
	@printf "$(BLUE)Building clientmanager-admin-api...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/clientmanager-admin-api ./$(CLIENTMANAGER_ADMIN_API_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/clientmanager-admin-api"

api-server: $(BINARY_DIR) ## Build api-server binary
```

(Only the new `.PHONY` entry, the new `CLIENTMANAGER_ADMIN_API_CMD` line, and the new `clientmanager-admin-api:` target block are additions — everything else shown is existing content for placement context.)

- [ ] **Step 4: Write `arguments.go`**

Create `src/cmd/clientmanager-admin-api/arguments.go`:

```go
// src/cmd/clientmanager-admin-api/arguments.go
package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Port         int
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
	Debug        bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "clientmanager-admin-api",
		Short: "CA-admin-equivalent gRPC writes onto client-manager's enrolled-client data",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().IntVar(&args.Port, "port", conf.ClientManagerAdminAPIPort, "Port to listen on")
	cmd.Flags().StringVar(&args.CAURL, "ca-url", "https://localhost:9000", "CA URL, e.g. https://localhost:9000")
	cmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	cmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	cmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")
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

- [ ] **Step 5: Write the failing test for `AddClient`**

Create `src/cmd/clientmanager-admin-api/server_test.go`:

```go
// src/cmd/clientmanager-admin-api/server_test.go
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

// stubMinter is a test double for the minter function type, recording its
// last call and returning a canned token/error.
type stubMinter struct {
	token    string
	err      error
	calls    int
	lastHost string
	lastSANs []string
}

func (r *stubMinter) mint(hostname string, sans []string, opts certmint.Options) (string, error) {
	r.calls++
	r.lastHost = hostname
	r.lastSANs = sans
	if r.err != nil {
		return "", r.err
	}
	if r.token == "" {
		return "tok-default", nil
	}
	return r.token, nil
}

func newTestAdminServer(t *testing.T) (*clientManagerAdminServer, *clientmanagerstore.Store, *stubMinter) {
	t.Helper()
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	rec := &stubMinter{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewClientManagerAdminServer(store, rec.mint, certmint.Options{Provisioner: "admin@backup.internal"}, logger)
	return srv, store, rec
}

func TestAddClient_MintsAndRecordsClient(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	rec.token = "tok-abc"

	resp, err := srv.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "node-1", Sans: []string{"alias.internal"}})
	require.NoError(t, err)
	assert.Equal(t, "tok-abc", resp.GetToken())
	assert.Equal(t, "node-1", rec.lastHost)
	assert.Equal(t, []string{"alias.internal"}, rec.lastSANs)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", got.Hostname)
}

func TestAddClient_DuplicateHostnameReturnsAlreadyExists(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	_, err := srv.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "node-1"})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
	assert.Equal(t, 0, rec.calls, "mint must not be called for a duplicate add")
}

func TestAddClient_MintFailureDoesNotRecordClient(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	rec.err = errors.New("ca unreachable")

	_, err := srv.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "node-1"})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))

	_, err = store.GetClient("node-1")
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -v`
Expected: FAIL to compile — `NewClientManagerAdminServer undefined`, `clientManagerAdminServer` undefined, etc.

- [ ] **Step 7: Write `server.go`**

Create `src/cmd/clientmanager-admin-api/server.go`:

```go
// src/cmd/clientmanager-admin-api/server.go
package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// minter mints an enrollment token for hostname/sans using the given
// provisioner credentials. certmint.Mint's own signature already matches
// this exactly, so production code passes it directly with no wrapper;
// tests inject a stub. Mirrors client-manager's own minter type.
type minter func(hostname string, sans []string, opts certmint.Options) (string, error)

type clientManagerAdminServer struct {
	pb.UnimplementedClientManagerAdminServiceServer
	store    *clientmanagerstore.Store
	mint     minter
	mintOpts certmint.Options
	logger   *slog.Logger
}

func NewClientManagerAdminServer(store *clientmanagerstore.Store, mint minter, mintOpts certmint.Options, logger *slog.Logger) *clientManagerAdminServer {
	return &clientManagerAdminServer{store: store, mint: mint, mintOpts: mintOpts, logger: logger}
}

func (s *clientManagerAdminServer) AddClient(ctx context.Context, req *pb.AddClientRequest) (*pb.AddClientResponse, error) {
	hostname := req.GetHostname()
	if hostname == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname is required")
	}

	if _, err := s.store.GetClient(hostname); err == nil {
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

	if err := s.store.AddClient(hostname, req.GetSans(), time.Now()); err != nil {
		s.logger.Error("AddClient: record failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "record client: %v", err)
	}

	return &pb.AddClientResponse{Token: token}, nil
}

// loadClient loads hostname's full record for a response, used by every
// RPC below AddClient/ReEnrollClient that returns the updated Client.
func (s *clientManagerAdminServer) loadClient(hostname string) (*pb.Client, error) {
	view, err := s.store.LoadClientView(hostname)
	if err != nil {
		s.logger.Error("loadClient: query failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "load client: %v", err)
	}
	return toProtoClient(view), nil
}

// toProtoClient converts a resolved client view into its wire
// representation. Deliberately a local copy of clientmanager-api's
// identical helper -- separate main packages, and storage/clientmanager
// can't import the generated pb package without an import cycle.
func toProtoClient(v *clientmanagerstore.ClientView) *pb.Client {
	client := &pb.Client{
		Hostname:     v.Hostname,
		Revoked:      v.Revoked,
		Sans:         v.SANs,
		Descriptions: v.Descriptions,
		Attributes:   v.Attributes,
	}
	if v.RevokedAt != nil {
		client.RevokedAt = v.RevokedAt.Unix()
	}
	if v.LastSeenAt != nil {
		client.LastSeenAt = v.LastSeenAt.Unix()
	}
	return client
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -v`
Expected: PASS (`TestAddClient_MintsAndRecordsClient`, `TestAddClient_DuplicateHostnameReturnsAlreadyExists`, `TestAddClient_MintFailureDoesNotRecordClient`)

- [ ] **Step 9: Write `main.go`**

Create `src/cmd/clientmanager-admin-api/main.go`:

```go
// clientmanager-admin-api holds the CA provisioner password directly and
// exposes gRPC writes onto client-manager's enrolled-client data: issue
// enrollment tokens, re-enroll, revoke/unrevoke, and manage
// description/attribute/SAN metadata. Deliberately separate from
// clientmanager-api, which stays read-only and password-free. See
// docs/components/clientmanager-admin-api.md and
// docs/superpowers/specs/2026-07-19-clientmanager-admin-api-design.md.
package main

import (
	"context"
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
	const appName = "clientmanager-admin-api"

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

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		logger.Error("var directory resolution failed", "error", err)
		os.Exit(1)
	}
	store, err := clientmanagerstore.New(varDir)
	if err != nil {
		logger.Error("failed to open client-manager store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	mintOpts := certmint.Options{
		CAURL:        arguments.CAURL,
		RootFile:     arguments.RootFile,
		Provisioner:  arguments.Provisioner,
		PasswordFile: arguments.PasswordFile,
	}
	srv := NewClientManagerAdminServer(store, certmint.Mint, mintOpts, logger)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("clientmanager-admin-api started", "port", arguments.Port)

	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
		pb.RegisterClientManagerAdminServiceServer(s, srv)
	}); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 10: Build the binary**

Run: `cd /home/alex/miniprotector && make clientmanager-admin-api`
Expected: `Built successfully: bin/clientmanager-admin-api`

- [ ] **Step 11: Full build and vet**

Run: `cd src && go build ./... && go vet ./...`
Expected: no errors

- [ ] **Step 12: Commit**

```bash
git add src/common/config/config.go Makefile src/cmd/clientmanager-admin-api/
git commit -m "feat(clientmanager-admin-api): add skeleton binary with AddClient RPC"
```

---

### Task 4: `clientmanager-admin-api` — `ReEnrollClient`

**Files:**
- Modify: `src/cmd/clientmanager-admin-api/server.go`
- Modify: `src/cmd/clientmanager-admin-api/server_test.go`

**Interfaces:**
- Produces: `(*clientManagerAdminServer) ReEnrollClient(ctx, *pb.ReEnrollClientRequest) (*pb.ReEnrollClientResponse, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/clientmanager-admin-api/server_test.go`:

```go
func TestReEnrollClient_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _, rec := newTestAdminServer(t)

	_, err := srv.ReEnrollClient(context.Background(), &pb.ReEnrollClientRequest{Hostname: "ghost"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Equal(t, 0, rec.calls)
}

func TestReEnrollClient_NoSANOverride_ReusesStoredSANs(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", []string{"alias1", "alias2"}, time.Now()))

	resp, err := srv.ReEnrollClient(context.Background(), &pb.ReEnrollClientRequest{Hostname: "node-1"})
	require.NoError(t, err)
	assert.Equal(t, "tok-default", resp.GetToken())
	assert.Equal(t, []string{"alias1", "alias2"}, rec.lastSANs)
}

func TestReEnrollClient_WithSANOverride_UsesOverrideNotStoredSANs(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", []string{"alias1"}, time.Now()))

	_, err := srv.ReEnrollClient(context.Background(), &pb.ReEnrollClientRequest{Hostname: "node-1", Sans: []string{"override1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"override1"}, rec.lastSANs)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -run TestReEnrollClient -v`
Expected: FAIL to compile — `srv.ReEnrollClient undefined`

- [ ] **Step 3: Implement `ReEnrollClient`**

In `src/cmd/clientmanager-admin-api/server.go`, add this method after `AddClient`:

```go
func (s *clientManagerAdminServer) ReEnrollClient(ctx context.Context, req *pb.ReEnrollClientRequest) (*pb.ReEnrollClientResponse, error) {
	hostname := req.GetHostname()
	rec, err := s.store.GetClient(hostname)
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

	return &pb.ReEnrollClientResponse{Token: token}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -v`
Expected: PASS (all tests so far)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/clientmanager-admin-api/server.go src/cmd/clientmanager-admin-api/server_test.go
git commit -m "feat(clientmanager-admin-api): add ReEnrollClient RPC"
```

---

### Task 5: `clientmanager-admin-api` — `RevokeClient` and `UnrevokeClient`

**Files:**
- Modify: `src/cmd/clientmanager-admin-api/server.go`
- Modify: `src/cmd/clientmanager-admin-api/server_test.go`

**Interfaces:**
- Produces: `(*clientManagerAdminServer) RevokeClient(ctx, *pb.RevokeClientRequest) (*pb.Client, error)`, `(*clientManagerAdminServer) UnrevokeClient(ctx, *pb.UnrevokeClientRequest) (*pb.Client, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/clientmanager-admin-api/server_test.go`:

```go
func TestRevokeClient_SetsRevokedAndReturnsUpdatedClient(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	client, err := srv.RevokeClient(context.Background(), &pb.RevokeClientRequest{Hostname: "node-1"})
	require.NoError(t, err)
	assert.True(t, client.GetRevoked())
	assert.NotZero(t, client.GetRevokedAt())
}

func TestRevokeClient_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _, _ := newTestAdminServer(t)

	_, err := srv.RevokeClient(context.Background(), &pb.RevokeClientRequest{Hostname: "ghost"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestUnrevokeClient_ClearsRevokedFlag(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.SetRevoked("node-1", true, time.Now()))

	client, err := srv.UnrevokeClient(context.Background(), &pb.UnrevokeClientRequest{Hostname: "node-1"})
	require.NoError(t, err)
	assert.False(t, client.GetRevoked())
	assert.Zero(t, client.GetRevokedAt())
}

func TestUnrevokeClient_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _, _ := newTestAdminServer(t)

	_, err := srv.UnrevokeClient(context.Background(), &pb.UnrevokeClientRequest{Hostname: "ghost"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -run "TestRevokeClient|TestUnrevokeClient" -v`
Expected: FAIL to compile

- [ ] **Step 3: Implement both RPCs**

In `src/cmd/clientmanager-admin-api/server.go`, add these methods after `ReEnrollClient`:

```go
func (s *clientManagerAdminServer) RevokeClient(ctx context.Context, req *pb.RevokeClientRequest) (*pb.Client, error) {
	return s.setRevoked(req.GetHostname(), true)
}

func (s *clientManagerAdminServer) UnrevokeClient(ctx context.Context, req *pb.UnrevokeClientRequest) (*pb.Client, error) {
	return s.setRevoked(req.GetHostname(), false)
}

func (s *clientManagerAdminServer) setRevoked(hostname string, revoked bool) (*pb.Client, error) {
	if err := s.store.SetRevoked(hostname, revoked, time.Now()); err != nil {
		if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
			return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
		}
		s.logger.Error("setRevoked: update failed", "hostname", hostname, "revoked", revoked, "error", err)
		return nil, status.Errorf(codes.Internal, "update revoked: %v", err)
	}
	return s.loadClient(hostname)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -v`
Expected: PASS (all tests so far)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/clientmanager-admin-api/server.go src/cmd/clientmanager-admin-api/server_test.go
git commit -m "feat(clientmanager-admin-api): add RevokeClient and UnrevokeClient RPCs"
```

---

### Task 6: `clientmanager-admin-api` — `UpdateDescription` and `UpdateAttributes`

**Files:**
- Modify: `src/cmd/clientmanager-admin-api/server.go`
- Modify: `src/cmd/clientmanager-admin-api/server_test.go`

**Interfaces:**
- Produces: `(*clientManagerAdminServer) UpdateDescription(ctx, *pb.UpdateClientKVRequest) (*pb.Client, error)`, `(*clientManagerAdminServer) UpdateAttributes(ctx, *pb.UpdateClientKVRequest) (*pb.Client, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/clientmanager-admin-api/server_test.go`:

```go
func TestUpdateDescription_SetsAndUnsetsKeys(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindDescription, "old", "gone"))

	client, err := srv.UpdateDescription(context.Background(), &pb.UpdateClientKVRequest{
		Hostname: "node-1",
		Set:      map[string]string{"owner": "alice"},
		Unset:    []string{"old"},
	})
	require.NoError(t, err)
	assert.Equal(t, "alice", client.GetDescriptions()["owner"])
	_, stillThere := client.GetDescriptions()["old"]
	assert.False(t, stillThere)
}

func TestUpdateDescription_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _, _ := newTestAdminServer(t)

	_, err := srv.UpdateDescription(context.Background(), &pb.UpdateClientKVRequest{Hostname: "ghost", Set: map[string]string{"k": "v"}})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestUpdateAttributes_SetsAndUnsetsKeys(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	client, err := srv.UpdateAttributes(context.Background(), &pb.UpdateClientKVRequest{
		Hostname: "node-1",
		Set:      map[string]string{"role": "db"},
	})
	require.NoError(t, err)
	assert.Equal(t, "db", client.GetAttributes()["role"])
}

func TestUpdateAttributes_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _, _ := newTestAdminServer(t)

	_, err := srv.UpdateAttributes(context.Background(), &pb.UpdateClientKVRequest{Hostname: "ghost", Set: map[string]string{"k": "v"}})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -run "TestUpdateDescription|TestUpdateAttributes" -v`
Expected: FAIL to compile

- [ ] **Step 3: Implement both RPCs**

In `src/cmd/clientmanager-admin-api/server.go`, add these methods after `setRevoked`:

```go
func (s *clientManagerAdminServer) UpdateDescription(ctx context.Context, req *pb.UpdateClientKVRequest) (*pb.Client, error) {
	return s.updateKV(req, clientmanagerstore.KindDescription)
}

func (s *clientManagerAdminServer) UpdateAttributes(ctx context.Context, req *pb.UpdateClientKVRequest) (*pb.Client, error) {
	return s.updateKV(req, clientmanagerstore.KindAttribute)
}

func (s *clientManagerAdminServer) updateKV(req *pb.UpdateClientKVRequest, kind clientmanagerstore.KVKind) (*pb.Client, error) {
	hostname := req.GetHostname()
	for key, value := range req.GetSet() {
		if err := s.store.SetKV(hostname, kind, key, value); err != nil {
			if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
				return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
			}
			s.logger.Error("updateKV: set failed", "hostname", hostname, "kind", kind, "key", key, "error", err)
			return nil, status.Errorf(codes.Internal, "set %s: %v", kind, err)
		}
	}
	for _, key := range req.GetUnset() {
		if err := s.store.UnsetKV(hostname, kind, key); err != nil {
			if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
				return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
			}
			s.logger.Error("updateKV: unset failed", "hostname", hostname, "kind", kind, "key", key, "error", err)
			return nil, status.Errorf(codes.Internal, "unset %s: %v", kind, err)
		}
	}
	return s.loadClient(hostname)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -v`
Expected: PASS (all tests so far)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/clientmanager-admin-api/server.go src/cmd/clientmanager-admin-api/server_test.go
git commit -m "feat(clientmanager-admin-api): add UpdateDescription and UpdateAttributes RPCs"
```

---

### Task 7: `clientmanager-admin-api` — `UpdateSANs`

**Files:**
- Modify: `src/cmd/clientmanager-admin-api/server.go`
- Modify: `src/cmd/clientmanager-admin-api/server_test.go`

**Interfaces:**
- Produces: `(*clientManagerAdminServer) UpdateSANs(ctx, *pb.UpdateClientSANsRequest) (*pb.Client, error)` — completes the `ClientManagerAdminService` implementation.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/clientmanager-admin-api/server_test.go`:

```go
func TestUpdateSANs_AddsAndRemovesAliases(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", []string{"old.internal"}, time.Now()))

	client, err := srv.UpdateSANs(context.Background(), &pb.UpdateClientSANsRequest{
		Hostname: "node-1",
		Add:      []string{"new.internal"},
		Remove:   []string{"old.internal"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"new.internal"}, client.GetSans())
}

func TestUpdateSANs_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _, _ := newTestAdminServer(t)

	_, err := srv.UpdateSANs(context.Background(), &pb.UpdateClientSANsRequest{Hostname: "ghost", Add: []string{"x.internal"}})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -run TestUpdateSANs -v`
Expected: FAIL to compile

- [ ] **Step 3: Implement `UpdateSANs`**

In `src/cmd/clientmanager-admin-api/server.go`, add this method after `updateKV`:

```go
func (s *clientManagerAdminServer) UpdateSANs(ctx context.Context, req *pb.UpdateClientSANsRequest) (*pb.Client, error) {
	hostname := req.GetHostname()
	for _, alias := range req.GetAdd() {
		if err := s.store.AddSAN(hostname, alias); err != nil {
			if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
				return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
			}
			s.logger.Error("UpdateSANs: add failed", "hostname", hostname, "alias", alias, "error", err)
			return nil, status.Errorf(codes.Internal, "add san: %v", err)
		}
	}
	for _, alias := range req.GetRemove() {
		if err := s.store.RemoveSAN(hostname, alias); err != nil {
			if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
				return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
			}
			s.logger.Error("UpdateSANs: remove failed", "hostname", hostname, "alias", alias, "error", err)
			return nil, status.Errorf(codes.Internal, "remove san: %v", err)
		}
	}
	return s.loadClient(hostname)
}
```

- [ ] **Step 4: Run the full package test suite**

Run: `cd src && go test ./cmd/clientmanager-admin-api/... -v`
Expected: PASS — every test written across Tasks 3–7.

- [ ] **Step 5: Build, vet, and integration-smoke-test the binary starts**

Run: `cd src && go build ./... && go vet ./...`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add src/cmd/clientmanager-admin-api/server.go src/cmd/clientmanager-admin-api/server_test.go
git commit -m "feat(clientmanager-admin-api): add UpdateSANs RPC, completing ClientManagerAdminService"
```

---

### Task 8: `api-server` plumbing + `AddClient`/`ReEnroll` REST endpoints

**Files:**
- Modify: `src/cmd/api-server/server.go`
- Modify: `src/cmd/api-server/errors.go`
- Modify: `src/cmd/api-server/errors_test.go`
- Modify: `src/cmd/api-server/main.go`
- Create: `src/cmd/api-server/clients_admin.go`
- Create: `src/cmd/api-server/clients_admin_test.go`

**Interfaces:**
- Consumes: `pb.NewClientManagerAdminServiceClient`, `pb.AddClientRequest/Response`, `pb.ReEnrollClientRequest/Response`, `conf.ClientManagerAdminAPIHost/Port` (Task 3), `connection.Connect` (existing).
- Produces: `clientManagerAdminClient` interface (7 methods — full surface, used incrementally by Tasks 8–11); `server.clientManagerAdmin clientManagerAdminClient` field (set post-construction, mirroring the existing `srv.loki = ...` pattern — **not** a `newServer` parameter, so none of the 34 existing `newServer(...)` call sites across the test suite need to change); `fakeClientManagerAdminClient` test double (all 7 methods, used by Tasks 8–11); handlers `handleAddClient`, `handleReEnrollClient`; routes `POST /api/v1/clients`, `POST /api/v1/clients/{hostname}/reenroll`.

- [ ] **Step 1: Add the `clientManagerAdminClient` interface and `server` field**

In `src/cmd/api-server/server.go`, add this interface definition immediately after the existing `clientManagerClient` interface:

```go
// clientManagerAdminClient is the subset of pb.ClientManagerAdminServiceClient
// the client-write handlers need -- the full RPC surface, satisfied by the
// real generated client and by a fake in tests.
type clientManagerAdminClient interface {
	AddClient(ctx context.Context, in *pb.AddClientRequest, opts ...grpc.CallOption) (*pb.AddClientResponse, error)
	ReEnrollClient(ctx context.Context, in *pb.ReEnrollClientRequest, opts ...grpc.CallOption) (*pb.ReEnrollClientResponse, error)
	RevokeClient(ctx context.Context, in *pb.RevokeClientRequest, opts ...grpc.CallOption) (*pb.Client, error)
	UnrevokeClient(ctx context.Context, in *pb.UnrevokeClientRequest, opts ...grpc.CallOption) (*pb.Client, error)
	UpdateDescription(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error)
	UpdateAttributes(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error)
	UpdateSANs(ctx context.Context, in *pb.UpdateClientSANsRequest, opts ...grpc.CallOption) (*pb.Client, error)
}
```

Add a field to the `server` struct — change:

```go
type server struct {
	clientManager clientManagerClient
	catalog       catalogQueryClient
	policy        policyServiceClient
	loki          lokiQuerier
	logger        *slog.Logger
}
```

to:

```go
type server struct {
	clientManager      clientManagerClient
	clientManagerAdmin clientManagerAdminClient
	catalog            catalogQueryClient
	policy             policyServiceClient
	loki               lokiQuerier
	logger             *slog.Logger
}
```

Add these two routes inside `registerRoutes`, immediately after the existing `mux.HandleFunc("GET /api/v1/clients/{hostname}", s.handleGetClient)` line:

```go
	mux.HandleFunc("GET /api/v1/clients/{hostname}", s.handleGetClient)
	mux.HandleFunc("POST /api/v1/clients", s.handleAddClient)
	mux.HandleFunc("POST /api/v1/clients/{hostname}/reenroll", s.handleReEnrollClient)
```

- [ ] **Step 2: Add the `codes.AlreadyExists` → 409 mapping**

In `src/cmd/api-server/errors.go`, in `writeGRPCError`'s `switch st.Code()`, add a case immediately after `case codes.InvalidArgument:`:

```go
	switch st.Code() {
	case codes.NotFound:
		writeJSONError(w, http.StatusNotFound, st.Message())
	case codes.InvalidArgument:
		writeJSONError(w, http.StatusBadRequest, st.Message())
	case codes.AlreadyExists:
		writeJSONError(w, http.StatusConflict, st.Message())
	default:
		writeJSONError(w, http.StatusBadGateway, st.Message())
	}
```

Append this test to `src/cmd/api-server/errors_test.go`:

```go
func TestWriteGRPCError_AlreadyExistsMapsTo409(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, status.Error(codes.AlreadyExists, "client node-1 already enrolled"))
	assert.Equal(t, http.StatusConflict, rec.Code)
}
```

- [ ] **Step 3: Write the failing handler tests**

Create `src/cmd/api-server/clients_admin_test.go`:

```go
// src/cmd/api-server/clients_admin_test.go
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// fakeClientManagerAdminClient implements the full clientManagerAdminClient
// interface -- Tasks 8-11 each exercise a different subset of its methods.
type fakeClientManagerAdminClient struct {
	addResp    *pb.AddClientResponse
	addErr     error
	lastAddReq *pb.AddClientRequest

	reEnrollResp    *pb.ReEnrollClientResponse
	reEnrollErr     error
	lastReEnrollReq *pb.ReEnrollClientRequest

	revokeResp *pb.Client
	revokeErr  error

	unrevokeResp *pb.Client
	unrevokeErr  error

	updateDescResp    *pb.Client
	updateDescErr     error
	lastUpdateDescReq *pb.UpdateClientKVRequest

	updateAttrResp    *pb.Client
	updateAttrErr     error
	lastUpdateAttrReq *pb.UpdateClientKVRequest

	updateSANsResp    *pb.Client
	updateSANsErr     error
	lastUpdateSANsReq *pb.UpdateClientSANsRequest
}

func (f *fakeClientManagerAdminClient) AddClient(ctx context.Context, in *pb.AddClientRequest, opts ...grpc.CallOption) (*pb.AddClientResponse, error) {
	f.lastAddReq = in
	return f.addResp, f.addErr
}

func (f *fakeClientManagerAdminClient) ReEnrollClient(ctx context.Context, in *pb.ReEnrollClientRequest, opts ...grpc.CallOption) (*pb.ReEnrollClientResponse, error) {
	f.lastReEnrollReq = in
	return f.reEnrollResp, f.reEnrollErr
}

func (f *fakeClientManagerAdminClient) RevokeClient(ctx context.Context, in *pb.RevokeClientRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	return f.revokeResp, f.revokeErr
}

func (f *fakeClientManagerAdminClient) UnrevokeClient(ctx context.Context, in *pb.UnrevokeClientRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	return f.unrevokeResp, f.unrevokeErr
}

func (f *fakeClientManagerAdminClient) UpdateDescription(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	f.lastUpdateDescReq = in
	return f.updateDescResp, f.updateDescErr
}

func (f *fakeClientManagerAdminClient) UpdateAttributes(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	f.lastUpdateAttrReq = in
	return f.updateAttrResp, f.updateAttrErr
}

func (f *fakeClientManagerAdminClient) UpdateSANs(ctx context.Context, in *pb.UpdateClientSANsRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	f.lastUpdateSANsReq = in
	return f.updateSANsResp, f.updateSANsErr
}

func newServerWithAdmin(fake clientManagerAdminClient) *server {
	srv := newServer(nil, nil, nil, testLogger())
	srv.clientManagerAdmin = fake
	return srv
}

func TestHandleAddClient_ReturnsTokenAnd201(t *testing.T) {
	fake := &fakeClientManagerAdminClient{addResp: &pb.AddClientResponse{Token: "tok-abc"}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{"hostname":"node-1","sans":["alias.internal"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "node-1", body["hostname"])
	assert.Equal(t, "tok-abc", body["token"])
	assert.Equal(t, "node-1", fake.lastAddReq.GetHostname())
	assert.Equal(t, []string{"alias.internal"}, fake.lastAddReq.GetSans())
}

func TestHandleAddClient_MissingHostnameReturns400(t *testing.T) {
	srv := newServerWithAdmin(&fakeClientManagerAdminClient{})
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleAddClient_MalformedJSONReturns400(t *testing.T) {
	srv := newServerWithAdmin(&fakeClientManagerAdminClient{})
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleAddClient_DuplicateHostnameReturns409(t *testing.T) {
	fake := &fakeClientManagerAdminClient{addErr: status.Error(codes.AlreadyExists, "client node-1 already enrolled")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{"hostname":"node-1"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestHandleReEnrollClient_ReturnsToken(t *testing.T) {
	fake := &fakeClientManagerAdminClient{reEnrollResp: &pb.ReEnrollClientResponse{Token: "tok-fresh"}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/node-1/reenroll", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "tok-fresh", body["token"])
	assert.Equal(t, "node-1", fake.lastReEnrollReq.GetHostname())
}

func TestHandleReEnrollClient_NoBodyIsAccepted(t *testing.T) {
	fake := &fakeClientManagerAdminClient{reEnrollResp: &pb.ReEnrollClientResponse{Token: "tok-fresh"}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/node-1/reenroll", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleReEnrollClient_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerAdminClient{reEnrollErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/ghost/reenroll", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run "TestHandleAddClient|TestHandleReEnrollClient|TestWriteGRPCError_AlreadyExists" -v`
Expected: FAIL to compile — `s.handleAddClient undefined`, `s.handleReEnrollClient undefined`, `srv.clientManagerAdmin` field/type mismatches

- [ ] **Step 5: Write `clients_admin.go`**

Create `src/cmd/api-server/clients_admin.go`:

```go
// src/cmd/api-server/clients_admin.go
package main

import (
	"encoding/json"
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type addClientInput struct {
	Hostname string   `json:"hostname"`
	Sans     []string `json:"sans"`
}

type reEnrollInput struct {
	Sans []string `json:"sans"`
}

func (s *server) handleAddClient(w http.ResponseWriter, r *http.Request) {
	var in addClientInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if in.Hostname == "" {
		writeJSONError(w, http.StatusBadRequest, "hostname is required")
		return
	}
	resp, err := s.clientManagerAdmin.AddClient(r.Context(), &pb.AddClientRequest{Hostname: in.Hostname, Sans: in.Sans})
	if err != nil {
		s.logger.Error("handleAddClient: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"hostname": in.Hostname, "token": resp.GetToken()})
}

func (s *server) handleReEnrollClient(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	var in reEnrollInput
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	resp, err := s.clientManagerAdmin.ReEnrollClient(r.Context(), &pb.ReEnrollClientRequest{Hostname: hostname, Sans: in.Sans})
	if err != nil {
		s.logger.Error("handleReEnrollClient: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hostname": hostname, "token": resp.GetToken()})
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS — including every pre-existing `api-server` test (this confirms the `server` struct field addition and the `clientManagerAdminClient` interface didn't break any of the 34 existing `newServer(...)` call sites, since none of them changed).

- [ ] **Step 7: Add config keys to `deploy/control-plane/api-server/local.conf`**

In `deploy/control-plane/api-server/local.conf`, add two lines immediately after `clientmanager_api_port=9500`:

```
# Where api-server dials clientmanager-api and catalog.
clientmanager_api_host=clientmanager-api
clientmanager_api_port=9500
clientmanager_admin_api_host=clientmanager-api
clientmanager_admin_api_port=9501
catalog_host=catalog
catalog_port=15723
```

(`clientmanager_admin_api_host` is the same hostname as `clientmanager_api_host` — both binaries live in the same container/service, per Task 12.)

- [ ] **Step 8: Wire up the outbound connection in `main.go`**

In `src/cmd/api-server/main.go`, add a new connection immediately after the existing `cmConn` block:

```go
	cmConn, err := connection.Connect(conf.ClientManagerAPIHost, conf.ClientManagerAPIPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Error("connect to clientmanager-api failed", "error", err)
		os.Exit(1)
	}
	defer cmConn.Close()

	cmAdminConn, err := connection.Connect(conf.ClientManagerAdminAPIHost, conf.ClientManagerAdminAPIPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Error("connect to clientmanager-admin-api failed", "error", err)
		os.Exit(1)
	}
	defer cmAdminConn.Close()
```

Change the `srv := newServer(...)` line and the line after it from:

```go
	srv := newServer(pb.NewClientManagerServiceClient(cmConn), pb.NewCatalogServiceClient(catalogConn), pb.NewPolicyServiceClient(policyConn), logger)
	srv.loki = newCachingLokiClient(newHTTPLokiClient(lokiBaseURL, lokiHTTPClient), 10*time.Second)
```

to:

```go
	srv := newServer(pb.NewClientManagerServiceClient(cmConn), pb.NewCatalogServiceClient(catalogConn), pb.NewPolicyServiceClient(policyConn), logger)
	srv.clientManagerAdmin = pb.NewClientManagerAdminServiceClient(cmAdminConn)
	srv.loki = newCachingLokiClient(newHTTPLokiClient(lokiBaseURL, lokiHTTPClient), 10*time.Second)
```

- [ ] **Step 9: Build and vet**

Run: `cd src && go build ./... && go vet ./...`
Expected: no errors

- [ ] **Step 10: Commit**

```bash
git add src/cmd/api-server/server.go src/cmd/api-server/errors.go src/cmd/api-server/errors_test.go src/cmd/api-server/main.go src/cmd/api-server/clients_admin.go src/cmd/api-server/clients_admin_test.go deploy/control-plane/api-server/local.conf
git commit -m "feat(api-server): add clientmanager-admin-api plumbing plus POST /api/v1/clients and /reenroll"
```

---

### Task 9: `api-server` — `revoke`/`unrevoke` REST endpoints

**Files:**
- Modify: `src/cmd/api-server/server.go`
- Modify: `src/cmd/api-server/clients_admin.go`
- Modify: `src/cmd/api-server/clients_admin_test.go`

**Interfaces:**
- Produces: handlers `handleRevokeClient`, `handleUnrevokeClient`; routes `POST /api/v1/clients/{hostname}/revoke`, `POST /api/v1/clients/{hostname}/unrevoke`.

- [ ] **Step 1: Add the routes**

In `src/cmd/api-server/server.go`, add two lines to `registerRoutes` immediately after the `reenroll` route added in Task 8:

```go
	mux.HandleFunc("POST /api/v1/clients/{hostname}/reenroll", s.handleReEnrollClient)
	mux.HandleFunc("POST /api/v1/clients/{hostname}/revoke", s.handleRevokeClient)
	mux.HandleFunc("POST /api/v1/clients/{hostname}/unrevoke", s.handleUnrevokeClient)
```

- [ ] **Step 2: Write the failing tests**

Append to `src/cmd/api-server/clients_admin_test.go`:

```go
func TestHandleRevokeClient_ReturnsUpdatedClient(t *testing.T) {
	fake := &fakeClientManagerAdminClient{revokeResp: &pb.Client{Hostname: "node-1", Revoked: true}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/node-1/revoke", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["revoked"])
}

func TestHandleRevokeClient_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerAdminClient{revokeErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/ghost/revoke", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleUnrevokeClient_ReturnsUpdatedClient(t *testing.T) {
	fake := &fakeClientManagerAdminClient{unrevokeResp: &pb.Client{Hostname: "node-1", Revoked: false}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/node-1/unrevoke", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, false, body["revoked"])
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run "TestHandleRevokeClient|TestHandleUnrevokeClient" -v`
Expected: FAIL to compile — handlers undefined

- [ ] **Step 4: Implement both handlers**

Append to `src/cmd/api-server/clients_admin.go`:

```go
func (s *server) handleRevokeClient(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	client, err := s.clientManagerAdmin.RevokeClient(r.Context(), &pb.RevokeClientRequest{Hostname: hostname})
	if err != nil {
		s.logger.Error("handleRevokeClient: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(client))
}

func (s *server) handleUnrevokeClient(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	client, err := s.clientManagerAdmin.UnrevokeClient(r.Context(), &pb.UnrevokeClientRequest{Hostname: hostname})
	if err != nil {
		s.logger.Error("handleUnrevokeClient: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(client))
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/server.go src/cmd/api-server/clients_admin.go src/cmd/api-server/clients_admin_test.go
git commit -m "feat(api-server): add POST /api/v1/clients/{hostname}/revoke and /unrevoke"
```

---

### Task 10: `api-server` — `description`/`attributes` PATCH endpoints

**Files:**
- Modify: `src/cmd/api-server/server.go`
- Modify: `src/cmd/api-server/clients_admin.go`
- Modify: `src/cmd/api-server/clients_admin_test.go`

**Interfaces:**
- Produces: handlers `handleUpdateDescription`, `handleUpdateAttributes`; routes `PATCH /api/v1/clients/{hostname}/description`, `PATCH /api/v1/clients/{hostname}/attributes`.

- [ ] **Step 1: Add the routes**

In `src/cmd/api-server/server.go`, add two lines to `registerRoutes` immediately after the `unrevoke` route added in Task 9:

```go
	mux.HandleFunc("POST /api/v1/clients/{hostname}/unrevoke", s.handleUnrevokeClient)
	mux.HandleFunc("PATCH /api/v1/clients/{hostname}/description", s.handleUpdateDescription)
	mux.HandleFunc("PATCH /api/v1/clients/{hostname}/attributes", s.handleUpdateAttributes)
```

- [ ] **Step 2: Write the failing tests**

Append to `src/cmd/api-server/clients_admin_test.go`:

```go
func TestHandleUpdateDescription_SendsSetAndUnset(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateDescResp: &pb.Client{Hostname: "node-1", Descriptions: map[string]string{"owner": "alice"}}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/description", strings.NewReader(`{"set":{"owner":"alice"},"unset":["old"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]string{"owner": "alice"}, fake.lastUpdateDescReq.GetSet())
	assert.Equal(t, []string{"old"}, fake.lastUpdateDescReq.GetUnset())
}

func TestHandleUpdateDescription_MalformedJSONReturns400(t *testing.T) {
	srv := newServerWithAdmin(&fakeClientManagerAdminClient{})
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/description", strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateDescription_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateDescErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/ghost/description", strings.NewReader(`{"set":{"k":"v"}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleUpdateAttributes_SendsSetAndUnset(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateAttrResp: &pb.Client{Hostname: "node-1", Attributes: map[string]string{"role": "db"}}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/attributes", strings.NewReader(`{"set":{"role":"db"}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]string{"role": "db"}, fake.lastUpdateAttrReq.GetSet())
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run "TestHandleUpdateDescription|TestHandleUpdateAttributes" -v`
Expected: FAIL to compile — handlers undefined

- [ ] **Step 4: Implement both handlers**

Append to `src/cmd/api-server/clients_admin.go` (note the `context` and `grpc` imports this requires — see Step 5):

```go
type kvUpdateInput struct {
	Set   map[string]string `json:"set"`
	Unset []string          `json:"unset"`
}

func (s *server) handleUpdateDescription(w http.ResponseWriter, r *http.Request) {
	s.handleUpdateKV(w, r, s.clientManagerAdmin.UpdateDescription)
}

func (s *server) handleUpdateAttributes(w http.ResponseWriter, r *http.Request) {
	s.handleUpdateKV(w, r, s.clientManagerAdmin.UpdateAttributes)
}

func (s *server) handleUpdateKV(w http.ResponseWriter, r *http.Request, call func(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error)) {
	hostname := r.PathValue("hostname")
	var in kvUpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	client, err := call(r.Context(), &pb.UpdateClientKVRequest{Hostname: hostname, Set: in.Set, Unset: in.Unset})
	if err != nil {
		s.logger.Error("handleUpdateKV: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(client))
}
```

- [ ] **Step 5: Update the file's import block**

At the top of `src/cmd/api-server/clients_admin.go`, change:

```go
import (
	"encoding/json"
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
)
```

to:

```go
import (
	"context"
	"encoding/json"
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add src/cmd/api-server/server.go src/cmd/api-server/clients_admin.go src/cmd/api-server/clients_admin_test.go
git commit -m "feat(api-server): add PATCH /api/v1/clients/{hostname}/description and /attributes"
```

---

### Task 11: `api-server` — `sans` PATCH endpoint

**Files:**
- Modify: `src/cmd/api-server/server.go`
- Modify: `src/cmd/api-server/clients_admin.go`
- Modify: `src/cmd/api-server/clients_admin_test.go`

**Interfaces:**
- Produces: handler `handleUpdateSANs`; route `PATCH /api/v1/clients/{hostname}/sans` — completes `api-server`'s client-write REST surface.

- [ ] **Step 1: Add the route**

In `src/cmd/api-server/server.go`, add one line to `registerRoutes` immediately after the `attributes` route added in Task 10:

```go
	mux.HandleFunc("PATCH /api/v1/clients/{hostname}/attributes", s.handleUpdateAttributes)
	mux.HandleFunc("PATCH /api/v1/clients/{hostname}/sans", s.handleUpdateSANs)
```

- [ ] **Step 2: Write the failing tests**

Append to `src/cmd/api-server/clients_admin_test.go`:

```go
func TestHandleUpdateSANs_SendsAddAndRemove(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateSANsResp: &pb.Client{Hostname: "node-1", Sans: []string{"new.internal"}}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/sans", strings.NewReader(`{"add":["new.internal"],"remove":["old.internal"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"new.internal"}, fake.lastUpdateSANsReq.GetAdd())
	assert.Equal(t, []string{"old.internal"}, fake.lastUpdateSANsReq.GetRemove())
}

func TestHandleUpdateSANs_MalformedJSONReturns400(t *testing.T) {
	srv := newServerWithAdmin(&fakeClientManagerAdminClient{})
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/sans", strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateSANs_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateSANsErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/ghost/sans", strings.NewReader(`{"add":["x.internal"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleUpdateSANs -v`
Expected: FAIL to compile — `s.handleUpdateSANs` undefined

- [ ] **Step 4: Implement the handler**

Append to `src/cmd/api-server/clients_admin.go`:

```go
type sansUpdateInput struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

func (s *server) handleUpdateSANs(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	var in sansUpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	client, err := s.clientManagerAdmin.UpdateSANs(r.Context(), &pb.UpdateClientSANsRequest{Hostname: hostname, Add: in.Add, Remove: in.Remove})
	if err != nil {
		s.logger.Error("handleUpdateSANs: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(client))
}
```

- [ ] **Step 5: Run the full `api-server` test suite**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS — every test in the package, old and new.

- [ ] **Step 6: Full workspace build, vet, and test**

Run: `cd src && go build ./... && go vet ./... && go test ./...`
Expected: no errors, all tests pass

- [ ] **Step 7: Commit**

```bash
git add src/cmd/api-server/server.go src/cmd/api-server/clients_admin.go src/cmd/api-server/clients_admin_test.go
git commit -m "feat(api-server): add PATCH /api/v1/clients/{hostname}/sans, completing the client-write REST surface"
```

---

### Task 12: Deployment — Dockerfile, entrypoint, docker-compose

**Files:**
- Modify: `deploy/control-plane/clientmanager-api/Dockerfile`
- Modify: `deploy/control-plane/clientmanager-api/entrypoint.sh`
- Modify: `deploy/control-plane/docker-compose.yml`

**Interfaces:**
- Consumes: `bin/clientmanager-admin-api` (built by Task 3's Makefile target), `deploy/control-plane/ca/data/certs/root_ca.crt` and `deploy/control-plane/ca/data/secrets/password` (existing files, already mounted into `issuer`'s container the same way).

- [ ] **Step 1: Update the Dockerfile's build stage**

In `deploy/control-plane/clientmanager-api/Dockerfile`, change:

```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make clientmanager-api certclient agent policyclient
```

to:

```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make clientmanager-api clientmanager-admin-api certclient agent policyclient
```

- [ ] **Step 2: Update the Dockerfile's copy step**

In the same file, change:

```dockerfile
COPY --from=builder /build/bin/clientmanager-api /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
```

to:

```dockerfile
COPY --from=builder /build/bin/clientmanager-api /build/bin/clientmanager-admin-api /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
```

- [ ] **Step 3: Update `entrypoint.sh` to run both binaries**

In `deploy/control-plane/clientmanager-api/entrypoint.sh`, change the final line:

```sh
exec ./clientmanager-api --debug="${DEBUG:-false}"
```

to:

```sh
./clientmanager-api --debug="${DEBUG:-false}" &
./clientmanager-admin-api --debug="${DEBUG:-false}" \
	--ca-url https://step-ca:9000 \
	--root /data/root_ca.crt \
	--provisioner admin@backup.internal \
	--password-file /data/secrets/password &
wait
```

(Plain POSIX `wait` — `entrypoint.sh` is `#!/bin/sh`, where bash's `wait -n` doesn't exist. This waits for both background jobs to exit before the script itself exits, matching this spec's documented trade-off: the container doesn't automatically stop if only one of the two processes crashes.)

- [ ] **Step 4: Update `docker-compose.yml`'s `clientmanager-api` service**

In `deploy/control-plane/docker-compose.yml`, change the `clientmanager-api` service block from:

```yaml
  clientmanager-api:
    build:
      context: ../..
      dockerfile: deploy/control-plane/clientmanager-api/Dockerfile
    depends_on:
      - step-ca
      - issuer
    volumes:
      - ./clientmanager-api/data:/data
      - ./clientmanager-api/local.conf:/data/local.conf:ro
      - ./client-manager/data:/data/client-manager
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    ports:
      - "9500:9500"
    restart: unless-stopped
```

to:

```yaml
  clientmanager-api:
    build:
      context: ../..
      dockerfile: deploy/control-plane/clientmanager-api/Dockerfile
    depends_on:
      - step-ca
      - issuer
    volumes:
      - ./clientmanager-api/data:/data
      - ./clientmanager-api/local.conf:/data/local.conf:ro
      - ./client-manager/data:/data/client-manager
      - ./ca/data/certs/root_ca.crt:/data/root_ca.crt:ro
      - ./ca/data/secrets/password:/data/secrets/password:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    ports:
      - "9500:9500"
      - "9501:9501"
    restart: unless-stopped
```

(`api-server`'s service block already lists `clientmanager-api` in its `depends_on` — no change needed there, since `clientmanager-admin-api` lives inside that same container.)

- [ ] **Step 5: Validate the compose file parses**

Run: `cd deploy/control-plane && docker compose config --quiet`
Expected: no output, exit code 0 (confirms YAML is well-formed and references resolve — this does not require `docker compose up`)

- [ ] **Step 6: Build the Docker image**

Run: `cd /home/alex/miniprotector && docker build -f deploy/control-plane/clientmanager-api/Dockerfile -t clientmanager-api-check .`
Expected: image builds successfully, confirming both binaries compile and copy correctly inside the container.

- [ ] **Step 7: Commit**

```bash
git add deploy/control-plane/clientmanager-api/Dockerfile deploy/control-plane/clientmanager-api/entrypoint.sh deploy/control-plane/docker-compose.yml
git commit -m "feat(deploy): package clientmanager-admin-api alongside clientmanager-api in one container"
```

---

### Task 13: Documentation

**Files:**
- Create: `docs/protocols/clientmanager-admin.md`
- Create: `docs/components/clientmanager-admin-api.md`
- Modify: `docs/components/clientmanager-api.md`
- Modify: `docs/components/client-manager.md`
- Modify: `docs/components/api-server.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Write the protocol doc**

Create `docs/protocols/clientmanager-admin.md`:

```markdown
# ClientManagerAdmin Protocol

`api-server` → `clientmanager-admin-api`'s sole RPC surface: CA-admin-equivalent writes onto the
same `clientmanager.sqlite` file `client-manager`'s CLI, `issuer`, and `clientmanager-api` already
share. mTLS (`common/mtls`, same transport every other gRPC call in this project uses). `api-server`
is the sole intended caller — see [Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
for why this isn't enforced at the transport layer (the existing mesh-wide "any operating-tier cert
may call any RPC it can reach" convention applies here too, deliberately).

## RPC

```proto
service ClientManagerAdminService {
  rpc AddClient(AddClientRequest) returns (AddClientResponse);
  rpc ReEnrollClient(ReEnrollClientRequest) returns (ReEnrollClientResponse);
  rpc RevokeClient(RevokeClientRequest) returns (clientmanagerapiservice.Client);
  rpc UnrevokeClient(UnrevokeClientRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateDescription(UpdateClientKVRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateAttributes(UpdateClientKVRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateSANs(UpdateClientSANsRequest) returns (clientmanagerapiservice.Client);
}
```

`RevokeClient`/`UnrevokeClient`/`UpdateDescription`/`UpdateAttributes`/`UpdateSANs` all return the
same `Client` message [`clientmanager-api`](../components/clientmanager-api.md)'s
`ListClients`/`GetClient` already return (imported from `clientmanager.proto` rather than
duplicated) — the caller sees the record's new state immediately, without a follow-up read.

## Behavior

- **`AddClient`**: mints a one-time enrollment token via `common/certmint.Mint` (the same mechanism
  `client-manager add` uses) and records the client, in that order — a mint failure never records a
  client, and an already-enrolled hostname (`codes.AlreadyExists`) never re-mints. `hostname` is
  required (`codes.InvalidArgument` if empty).
- **`ReEnrollClient`**: mints a fresh token for an already-tracked hostname (`codes.NotFound`
  otherwise). `sans`, if given, overrides the stored SAN list for this token only and is **not**
  persisted back to the record — matches `client-manager re-enroll`'s existing behavior exactly. Use
  `UpdateSANs` for a persistent SAN change.
- **`RevokeClient`/`UnrevokeClient`**: flip the stored `revoked` flag/timestamp. `codes.NotFound` for
  an untracked hostname. Enforcement (refusing a revoked hostname's next operating-certificate
  request) remains [`issuer`](../components/issuer.md)'s job, unchanged by this service.
- **`UpdateDescription`/`UpdateAttributes`**: apply `set` (upsert) then `unset` (delete) against the
  named key/value kind. `codes.NotFound` for an untracked hostname, mid-way through a batch included
  — a request against an unknown hostname leaves no partial writes only in the trivial sense that the
  very first `SetKV`/`UnsetKV` call already fails for an untracked hostname (the store checks
  existence before every write).
- **`UpdateSANs`**: applies `add` then `remove` against the hostname's SAN list — both are no-ops
  (not errors) for an alias already present/absent, matching `Store.AddSAN`/`RemoveSAN`. Both
  description/attribute and SAN changes reach an already-bootstrapped node on its next
  operating-certificate refresh, the same "genuinely live" mechanism
  [Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
  established — nothing here is retroactive to a certificate already issued.

## See Also

- [clientmanager-admin-api](../components/clientmanager-admin-api.md)
- [clientmanager-api](../components/clientmanager-api.md) — the read-only sibling service
- [client-manager](../components/client-manager.md) — the CLI this service's write logic mirrors
- [REST API v1](../api/rest-v1.md) — `api-server`'s REST surface onto this protocol
- [Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
- [Security Model](../SECURITY.md)
```

- [ ] **Step 2: Write the component doc**

Create `docs/components/clientmanager-admin-api.md`:

```markdown
# clientmanager-admin-api

CA-admin-equivalent gRPC writes onto `client-manager`'s enrolled-client data: issue/re-enroll
enrollment tokens, revoke/unrevoke, and manage description/attribute/SAN metadata — reachable
over the network via [`api-server`](./api-server.md), unlike `client-manager`'s CLI (which stays a
single-operator, no-network tool; this is an additional path, not a replacement). **Control-plane
component**, holds the CA provisioner password directly (same requirement as
[`client-manager`](./client-manager.md)), and shares `clientmanager.sqlite` with `client-manager`,
[`issuer`](./issuer.md), and [`clientmanager-api`](./clientmanager-api.md).

Deliberately a separate binary from `clientmanager-api`, packaged in the *same container* (see
Deployment, below): `clientmanager-api` stays completely password-free and read-only; this service's
RPC surface is fixed and small (seven RPCs, no unrelated read/query features), which is what keeps a
process holding CA-admin-equivalent access auditable. See
[Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
for the full reasoning, including what packaging both services in one container trades away
(filesystem-level isolation, in exchange for one shared `agent`/enrollment instead of two).

## Usage

```bash
clientmanager-admin-api --port 9501 --ca-url https://step-ca:9000 --root /data/root_ca.crt \
    --provisioner admin@backup.internal --password-file /data/secrets/password
```

| Flag | Default | Description |
|------|---------|--------------|
| `--port` | `clientmanager_admin_api_port` config value (default: 9501) | Port to listen on |
| `--ca-url` | `https://localhost:9000` | CA URL |
| `--root` | `deploy/control-plane/ca/data/certs/root_ca.crt` | Path to the CA's root certificate |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `deploy/control-plane/ca/data/secrets/password` | Path to the provisioner password file |
| `--debug` | false | Enable debug logging |

## How It Works

No new business logic: every RPC calls the same `storage/clientmanager.Store` methods and
`common/certmint.Mint` function `client-manager`'s CLI already uses. See the
[ClientManagerAdmin protocol](../protocols/clientmanager-admin.md) for the full RPC behavior.

## Configuration Keys

- `clientmanager_admin_api_port` — port to listen on *(default: 9501)*
- `var_path` — must point at the same directory `client-manager`'s SQLite database lives in (shared
  volume with `client-manager`/`issuer`/`clientmanager-api`)

## Certificates

Same mTLS pattern as every other mesh component: identity bootstrapped/renewed via `certclient`
against `MP_CONFIG_PATH/certs` — shared with `clientmanager-api`, since both binaries run in the same
container and use the same `agent`-managed identity (see Deployment).

## Deployment

Ships in the *same container* as `clientmanager-api` (one Dockerfile, one `entrypoint.sh`, one
`agent` process, one mesh enrollment) rather than a separate service — see
[`deploy/control-plane/README.md`](../../deploy/control-plane/README.md) and the design spec's
"Packaging" section for why. Additionally mounts the CA's root certificate and provisioner password
file read-only, the same two mounts [`issuer`](./issuer.md) already has.

## Building

```bash
make clientmanager-admin-api
```

## See Also

- [clientmanager-api](./clientmanager-api.md) — the read-only sibling sharing this container
- [client-manager](./client-manager.md) — the CLI this service's write logic mirrors
- [issuer](./issuer.md) — enforces `revoked`, reads live `attribute`/SAN values; unaffected by this service
- [api-server](./api-server.md) — the only intended caller
- [ClientManagerAdmin Protocol](../protocols/clientmanager-admin.md)
- [REST API v1](../api/rest-v1.md)
- [Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
- [Security Model](../SECURITY.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 3: Cross-link from `clientmanager-api.md`**

In `docs/components/clientmanager-api.md`, in its `## See Also` section, add this line immediately after the entry for `client-manager`:

```markdown
- [client-manager](./client-manager.md) — the CLI tool sharing this component's database
- [clientmanager-admin-api](./clientmanager-admin-api.md) — the write-capable sibling service, packaged in the same container
```

- [ ] **Step 4: Cross-link from `client-manager.md`**

In `docs/components/client-manager.md`, in its `## See Also` section, add this line immediately after the entry for `clientmanager-api`:

```markdown
- [clientmanager-api](./clientmanager-api.md) — a separate daemon sharing this component's database
  for read-only access, the same way `issuer` already does; `client-manager` itself is unaffected
- [clientmanager-admin-api](./clientmanager-admin-api.md) — a separate daemon sharing this component's
  database for network-reachable writes (issue/revoke/description/attribute/SAN), reachable via
  `api-server`; this CLI remains available for direct, on-host admin access
```

- [ ] **Step 5: Update `api-server.md`**

In `docs/components/api-server.md`, change the opening sentence of the summary paragraph:

```markdown
Unified REST API in front of the control plane's client, catalog, and policy data — for browsers
and admin tools that don't hold a mesh mTLS client certificate. Client and catalog access are
read-only; policies additionally support create/update/delete. **Control-plane component.**
```

to:

```markdown
Unified REST API in front of the control plane's client, catalog, and policy data — for browsers
and admin tools that don't hold a mesh mTLS client certificate. Catalog access is read-only; policies
support create/update/delete; client data supports both read (via `clientmanager-api`) and writes —
enroll/re-enroll, revoke/unrevoke, description/attribute/SAN management (via
`clientmanager-admin-api`, see [Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)).
**Control-plane component.**
```

In the same file's `## Configuration Keys` section, add two lines immediately after `clientmanager_api_host` / `clientmanager_api_port`:

```markdown
- `clientmanager_api_host` / `clientmanager_api_port` — where to dial `clientmanager-api`
- `clientmanager_admin_api_host` / `clientmanager_admin_api_port` — where to dial `clientmanager-admin-api` *(default port: 9501)*
```

In the same file's `## See Also` section, add this line immediately after the entry for `clientmanager-api`:

```markdown
- [clientmanager-api](./clientmanager-api.md) — one of the two backends this component reads from
- [clientmanager-admin-api](./clientmanager-admin-api.md) — the write-capable backend behind this component's client-write endpoints
```

- [ ] **Step 6: Update `docs/api/rest-v1.md`**

In `docs/api/rest-v1.md`, immediately after the existing `## GET /api/v1/clients/{hostname}` section and before `## GET /api/v1/catalog`, insert:

```markdown
## `POST /api/v1/clients`

Enrolls a new client and mints a one-time enrollment token for it. Body:

```json
{"hostname": "node-east-02", "sans": ["node-east-02.internal"]}
```

`201` with `{"hostname": "...", "token": "..."}` on success — the token is returned exactly once;
relay it to the target node out-of-band, the same as `client-manager add` today. `400` if `hostname`
is empty. `409` if `hostname` is already enrolled.

## `POST /api/v1/clients/{hostname}/reenroll`

Mints a fresh enrollment token for an already-tracked hostname. Body (optional):

```json
{"sans": ["override.internal"]}
```

`200` with `{"hostname": "...", "token": "..."}`. `sans`, if given, overrides the stored SAN list for
this token only — it is not persisted; use `PATCH .../sans` for a persistent change. `404` if
`hostname` isn't enrolled.

## `POST /api/v1/clients/{hostname}/revoke`

## `POST /api/v1/clients/{hostname}/unrevoke`

No body. `200` with the client's updated record (same shape as `GET /api/v1/clients/{hostname}`).
`404` if `hostname` isn't enrolled. Enforcement (refusing a revoked node's next operating-certificate
request) happens on the node's next credential refresh, not synchronously with this call.

## `PATCH /api/v1/clients/{hostname}/description`

## `PATCH /api/v1/clients/{hostname}/attributes`

Partial update — set then unset, per key (not a full-replace `PUT` like policies get). Body:

```json
{"set": {"owner": "alice"}, "unset": ["old-key"]}
```

`200` with the client's updated record. `404` if `hostname` isn't enrolled. `attributes` is this
system's "attribute labels" (the same key/value pairs `policy-server`'s `client_filters.labels`
matches against) — JSON field stays `attributes`, matching `GET /api/v1/clients`'s existing response
shape.

## `PATCH /api/v1/clients/{hostname}/sans`

Body:

```json
{"add": ["new.internal"], "remove": ["old.internal"]}
```

`200` with the client's updated record. `404` if `hostname` isn't enrolled. Adding an already-present
alias or removing an absent one is a no-op, not an error.

```

- [ ] **Step 7: Update `README.md`**

In `README.md`'s `## Components` section, add this line immediately after the entry for `clientmanager-api` (or `client-manager`, whichever this file lists last among that family):

```markdown
- **[clientmanager-admin-api](docs/components/clientmanager-admin-api.md)** - CA-admin-equivalent gRPC writes (issue/revoke/description/attribute/SAN) onto client-manager's enrolled-client data, reachable via `api-server`
```

- [ ] **Step 8: Update `docs/ARCHITECTURE.md`**

In the components table (the block containing the `clientmanager-api` and `api-server` rows), add a
row immediately after the `clientmanager-api` row:

```markdown
| clientmanager-api | Read-only gRPC daemon exposing `client-manager`'s enrolled-client data (`ListClients`/`GetClient`), sharing its SQLite file the same way `issuer` already does | Implemented |
| clientmanager-admin-api | CA-admin-equivalent gRPC writes (issue/re-enroll/revoke/unrevoke/description/attribute/SAN) onto the same database, packaged in clientmanager-api's container | Implemented |
```

In the "Control Plane vs. Agents" table's `Components` row, add `clientmanager-admin-api` immediately
after `clientmanager-api`:

```markdown
| Components | `deploy/control-plane/ca/` (step-ca container), `catalog`, `policy-server`, `client-manager`, `issuer`, `clientmanager-api`, `clientmanager-admin-api`, `api-server` | `bwfs`, `brfs`, `rwfs`, `certclient`, `agent` |
```

In the same table's `Runs where` row, add it to the CA-host group:

```markdown
| Runs where | On the CA host (`client-manager`, `issuer`, `clientmanager-api`, `clientmanager-admin-api`); `catalog`/`policy-server`/`api-server` run centrally, wherever each deployment lives — see below | Dial `ca_host:9000` outbound for enrollment/renewal and `issuer_host:9200` outbound for operating-certificate refresh, and `policy_server_host:9300` outbound for policy fetching; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
```

In the same table's `Network role` row, append a clause about `clientmanager-admin-api`:

```markdown
| Network role | Serves enrollment/renewal/admin (`/sign`, `/renew`, `/roots`, `/provisioners`) on `:9000`; `issuer` serves `RequestOperatingCert`/`DescribeSANs` on `:9200` (mTLS); `policy-server` serves `GetPolicies` on `:9300` (mTLS, fetched by `agent` via `policyclient`); `clientmanager-api` serves `ListClients`/`GetClient` on `:9500` (mTLS); `clientmanager-admin-api` serves `AddClient`/`ReEnrollClient`/`RevokeClient`/`UnrevokeClient`/`UpdateDescription`/`UpdateAttributes`/`UpdateSANs` on `:9501` (mTLS) — a third holder of CA-admin-equivalent access, alongside `client-manager` and `issuer`, stated explicitly rather than left implicit; `api-server` serves this system's first REST (not gRPC) surface on `:8090` (plain HTTP, bearer-token authenticated), dialing `clientmanager-api`, `clientmanager-admin-api`, and `catalog` outbound over mTLS on their behalf — none of these has a role in backup traffic | Dial `ca_host:9000` (bootstrap/renew) and `issuer_host:9200` (operating-refresh) outbound only; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
```

Immediately after the paragraph beginning `clientmanager-api runs on the CA host alongside...`
(around line 54), add a new paragraph:

```markdown
`clientmanager-admin-api` runs alongside `clientmanager-api` in the *same container*, sharing one
mesh identity/`agent` process rather than enrolling separately — see
[clientmanager-admin-api](components/clientmanager-admin-api.md) and
[Design: clientmanager-admin-api](superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
for why (avoiding a second one-time enrollment token and a second `agent` process for what would
otherwise be a purely operational cost, at the price of shared container-filesystem isolation between
the two binaries).
```

- [ ] **Step 9: Add the CHANGELOG entry**

At the top of `CHANGELOG.md`, immediately after the `All notable changes...` line and before the most
recent existing entry, insert:

```markdown
## 2026-07-19 — clientmanager-admin-api: network-reachable client enrollment/revocation/metadata writes

Added `clientmanager-admin-api`, a new gRPC daemon holding the CA provisioner password directly and
exposing the write operations `client-manager`'s CLI already had (issue/re-enroll enrollment tokens,
revoke/unrevoke, description/attribute/SAN management) over the network for the first time, via seven
new `api-server` REST endpoints under `/api/v1/clients`. Packaged in the same container as the
existing (unchanged, still read-only) `clientmanager-api` to avoid a second mesh enrollment, keeping
the two as separate processes for isolation. `client-manager`'s CLI remains available unchanged for
direct, on-host admin access.
```

- [ ] **Step 10: Commit**

```bash
git add docs/protocols/clientmanager-admin.md docs/components/clientmanager-admin-api.md docs/components/clientmanager-api.md docs/components/client-manager.md docs/components/api-server.md docs/api/rest-v1.md README.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document clientmanager-admin-api, its protocol, and the new client-write REST endpoints"
```

---

## Self-Review

**Spec coverage:**
- Architecture / write path reusing existing store + certmint logic → Task 1 (extraction), Tasks 3–7 (RPCs).
- gRPC surface (`clientmanageradmin.proto`, all 7 RPCs, `AlreadyExists`/`NotFound` semantics) → Task 2 (proto), Tasks 3–7 (implementation).
- REST surface (all 7 endpoints, JSON shapes, `attributes` field naming, `409` mapping) → Tasks 8–11.
- Deployment (same container, two binaries, shared volumes/ports, `wait`-based entrypoint) → Task 12.
- Security Evaluation's stated trade-offs (third password holder, shared-filesystem isolation loss, no caller restriction, existing bearer token, token-exposure-once) → reflected as doc content in Task 13's protocol/component docs rather than code — no code task was skipped for these, since the spec explicitly chose *not* to add caller restriction or a second token (both are documentation of an accepted trade-off, not a build step).
- Testing plan (unit tests per RPC/handler, `LoadClientView` extraction test, `api-server` error-mapping tests) → covered across Tasks 1, 3–11.
- Documentation Impact list → Task 13, one step per listed file.

**Placeholder scan:** no TBD/TODO; every step shows complete code; no "similar to Task N" references — Tasks 4–7 and 9–11 each restate their own complete test/implementation code rather than pointing back at Task 3/8.

**Type consistency:** `clientManagerAdminClient` interface (Task 8) method signatures match `pb.ClientManagerAdminServiceClient` exactly as generated in Task 2, and match `clientManagerAdminServer`'s method signatures from Tasks 3–7 (same request/response types throughout: `*pb.AddClientRequest`→`*pb.AddClientResponse`, etc.). `toProtoClient` (Task 1, Task 3) has matching signatures in both packages (`func toProtoClient(v *clientmanagerstore.ClientView) *pb.Client`) — intentionally duplicated per-package, not shared, as the spec's Architecture section specifies. `ClientManagerAdminAPIPort`/`ClientManagerAdminAPIHost` (Task 3) are the exact names Task 8's `main.go` edit references.
