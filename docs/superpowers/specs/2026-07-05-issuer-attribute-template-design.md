# Issuer Attribute Certificate Template — Design

> Closes the gap `docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md` explicitly
> deferred: "Agent-side integration (actually calling `issuer` on a schedule) and a CA-side custom
> certificate template (to actually bake attributes into certificate extensions) are deliberately
> deferred to a later, separate piece of work." Agent-side integration shipped as phase 2c
> (`docs/superpowers/specs/2026-07-05-client-manager-phase2c-design.md`). This is the other half.
>
> `src/cmd/issuer/e2e_test.go`'s `TestE2E_MintAndSignAcceptedByCAWithTemplateData` already proves a
> real step-ca accepts a `Sign` request carrying attribute data via `TemplateData` without
> rejecting it, but its own doc comment states plainly: "It does NOT prove that the attribute data
> round-trips into a certificate extension." This design closes exactly that gap.

## Problem

`issuer` already marshals a client's `attribute` key/value pairs (arbitrary strings, unbounded set,
stored via `client-manager attribute set/unset`) into JSON and passes them as `TemplateData` on
every `RequestOperatingCert`-triggered `Sign` call (`src/cmd/issuer/mintsign.go`). But step-ca's
default provisioner template (`x509util.DefaultLeafTemplate`) ignores `TemplateData` entirely — it
only ever sets `subject`, `sans`, `keyUsage`, and `extKeyUsage`. Today, attributes reach the CA on
the wire and are silently dropped; nothing about them survives into the issued certificate. Any
future consumer (an RBAC check in a gRPC server, for instance) has nothing to read.

## Scope

**In scope:** make attributes actually land in the issued certificate as a real, parseable X.509
extension, end to end from `client-manager attribute set` through to a certificate a Go program can
inspect with the standard library.

**Out of scope:** anything that reads or enforces these attributes. No gRPC server in this
codebase currently authorizes based on peer certificate contents at all; adding that is a separate,
later piece of work, consistent with how phase 2's design already drew this boundary.

## Design

### Extension format

One custom, non-critical extension per certificate:

- **OID:** `2.25.937350326255657553.1`. The X.667/RFC 4122 `2.25.<uuid>` arc is the standard
  no-registration-required way to mint a private OID, but its canonical form uses the *full*
  128-bit UUID integer as a single arc component — verified (by attempting to compile it) to
  overflow Go's `asn1.ObjectIdentifier` (`[]int`, effectively `int64` on any real platform), and
  step-ca's own template OID parser (`go.step.sm/crypto/x509util/marshal_utils.go`) uses
  `strconv.Atoi` per component, which fails identically on a number this large. Since this system
  is entirely closed — certificates are signed by our own private CA and only ever interpreted by
  our own code, never an external registry or third party — the collision-avoidance the full UUID
  buys isn't actually load-bearing here. The OID above truncates the generated UUID
  (`cc60c22b-c6cd-4ed6-8d02-2327ca90a251`) to its low 60 bits (`937350326255657553`, safely within
  `int64`), kept for continuity with the original generation but explicitly **not** a standards-
  compliant X.667 UUID-arc — it's just an arbitrarily-chosen, hardcoded-once integer. `.1` under it
  is reserved for "attributes" specifically, leaving room for a second custom extension under the
  same root later.
- **Value:** the raw JSON encoding of the attributes map (e.g. `{"role":"prod-db"}`), not wrapped in
  further ASN.1 structure. Both producer (this template) and any future consumer are our own Go
  code, so JSON is simplest; nothing here depends on generic ASN.1 tooling being able to parse the
  extension's contents.
- **Critical:** `false`. A critical, unrecognized extension causes standard X.509 verifiers to
  reject the certificate outright — this extension is informational, not something that should be
  able to break ordinary TLS handshakes for libraries that don't know about it.

Rejected alternatives:
- **Per-attribute-key sub-OIDs** — attribute keys are arbitrary, operator-chosen strings; there's no
  stable way to map them onto a fixed OID arc.
- **Subject RDNs** — attributes are an unbounded key/value map, which doesn't fit Subject's fixed
  field shape. Extensions are the conventional place for custom claims like this (the same approach
  SPIFFE and Teleport use for embedding identity/role data in certificates).

### Template content

A new file, `deploy/control-plane/ca/templates/leaf.tpl`, extends step-ca's actual built-in
`x509util.DefaultLeafTemplate` (verified against `go.step.sm/crypto@v0.77.1/x509util/templates.go`
rather than reconstructed from memory) with one addition:

