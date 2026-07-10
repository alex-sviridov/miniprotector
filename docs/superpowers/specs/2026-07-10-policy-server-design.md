# Policy Server — Design

> Relationship to prior work: `docs/superpowers/specs/2026-07-03-policy-reconciliation-design.md`
> ("proposal #2") sketched an earlier, YAML-based policy server as part of a combined
> policy-server+agent-scheduling design, and was explicitly deferred — phase 2c's design doc lists
> "No policy-server / dynamic policy fetch for `agent`" as a non-goal. This spec supersedes that
> proposal's policy-server half only: different storage format (JSON, one file per policy, not one
> YAML list), a richer schema (`rpo`, multiple `backup_window` crons, structured `object_filters`,
> label-based filtering), and a self-contained label source (the client's own mTLS certificate,
> not a live database query). It deliberately does **not** revisit proposal #2's agent-side
> consumption story (cron registration, `flock`, reconcile loop) — that remains future work, out
> of scope here, same as this spec's own explicit non-goal below.

## Problem

Backup targets, schedules, and RPO requirements currently have no home. `brfs` takes a single
source folder as a CLI argument; there is no way to declare "back up these paths, on these hosts
matching these labels, on this schedule, with this RPO" anywhere in the system. Every backup today
is manually invoked with hand-picked arguments.

## Goals

- Operators express backup policies declaratively as JSON files, one per policy, under
  `$MP_CONFIG_PATH/policies/`.
- A policy targets clients via hostname glob patterns and/or label matches — no manual per-host
  wiring.
- A running node can ask "which policies apply to me?" and get back exactly the policies whose
  filters match its own identity, with no data it doesn't need (no filter internals echoed back).
- Policy authors can batch-edit multiple policy files and trigger a single atomic reload, rather
  than having each file's edit picked up independently mid-batch.
- `policy-server` itself carries no new trust mechanism — it reuses the fleet's existing mTLS
  identity and the attribute data `issuer` already embeds in every operating certificate.

## Non-Goals (this pass)

- **No client-side consumer.** Nothing in this pass calls `GetPolicies` — not `agent`, not `brfs`.
  Wiring an actual backup-scheduling consumer (cron registration, execution, RPO tracking) is
  separate, later work, exactly as `issuer`'s phase 2b left `agent` integration for phase 2c.
- **No policy CRUD API or admin UI.** Policies are static JSON files an operator edits directly on
  `policy-server`'s host filesystem — mirrors every other control-plane component's "operator edits
  a file" convention in this project (e.g. `client-manager`'s own direct-DB-on-host model).
- **No RPO/backup_window enforcement or interpretation.** `policy-server` stores and returns these
  fields verbatim; it never evaluates a cron expression or measures actual RPO compliance. That's
  entirely a future consumer's job.
- **No cross-file consistency validation** (e.g. duplicate policy names across files, overlapping
  object_filters). Each file is validated independently; nothing checks relationships between
  policies.
- **No high availability.** Single instance, in-memory cache rebuilt from disk on restart — same
  posture `issuer` already accepts for the same reason (Non-Goals, phase 2 design).

## Architecture

**New binary:** `src/cmd/policy-server/`, following the same file layout every other small,
narrow control-plane gRPC service in this repo uses (`catalog`, `issuer`): `arguments.go` (cobra
CLI flags), `main.go` (wiring), `server.go` (RPC logic + tests), plus new files for cert-extension
parsing and file-cache/watch logic.

**Identity:** `policy-server` is enrolled and certificate-managed exactly like any other node in
the mesh — `client-manager add`, credentials obtained and refreshed by its own `agent` through
`issuer`, same as `catalog`/`bwfs`/every other component. No special bootstrap path.

**Listener:** plain `connection.StartServer` (which resolves to `mtls.LoadServerCredentials`,
default operating-tier enforcement) — identical to `catalog`'s own listener setup. No tier
special-casing is needed, unlike `issuer`'s own listener (which uniquely requires the *bootstrap*
tier since it's the thing that mints operating certs in the first place); `policy-server` is called
by nodes presenting their ordinary operating certificate, exactly the default case.

