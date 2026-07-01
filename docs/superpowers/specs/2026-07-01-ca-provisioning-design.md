# CA Provisioning: Docker Compose + certrequest/certclient — Design Spec

Date: 2026-07-01

## Overview

A `step-ca` prototype already exists under `ca/` (untracked), set up via manual `init.sh`/
`start.sh` scripts and hand-typed `docker exec` commands documented in `ca/README.md`. It was used
to hand-provision the fixture certs consumed by the [mTLS transport
integration](2026-07-01-mtls-integration-design.md). Cert issuance, reissuance, and rotation were
explicitly out of scope for that spec, deferred to "a separate binary later."

This spec is that binary — or rather, two binaries plus a packaging cleanup:

1. **`ca/docker-compose.yml`** — replaces the manual `init.sh`/`start.sh`/`docker exec` workflow
   with a single idempotent `docker compose up`.
2. **`MP_CA_HOST`** (as `ca_host` in `local.conf`) — tells node-side tooling where the CA lives.
3. **`certrequest`** — an admin-side binary that mints one-time enrollment tokens (run on/near the
   CA host).
4. **`certclient`** — an agent-side binary that bootstraps a new identity from a token, or renews
   an existing one, populating the certs directory that `common/mtls` already expects
   (`ca.crt`, `client.crt`, `client.key`).

