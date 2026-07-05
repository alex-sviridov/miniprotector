# Credential Tier Enforcement — Design

> Closes a gap `docs/SECURITY.md` already discloses by name: "The bootstrap credential is not yet
> cryptographically confined to only reaching `issuer`... this boundary is an operational
> expectation... not an enforced one." Surfaced during a security review of the phase 2/2a-2d work
> (`bootstrap.crt` can currently authenticate to `bwfs`/`catalog` exactly as well as an operating
> credential can, since `common/mtls`'s server-side check trusts any certificate signed by the CA
> regardless of which credential tier issued it). This design makes that boundary real.

## Problem

Two credential tiers exist in this codebase today, structurally identical:

| | Bootstrap credential | Operating credential |
|---|---|---|
| Files | `bootstrap.crt`/`bootstrap.key` | `client.crt`/`client.key` |
| Lifetime | Long (~90 days) | Short (1 hour default) |
| Intended use | Only to authenticate to `issuer`'s `RequestOperatingCert`/`DescribeSANs` | Every other mTLS connection — `bwfs`, `catalog`, and any client dialing them |

`src/common/mtls.LoadServerCredentials` — used identically by `bwfs`, `catalog`, and `issuer` — has
no concept of tier: any certificate signed by the CA is trusted. A leaked or misused bootstrap
credential can today open a `bwfs`/`catalog` session directly, something never intended, and
`client-manager revoke` (which only makes `issuer` refuse future reissuance) does nothing to stop
it — the bootstrap credential simply keeps working against the data plane for the rest of its
~90-day life.

## Scope

**In scope:** cryptographically distinguish the two tiers at the certificate level, and enforce
that distinction at every mTLS listener in this codebase — `bwfs`/`catalog` accept only operating
credentials; `issuer` accepts only bootstrap credentials.

**Out of scope:** revoking already-issued certificates at the CA level (still issuer-side-only,
per `docs/SECURITY.md`'s existing, separately-documented trade-off). No live migration path for
already-enrolled nodes — this ships against the demo-lab environment
(`deploy/control-plane`), where a clean re-provision (wipe the CA/client-manager volumes, re-run
the enroll walkthrough) after deploying this change is expected and sufficient.

## Design

### Tier marker: a custom Extended Key Usage

Operating certificates keep exactly what they have today —
`extKeyUsage: ["serverAuth", "clientAuth"]` — since they legitimately need both TLS server and
client capability across the mesh. No new marker is added to them; this is the implicit default
tier.

Bootstrap certificates change to `extKeyUsage: ["clientAuth"]` only (a bootstrap credential never
runs a server — it only ever dials `issuer`), and additionally carry one new custom EKU object
identifier in the certificate's `unknownExtKeyUsage` list:

- **OID:** `1.3.6.1.4.1.61183.1.3` (the next free arc after `.1.1`, the existing attribute
  extension — see `docs/superpowers/specs/2026-07-05-issuer-attribute-template-design.md` for why
  this numbering scheme was chosen).
- **Name:** `EKUIssuerCaller` in code and docs. Deliberately not named around "server"/"client" —
  those words are already overloaded in this codebase for backup roles (`bwfs`, `brfs`) and RPC
  roles (`LoadClientCredentials`, `LoadServerCredentials`). `EKUIssuerCaller` names the marker by
  its actual purpose: this credential's only legitimate use is calling `issuer`.
- **Semantics:** presence marks a bootstrap-tier credential; absence marks operating-tier. No
  separate marker is needed for the operating side — it's the default.

This uses `x509util`'s `unknownExtKeyUsage` template field (confirmed in
`go.step.sm/crypto@v0.77.1/x509util/certificate_request.go`), which accepts arbitrary OID strings
distinct from the standard `extKeyUsage` keyword list (`serverAuth`, `clientAuth`, etc.) — the same
kind of custom-marker mechanism the attribute extension already established, applied here to EKU
instead of a generic extension.

### Minting: both Sign call sites start declaring tier

Two code paths call `(*ca.Client).Sign` in this codebase, and both need to say which tier they're
minting:

- **`cmd/certclient/bootstrap.go`**, redeeming a one-time enrollment token: `ca.CreateSignRequest`
  returns a mutable `*api.SignRequest`; before calling `client.Sign(req)`, set
  `req.TemplateData = json.Marshal(map[string]string{"tier": "bootstrap"})`.
- **`cmd/issuer/mintsign.go`**'s `mintAndSign`, used both for real `RequestOperatingCert` calls and
  `issuer`'s own self-mint (`selfidentity.go`) — both are operating-tier by construction (the
  self-minted cert needs `serverAuth` to run `issuer`'s own listener). `TemplateData` changes from
  the bare attributes map to `{"tier": "operating", "attributes": {...}}`.

### CA template: branch on tier

`deploy/control-plane/ca/templates/leaf.tpl` gains a branch on `.Insecure.User.tier`:

```
{{- if eq .Insecure.User.tier "bootstrap" }}
	"extKeyUsage": ["clientAuth"],
	"unknownExtKeyUsage": ["1.3.6.1.4.1.61183.1.3"]
{{- else }}
	"extKeyUsage": ["serverAuth", "clientAuth"]
{{- end }}
{{- if .Insecure.User.attributes }},
	"extensions": [{
		"id": "1.3.6.1.4.1.61183.1.1",
		"critical": false,
		"value": "{{ toJson .Insecure.User.attributes | b64enc }}"
	}]
{{- end }}
```

The existing attribute extension now reads `.Insecure.User.attributes` instead of the whole
`Insecure.User` object, matching the new nested `TemplateData` shape. As today, this template
change must be applied on every CA boot, not gated behind first-init (per the fix already landed
in `b212082`), so an already-initialized CA volume picks it up too — moot here only because this
ships against a clean-slate demo environment, but the entrypoint mechanism doesn't need to change.

### Enforcement: default-safe, minimal blast radius

`bwfs` and `catalog` wanting "operating tier only" is the common case; `issuer` wanting the inverse
is the one exception. Rather than threading a required-tier parameter through every server's
`main.go`, the default behavior changes and only `issuer` opts out of it:

- **`src/common/mtls/mtls.go`**: `LoadServerCredentials(certsDir)` keeps its exact current
  signature. It gains a `VerifyPeerCertificate` callback (the same mechanism `verifyChainOnly`
  already uses for the loopback case) that rejects any peer certificate carrying
  `EKUIssuerCaller` in its `UnknownExtKeyUsage`. Every existing caller (`bwfs`, `catalog`, and any
  future server) gets this rejection for free, with no call-site change.
- A new `LoadIssuerServerCredentials(certsDir)` builds the inverted check: reject any peer
  certificate that does **not** carry `EKUIssuerCaller`.
- **`src/common/connection/server.go`**: `StartServer` (used by `bwfs`/`catalog`) is unchanged. A
  new `StartServerWithCredentials(ctx, logger, port, creds, register)` accepts pre-built
  credentials instead of loading the default ones.
- **`cmd/issuer/main.go`**: switches from `connection.StartServer(...)` to
  `mtls.LoadIssuerServerCredentials(certsDir)` + `connection.StartServerWithCredentials(...)`.
  This is the only `main.go` this design touches.

### Error handling

A tier mismatch surfaces as a plain TLS/gRPC handshake failure — the same failure mode as today's
existing chain-verification errors (expired cert, untrusted CA, etc.). No new error type, status
code, or structured signal. The `VerifyPeerCertificate` callback returns a descriptive Go error
(consistent with `verifyChainOnly`'s existing pattern), which surfaces in the server's handshake
log and is sufficient to diagnose after the fact. The connecting side sees an ordinary connection
failure and relies on its existing retry/backoff (e.g. `agent`'s reconcile loop) — no new recovery
path needed.

### Testing

- **Unit — template rendering**: both tiers render the expected `extKeyUsage` /
  `unknownExtKeyUsage` / attribute-extension combination from `TemplateData`, extending whatever
  harness already covers `leaf.tpl` from the attribute-extension work.
- **Unit — `common/mtls`**: fixture certs with and without `EKUIssuerCaller` against both
  `LoadServerCredentials`'s new default rejection and `LoadIssuerServerCredentials`'s inverted
  check — four cases (bootstrap/operating × accept/reject).
- **E2E — real CA**: extend `src/cmd/issuer/e2e_test.go` (already Docker-backed, already proves
  the attribute round-trip) with a case proving a bootstrap-tier certificate is actually rejected
  by an operating-only listener, and — symmetrically — an operating-tier certificate is rejected
  by `issuer`'s own listener. This is the same rigor already applied to the SAN exact-match and
  attribute-extension e2e assertions.

### Documentation

- **`docs/SECURITY.md`**: rewrite the "bootstrap credential is not yet cryptographically confined"
  paragraph to describe the closed gap (mirroring how the attribute-extension changelog entry
  described its own closed gap), and note the mechanism (`EKUIssuerCaller`) and OID.
- **`docs/components/issuer.md`**, **`docs/components/client-manager.md`**,
  **`docs/components/certclient.md`**, **`docs/protocols/issuer.md`**: document the tier marker
  and which listener enforces which requirement.
- **`CHANGELOG.md`**: new entry, including the clean-slate/re-enrollment note for any existing
  demo-lab deployment (certs issued before this change lack the marker and won't pass the new
  checks).

## Non-goals (explicit)

- Certificate-level revocation (still issuer-side-only, unchanged, already documented as a
  separate accepted trade-off).
- Any live-migration or grace-period handling for pre-existing certificates lacking the marker.
- Any change to `brfs`/`rwfs`/`catalogsync` — they're mTLS clients only, never servers, so they
  have no listener to enforce a tier requirement on.