**Label source:** a calling client's labels are read directly off its presented mTLS peer
certificate, not queried from any database. `issuer` already embeds every hostname's current
`attribute` key/value pairs into the operating certificate it mints, as a custom X.509 extension
(OID `1.3.6.1.4.1.61183.1.1`, base64-then-JSON-encoded per `deploy/control-plane/ca/templates/leaf.tpl`).
`policy-server` extracts the peer certificate from the gRPC connection's `credentials.TLSInfo`,
locates that extension by OID, and `json.Unmarshal`s it into `map[string]string`. This keeps
`policy-server` entirely self-contained: no database, no RPC to `client-manager` or `issuer`, no
shared storage with any other component — its only external dependency is the mTLS handshake
already required to reach it at all. (No reusable helper for this extraction exists yet — `issuer`'s
own e2e test has an inline `findExtension` helper that this component's implementation should
generalize into `common/mtls` or a small local helper, at the plan's discretion.)

**Hostname:** `mtls.PeerHostname(ctx)`, the same verified-peer-identity derivation used by every
other authenticated RPC in this project (never a request field).

## RPC

One method, request/response (no streaming):

```proto
service PolicyService {
  rpc GetPolicies(GetPoliciesRequest) returns (GetPoliciesResponse);
}

message GetPoliciesRequest {}
// Caller identity (hostname) and labels are derived entirely from the mTLS peer
// certificate presented on the connection -- never a field on this message, same
// trust model as every other authenticated RPC in this project.

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
  string rpo = 5;                     // duration string, e.g. "24h" (time.ParseDuration format)
  repeated string backup_window = 6;  // list of cron expressions (5-field), e.g. ["0 2 * * *"]
}
```

`client_filters` is intentionally **not** part of `Policy` in the response — a returned policy has
already matched, so the filter that selected it carries no further meaning to the caller. Keeping
it server-side-only also means changing filter internals never touches the wire schema.

## On-disk schema

`$MP_CONFIG_PATH/policies/*.json`, one file per policy, operator-authored:

```json
{
  "metadata": {
    "name": "nightly-web-backup",
    "created_at": "2026-07-10T00:00:00Z",
    "updated_at": "2026-07-10T00:00:00Z"
  },
  "client_filters": {
    "hostnames": ["web-*"],
    "labels": {"env": "prod"}
  },
  "object_filters": [
    {"path": "/var/www"}
  ],
  "rpo": "24h",
  "backup_window": ["0 2 * * *", "0 20 * * *"]
}
```

- `metadata.name` is the policy's identity (not the filename — a file can be renamed without
  changing what policy it represents). `created_at`/`updated_at` are operator-maintained,
  informational only; `policy-server` never writes to these files or computes these values itself.
- `client_filters.hostnames`: a list of glob patterns (e.g. `web-*`); empty or absent means "no
  hostname restriction." `client_filters.labels`: a map of exact key=value pairs that must **all**
  be present among the client's cert-embedded attributes.
