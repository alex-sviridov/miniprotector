# mTLS for gRPC Transport — Design Spec

Date: 2026-07-01

## Overview

All gRPC traffic between `brfs`/`rwfs` (clients) and `bwfs` (server) is currently plaintext
(`grpc.NewServer()` with no credentials; `grpc.WithTransportCredentials(insecure.NewCredentials())`
on the client side). A `step-ca` prototype already exists under `ca/` for issuing certificates
manually, and a certs directory (`bin/certs/{ca.crt,client.crt,client.key}`) has already been
hand-provisioned from it.

This spec covers wiring mutual TLS into the two existing shared connection chokepoints
(`src/common/connection/server.go`'s `StartServer`, `client.go`'s `Connect`), using certs read
from disk at a configurable base directory. **Cert issuance, reissuance, and rotation are out of
scope** — that remains manual via the `ca/` prototype today, and will move to a separate binary
later.

Priorities: **mandatory transport security, minimal moving parts.**

---

## 1. Config: `MP_CONFIG_PATH` Replaces `MP_CONFIGFILE`

`src/common/config/config.go` currently resolves a config *file* path via `MP_CONFIGFILE`, with a
two-candidate fallback search (`<exeDir>/.config/local.conf`, `<exeDir>/../.config/local.conf`)
if unset. This spec replaces that with a single base *directory*:

```go
const ConfigPathEnvVar = "MP_CONFIG_PATH"

// ResolveBaseDir returns MP_CONFIG_PATH if set, otherwise the running
// binary's own directory.
func ResolveBaseDir() (string, error) {
	if envPath := os.Getenv(ConfigPathEnvVar); envPath != "" {
		return envPath, nil
	}
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to determine executable path: %w", err)
	}
	return filepath.Dir(exePath), nil
}

func ResolveConfigPath() (string, error) {
	baseDir, err := ResolveBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "local.conf"), nil
}

func ResolveCertsDir() (string, error) {
	baseDir, err := ResolveBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "certs"), nil
}
```

`MP_CONFIGFILE` and the old two-candidate search are removed — one env var, one base directory,
covering both the config file and the certs directory as siblings under it.

**Repo layout migration:** `.config/local.conf` (currently a sibling of `bin/`, matching the old
candidate-2 search) moves to `bin/local.conf`, since `MP_CONFIG_PATH` defaults to the binary's own
directory. `src/e2e/Dockerfile`'s `COPY src/e2e/config.conf .config/local.conf` becomes
`COPY src/e2e/config.conf local.conf` (still lands in `/app`, the binaries' own directory, so no
`MP_CONFIG_PATH` env var is needed in the container — the default already points at `/app`).

---

## 2. `mtls` Package: Cert Loading & Trust Policy

New package `src/common/mtls` (`src/common/mtls/mtls.go`):

```go
func LoadServerCredentials(certsDir string) (credentials.TransportCredentials, error)
func LoadClientCredentials(certsDir, host string) (credentials.TransportCredentials, error)
```

Both read the same three files from `certsDir`: `ca.crt`, `client.crt`, `client.key`. Every node
(bwfs, brfs, rwfs) presents the same identity cert regardless of its client/server role — there is
no separate "server cert" — so both functions load an identical `tls.Certificate` from
`client.crt`/`client.key` and an identical CA pool from `ca.crt`.

**Server side (`LoadServerCredentials`):**

```go
tls.Config{
	Certificates: []tls.Certificate{clientCert},
	ClientCAs:    caPool,
	ClientAuth:   tls.RequireAndVerifyClientCert,
}
```

Any client presenting a cert signed by `ca.crt` is trusted — no CN/SAN allowlist, no
per-identity authorization. That's a deliberately separate, later concern.

**Client side (`LoadClientCredentials`):**

```go
tls.Config{
	Certificates: []tls.Certificate{clientCert},
	RootCAs:      caPool,
	ServerName:   host,
}
```