Priorities, consistent with the mTLS spec: **mandatory transport security where it already exists,
minimal moving parts where we control them, and deliberate reuse of proven code where the logic is
genuinely security-critical** (see "Why reuse smallstep's client library" below — this is the one
place this spec departs from the project's general stdlib-first bias).

---

## 1. `ca/` — Docker Compose

`ca/docker-compose.yml` (new):

```yaml
services:
  step-ca:
    image: smallstep/step-ca
    volumes:
      - ./data:/home/step
    ports:
      - "9000:9000"
    entrypoint: ["/home/step/entrypoint.sh"]
    restart: unless-stopped
```

`ca/entrypoint.sh` (new, committed — holds no secrets):

```bash
#!/bin/sh
set -e
if [ ! -f /home/step/config/ca.json ]; then
  step ca init --deployment-type=standalone \
    --name="Enterprise Backup Cluster CA" \
    --dns="ca.backup.internal,localhost" \
    --address=":9000" \
    --provisioner="admin@backup.internal" \
    --password-file=/home/step/secrets/password
fi
exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
```

`ca/init.sh` and `ca/start.sh` are removed; `ca/README.md` is rewritten to document:

1. One-time, manual secret generation before first run (can't be automated away without either
   committing a secret or inventing a new secret-distribution mechanism):
   ```bash
   mkdir -p data/secrets
   openssl rand -base64 32 > data/secrets/password
   ```
2. `docker compose up -d` — idempotent from then on; re-running never re-inits an
   already-initialized `ca.json`.
3. The updated `certrequest`/`certclient` workflow (replacing the old manual `step ca
   token`/`step ca certificate` commands) — see sections 3–4.

**Why a checked-in entrypoint script instead of the `smallstep/step-ca` image's env-var auto-init
feature (`DOCKER_STEPCA_INIT_*`)?** That feature exists, but pinning its exact variable names and
semantics from memory risks getting it subtly wrong. The entrypoint script here uses only `step`/
`step-ca` CLI flags that can be verified directly against `--help`, at the cost of a few lines of
shell instead of a purely declarative compose file.

`ca/.gitignore` is unchanged (`data/` stays ignored — all secrets and the CA's live state live
there).

---

## 2. Config: `ca_host` in `local.conf`

`src/common/config/config.go`'s `Config` struct gains:

```go
type Config struct {
    // ...existing fields...
    CAHost string // ca_host=<host:port>, e.g. ca.backup.internal:9000
}
```

Parsed from a new `ca_host=...` key in `ParseConfig`, following the existing `switch key` pattern.
**Not** added to `requiredFields` — `bwfs`/`brfs`/`rwfs` have no use for it and shouldn't be forced
to configure it. `certclient` validates it explicitly at startup instead (`"ca_host not set in
local.conf"` if empty), matching the existing pattern where each binary validates the config it
actually needs at its own point of use (e.g. certs-dir resolution failures in `bwfs`/`brfs`
`main.go`).

`certrequest` does not need `ca_host` — see section 3, it mints tokens fully offline.

---

## 3. `certrequest` — Admin-Side Token Minting

New binary `src/cmd/certrequest/main.go`. **Control-plane tool**: run on or near the CA host, with
direct filesystem access to `ca/data/`. Never deployed onto an agent host or bundled into an agent
Docker image (see section 5, "Control Plane vs. Agents").

**Usage:**

```
certrequest <hostname> [--san alias]... [--ca-config path] [--password-file path]
```

`--ca-config`/`--password-file` default to `ca/data/config/ca.json` / `ca/data/secrets/password`,
overridable for flexibility (e.g. running from a copy of those files rather than in place).

**Mechanism** (confirmed against the real `ca.json` already present in `ca/data/config/`, which
stores a JWK provisioner's public key plus a JWE-encrypted private key):

1. Read `ca.json`; locate the JWK provisioner entry (its public `key` and `encryptedKey`).
2. Decrypt `encryptedKey` with the password file, via `go.step.sm/crypto/jose` (JWE, PBES2), to
   recover the provisioner's private JWK. **Held only in memory — never written to disk.**
3. Read the root CA fingerprint from `ca/data/config/defaults.json`'s `fingerprint` field (already
   precomputed by `step ca init`; no need to recompute it).
4. Mint a JWS-signed one-time token (OTT) with standard step-ca provisioner claims: `iss`
   (provisioner name), `aud` (`https://<ca-url>/1.0/sign`), `sub` (hostname), `sans` (hostname plus
   `--san` aliases), `sha` (root fingerprint from step 3), plus `exp`/`nbf`/`iat`/`jti`. Signed with
   the recovered private JWK.
5. Print the token to stdout. The operator relays it to the target node out-of-band (SSH, etc.) as
   `MP_CERT_TOKEN`.

This is fully offline — no network call to the CA is needed to mint a token, mirroring how `step ca
token --ca-config=...` behaves when run on the CA host itself.

**New dependency:** `go.step.sm/crypto` (smallstep's crypto/JOSE primitives library — same org as
the `step-ca`/`step` binaries we already run operationally). Exact claim names and JWE parameters
get pinned down against the installed module version and step-ca's provisioner source during
implementation; this section describes the mechanism, not final code.

Token TTL/SAN authorization is bounded by the provisioner's own `claims` in `ca.json`
(`minTL`/`maxTTL`/`defaultTTL`) — `certrequest` doesn't need to reimplement or duplicate those
limits.

---

## 4. `certclient` — Agent-Side Bootstrap/Renew

New binary `src/cmd/certclient/main.go`. **Agent tool**: runs on every node also running
`bwfs`/`brfs`/`rwfs`, populating the certs directory `common/mtls` already reads
(`ca.crt`/`client.crt`/`client.key`). Resolves `local.conf` (for `ca_host`) and the certs dir the
same way the other binaries do (`config.ResolveConfigPath`/`ParseConfig`,
`config.ResolveCertsDir`).

**A. Cert already exists in the certs dir → renew.**
Build a `ca.Client` (from `github.com/smallstep/certificates/ca`) using the existing
`client.crt`/`client.key`/`ca.crt`, and call its renew method — mTLS-authenticated against
`/1.0/renew`, no token involved. Overwrite `client.crt`/`client.key` with the result. Always
renews when invoked (no expiry check); scheduling this periodically (cron/systemd timer) is an
operational concern outside this binary.

**B. No cert exists → bootstrap using a token.**

1. Get the token: `--token` flag → `MP_CERT_TOKEN` env var → stdin prompt (in that preference
   order — see security note below on why `--token` is the least-preferred of the three).
2. `client, err := ca.Bootstrap(token)` — smallstep's own client constructor. It decodes the
   token, fetches `/roots`, and pins trust via the token's embedded `sha` fingerprint claim
   internally. This spec does not reimplement any of that logic (see "Why reuse smallstep's client
   library" below).
3. `req, pk, err := ca.CreateSignRequest(token)` — builds the CSR and private key using the
   identity already encoded in the token's `sub`/`sans` claims. `certclient` needs no separate
   `--hostname` flag; the identity was decided when `certrequest` minted the token.
4. `resp, err := client.Sign(req)`.
5. Write the resulting cert chain, the bootstrapped root, and `pk` to `client.crt`, `ca.crt`, and
   `client.key` respectively. **`client.key` is written with `0600` permissions.**

**Dependencies:** `github.com/smallstep/certificates/ca` (pulling in `go.step.sm/crypto`
transitively), added to `certclient` as well as `certrequest`. Exact function signatures get
pinned down against the installed module version during implementation.

---

## 5. Control Plane vs. Agents

This feature introduces the project's first control-plane component (the CA) alongside the
existing data-plane agents (`bwfs`/`brfs`/`rwfs`), and the separation is made explicit rather than
left implicit:

| | Control plane | Agents |
|---|---|---|
| Components | `ca/` (step-ca container), `certrequest` | `bwfs`, `brfs`, `rwfs`, `certclient` |
| Runs where | On/near the CA host | On every backup node |
| Network role | Serves enrollment/renewal on `:9000`; has no role in backup traffic | Dial `ca_host:9000` *outbound only*, for enrollment/renewal; otherwise mesh with each other over gRPC on `:8080` (mTLS, per the existing spec) |
| Docker/e2e images | `certrequest` never ships onto an agent host or into an agent image | Agent images bundle `certclient` only |

Build layout stays flat (`src/cmd/certrequest`, `src/cmd/certclient`) for Makefile/module
consistency — this separation is a deployment and documentation concern, not a source-tree
restructuring. `docs/ARCHITECTURE.md` gains a short section stating this table's contents alongside
the existing topology/data-flow description.

---

## 6. Security Review

- **Token exposure via `--token`:** CLI arguments are visible via process listings (`ps`) on
  shared hosts. `MP_CERT_TOKEN` (env var) or the stdin prompt are preferred; `--token` remains
  available for scripting convenience but is documented as the least-safe of the three, the same
  caveat applied to passing passwords on a command line.
- **Token replay:** not something this design needs to build — step-ca provisioner OTTs are
  short-lived and single-use (`jti`-tracked) by the CA itself. Inherited for free from step-ca.
- **Private key handling:** `certrequest` holds the decrypted provisioner private key only in
  memory, never persisting it to disk. `certclient` writes `client.key` with `0600` permissions.
- **Trust boundary (stated, not a gap):** anyone able to run `certrequest` with read access to
  `ca/data/config/ca.json` and `ca/data/secrets/password` has full token-minting authority for any
  hostname — equivalent to CA-admin privilege. This is inherent to the offline-minting model (the
  same is true of the raw `step` CLI used manually today) and is why `certrequest` is a
  control-plane-only tool per section 5, not something distributed to agent hosts.
- **Bootstrap trust (fingerprint pinning):** delegated entirely to `ca.Bootstrap` from smallstep's
  client library rather than hand-implemented — see below.

**Why reuse smallstep's client library for `certclient`/`certrequest`, given this project's
general stdlib-first bias (e.g. `common/mtls` hand-implements its TLS config, `common/config`
hand-parses `key=value` rather than pulling a config library)?**

The bootstrap-trust step — deciding whether to trust a CA root fetched over an otherwise-unverified
connection, based on a fingerprint claim extracted from a token — is exactly the kind of
security-critical logic where a subtle mistake (e.g. an off-by-one in fingerprint comparison, or a
missed edge case in token parsing) creates a real vulnerability, not just a bug. Reusing smallstep's
own client (`github.com/smallstep/certificates/ca`, `go.step.sm/crypto`) — the same code path
`step ca certificate`/`step ca renew` use — means staying aligned with any future changes to
step-ca's wire format for free, at the cost of a heavier dependency than this project otherwise
takes on. This is a deliberate, scoped exception to the stdlib-first pattern, not a reversal of it:
`common/mtls`'s existing hand-rolled TLS config (loading already-issued certs from disk) is
unaffected and stays as-is.

---

## 7. Files Changed

| Path | Change |
|------|--------|
| `ca/docker-compose.yml` (new) | Single `step-ca` service |
| `ca/entrypoint.sh` (new) | Idempotent init-if-needed + start |
| `ca/init.sh`, `ca/start.sh` | Removed |
| `ca/README.md` | Rewritten: one-time password generation, `docker compose up`, `certrequest`/`certclient` workflow |
| `src/common/config/config.go` | Add `CAHost` field, `ca_host` key parsing (optional) |
| `src/cmd/certrequest/main.go` (new) | Token minting CLI |
| `src/cmd/certclient/main.go` (new) | Bootstrap/renew CLI |
| `Makefile` | Add `certrequest`/`certclient` build targets (matching `brfs`/`bwfs`/`rwfs`); `$(BINARIES)` wildcard already covers new `src/cmd/*` dirs for `make build` |
| `go.mod` | Add `github.com/smallstep/certificates/ca` (+ transitive `go.step.sm/crypto`) |
| `docs/components/certrequest.md`, `docs/components/certclient.md` (new) | Per project doc rules for new components |
| `README.md` | Cross-link new component docs |
| `docs/ARCHITECTURE.md` | Control-plane-vs-agents table (section 5); CA enrollment data flow |

---

## 8. Testing

- **`certrequest`:** unit tests around JWE decryption and JWT claim construction, using a fixture
  `ca.json`/password generated for the test — not the real `ca/data/`.
- **`certclient`:** unit tests mocking the `ca.Client` interface for both branches (bootstrap
  success, fingerprint-mismatch failure, renew success, renew failure) rather than hitting a live
  CA.
- **e2e:** flagged as a stretch goal / follow-up, not required for the initial implementation. The
  existing e2e harness already covers mTLS transport itself using committed fixture certs; a full
  `step-ca` + `certrequest` + `certclient` end-to-end scenario would add real value but is a
  larger, separate lift (standing up `step-ca` in the Docker e2e network, minting a real token,
  etc.) better scoped as its own follow-up plan once this lands.

---

## Key Design Decisions

**Why `ca_host` in `local.conf` instead of an `MP_CA_HOST` env var (unlike `MP_CONFIG_PATH`)?**
`MP_CONFIG_PATH` governs where configuration and certs *live on disk* — a bootstrapping concern
that has to be resolvable before any config file can be read. `ca_host` is an ordinary operational
setting (which CA to enroll against), the same category as `default_port` or `logfolder`, and
belongs alongside them in `local.conf` rather than introducing a second, inconsistent
configuration channel.

**Why does `certrequest` run offline, on/near the CA host, rather than exposing a remote admin
API?** Fewer moving parts and no new network-facing attack surface on the CA server itself. The
CA's only exposed surface stays the standard step-ca REST API (`/1.0/sign`, `/1.0/renew`,
`/roots`) that `certclient` already needs to talk to; token minting is a local file-and-crypto
operation, matching how the `step` CLI itself works today.

**Why does `certclient` need no `--hostname` flag?** The identity (`sub`/`sans`) is decided once,
by `certrequest`, at token-minting time. Baking it into the token means the token itself is the
sole authorization artifact — `certclient` can't be tricked or misconfigured into requesting a
different identity than the one the CA operator authorized.

**Why reuse smallstep's client library here but not elsewhere in the project?** Covered in detail
in section 6 — this is a deliberate, narrow exception for security-critical bootstrap-trust logic,
not a general policy change.