- **Filter match rule (this is the whole of `policy-server`'s decision logic):** a policy matches a
  requesting client if — (`hostnames` is empty OR the client's hostname matches at least one glob
  pattern) **AND** (every key/value pair in `labels` is present in the client's attributes). Both
  conditions must hold; there is no OR-across-hostname-and-labels mode.
- `object_filters` is a list of single-field objects (`{"path": "..."}`) rather than bare strings —
  chosen deliberately even though only `path` is populated today, to leave room for future
  per-path options (e.g. excludes) without a breaking schema change; `policy-server` treats it as
  opaque pass-through data, same as `rpo`/`backup_window`.
- `backup_window` is a list of cron expressions rather than a single string, so a policy can
  express multiple disjoint windows (e.g. a night window and a separate weekend window) without
  overloading a single cron field's range syntax. `policy-server` never parses or validates these
  beyond confirming the field is present — it's opaque pass-through data for a future consumer.

## Caching & reload

- On startup, and on every reload, `policy-server` reads every `*.json` file directly under
  `policies/` (non-recursive), parses each independently, and builds a fresh in-memory list.
- **Reload trigger:** `policies/.changed`, a plain sentinel file an operator touches after
  finishing a (possibly multi-file) edit. `policy-server` watches this one file via `fsnotify`;
  any write event on it triggers a full reload. This is a deliberate choice over watching the
  `*.json` files directly: an operator editing several policies as a batch touches `.changed` once
  when done, producing a single atomic cache swap — no window where a partially-edited multi-file
  change is served as a mix of old and new policies.
- **Malformed file handling:** a single `*.json` file that fails to parse (invalid JSON, or missing
  a required field) is skipped for that reload, logged loudly (hostname/path/error), and does not
  block the rest of the directory from loading — one operator typo doesn't take every other policy
  down. If every file in a reload fails, the previous good in-memory cache is kept rather than
  swapped to an empty list (an empty `policies/` directory legitimately means "no policies" and
  that state is valid to serve, but a reload that produced zero successes out of N>0 attempted
  files is treated as a failed reload, not an intentional empty state).
- The cache swap itself is atomic from a reader's perspective (build the new list fully off to the
  side, then swap a pointer/mutex-guarded reference) — concurrent `GetPolicies` calls never observe
  a half-built reload.

## Configuration

New `local.conf` keys, following the existing `_host`/`_port` convention (`issuer_host`/
`issuer_port`, `catalog_host`/`catalog_port`):

- `policy_server_host` / `policy_server_port` (default `9300`) — where `policy-server` listens;
  read by any future client of `GetPolicies` and set on `policy-server`'s own host.

No new `MP_CONFIG_PATH`-relative path config key is needed — `policies/` is resolved directly under
the same base directory `local.conf`/`certs/` already live in (`config.ResolveBaseDir`), consistent
with how every other on-disk convention in this project is anchored.

## Tech Stack addition

`github.com/fsnotify/fsnotify` becomes a direct dependency (currently only present transitively in
`go.sum`) — used solely to watch `policies/.changed` for write events; no other filesystem-watching
behavior.

## Testing

- Unit: hostname glob matching (`web-*` matches `web-01`, doesn't match `db-01`; empty pattern list
  matches any hostname).
- Unit: label matching (all-required-present, extra client attributes beyond what's listed don't
  disqualify a match, a missing required label disqualifies).
- Unit: combined filter AND logic (hostname-only policy, label-only policy, both, neither —
  neither means "matches everyone").
- Unit: cert-extension parsing — a fabricated peer certificate carrying the attribute extension
  (mirrors `issuer`'s own `fakeAuthContext` test helper pattern) round-trips through extraction
  correctly; a peer certificate with no such extension yields empty labels, not an error.
- Unit: malformed-file skip-and-continue (one bad file among several good ones still serves the
  good ones); reload-produces-zero-successes keeps the previous cache.
- Unit: `.changed`-triggered reload actually swaps the served policy set (fsnotify-driven, using a
  temp directory).
- No real-CA e2e test is needed — `policy-server` never talks to `step-ca` directly (it only reads
  an extension out of a certificate it receives as a connection's peer identity, already verified
  by the mTLS handshake itself).

## Documentation Impact

- New `docs/components/policy-server.md`.
- New `docs/protocols/policy-server.md` (per this repo's rule: any new/changed gRPC protocol needs
  a corresponding protocol doc, cross-linked from `README.md` and the component doc).
- Update `docs/ARCHITECTURE.md`: new components-table row, and update `docs/components/agent.md`'s
  existing note about "policy-server-fetched work" once this exists (it currently reads as
  forward-looking to a component that didn't exist yet).
- `CHANGELOG.md` entry on merge, per this repo's standing rule.

## Relationship to Phase 1/2/2c work

Independent of the `client-manager`/`issuer`/agent credential-refresh work (phases 1–2c) except for
one load-bearing reuse: the attribute-embedding mechanism `issuer` already ships (CA-side
`leaf.tpl` template, `TemplateData.attributes`) is exactly what makes `policy-server`'s
database-free label lookup possible. No changes to `client-manager`, `issuer`, or `agent` are
needed by this spec — this is purely additive, a new, independently-deployable component.