Standard hostname/SAN verification against `host` (the dialed address), **except** when `host` is
a loopback value — `host == "localhost"` or `net.ParseIP(host).IsLoopback()` (covers the whole
`127.0.0.0/8` range and `::1`) — in that case hostname verification is skipped
via a custom `VerifyPeerCertificate` that verifies the presented chain against `caPool` only
(`InsecureSkipVerify: true` to disable Go's automatic hostname check, replaced by the manual
chain-only check). This exists because `--destination localhost:8080` and similar dev/test dial
targets won't generally match a cert's issued SAN, while still requiring the cert to chain to the
trusted CA.

Missing directory, missing/unreadable file, or unparseable PEM data is a hard error from either
function — there is no plaintext fallback path.

---

## 3. Wiring into `StartServer` / `Connect`

`src/common/connection/server.go`:

```go
func StartServer(ctx context.Context, logger *slog.Logger, port int, certsDir string, register func(*grpc.Server)) error {
	creds, err := mtls.LoadServerCredentials(certsDir)
	if err != nil {
		return fmt.Errorf("failed to load server credentials: %w", err)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	...
	grpcServer := grpc.NewServer(grpc.Creds(creds))
	...
}
```

`src/common/connection/client.go`:

```go
func Connect(host string, port, timeout int, certsDir string) (*grpc.ClientConn, error) {
	creds, err := mtls.LoadClientCredentials(certsDir, host)
	if err != nil {
		return nil, fmt.Errorf("failed to load client credentials: %w", err)
	}
	conn, err := grpc.NewClient(
		fmt.Sprintf("%s:%d", host, port),
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepaliveParams),
	)
	...
}
```

Call sites each resolve `certsDir` once via `config.ResolveCertsDir()` and pass it through:

- `src/cmd/bwfs/main.go:88` (`connection.StartServer(...)`)
- `src/cmd/brfs/main.go:87` (`connection.Connect(...)`)
- `src/cmd/rwfs/list.go:14`, `src/cmd/rwfs/verify.go:31` (`connection.Connect(...)`)

No new CLI flags — the certs directory is derived from `MP_CONFIG_PATH`, not user-configurable
per invocation, matching the "mandatory, uniform" decisions above.

---

## 4. Files Changed

| Path | Change |
|------|--------|
| `src/common/config/config.go` | Remove `MP_CONFIGFILE`/`ConfigFileEnvVar` and the two-candidate search; add `MP_CONFIG_PATH`/`ResolveBaseDir()`; `ResolveConfigPath()` and new `ResolveCertsDir()` both derive from it |
| `src/common/mtls/mtls.go` (new) | `LoadServerCredentials`, `LoadClientCredentials` |
| `src/common/mtls/mtls_test.go` (new) | Unit tests: valid certs load successfully; missing/corrupt files error; loopback host skips hostname check while a mismatched non-loopback host fails it |
| `src/common/connection/server.go` | `StartServer` gains `certsDir string` param, calls `mtls.LoadServerCredentials`, passes `grpc.Creds(creds)` |
| `src/common/connection/client.go` | `Connect` gains `certsDir string` param, calls `mtls.LoadClientCredentials`, passes `grpc.WithTransportCredentials(creds)` instead of `insecure.NewCredentials()` |
| `src/cmd/bwfs/main.go` | Resolve certs dir, pass to `connection.StartServer` |
| `src/cmd/brfs/main.go` | Resolve certs dir, pass to `connection.Connect` |
| `src/cmd/rwfs/list.go`, `src/cmd/rwfs/verify.go` | Resolve certs dir, pass to `connection.Connect` |
| `.config/local.conf` → `bin/local.conf` | Moved to match `MP_CONFIG_PATH` defaulting to the binary's directory |
| `src/e2e/Dockerfile` | `COPY src/e2e/config.conf .config/local.conf` → `COPY src/e2e/config.conf local.conf`; add `COPY src/e2e/testdata/certs certs` |
| `src/e2e/testdata/certs/{ca.crt,client.crt,client.key}` (new) | Committed fixture CA + identity cert, long expiry, for containerized e2e binaries |
| `src/e2e/docker.go:258` (`waitForBwfs`) | Readiness poll only needs to confirm the port accepts TCP connections, not complete a TLS handshake — switch from `grpc.NewClient(..., insecure.NewCredentials())` to a plain `net.Dial("tcp", addr)` to avoid needing certs in the polling path |
| `docs/protocols/*.md`, `docs/components/{bwfs,brfs,rwfs}.md`, `docs/ARCHITECTURE.md` | Document that all gRPC transport is now mutually authenticated (per project doc rules for feature/behavior changes) |

