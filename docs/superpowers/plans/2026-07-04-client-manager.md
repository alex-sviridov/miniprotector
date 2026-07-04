# Client Manager (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persistent, admin-facing `client-manager` tool that tracks enrolled clients (when added, descriptions, RBAC-bound attributes, revoked status) and mints their enrollment tokens through a new, narrowly-scoped `certrequest serve` RPC — without `client-manager` ever holding the CA's provisioner password.

**Architecture:** `certrequest` grows a `serve` mode (mirrors `agent serve`/`agent list-policies`): a persistent gRPC service on/near the CA host exposing exactly one mTLS-authenticated RPC, `MintEnrollmentToken`, trusting only the single peer whose verified hostname matches `client_manager_host`. `client-manager` is a new binary — an ordinarily-enrolled node with its own SQLite DB (`clients`, `client_kv` tables) — whose `add`/`re-enroll` subcommands call that RPC instead of touching the CA directly. Everything else is local DB CRUD.

**Tech Stack:** Go, cobra (CLI), gorm + `modernc.org/sqlite` (DB), gRPC + protobuf (RPC), existing `common/mtls`/`common/connection` helpers (mTLS transport — no new TLS code).

## Global Constraints

- No changes to `common/mtls`'s handshake/verification logic — out of scope per the approved spec (`docs/superpowers/specs/2026-07-04-client-manager-design.md`).
- `revoke`/`unrevoke` and `attribute` values are stored only in this phase — no enforcement, no cert-baking. That's phase 2 (separate spec, not this plan).
- `client-manager` never reads the CA's provisioner password or `deploy/control-plane/ca/data/secrets/password` — only `certrequest serve` does.
- All new config keys follow the existing flat `key=value` `local.conf` format in `src/common/config/config.go` — no new config file or format.
- All new gRPC transport reuses `common/mtls`/`common/connection` — no new TLS code.
- All new SQLite storage reuses the `gorm.io/driver/sqlite` + `modernc.org/sqlite` + `SetMaxOpenConns(1)` + `PRAGMA journal_mode=WAL` pattern already established in `src/storage/catalog/db.go`.

---

## File Structure

| File | Responsibility |
|---|---|
| `src/common/config/config.go` (modify) | New `ClientManagerHost`, `CertrequestHost`, `CertrequestPort` fields + parsing |
| `src/common/certmint/certmint.go` (new) | Factored-out provisioner token-minting call, shared by `certrequest`'s CLI and `serve` modes |
| `src/api/enrollment_broker.proto` (new) + generated | `MintEnrollmentToken` RPC schema |
| `src/cmd/certrequest/broker_server.go` (new) | `certrequest serve`'s gRPC handler: hostname-match auth + mint |
| `src/cmd/certrequest/arguments.go` (modify) | Add `serve` subcommand alongside the existing one-shot CLI |
| `src/cmd/certrequest/main.go` (modify) | Dispatch mint (existing, now via `certmint`) vs. `serve` |
| `src/cmd/certrequest/e2e_test.go` (modify) | Use `certmint.Mint` instead of inlined calls |
| `src/storage/clientmanager/models.go`, `db.go`, `store.go` (new) | `clients`/`client_kv` SQLite schema + CRUD |
| `src/cmd/clientmanager/arguments.go` (new) | Cobra command tree, built incrementally across Tasks 7–9 |
| `src/cmd/clientmanager/main.go` (new) | Entrypoint, config/store wiring, action dispatch |
| `src/cmd/clientmanager/broker_client.go` (new) | mTLS client calling `certrequest serve` |
| `src/cmd/clientmanager/add.go` (new) | `add`/`re-enroll` |
| `src/cmd/clientmanager/list.go` (new) | `list`/`show`/`revoke`/`unrevoke` |
| `src/cmd/clientmanager/label.go` (new) | `description`/`attribute` `set`/`unset` |
| `Makefile` (modify) | `clientmanager` build target |
| `docs/components/client-manager.md` (new), `docs/components/certrequest.md`, `docs/protocols/enrollment-broker.md` (new), `docs/ARCHITECTURE.md`, `README.md` (modify) | Documentation |

---

### Task 1: Config keys

**Files:**
- Modify: `src/common/config/config.go`
- Test: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `Config.ClientManagerHost string`, `Config.CertrequestHost string`, `Config.CertrequestPort int` (default `9100`), parsed from `client_manager_host`, `certrequest_host`, `certrequest_port` keys.

- [ ] **Step 1: Write the failing tests**

Append to `src/common/config/config_test.go`:

```go
func TestParseConfig_ClientManagerHostParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nclient_manager_host=client-manager.internal\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "client-manager.internal", conf.ClientManagerHost)
}

func TestParseConfig_CertrequestHostParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\ncertrequest_host=ca.backup.internal\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "ca.backup.internal", conf.CertrequestHost)
}

func TestParseConfig_CertrequestPortDefaultsTo9100(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9100, conf.CertrequestPort)
}

func TestParseConfig_CertrequestPortParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\ncertrequest_port=9200\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9200, conf.CertrequestPort)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/config/... -run TestParseConfig_ClientManagerHost -v`
Expected: FAIL — `conf.ClientManagerHost` undefined (compile error).

- [ ] **Step 3: Implement**

In `src/common/config/config.go`, add three fields to the `Config` struct (after `ReconcileIntervalSec`):

```go
type Config struct {
	// ...existing fields...
	VarPath              string
	ReconcileIntervalSec int
	ClientManagerHost    string
	CertrequestHost      string
	CertrequestPort      int
}
```

Add `CertrequestPort: 9100,` to the defaults literal in `ParseConfig`:

```go
	config := &Config{
		JobTimeoutSec:              30,
		CatalogSyncBatchSize:       500,
		CatalogSyncPollIntervalSec: 5,
		CatalogSyncMaxBackoffSec:   60,
		CatalogPort:                15723,
		ReconcileIntervalSec:       30,
		CertrequestPort:            9100,
	}
```

Add three new `case`s to the `switch key` block (alongside `case "catalog_host":`):

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

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS (all tests, including pre-existing ones).

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add client_manager_host, certrequest_host, certrequest_port"
```

---

### Task 2: Extract shared token-minting logic (`certmint`)

Pure refactor — `certrequest`'s one-shot CLI behavior must not change. Sets up the code both `certrequest serve` (Task 4) and the e2e test will call.

**Files:**
- Create: `src/common/certmint/certmint.go`
- Modify: `src/cmd/certrequest/main.go`
- Modify: `src/cmd/certrequest/e2e_test.go` (build-tagged `e2e`, run separately)

**Interfaces:**
- Produces: `certmint.Options{CAURL, RootFile, Provisioner, PasswordFile string}`, `certmint.Mint(hostname string, sans []string, opts Options) (string, error)`.

- [ ] **Step 1: Create the shared package**

`src/common/certmint/certmint.go`:

```go
// Package certmint mints one-time CA enrollment tokens using a
// provisioner's password-protected key. Shared by certrequest's one-shot
// CLI and its serve mode -- the only two callers that need CA-admin-
// equivalent access to a provisioner's key.
package certmint

import (
	"fmt"
	"os"
	"strings"

	"github.com/smallstep/certificates/ca"
)

// Options bundles the inputs needed to mint a token for a hostname.
type Options struct {
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
}