```
{
	"subject": {{ toJson .Subject }},
	"sans": {{ toJson .SANs }},
{{- if typeIs "*rsa.PublicKey" .Insecure.CR.PublicKey }}
	"keyUsage": ["keyEncipherment", "digitalSignature"],
{{- else }}
	"keyUsage": ["digitalSignature"],
{{- end }}
	"extKeyUsage": ["serverAuth", "clientAuth"]
{{- if .Insecure.User }},
	"extensions": [{
		"id": "2.25.937350326255657553.1",
		"critical": false,
		"value": "{{ toJson .Insecure.User | b64enc }}"
	}]
{{- end }}
}
```

`{{ if .Insecure.User }}` is Go template truthiness applied to the unmarshaled `TemplateData` JSON:
false for both `null` (issuer's self-mint via `mintSelfIdentity`, which passes `nil` attributes) and
`{}` (a tracked client with zero attributes currently set) — so those certificates come out with no
extension at all, rather than an empty one. `b64enc` is a `sprig` function; step-ca already wires
`sprig.TxtFuncMap()` into every template's `FuncMap` (`go.step.sm/crypto/internal/templates`), so
this introduces no new dependency.

### Wiring the template to the CA

`deploy/control-plane/ca/entrypoint.sh` currently only runs `step ca init` on first boot:

```sh
if [ ! -f /home/step/config/ca.json ]; then
  step ca init --deployment-type=standalone \
    --name="Enterprise Backup Cluster CA" \
    --dns="ca.backup.internal,localhost,step-ca" \
    --address=":9000" \
    --provisioner="admin@backup.internal" \
    --password-file=/home/step/secrets/password
fi
exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
```

Add one line inside that `if` block, after `step ca init` succeeds:

```sh
step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/leaf.tpl
```

This uses step's own CLI subcommand (which validates the template before writing it into
`ca.json`) rather than hand-patching `ca.json` with `jq`. `docker-compose.yml` gains a new read-only
volume mount for the template file, the same way `entrypoint.sh` itself is already mounted:

```yaml
  step-ca:
    volumes:
      - ./ca/data:/home/step
      - ./ca/entrypoint.sh:/home/step/entrypoint.sh:ro
      - ./ca/templates/leaf.tpl:/home/step/templates/leaf.tpl:ro
```

### Testing

Go templates execute server-side inside step-ca, so this can only be verified against a real CA —
it extends the existing `//go:build e2e` docker-compose-backed suite in `src/cmd/issuer/e2e_test.go`
rather than adding unit tests:

- Extend `TestE2E_MintAndSignAcceptedByCAWithTemplateData` (or add a sibling test) to also copy
  `deploy/control-plane/ca/templates/leaf.tpl` into the test's temp compose directory and use the
  updated `entrypoint.sh`, then parse the returned leaf certificate's `Extensions`, locate the
  custom OID, base64/JSON-decode its value, and assert it equals the exact attributes map passed to
  `mintAndSign` (e.g. `{"role": "prod-db"}`).
- Add a case with `nil`/empty attributes (mirroring what self-mint already does) asserting the
  extension is **absent**, not present-but-empty — proving the `{{ if .Insecure.User }}` guard
  actually works and self-mint certificates stay clean.
- `TestE2E_MintSelfIdentityProducesAWorkingServerCertificate` should keep passing unchanged (no
  attributes, no extension) — free regression coverage that the guard doesn't misfire on the
  self-mint path.

### Documentation impact

Per this repo's `.claude/CLAUDE.md` feature-change rules:

- `docs/components/issuer.md` and `docs/components/client-manager.md` — note that `attribute`
  values now really land in the issued certificate as an extension, not just in `TemplateData` on
  the wire.
- `docs/SECURITY.md` — update the "attribute changes propagate" language (currently just describes
  attributes reaching `TemplateData`) to state they are now cryptographically present in the
  certificate itself, while still explicitly noting nothing yet reads or enforces them.
- `deploy/control-plane/README.md` — mention the new template file / provisioner-update step if it
  affects the enrollment walkthrough.
- `CHANGELOG.md` — new dated entry.

## Non-goals (explicitly out of scope)

- Reading or enforcing attribute extensions anywhere (RBAC checks in `brfs`/`bwfs`/`catalog`
  servers, etc.) — later, separate work.
- Any change to which attributes exist or how they're set/unset via `client-manager` — unchanged.
- Any change to SAN handling, revocation, or the two-tier credential model — unchanged; this only
  adds an extension to the same certificates already being issued today.