`src/cmd/bwfs/{integration_test,listserver_test,restore_test}.go` are **unaffected**: they build
`grpc.NewServer()`/`grpc.NewClient()` directly, bypassing `StartServer`/`Connect` and the `mtls`
package entirely, to test service registration/logic in-process. They never exercised transport
security and don't need fixture certs.

No proto changes.

---

## 5. Testing

**`src/common/mtls/mtls_test.go` (new):**
1. `LoadServerCredentials`/`LoadClientCredentials` succeed given the fixture cert directory.
2. Missing directory, missing individual file, and corrupt/unparseable PEM each produce a
   descriptive error (not a panic, not a silent empty credential).
3. `LoadClientCredentials` with a loopback host (`localhost`, `127.0.0.1`, `::1`) succeeds against
   a leaf cert whose SAN does *not* include that hostname (proves the skip actually skips).
4. `LoadClientCredentials` with a non-loopback host whose SAN doesn't match fails at handshake
   time (proves the skip is loopback-only, not global).

**e2e (`src/e2e/`):** existing scenarios (`TestE2E_Backup_*`, `TestE2E_Restore_*`, etc.) continue
to run unmodified in behavior — they now exercise real mTLS handshakes between containerized
`brfs`/`bwfs`/`rwfs` binaries using the committed fixture certs, instead of plaintext. This is
sufficient coverage for "mTLS wiring doesn't break existing traffic"; no new e2e scenario is
needed solely for this change.

---

## Key Design Decisions

**Why remove `MP_CONFIGFILE` instead of adding `MP_CONFIG_PATH` alongside it?**
Keeping both would mean two independent env vars governing overlapping concerns (where's the
config file vs. where's everything else), with no clear precedence rule that isn't itself another
decision to maintain. A single base directory that both the config file and the certs directory
sit under is simpler to reason about and document, at the cost of one manual migration of the dev
`local.conf`.

**Why no CN/SAN allowlist on the server side?**
Out of scope by explicit design choice: this pass wires up transport-level mutual authentication
only. Anyone holding a cert signed by the deployment's CA is trusted; per-identity authorization
is a separate, later concern that would need its own design (what identities are valid, how
they're provisioned, how the allowlist itself is distributed/updated).

**Why skip hostname verification for loopback but not everywhere?**
Real deployments dial real hostnames/IPs that should match issued SANs — skipping verification
globally would silently defeat the point of mTLS (accepting any CA-signed cert regardless of which
node presented it, station-to-station). Loopback addresses are overwhelmingly a
dev/test/single-host convenience (`--destination localhost:8080`) where requiring the cert's SAN
to include `localhost` specifically would be an artificial provisioning burden with no real
security benefit, since anything reachable via loopback is already running on the same trusted
machine.

**Why commit fixture certs instead of generating them at test-run time?**
Deterministic, no CA/tooling dependency added to the test run itself, and no risk of a generation
step silently drifting between local runs and CI. The trade-off (certs technically only need
regenerating once they expire, and the fixture CA's key sits in the repo) is acceptable since
these certs authenticate nothing outside the disposable e2e docker network.

**Why does `waitForBwfs` switch to a plain TCP dial instead of also using the fixture certs?**
It only needs to know the container's gRPC port is accepting connections before the harness starts
issuing real commands — it never calls a service method. Doing a full mTLS handshake there would
duplicate cert-loading logic in the harness for no observable benefit over a bare `net.Dial`.