// Mint decrypts the named provisioner's key (password-gated, read fresh
// from PasswordFile on every call -- never cached) and mints a one-time
// enrollment token for hostname, with sans as additional SAN aliases.
func Mint(hostname string, sans []string, opts Options) (string, error) {
	passwordBytes, err := os.ReadFile(opts.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("read password file: %w", err)
	}
	password := []byte(strings.TrimSpace(string(passwordBytes)))

	provisioner, err := ca.NewProvisioner(opts.Provisioner, "", opts.CAURL, password, ca.WithRootFile(opts.RootFile))
	if err != nil {
		return "", fmt.Errorf("load provisioner: %w", err)
	}

	allSANs := append([]string{hostname}, sans...)
	token, err := provisioner.Token(hostname, allSANs...)
	if err != nil {
		return "", fmt.Errorf("mint token: %w", err)
	}
	return token, nil
}
```

- [ ] **Step 2: Point `certrequest`'s CLI at it**

In `src/cmd/certrequest/main.go`, replace the body of `main()` from the `passwordBytes` read through the `provisioner.Token` call with:

```go
	token, err := certmint.Mint(args.Hostname, args.SANs, certmint.Options{
		CAURL:        args.CAURL,
		RootFile:     args.RootFile,
		Provisioner:  args.Provisioner,
		PasswordFile: args.PasswordFile,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to mint token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(token)
```

Update the import block: remove `"strings"` and `"github.com/smallstep/certificates/ca"`, add `"github.com/alex-sviridov/miniprotector/common/certmint"`.

- [ ] **Step 3: Run the existing unit tests to confirm no regression**

Run: `cd src && go test ./cmd/certrequest/... -v`
Expected: PASS (all of `arguments_test.go`'s existing tests — they test `parseArguments`, untouched).

- [ ] **Step 4: Update the e2e test to exercise the shared function**

In `src/cmd/certrequest/e2e_test.go`, replace:

```go
	provisioner, err := ca.NewProvisioner("admin@backup.internal", "", caURL, []byte(password), ca.WithRootFile(rootPath))
	require.NoError(t, err, "ca.NewProvisioner")

	token, err := provisioner.Token("e2e-test-host")
	require.NoError(t, err, "Provisioner.Token")
	require.NotEmpty(t, token)
```

with:

```go
	token, err := certmint.Mint("e2e-test-host", nil, certmint.Options{
		CAURL:        caURL,
		RootFile:     rootPath,
		Provisioner:  "admin@backup.internal",
		PasswordFile: filepath.Join(secretsDir, "password"),
	})
	require.NoError(t, err, "certmint.Mint")
	require.NotEmpty(t, token)
```

Add `"github.com/alex-sviridov/miniprotector/common/certmint"` to the import block.

- [ ] **Step 5: Run the e2e test to confirm no regression**

Run: `cd src && go test -tags=e2e -timeout=120s ./cmd/certrequest/... -run TestE2E_TokenMintAndRedeem -v`
Expected: PASS (skips with a clear message if Docker isn't available).

- [ ] **Step 6: Commit**

```bash
git add src/common/certmint/certmint.go src/cmd/certrequest/main.go src/cmd/certrequest/e2e_test.go
git commit -m "refactor(certrequest): extract token minting into common/certmint"
```

---

### Task 3: `enrollment_broker.proto`

**Files:**
- Create: `src/api/enrollment_broker.proto`
- Generated (via `make proto`): `src/api/enrollment_broker.pb.go`, `src/api/enrollment_broker_grpc.pb.go`

**Interfaces:**
- Produces: `pb.EnrollmentBrokerServiceServer`, `pb.RegisterEnrollmentBrokerServiceServer`, `pb.NewEnrollmentBrokerServiceClient`, `pb.MintEnrollmentTokenRequest{Hostname string, Sans []string}`, `pb.MintEnrollmentTokenResponse{Token string}`, `pb.UnimplementedEnrollmentBrokerServiceServer`.

- [ ] **Step 1: Write the proto file**

`src/api/enrollment_broker.proto`:

```proto
syntax = "proto3";

package enrollmentbroker;

option go_package = "./proto";

// EnrollmentBrokerService is certrequest serve's entire RPC surface: one
// method, mint an enrollment token, gated by an exact caller-hostname
// match against client_manager_host -- not the mesh's usual
// any-valid-cert-is-trusted posture, since this RPC is equivalent to
// CA-admin privilege.
service EnrollmentBrokerService {
  rpc MintEnrollmentToken(MintEnrollmentTokenRequest) returns (MintEnrollmentTokenResponse);
}

message MintEnrollmentTokenRequest {
  string hostname = 1;
  repeated string sans = 2;
}

message MintEnrollmentTokenResponse {
  string token = 1;
}
```

- [ ] **Step 2: Generate the Go code**

Run: `make proto`
Expected output: `Protobuf code generated in src/api/` and new files `src/api/enrollment_broker.pb.go`, `src/api/enrollment_broker_grpc.pb.go` present.

If `protoc`/`protoc-gen-go`/`protoc-gen-go-grpc` aren't installed, install them per your Go toolchain's usual method before this step; that setup is outside this plan's scope (the repo's existing `api/*.proto` files already establish this dependency).

- [ ] **Step 3: Confirm it compiles**

Run: `cd src && go build ./api/...`
Expected: no output, exit code 0.

- [ ] **Step 4: Commit**

```bash
git add src/api/enrollment_broker.proto src/api/enrollment_broker.pb.go src/api/enrollment_broker_grpc.pb.go
git commit -m "feat(api): add enrollment_broker proto (MintEnrollmentToken RPC)"
```

---

### Task 4: `certrequest serve`'s broker server (auth + mint)

**Files:**
- Create: `src/cmd/certrequest/broker_server.go`
- Test: `src/cmd/certrequest/broker_server_test.go`

**Interfaces:**
- Consumes: `pb.EnrollmentBrokerServiceServer`/`pb.MintEnrollmentTokenRequest`/`Response` (Task 3), `mtls.PeerHostname(ctx) (string, error)` (existing, `src/common/mtls/peer.go`).
- Produces: `newBrokerServer(trustedCaller string, mint mintFunc) *brokerServer`, `type mintFunc func(hostname string, sans []string) (string, error)` — the real one wired in Task 5 is `func(hostname string, sans []string) (string, error) { return certmint.Mint(hostname, sans, opts) }`.

- [ ] **Step 1: Write the failing tests**

`src/cmd/certrequest/broker_server_test.go`:

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
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// fakeAuthContext mirrors cmd/catalog/server_test.go's helper of the same
// name: builds a context carrying a self-signed cert with the given
// hostname as its SAN, simulating a verified mTLS peer identity without a
// real handshake.
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

func TestMintEnrollmentToken_TrustedCallerMints(t *testing.T) {
	called := false
	mint := func(hostname string, sans []string) (string, error) {
		called = true
		assert.Equal(t, "node-east-01", hostname)
		assert.Equal(t, []string{"node-east-01.internal"}, sans)
		return "tok-abc", nil
	}
	srv := newBrokerServer("client-manager.internal", mint)

	resp, err := srv.MintEnrollmentToken(fakeAuthContext(t, "client-manager.internal"), &pb.MintEnrollmentTokenRequest{
		Hostname: "node-east-01",
		Sans:     []string{"node-east-01.internal"},
	})
	require.NoError(t, err)
	assert.Equal(t, "tok-abc", resp.Token)
	assert.True(t, called)
}

func TestMintEnrollmentToken_UntrustedCallerRejected(t *testing.T) {
	called := false
	mint := func(hostname string, sans []string) (string, error) {
		called = true
		return "tok-abc", nil
	}
	srv := newBrokerServer("client-manager.internal", mint)

	_, err := srv.MintEnrollmentToken(fakeAuthContext(t, "attacker.internal"), &pb.MintEnrollmentTokenRequest{
		Hostname: "node-east-01",
	})
	assert.Error(t, err)
	assert.False(t, called, "mint must not be called for an untrusted caller")
}

func TestMintEnrollmentToken_NoPeerIdentityRejected(t *testing.T) {
	srv := newBrokerServer("client-manager.internal", func(string, []string) (string, error) {
		t.Fatal("mint must not be called without a peer identity")
		return "", nil
	})

	_, err := srv.MintEnrollmentToken(context.Background(), &pb.MintEnrollmentTokenRequest{Hostname: "node-east-01"})
	assert.Error(t, err)
}

func TestMintEnrollmentToken_EmptyHostnameRejected(t *testing.T) {
	srv := newBrokerServer("client-manager.internal", func(string, []string) (string, error) {
		t.Fatal("mint must not be called for an empty hostname")
		return "", nil
	})

	_, err := srv.MintEnrollmentToken(fakeAuthContext(t, "client-manager.internal"), &pb.MintEnrollmentTokenRequest{Hostname: ""})
	assert.Error(t, err)
}

func TestMintEnrollmentToken_MintFailurePropagates(t *testing.T) {
	srv := newBrokerServer("client-manager.internal", func(string, []string) (string, error) {
		return "", assert.AnError
	})

	_, err := srv.MintEnrollmentToken(fakeAuthContext(t, "client-manager.internal"), &pb.MintEnrollmentTokenRequest{Hostname: "node-east-01"})
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/certrequest/... -run TestMintEnrollmentToken -v`
Expected: FAIL — `newBrokerServer`/`brokerServer` undefined (compile error).

- [ ] **Step 3: Implement**

`src/cmd/certrequest/broker_server.go`:

```go
package main

import (
	"context"
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
)

// mintFunc mints a token for hostname/sans. Production wires this to
// certmint.Mint; tests inject a stub so this file's unit tests never touch
// a real CA (that's the e2e test's job, Task 5).
type mintFunc func(hostname string, sans []string) (string, error)

// brokerServer implements EnrollmentBrokerService: the sole RPC an
// enrolled node may call to obtain a fresh CA enrollment token, gated by
// exact hostname match against trustedCaller (client_manager_host from
// local.conf) rather than "any valid cert" -- this RPC is equivalent to
// CA-admin privilege, so it does not use the mesh's normal
// any-cert-is-trusted posture.
type brokerServer struct {
	pb.UnimplementedEnrollmentBrokerServiceServer
	trustedCaller string
	mint          mintFunc
}

func newBrokerServer(trustedCaller string, mint mintFunc) *brokerServer {
	return &brokerServer{trustedCaller: trustedCaller, mint: mint}
}

func (s *brokerServer) MintEnrollmentToken(ctx context.Context, req *pb.MintEnrollmentTokenRequest) (*pb.MintEnrollmentTokenResponse, error) {
	caller, err := mtls.PeerHostname(ctx)
	if err != nil {
		return nil, fmt.Errorf("determine caller identity: %w", err)
	}
	if caller != s.trustedCaller {
		return nil, fmt.Errorf("caller %q is not the trusted client-manager (%q)", caller, s.trustedCaller)
	}

	hostname := req.GetHostname()
	if hostname == "" {
		return nil, fmt.Errorf("hostname is required")
	}

	token, err := s.mint(hostname, req.GetSans())
	if err != nil {
		return nil, fmt.Errorf("mint token for %s: %w", hostname, err)
	}
	return &pb.MintEnrollmentTokenResponse{Token: token}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/certrequest/... -run TestMintEnrollmentToken -v`
Expected: PASS (all 5 tests).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/certrequest/broker_server.go src/cmd/certrequest/broker_server_test.go
git commit -m "feat(certrequest): add broker server for MintEnrollmentToken"
```

---

### Task 5: Wire `certrequest serve` into the CLI, plus a real-CA e2e test

**Files:**
- Modify: `src/cmd/certrequest/arguments.go`
- Modify: `src/cmd/certrequest/main.go`
- Test: `src/cmd/certrequest/serve_e2e_test.go` (new, build-tagged `e2e`)

**Interfaces:**
- Consumes: `newBrokerServer` (Task 4), `certmint.Mint`/`certmint.Options` (Task 2), `connection.StartServer`/`connection.Connect` (existing, `src/common/connection`), `config.Config.ClientManagerHost`/`CertrequestPort` (Task 1).

- [ ] **Step 1: Add the `serve` subcommand**

In `src/cmd/certrequest/arguments.go`, restructure `parseArguments` so the existing one-shot behavior becomes the root command's own `Args`/`Run`, and a new `serve` subcommand is added alongside it. Replace the whole function body:

```go
// Arguments holds parsed command line arguments.
type Arguments struct {
	Action string // "mint" (default, positional hostname) | "serve"

	// mint fields
	Hostname     string
	SANs         []string
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string

	// serve-only fields
	Debug bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{Action: "mint"}
	var caURLFlag, defaultsFile string

	cmd := &cobra.Command{
		Use:   "certrequest <hostname>",
		Short: "Mint a one-time enrollment token for a node",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, cliArgs []string) {
			args.Hostname = cliArgs[0]
		},
	}
	cmd.Flags().StringArrayVar(&args.SANs, "san", nil, "Additional SAN alias for the token (repeatable)")
	cmd.Flags().StringVar(&caURLFlag, "ca-url", "", "CA URL, e.g. https://localhost:9000 (default: read from --defaults-file)")
	cmd.Flags().StringVar(&defaultsFile, "defaults-file", "deploy/control-plane/ca/data/config/defaults.json", "Path to step-ca's defaults.json, used to default --ca-url")
	cmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	cmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	cmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the enrollment broker: mints tokens on behalf of the trusted client-manager only",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			args.Action = "serve"
		},
	}
	serveCmd.Flags().StringVar(&caURLFlag, "ca-url", "", "CA URL, e.g. https://localhost:9000 (default: read from --defaults-file)")
	serveCmd.Flags().StringVar(&defaultsFile, "defaults-file", "deploy/control-plane/ca/data/config/defaults.json", "Path to step-ca's defaults.json, used to default --ca-url")
	serveCmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	serveCmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	serveCmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")
	serveCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
	cmd.AddCommand(serveCmd)

	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "mint" {
		if args.Hostname == "" {
			return nil, fmt.Errorf("hostname is required")
		}
		args.CAURL = caURLFlag
		if args.CAURL == "" {
			defaultURL, err := readDefaultCAURL(defaultsFile)
			if err != nil {
				return nil, fmt.Errorf("--ca-url not given and could not be read from %s: %w", defaultsFile, err)
			}
			args.CAURL = defaultURL
		}
		return args, nil
	}

	// serve
	args.CAURL = caURLFlag
	if args.CAURL == "" {
		defaultURL, err := readDefaultCAURL(defaultsFile)
		if err != nil {
			return nil, fmt.Errorf("--ca-url not given and could not be read from %s: %w", defaultsFile, err)
		}
		args.CAURL = defaultURL
	}
	return args, nil
}
```

(`readDefaultCAURL` is unchanged, already in this file.)

- [ ] **Step 2: Run the existing arguments tests**

Run: `cd src && go test ./cmd/certrequest/... -run TestParseArguments -v`
Expected: PASS — the existing mint-path tests still hold (`args.Action` defaults to `"mint"`, unchecked by any existing assertion, so no test needs updating).

- [ ] **Step 3: Dispatch `serve` in `main.go`**

Replace `src/cmd/certrequest/main.go`'s body with:

```go
// certrequest mints a one-time enrollment token for a node, run on or near
// the CA host. It also runs as a persistent broker (`certrequest serve`)
// that mints tokens on behalf of client-manager over mTLS -- see
// docs/components/certrequest.md's "serve mode" section.
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
	"google.golang.org/grpc"
)

func main() {
	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	if args.Action == "serve" {
		runServe(args)
		return
	}

	token, err := certmint.Mint(args.Hostname, args.SANs, certmint.Options{
		CAURL:        args.CAURL,
		RootFile:     args.RootFile,
		Provisioner:  args.Provisioner,
		PasswordFile: args.PasswordFile,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to mint token: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(token)
}

func runServe(args *Arguments) {
	const appName = "certrequest"

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
	if conf.ClientManagerHost == "" {
		fmt.Fprintln(os.Stderr, "client_manager_host not set in local.conf")
		os.Exit(1)
	}

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

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
	mint := func(hostname string, sans []string) (string, error) {
		return certmint.Mint(hostname, sans, mintOpts)
	}
	srv := newBrokerServer(conf.ClientManagerHost, mint)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("certrequest serve started", "port", conf.CertrequestPort, "trusted_caller", conf.ClientManagerHost)

	if err := connection.StartServer(signalCtx, logger, conf.CertrequestPort, certsDir, func(s *grpc.Server) {
		pb.RegisterEnrollmentBrokerServiceServer(s, srv)
	}); err != nil {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Confirm it builds**

Run: `cd src && go build ./cmd/certrequest/...`
Expected: no output, exit code 0.

- [ ] **Step 5: Write the e2e test**

`src/cmd/certrequest/serve_e2e_test.go` (reuses `requireDocker`, `repoRootDir`, `copyFile`, `copyComposeFileWithEphemeralPort`, `discoverHostPort`, `randomPassword`, `waitForCA` — all already defined, same package, in `e2e_test.go`):

```go
//go:build e2e

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallstep/certificates/ca"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/connection"
)

const brokerFixtureCertsDir = "../../common/testdata/certs"

func freeTCPPortForServe(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestE2E_ServeMintsRealRedeemableToken proves certrequest serve's
// MintEnrollmentToken RPC, talking to a real throwaway step-ca, returns a
// token certclient's own bootstrap path can actually redeem -- not just
// that the RPC plumbing round-trips (unit tests in broker_server_test.go
// already cover the auth-rejection path with a stubbed minter; this test's
// job is the real certmint.Mint call over a real gRPC/mTLS transport).
func TestE2E_ServeMintsRealRedeemableToken(t *testing.T) {
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

	projectName := fmt.Sprintf("certrequest-serve-e2e-%d", time.Now().UnixNano())
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

	mintOpts := certmint.Options{
		CAURL:        caURL,
		RootFile:     rootPath,
		Provisioner:  "admin@backup.internal",
		PasswordFile: filepath.Join(secretsDir, "password"),
	}
	mint := func(hostname string, sans []string) (string, error) {
		return certmint.Mint(hostname, sans, mintOpts)
	}

	// The fixture certs dir's identity ("bwfs.internal", per
	// common/mtls's existing test fixtures) is used for both the
	// server's own transport identity and the calling client's identity
	// below -- auth-rejection is already covered by unit tests with a
	// stubbed minter; this test is only about the real minting call.
	srv := newBrokerServer("bwfs.internal", mint)

	grpcPort := freeTCPPortForServe(t)
	serverCtx, stopServer := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(serverCtx, testLogger(), grpcPort, brokerFixtureCertsDir, func(s *grpc.Server) {
			pb.RegisterEnrollmentBrokerServiceServer(s, srv)
		})
	}()
	t.Cleanup(func() {
		stopServer()
		<-errCh
	})
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", grpcPort), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "broker server did not start listening")

	conn, err := connection.Connect("localhost", grpcPort, 5, brokerFixtureCertsDir)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewEnrollmentBrokerServiceClient(conn)
	resp, err := client.MintEnrollmentToken(context.Background(), &pb.MintEnrollmentTokenRequest{Hostname: "e2e-enrolled-host"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Token)

	// Redeem it exactly like certclient's bootstrap path does.
	caClient, err := ca.Bootstrap(resp.Token)
	require.NoError(t, err, "ca.Bootstrap")
	req, _, err := ca.CreateSignRequest(resp.Token)
	require.NoError(t, err, "ca.CreateSignRequest")
	signResp, err := caClient.Sign(req)
	require.NoError(t, err, "Client.Sign")
	leaf, err := ca.Certificate(signResp)
	require.NoError(t, err)
	require.Equal(t, "e2e-enrolled-host", leaf.Subject.CommonName)
}
```

This needs a `testLogger()` helper; add it to the same file (it isn't defined elsewhere in this package):

```go
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
```

Add `"log/slog"` to the import block.

- [ ] **Step 6: Run the e2e test**

Run: `cd src && go test -tags=e2e -timeout=120s ./cmd/certrequest/... -run TestE2E_ServeMintsRealRedeemableToken -v`
Expected: PASS (or a clear Docker-unavailable skip message).

- [ ] **Step 7: Commit**

```bash
git add src/cmd/certrequest/arguments.go src/cmd/certrequest/main.go src/cmd/certrequest/serve_e2e_test.go
git commit -m "feat(certrequest): wire serve subcommand into the CLI, add e2e coverage"
```

---

### Task 6: `storage/clientmanager` — schema + CRUD

**Files:**
- Create: `src/storage/clientmanager/models.go`
- Create: `src/storage/clientmanager/db.go`
- Create: `src/storage/clientmanager/store.go`
- Test: `src/storage/clientmanager/store_test.go`

**Interfaces:**
- Produces: `clientmanager.New(varDir string) (*Store, error)`, `(*Store).AddClient(hostname string, addedAt time.Time) error`, `(*Store).GetClient(hostname string) (*ClientRecord, error)`, `(*Store).ListClients() ([]ClientRecord, error)`, `(*Store).SetRevoked(hostname string, revoked bool, at time.Time) error`, `(*Store).KV(hostname string, kind KVKind) ([]ClientKVRecord, error)`, `(*Store).SetKV(hostname string, kind KVKind, key, value string) error`, `(*Store).UnsetKV(hostname string, kind KVKind, key string) error`, `(*Store).Close() error`, `ErrClientExists`, `ErrClientNotFound`, `KindDescription`, `KindAttribute`.

- [ ] **Step 1: Write the failing tests**

`src/storage/clientmanager/store_test.go`:

```go
package clientmanager

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestAddClient_ThenGetClient_RoundTrips(t *testing.T) {
	store := newTestStore(t)
	addedAt := time.Now().Truncate(time.Second)

	require.NoError(t, store.AddClient("node-1", addedAt))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", got.Hostname)
	assert.True(t, addedAt.Equal(got.AddedAt))
	assert.False(t, got.Revoked)
}

func TestAddClient_DuplicateReturnsErrClientExists(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))

	err := store.AddClient("node-1", time.Now())
	assert.ErrorIs(t, err, ErrClientExists)
}

func TestGetClient_UnknownReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetClient("ghost")
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestListClients_OrderedByHostname(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("zebra", time.Now()))
	require.NoError(t, store.AddClient("apple", time.Now()))

	clients, err := store.ListClients()
	require.NoError(t, err)
	require.Len(t, clients, 2)
	assert.Equal(t, "apple", clients[0].Hostname)
	assert.Equal(t, "zebra", clients[1].Hostname)
}

func TestSetRevoked_ThenGetClient_ReflectsFlag(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))
	revokedAt := time.Now().Truncate(time.Second)

	require.NoError(t, store.SetRevoked("node-1", true, revokedAt))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.True(t, got.Revoked)
	require.NotNil(t, got.RevokedAt)
	assert.True(t, revokedAt.Equal(*got.RevokedAt))

	require.NoError(t, store.SetRevoked("node-1", false, time.Now()))
	got, err = store.GetClient("node-1")
	require.NoError(t, err)
	assert.False(t, got.Revoked)
	assert.Nil(t, got.RevokedAt)
}

func TestSetRevoked_UnknownReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.SetRevoked("ghost", true, time.Now())
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestSetKV_ThenKV_RoundTrips(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))

	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "alice"))
	require.NoError(t, store.SetKV("node-1", KindAttribute, "role", "prod-db"))

	descs, err := store.KV("node-1", KindDescription)
	require.NoError(t, err)
	require.Len(t, descs, 1)
	assert.Equal(t, "owner", descs[0].Key)
	assert.Equal(t, "alice", descs[0].Value)

	attrs, err := store.KV("node-1", KindAttribute)
	require.NoError(t, err)
	require.Len(t, attrs, 1)
	assert.Equal(t, "role", attrs[0].Key)
	assert.Equal(t, "prod-db", attrs[0].Value)
}

func TestSetKV_UpsertOverwritesValue(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))
	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "alice"))
	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "bob"))

	descs, err := store.KV("node-1", KindDescription)
	require.NoError(t, err)
	require.Len(t, descs, 1)
	assert.Equal(t, "bob", descs[0].Value)
}

func TestSetKV_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.SetKV("ghost", KindDescription, "owner", "alice")
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestUnsetKV_RemovesRow(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))
	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "alice"))

	require.NoError(t, store.UnsetKV("node-1", KindDescription, "owner"))

	descs, err := store.KV("node-1", KindDescription)
	require.NoError(t, err)
	assert.Empty(t, descs)
}

func TestNew_OpensAndClosesCleanly(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Close())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./storage/clientmanager/... -v`
Expected: FAIL — package `clientmanager` has no exported `New`/`Store` (compile error; package doesn't exist yet).

- [ ] **Step 3: Implement the models**

`src/storage/clientmanager/models.go`:

```go
package clientmanager

import "time"

// ClientRecord is one enrolled client tracked by client-manager.
type ClientRecord struct {
	Hostname  string `gorm:"primaryKey"`
	AddedAt   time.Time
	Revoked   bool
	RevokedAt *time.Time
}

// KVKind distinguishes annotation-only descriptions from attributes meant
// to be baked into a client's certificate by a future CA-side mechanism
// (phase 2, not this package).
type KVKind string

const (
	KindDescription KVKind = "description"
	KindAttribute   KVKind = "attribute"
)

// ClientKVRecord is one key/value pair (description or attribute) attached
// to a client. (Hostname, Kind, Key) is the primary key.
type ClientKVRecord struct {
	Hostname string `gorm:"primaryKey"`
	Kind     KVKind `gorm:"primaryKey"`
	Key      string `gorm:"primaryKey"`
	Value    string
}
```

- [ ] **Step 4: Implement the DB open/migrate**

`src/storage/clientmanager/db.go`:

```go
package clientmanager

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

func openDB(varDir string) (*gorm.DB, error) {
	if err := os.MkdirAll(varDir, 0755); err != nil {
		return nil, fmt.Errorf("create var dir: %w", err)
	}

	dbPath := filepath.Join(varDir, "clientmanager.sqlite") + "?_busy_timeout=5000"

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

	if err := db.AutoMigrate(&ClientRecord{}, &ClientKVRecord{}); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("automigrate: %w", err)
	}
	return db, nil
}
```

- [ ] **Step 5: Implement the store**

`src/storage/clientmanager/store.go`:

```go
package clientmanager

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrClientExists   = errors.New("client already exists")
	ErrClientNotFound = errors.New("client not found")
)

type Store struct {
	db *gorm.DB
}

func New(varDir string) (*Store, error) {
	db, err := openDB(varDir)
	if err != nil {
		return nil, fmt.Errorf("open client-manager db: %w", err)
	}
	return &Store{db: db}, nil
}

// AddClient records a newly-enrolled client. Returns ErrClientExists if
// hostname is already tracked -- callers use re-enrollment or
// description/attribute updates for an existing client instead of add.
func (s *Store) AddClient(hostname string, addedAt time.Time) error {
	var count int64
	if err := s.db.Model(&ClientRecord{}).Where("hostname = ?", hostname).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrClientExists
	}
	return s.db.Create(&ClientRecord{Hostname: hostname, AddedAt: addedAt}).Error
}

// GetClient returns hostname's record, or ErrClientNotFound.
func (s *Store) GetClient(hostname string) (*ClientRecord, error) {
	var rec ClientRecord
	err := s.db.First(&rec, "hostname = ?", hostname).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrClientNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListClients returns every tracked client, ordered by hostname.
func (s *Store) ListClients() ([]ClientRecord, error) {
	var recs []ClientRecord
	err := s.db.Order("hostname").Find(&recs).Error
	return recs, err
}

// SetRevoked updates hostname's revoked flag/timestamp. Returns
// ErrClientNotFound if hostname isn't tracked. Clearing the flag also
// clears revoked_at.
func (s *Store) SetRevoked(hostname string, revoked bool, at time.Time) error {
	updates := map[string]any{"revoked": revoked}
	if revoked {
		updates["revoked_at"] = at
	} else {
		updates["revoked_at"] = nil
	}
	res := s.db.Model(&ClientRecord{}).Where("hostname = ?", hostname).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrClientNotFound
	}
	return nil
}

// KV returns all rows of the given kind for hostname, ordered by key.
func (s *Store) KV(hostname string, kind KVKind) ([]ClientKVRecord, error) {
	var recs []ClientKVRecord
	err := s.db.Where("hostname = ? AND kind = ?", hostname, kind).Order("key").Find(&recs).Error
	return recs, err
}

// SetKV upserts one key/value pair for hostname. Returns ErrClientNotFound
// if hostname isn't tracked.
func (s *Store) SetKV(hostname string, kind KVKind, key, value string) error {
	if _, err := s.GetClient(hostname); err != nil {
		return err
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hostname"}, {Name: "kind"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&ClientKVRecord{Hostname: hostname, Kind: kind, Key: key, Value: value}).Error
}

// UnsetKV deletes one key/value pair for hostname. Returns ErrClientNotFound
// if hostname isn't tracked.
func (s *Store) UnsetKV(hostname string, kind KVKind, key string) error {
	if _, err := s.GetClient(hostname); err != nil {
		return err
	}
	return s.db.Delete(&ClientKVRecord{}, "hostname = ? AND kind = ? AND key = ?", hostname, kind, key).Error
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd src && go test ./storage/clientmanager/... -v`
Expected: PASS (all tests).

- [ ] **Step 7: Commit**

```bash
git add src/storage/clientmanager/
git commit -m "feat(clientmanager): add SQLite-backed client/description/attribute store"
```

---

### Task 7: `client-manager` CLI skeleton — `add`/`re-enroll`

**Files:**
- Create: `src/cmd/clientmanager/arguments.go`
- Create: `src/cmd/clientmanager/broker_client.go`
- Create: `src/cmd/clientmanager/add.go`
- Create: `src/cmd/clientmanager/main.go`
- Test: `src/cmd/clientmanager/add_test.go`

**Interfaces:**
- Consumes: `clientmanagerstore.New/Store/ErrClientNotFound` (Task 6), `config.Config`/`ResolveConfigPath`/`ParseConfig`/`ResolveVarDir`/`ResolveCertsDir` (existing + Task 1), `connection.Connect` (existing), `pb.NewEnrollmentBrokerServiceClient`/`MintEnrollmentTokenRequest` (Task 3).
- Produces: `Arguments{Action, Hostname, SANs, KVPairs, Key string/[]string}`, `parseArguments() (*Arguments, error)`, `mintToken(conf *config.Config, certsDir, hostname string, sans []string) (string, error)`, `type minter func(conf *config.Config, certsDir, hostname string, sans []string) (string, error)`, `runAdd`/`runReEnroll(conf, certsDir, store, args, mint minter, out io.Writer) error`, `run(conf, certsDir, store, args, out) error`.

- [ ] **Step 1: Write the CLI arg parsing (add + re-enroll only)**

`src/cmd/clientmanager/arguments.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments for whichever subcommand
// was invoked; Action tells the caller which fields are populated.
type Arguments struct {
	Action   string // "add" | "re-enroll" (more added in later tasks)
	Hostname string
	SANs     []string
	KVPairs  []string // "key=value" strings, for description/attribute set (Task 9)
	Key      string   // for description/attribute unset (Task 9)
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "client-manager <command>",
		Short: "Manage enrolled clients: list, annotate, revoke",
	}

	addCmd := &cobra.Command{
		Use:   "add <hostname>",
		Short: "Enroll a new client and record it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "add"
			args.Hostname = cliArgs[0]
			return nil
		},
	}
	addCmd.Flags().StringArrayVar(&args.SANs, "san", nil, "Additional SAN alias for the token (repeatable)")

	reEnrollCmd := &cobra.Command{
		Use:   "re-enroll <hostname>",
		Short: "Mint a fresh token for an already-tracked client",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "re-enroll"
			args.Hostname = cliArgs[0]
			return nil
		},
	}

	rootCmd.AddCommand(addCmd, reEnrollCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: add, re-enroll")
	}
	return args, nil
}
```

- [ ] **Step 2: Write the broker client**

`src/cmd/clientmanager/broker_client.go`:

```go
package main

import (
	"context"
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
)

// minter mints an enrollment token for hostname via a broker. Tests inject
// a stub; production wires mintToken.
type minter func(conf *config.Config, certsDir, hostname string, sans []string) (string, error)

// mintToken dials certrequest serve (at conf.CertrequestHost:CertrequestPort)
// over mTLS using this node's own identity and asks it to mint an
// enrollment token for hostname. client-manager never holds the CA's
// provisioner password itself -- see docs/components/certrequest.md's
// "serve mode" section for why.
func mintToken(conf *config.Config, certsDir, hostname string, sans []string) (string, error) {
	if conf.CertrequestHost == "" {
		return "", fmt.Errorf("certrequest_host not set in local.conf")
	}
	conn, err := connection.Connect(conf.CertrequestHost, conf.CertrequestPort, 5, certsDir)
	if err != nil {
		return "", fmt.Errorf("connect to certrequest serve: %w", err)
	}
	defer conn.Close()

	client := pb.NewEnrollmentBrokerServiceClient(conn)
	resp, err := client.MintEnrollmentToken(context.Background(), &pb.MintEnrollmentTokenRequest{
		Hostname: hostname,
		Sans:     sans,
	})
	if err != nil {
		return "", fmt.Errorf("mint enrollment token: %w", err)
	}
	return resp.Token, nil
}
```

- [ ] **Step 3: Write `add`/`re-enroll` logic**

`src/cmd/clientmanager/add.go`:

```go
package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func runAdd(conf *config.Config, certsDir string, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	if _, err := store.GetClient(args.Hostname); err == nil {
		return fmt.Errorf("client %q already exists; use re-enroll or description/attribute set instead", args.Hostname)
	} else if !errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return fmt.Errorf("check existing client: %w", err)
	}

	token, err := mint(conf, certsDir, args.Hostname, args.SANs)
	if err != nil {
		return fmt.Errorf("add %s: %w", args.Hostname, err)
	}

	if err := store.AddClient(args.Hostname, time.Now()); err != nil {
		return fmt.Errorf("record client %s: %w", args.Hostname, err)
	}

	fmt.Fprintln(out, token)
	return nil
}

func runReEnroll(conf *config.Config, certsDir string, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	if _, err := store.GetClient(args.Hostname); err != nil {
		return fmt.Errorf("re-enroll %s: %w", args.Hostname, err)
	}

	token, err := mint(conf, certsDir, args.Hostname, args.SANs)
	if err != nil {
		return fmt.Errorf("re-enroll %s: %w", args.Hostname, err)
	}

	fmt.Fprintln(out, token)
	return nil
}
```

- [ ] **Step 4: Write `main.go`**

`src/cmd/clientmanager/main.go`:

```go
// client-manager owns the persistent list of enrolled clients: when they
// were added, their annotations and RBAC attributes, and whether they've
// been revoked. Enrollment itself is delegated to certrequest serve's
// MintEnrollmentToken RPC -- client-manager never holds the CA's
// provisioner password. See docs/components/client-manager.md.
package main

import (
	"fmt"
	"io"
	"os"

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

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

	if err := run(conf, certsDir, store, args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// run dispatches on args.Action. Broken out from main so tests can drive
// it directly against a temp-dir store without touching os.Exit.
func run(conf *config.Config, certsDir string, store *clientmanagerstore.Store, args *Arguments, out io.Writer) error {
	switch args.Action {
	case "add":
		return runAdd(conf, certsDir, store, args, mintToken, out)
	case "re-enroll":
		return runReEnroll(conf, certsDir, store, args, mintToken, out)
	default:
		return fmt.Errorf("unknown action %q", args.Action)
	}
}
```

- [ ] **Step 5: Write the failing tests, then confirm they pass**

`src/cmd/clientmanager/add_test.go`:

```go
package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/config"
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
	stubMint := func(conf *config.Config, certsDir, hostname string, sans []string) (string, error) {
		assert.Equal(t, "node-1", hostname)
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(&config.Config{}, "", store, args, stubMint, &out)
	require.NoError(t, err)
	assert.Equal(t, "tok-abc\n", out.String())

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", got.Hostname)
}

func TestRunAdd_DuplicateHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))

	called := false
	stubMint := func(conf *config.Config, certsDir, hostname string, sans []string) (string, error) {
		called = true
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(&config.Config{}, "", store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)
	assert.False(t, called, "mint must not be called for a duplicate add")
}

func TestRunAdd_MintFailureDoesNotRecordClient(t *testing.T) {
	store := newTestManagerStore(t)
	stubMint := func(conf *config.Config, certsDir, hostname string, sans []string) (string, error) {
		return "", assert.AnError
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(&config.Config{}, "", store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)

	_, err = store.GetClient("node-1")
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunReEnroll_UnknownHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	stubMint := func(conf *config.Config, certsDir, hostname string, sans []string) (string, error) {
		t.Fatal("mint must not be called for an unknown hostname")
		return "", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "ghost"}
	err := runReEnroll(&config.Config{}, "", store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)
}

func TestRunReEnroll_MintsFreshToken(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))
	var out bytes.Buffer
	stubMint := func(conf *config.Config, certsDir, hostname string, sans []string) (string, error) {
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1"}
	err := runReEnroll(&config.Config{}, "", store, args, stubMint, &out)
	require.NoError(t, err)
	assert.Equal(t, "tok-fresh\n", out.String())
}
```

Run: `cd src && go test ./cmd/clientmanager/... -v`
Expected: PASS (all 5 tests).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/clientmanager/
git commit -m "feat(clientmanager): add CLI skeleton with add/re-enroll"
```

---

### Task 8: `list`/`show`/`revoke`/`unrevoke`

**Files:**
- Modify: `src/cmd/clientmanager/arguments.go`
- Modify: `src/cmd/clientmanager/main.go`
- Create: `src/cmd/clientmanager/list.go`
- Test: `src/cmd/clientmanager/list_test.go`

**Interfaces:**
- Consumes: `Store.ListClients/GetClient/SetRevoked/KV` (Task 6).
- Produces: `runList(store, out) error`, `runShow(store, args, out) error`, `runRevoke(store, args) error`, `runUnrevoke(store, args) error`.

- [ ] **Step 1: Extend arg parsing**

In `src/cmd/clientmanager/arguments.go`, add before `rootCmd.AddCommand(addCmd, reEnrollCmd)`:

```go
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all tracked clients",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			args.Action = "list"
			return nil
		},
	}

	showCmd := &cobra.Command{
		Use:   "show <hostname>",
		Short: "Show a client's full detail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "show"
			args.Hostname = cliArgs[0]
			return nil
		},
	}

	revokeCmd := &cobra.Command{
		Use:   "revoke <hostname>",
		Short: "Mark a client revoked (does not yet block renewal -- see phase 2)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "revoke"
			args.Hostname = cliArgs[0]
			return nil
		},
	}

	unrevokeCmd := &cobra.Command{
		Use:   "unrevoke <hostname>",
		Short: "Clear a client's revoked flag",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "unrevoke"
			args.Hostname = cliArgs[0]
			return nil
		},
	}
```

Then change:

```go
	rootCmd.AddCommand(addCmd, reEnrollCmd)
```

to:

```go
	rootCmd.AddCommand(addCmd, reEnrollCmd, listCmd, showCmd, revokeCmd, unrevokeCmd)
```

And update the "subcommand is required" message:

```go
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: add, re-enroll, list, show, revoke, unrevoke")
	}
```

- [ ] **Step 2: Write `list.go`**

`src/cmd/clientmanager/list.go`:

```go
package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

const timeLayout = "2006-01-02 15:04:05"

func runList(store *clientmanagerstore.Store, out io.Writer) error {
	clients, err := store.ListClients()
	if err != nil {
		return fmt.Errorf("list clients: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HOSTNAME\tADDED_AT\tREVOKED\tLAST_SEEN")
	for _, c := range clients {
		revoked := "no"
		if c.Revoked {
			revoked = "yes"
		}
		// LAST_SEEN is always "unknown" in phase 1 -- renewal happens
		// directly between certclient and the CA, with no signal back
		// to client-manager until the phase-2 CA-side responder exists.
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Hostname, c.AddedAt.Format(timeLayout), revoked, "unknown")
	}
	return tw.Flush()
}

func runShow(store *clientmanagerstore.Store, args *Arguments, out io.Writer) error {
	client, err := store.GetClient(args.Hostname)
	if err != nil {
		return fmt.Errorf("show %s: %w", args.Hostname, err)
	}
	fmt.Fprintf(out, "hostname:   %s\n", client.Hostname)
	fmt.Fprintf(out, "added_at:   %s\n", client.AddedAt.Format(timeLayout))
	fmt.Fprintf(out, "revoked:    %v\n", client.Revoked)
	if client.RevokedAt != nil {
		fmt.Fprintf(out, "revoked_at: %s\n", client.RevokedAt.Format(timeLayout))
	}
	fmt.Fprintln(out, "last_seen:  unknown")

	descs, err := store.KV(args.Hostname, clientmanagerstore.KindDescription)
	if err != nil {
		return fmt.Errorf("show %s: load descriptions: %w", args.Hostname, err)
	}
	fmt.Fprintln(out, "descriptions:")
	for _, d := range descs {
		fmt.Fprintf(out, "  %s=%s\n", d.Key, d.Value)
	}

	attrs, err := store.KV(args.Hostname, clientmanagerstore.KindAttribute)
	if err != nil {
		return fmt.Errorf("show %s: load attributes: %w", args.Hostname, err)
	}
	fmt.Fprintln(out, "attributes:")
	for _, a := range attrs {
		fmt.Fprintf(out, "  %s=%s\n", a.Key, a.Value)
	}
	return nil
}

func runRevoke(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.SetRevoked(args.Hostname, true, time.Now()); err != nil {
		return fmt.Errorf("revoke %s: %w", args.Hostname, err)
	}
	return nil
}

func runUnrevoke(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.SetRevoked(args.Hostname, false, time.Now()); err != nil {
		return fmt.Errorf("unrevoke %s: %w", args.Hostname, err)
	}
	return nil
}
```

- [ ] **Step 3: Wire into `run()`**

In `src/cmd/clientmanager/main.go`, add cases to the `switch`:

```go
	case "list":
		return runList(store, out)
	case "show":
		return runShow(store, args, out)
	case "revoke":
		return runRevoke(store, args)
	case "unrevoke":
		return runUnrevoke(store, args)
```

- [ ] **Step 4: Write the failing tests, then confirm they pass**

`src/cmd/clientmanager/list_test.go`:

```go
package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func TestRunList_EmptyStore(t *testing.T) {
	store := newTestManagerStore(t)
	var out bytes.Buffer
	require.NoError(t, runList(store, &out))
	assert.Equal(t, "HOSTNAME  ADDED_AT  REVOKED  LAST_SEEN\n", out.String())
}

func TestRunList_ShowsAddedClients(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))
	var out bytes.Buffer
	require.NoError(t, runList(store, &out))
	assert.Contains(t, out.String(), "node-1")
	assert.Contains(t, out.String(), "unknown")
}

func TestRunShow_UnknownErrors(t *testing.T) {
	store := newTestManagerStore(t)
	err := runShow(store, &Arguments{Hostname: "ghost"}, &bytes.Buffer{})
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunShow_PrintsDescriptionsAndAttributes(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindDescription, "owner", "alice"))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindAttribute, "role", "prod-db"))

	var out bytes.Buffer
	require.NoError(t, runShow(store, &Arguments{Hostname: "node-1"}, &out))
	assert.Contains(t, out.String(), "owner=alice")
	assert.Contains(t, out.String(), "role=prod-db")
}

func TestRunRevoke_SetsFlag(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))
	require.NoError(t, runRevoke(store, &Arguments{Hostname: "node-1"}))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.True(t, got.Revoked)
}

func TestRunUnrevoke_ClearsFlag(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))
	require.NoError(t, runRevoke(store, &Arguments{Hostname: "node-1"}))
	require.NoError(t, runUnrevoke(store, &Arguments{Hostname: "node-1"}))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.False(t, got.Revoked)
}
```

Run: `cd src && go test ./cmd/clientmanager/... -v`
Expected: PASS (all tests, including Task 7's).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/clientmanager/arguments.go src/cmd/clientmanager/main.go src/cmd/clientmanager/list.go src/cmd/clientmanager/list_test.go
git commit -m "feat(clientmanager): add list/show/revoke/unrevoke"
```

---

### Task 9: `description`/`attribute` `set`/`unset`

**Files:**
- Modify: `src/cmd/clientmanager/arguments.go`
- Modify: `src/cmd/clientmanager/main.go`
- Create: `src/cmd/clientmanager/label.go`
- Test: `src/cmd/clientmanager/label_test.go`

**Interfaces:**
- Consumes: `Store.SetKV/UnsetKV`, `KindDescription`, `KindAttribute` (Task 6).
- Produces: `parseKV(s string) (key, value string, err error)`, `runKVSet(store, kind, args) error`, `runKVUnset(store, kind, args) error`.

- [ ] **Step 1: Extend arg parsing**

In `src/cmd/clientmanager/arguments.go`, add before the final `rootCmd.AddCommand(...)` line:

```go
	descriptionCmd := &cobra.Command{Use: "description", Short: "Manage a client's human-facing annotations"}
	descriptionCmd.AddCommand(
		&cobra.Command{
			Use:   "set <hostname> key=value [key=value...]",
			Short: "Set one or more description key/value pairs",
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "description-set"
				args.Hostname = cliArgs[0]
				args.KVPairs = cliArgs[1:]
				return nil
			},
		},
		&cobra.Command{
			Use:   "unset <hostname> <key>",
			Short: "Remove a description key",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "description-unset"
				args.Hostname = cliArgs[0]
				args.Key = cliArgs[1]
				return nil
			},
		},
	)

	attributeCmd := &cobra.Command{Use: "attribute", Short: "Manage a client's RBAC attributes (baked into future certificates)"}
	attributeCmd.AddCommand(
		&cobra.Command{
			Use:   "set <hostname> key=value [key=value...]",
			Short: "Set one or more attribute key/value pairs",
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "attribute-set"
				args.Hostname = cliArgs[0]
				args.KVPairs = cliArgs[1:]
				return nil
			},
		},
		&cobra.Command{
			Use:   "unset <hostname> <key>",
			Short: "Remove an attribute key",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "attribute-unset"
				args.Hostname = cliArgs[0]
				args.Key = cliArgs[1]
				return nil
			},
		},
	)
```

Change:

```go
	rootCmd.AddCommand(addCmd, reEnrollCmd, listCmd, showCmd, revokeCmd, unrevokeCmd)
```

to:

```go
	rootCmd.AddCommand(addCmd, reEnrollCmd, listCmd, showCmd, revokeCmd, unrevokeCmd, descriptionCmd, attributeCmd)
```

And update the error message:

```go
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: add, re-enroll, list, show, revoke, unrevoke, description, attribute")
	}
```

- [ ] **Step 2: Write `label.go`**

`src/cmd/clientmanager/label.go`:

```go
package main

import (
	"fmt"
	"strings"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

// parseKV splits a "key=value" string, erroring if the shape doesn't match.
func parseKV(s string) (key, value string, err error) {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", "", fmt.Errorf("invalid key=value pair: %q", s)
	}
	return parts[0], parts[1], nil
}

func runKVSet(store *clientmanagerstore.Store, kind clientmanagerstore.KVKind, args *Arguments) error {
	for _, pair := range args.KVPairs {
		key, value, err := parseKV(pair)
		if err != nil {
			return err
		}
		if err := store.SetKV(args.Hostname, kind, key, value); err != nil {
			return fmt.Errorf("set %s %s on %s: %w", kind, key, args.Hostname, err)
		}
	}
	return nil
}

func runKVUnset(store *clientmanagerstore.Store, kind clientmanagerstore.KVKind, args *Arguments) error {
	if err := store.UnsetKV(args.Hostname, kind, args.Key); err != nil {
		return fmt.Errorf("unset %s %s on %s: %w", kind, args.Key, args.Hostname, err)
	}
	return nil
}
```

- [ ] **Step 3: Wire into `run()`**

In `src/cmd/clientmanager/main.go`, add cases to the `switch`:

```go
	case "description-set":
		return runKVSet(store, clientmanagerstore.KindDescription, args)
	case "description-unset":
		return runKVUnset(store, clientmanagerstore.KindDescription, args)
	case "attribute-set":
		return runKVSet(store, clientmanagerstore.KindAttribute, args)
	case "attribute-unset":
		return runKVUnset(store, clientmanagerstore.KindAttribute, args)
```

- [ ] **Step 4: Write the failing tests, then confirm they pass**

`src/cmd/clientmanager/label_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func TestParseKV_ValidPair(t *testing.T) {
	key, value, err := parseKV("owner=alice")
	require.NoError(t, err)
	assert.Equal(t, "owner", key)
	assert.Equal(t, "alice", value)
}

func TestParseKV_MissingEqualsErrors(t *testing.T) {
	_, _, err := parseKV("owner")
	assert.Error(t, err)
}

func TestRunKVSet_MultiplePairs(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))

	args := &Arguments{Hostname: "node-1", KVPairs: []string{"owner=alice", "location=rack3"}}
	require.NoError(t, runKVSet(store, clientmanagerstore.KindDescription, args))

	descs, err := store.KV("node-1", clientmanagerstore.KindDescription)
	require.NoError(t, err)
	assert.Len(t, descs, 2)
}

func TestRunKVSet_UnknownHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	args := &Arguments{Hostname: "ghost", KVPairs: []string{"owner=alice"}}
	err := runKVSet(store, clientmanagerstore.KindDescription, args)
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunKVUnset_RemovesKey(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindAttribute, "role", "prod-db"))

	args := &Arguments{Hostname: "node-1", Key: "role"}
	require.NoError(t, runKVUnset(store, clientmanagerstore.KindAttribute, args))

	attrs, err := store.KV("node-1", clientmanagerstore.KindAttribute)
	require.NoError(t, err)
	assert.Empty(t, attrs)
}

func TestRunKVSet_KindsAreIsolated(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", time.Now()))

	require.NoError(t, runKVSet(store, clientmanagerstore.KindDescription, &Arguments{Hostname: "node-1", KVPairs: []string{"role=not-an-attribute"}}))

	attrs, err := store.KV("node-1", clientmanagerstore.KindAttribute)
	require.NoError(t, err)
	assert.Empty(t, attrs, "a description must not be visible as an attribute")
}
```

Run: `cd src && go test ./cmd/clientmanager/... -v`
Expected: PASS (all tests across all three test files in this package).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/clientmanager/arguments.go src/cmd/clientmanager/main.go src/cmd/clientmanager/label.go src/cmd/clientmanager/label_test.go
git commit -m "feat(clientmanager): add description/attribute set/unset"
```

---

### Task 10: Build target + documentation

**Files:**
- Modify: `Makefile`
- Create: `docs/components/client-manager.md`
- Modify: `docs/components/certrequest.md`
- Create: `docs/protocols/enrollment-broker.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `README.md`

- [ ] **Step 1: Add the Makefile target**

In `Makefile`, add near the other `*_CMD` variables:

```makefile
CLIENTMANAGER_CMD := cmd/clientmanager
```

Add to the `.PHONY` line (append `clientmanager`):

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certrequest certclient catalogsync catalog agent clientmanager test test-e2e lint control-plane-up
```

Add a new target, placed after the `agent:` target:

```makefile
clientmanager: $(BINARY_DIR) ## Build client-manager binary
	@printf "$(BLUE)Building client-manager...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/clientmanager ./$(CLIENTMANAGER_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/clientmanager"
```

- [ ] **Step 2: Verify the build**

Run: `make clientmanager && make certrequest`
Expected: both report `Built successfully`.

Run: `cd src && go test ./... 2>&1 | tail -30`
Expected: `ok` for every package touched by this plan (`common/config`, `common/certmint`, `api`, `cmd/certrequest`, `storage/clientmanager`, `cmd/clientmanager`), no failures.

Run: `cd src && go vet ./...`
Expected: no output, exit code 0.

- [ ] **Step 3: Write `docs/components/client-manager.md`**

```markdown
# client-manager

Owns the persistent list of enrolled clients: when they were added, free-form annotations
(`description`), attributes intended for future baking into a client's certificate (`attribute`,
see [Design: Client Manager](../superpowers/specs/2026-07-04-client-manager-design.md)), and a
revoked flag. **Control-plane tool** — runs on its own host, as an ordinarily-enrolled node (its
own mTLS identity via `certclient`), separate from the CA.

## Usage

```
client-manager add <hostname> [--san alias]...
client-manager re-enroll <hostname>
client-manager list
client-manager show <hostname>
client-manager revoke <hostname>
client-manager unrevoke <hostname>

client-manager description set <hostname> k=v [k=v...]
client-manager description unset <hostname> k
client-manager attribute set <hostname> k=v [k=v...]
client-manager attribute unset <hostname> k
```

`add`/`re-enroll` are the only two commands that touch the network: they call
[`certrequest serve`](./certrequest.md)'s `MintEnrollmentToken` RPC over mTLS, using
`client-manager`'s own bootstrapped identity — `client-manager` never holds the CA's provisioner
password. The returned token is printed to stdout for the operator to relay out-of-band, same as
`certrequest`'s one-shot CLI today. Everything else is local SQLite CRUD.

## Behavior

- `add` errors if `hostname` is already tracked (use `re-enroll` or `description|attribute set`
  instead) and records nothing locally unless minting actually succeeded.
- `revoke`/`unrevoke` only set a flag in `client-manager`'s own database in this phase — nothing
  yet blocks a renewal or invalidates a live certificate. See the design spec's "Non-Goals" for
  why, and its "Relationship to Phase 2" for what closes that gap.
- `attribute` values are stored only; baking them into an issued certificate requires the phase-2
  CA-side webhook responder, not yet built.
- `list`'s `LAST_SEEN` column always reads `unknown` in this phase — `client-manager` has no
  visibility into renewals, which happen directly between `certclient` and the CA.

## Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `certrequest_host` | | Host where `certrequest serve` runs (typically the CA host) |
| `certrequest_port` | 9100 | Port `certrequest serve` listens on |
| `var_path` | binary's own directory | Where `clientmanager.sqlite` lives |

## Building

```bash
make clientmanager
```

## See Also

- [certrequest](./certrequest.md) — `serve` mode is the only thing `client-manager` calls over the network
- [Enrollment Broker Protocol](../protocols/enrollment-broker.md)
- [Design: Client Manager](../superpowers/specs/2026-07-04-client-manager-design.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 4: Update `docs/components/certrequest.md`**

Add a new section after "How it works" (before "## Building"):

```markdown
## `serve` mode

```bash
certrequest serve
```

Runs as a persistent process, still on/near the CA host, holding the provisioner password and
exposing exactly one mTLS-authenticated RPC: `MintEnrollmentToken(hostname, sans)`. This is now
the highest-value target in the system (network-reachable, CA-admin-equivalent privilege), so its
surface stays deliberately minimal — no other RPCs, no revoke-forwarding, no query API.

It trusts exactly one caller: whoever's mTLS-verified hostname matches `client_manager_host` from
its own `local.conf`, checked via the same `mtls.PeerHostname` derivation `catalog` already uses
for `source_node`. Any other caller is rejected outright, regardless of whether its certificate is
otherwise valid — unlike the rest of the mesh, which trusts any CA-signed cert.

[`client-manager`](./client-manager.md) is the intended (and, after initial bootstrap, only)
caller — see its docs for the `add`/`re-enroll` flow this powers. This exists specifically so
`client-manager` never has to hold the CA's provisioner password itself: minting stays confined to
wherever `certrequest serve` runs, same as today's one-shot CLI.

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` | false | Enable debug logging |
| `--ca-url`, `--defaults-file`, `--root`, `--provisioner`, `--password-file` | same as the one-shot CLI | Provisioner credentials used to mint tokens on callers' behalf |

## Configuration Keys (serve mode)

| Key | Default | Description |
|-----|---------|-------------|
| `client_manager_host` | | The sole hostname `certrequest serve` trusts to call `MintEnrollmentToken` |
| `certrequest_port` | 9100 | Port `certrequest serve` listens on |
```

Add to its "See Also" list: `- [client-manager](./client-manager.md) — the intended caller of \`serve\` mode` and `- [Enrollment Broker Protocol](../protocols/enrollment-broker.md)`.

- [ ] **Step 5: Write `docs/protocols/enrollment-broker.md`**

```markdown
# Enrollment Broker Protocol

`client-manager` → `certrequest serve`'s `MintEnrollmentToken` RPC, mTLS (`common/mtls`, same
transport every other gRPC call in this project uses — no bespoke wire format).

## RPC

```proto
service EnrollmentBrokerService {
  rpc MintEnrollmentToken(MintEnrollmentTokenRequest) returns (MintEnrollmentTokenResponse);
}

message MintEnrollmentTokenRequest {
  string hostname = 1;
  repeated string sans = 2;
}

message MintEnrollmentTokenResponse {
  string token = 1;
}
```

## Authorization

The server (`certrequest serve`) checks the caller's mTLS-verified hostname
(`mtls.PeerHostname(ctx)`) against its own configured `client_manager_host`. A mismatch is
rejected outright — this is the one RPC in the system that does **not** use "any CA-signed cert is
trusted"; it's equivalent to CA-admin privilege, so only the single configured caller may invoke
it at all.

## Behavior

- `hostname`/`sans` mirror `certrequest`'s existing one-shot CLI's positional argument and `--san`
  flags — same minting call underneath (`common/certmint`), same token semantics (short-lived,
  single-use, `jti`-tracked by the CA itself).
- The returned `token` is never persisted by either side — `client-manager` prints it to stdout
  for the operator to relay out-of-band, same as `certrequest`'s CLI does today.
- Any minting failure (bad hostname, CA unreachable, provisioner password unreadable) surfaces as
  a gRPC error; the caller (`client-manager add`/`re-enroll`) does not record anything locally
  unless a token was actually returned.

## See Also

- [certrequest](../components/certrequest.md) — `serve` mode
- [client-manager](../components/client-manager.md)
- [Design: Client Manager](../superpowers/specs/2026-07-04-client-manager-design.md)
```

- [ ] **Step 6: Update `docs/ARCHITECTURE.md`**

In the "Components" table, add a row after `agent`:

```markdown
| client-manager | Owns the enrolled-client list: descriptions, RBAC-bound attributes, revoked status | Implemented (phase 1: no enforcement yet) |
```

In the "Control Plane vs. Agents" table's "Components" row, change:

```
`deploy/control-plane/ca/` (step-ca container), `certrequest`, `catalog`
```

to:

```
`deploy/control-plane/ca/` (step-ca container), `certrequest` (one-shot CLI and `serve` mode), `catalog`, `client-manager`
```

Add a short paragraph after the existing `agent` paragraph:

```markdown
`client-manager` is control plane by role (an admin-facing service tracking the enrolled-client
fleet) but, like `catalog`, bootstraps its own mTLS identity the same way agents do, via
`certclient`. Its only network role is calling `certrequest serve`'s `MintEnrollmentToken` RPC —
see [Design: Client Manager](superpowers/specs/2026-07-04-client-manager-design.md) for why
token-minting is routed through a narrow broker rather than giving `client-manager` the CA's
provisioner password directly.
```

- [ ] **Step 7: Update `README.md`**

In the "Components" bullet list, add after the `agent` line:

```markdown
- **[client-manager](docs/components/client-manager.md)** - Owns the enrolled-client list: descriptions, RBAC-bound attributes, revoked status (control-plane component)
```

- [ ] **Step 8: Commit**

```bash
git add Makefile docs/components/client-manager.md docs/components/certrequest.md docs/protocols/enrollment-broker.md docs/ARCHITECTURE.md README.md
git commit -m "docs: document client-manager and certrequest serve mode"
```

---

## Self-Review

**Spec coverage:**
- Persistent client list, `added_at`, `revoked` → Task 6 (`clients` table), Task 8 (`list`/`show`).
- Descriptions (annotation-only kv) → Task 6 (`client_kv`, `KindDescription`), Task 9.
- Attributes (kv, phase-2 cert-baking target) → Task 6 (`KindAttribute`), Task 9.
- `add`/`re-enroll` mint via `certrequest serve`, not directly → Tasks 2–5, 7.
- Privilege boundary (`client-manager` never holds the provisioner password) → Task 4/5 (`brokerServer` hostname check), Task 7 (`mintToken` only ever dials the broker).
- `client_manager_host`/`certrequest_host`/`certrequest_port` config → Task 1.
- `var_path`/`clientmanager.sqlite` DB location → Task 6/7 (`config.ResolveVarDir`).
- Noun-then-verb CLI (`description set`, `attribute set`) → Tasks 7–9.
- Non-goals (no enforcement, no real `last_seen`, no web UI) → explicitly not built by any task; stated in Task 8's `list`/`show` output and Task 10's docs.
- Testing (unit CRUD, unit auth check, integration real-CA) → Tasks 4, 5, 6, 7, 8, 9.
- Documentation impact → Task 10.

**Placeholder scan:** no `TBD`/`TODO`/"implement later" strings in any task; every code block is complete, runnable code; every test has real assertions.

**Type consistency:** `minter`/`mintFunc` signatures match between definition (Tasks 4, 7) and call sites (Tasks 5, 7); `clientmanagerstore.KVKind`/`KindDescription`/`KindAttribute` used identically across Tasks 6, 8, 9; `Arguments` struct fields (`Action`, `Hostname`, `SANs`, `KVPairs`, `Key`) introduced in Task 7 and only ever extended (never renamed) in Tasks 8–9; `run(conf, certsDir, store, args, out)`'s signature in Task 7 is not changed by Tasks 8–9, only its `switch` body grows.

No gaps found.
