# Design: adhoc policy endpoint

**Date:** 2026-08-02
**Status:** Approved for planning

## Problem

Running a one-time backup today means hand-crafting a `POST /api/v1/policies` body that composes
`backup_window`/`rpo`/`disabled_at` correctly by hand: a `backup_window` full of `*` (so the window
is open immediately), an `rpo` equal to the desired timeout (so it fires exactly once per matched
node within that window), and a `disabled_at` set to `now + timeout` (so the policy stops applying
and its per-node state gets pruned on its own, no follow-up `DeletePolicy` required). This is exactly
the convenience call the `disabled_at` design
([2026-08-02-policy-disabled-at-design.md](2026-08-02-policy-disabled-at-design.md)) deferred:
"Composing that ... into a convenience 'create adhoc policy' call is `api-server`'s job, and is
explicitly deferred — this design only adds the primitive."

This adds that convenience endpoint: `POST /api/v1/policies/adhoc`, taking the same fields as a
normal backup policy create (name, client filters, object filters, destination) and computing
`backup_window`/`rpo`/`disabled_at` itself from a configured timeout, so a caller never composes
those three fields by hand.

## Scope

Pure `api-server` addition. No `policyserver.proto` or `policy-server` changes — `CreatePolicyRequest`
already carries `rpo`/`backup_window`/`disabled_at` from the prior design, so this endpoint is only a
new REST handler that composes an existing gRPC request differently.

- `src/cmd/api-server/policies.go`: new `handleCreateAdhocPolicy`, wired to `POST
  /api/v1/policies/adhoc`.
- `src/cmd/api-server/policies.go`: `policyDTO`/`toPolicyDTO` gain `disabled_at` — currently missing
  from every policy response despite `policy-server`/proto already carrying it since the prior
  design. General fix, not adhoc-specific; the new endpoint's response gets it for free by reusing
  the same DTO.
- `src/common/config/config.go`: new `AdhocPolicyTimeoutSec int` field, default `3600`.
- `src/cmd/api-server/main.go`: reads `conf.AdhocPolicyTimeoutSec`, sets it on the `server` struct as
  a `time.Duration`.
- Docs: `docs/components/api-server.md`, `docs/api/rest-v1.md`, `CHANGELOG.md` (per this project's
  standing documentation rules).

## Out of scope

- Any UI/CLI surface — REST only, per the request.
- Enforcing `adhoc_policy_timeout_sec` > `policy_fetch_interval_sec` at request time or startup. If
  an operator configures the timeout shorter than nodes' polling interval, some nodes simply miss the
  policy before it disables itself — a silent no-op for them, not an error. Documented as an
  operational note, not validated in code.
- Any change to how `policy-server` stores, validates, or expires policies — it already treats
  `disabled_at` generically and has no notion of "adhoc."
- Preventing duplicate/overlapping adhoc policies (e.g. two adhoc requests with the same underlying
  name in flight at once). `policy-server`'s existing `uniqueFilename` collision handling
  (`<slug>-2.json`, `-3.json`, ...) already makes this harmless — both simply become distinct
  policies, each independently timing out.
- Editing or deleting an adhoc policy early — `PUT`/`DELETE /api/v1/policies/{id}` already work on it
  like any other backup policy; no new endpoint needed for that.

## `api-server` REST surface

`POST /api/v1/policies/adhoc` — same conventions as every other endpoint (bearer token, JSON body,
`writeGRPCError` mapping for backend failures).

**Request body** (reuses the existing `policyInput` struct as-is):

```json
{
  "name": "webserver-emergency",
  "client_filters": {"hostnames": ["web-*"]},
  "object_filters": [{"path": "/var/www", "exclude": ["*.log"]}],
  "destination": "bwfs-primary"
}
```

`rpo`/`backup_window` decode into the same struct (it's shared with `POST /api/v1/policies`) but are
never read by the handler — always computed server-side and silently overridden if present in the
body. This isn't validated/rejected; a caller that sets them just sees them ignored, consistent with
`policy-server` itself never treating extra/incidental fields as an error elsewhere in this API.

**Server-side composition**, in `handleCreateAdhocPolicy`:

- `name` = `"adhoc_" + in.Name`
- `type` = `"backup"` (fixed — there is no adhoc storage-policy concept)
- `backup_window` = `["* * * * *"]` — matches every minute, so the window is open as soon as a node
  polls
- `rpo` = `s.adhocPolicyTimeout.String()` (e.g. `"1h0m0s"` for the 3600s default) — since `rpo` and
  `disabled_at` share the same timeout, the policy is only ever due once per matched node: after that
  one run, `rpoElapsed` stays false until the timeout, by which point `disabled_at` has already
  removed the policy
- `disabled_at` = `time.Now().UTC().Add(s.adhocPolicyTimeout)`, sent as the proto `Timestamp`
- `client_filters`, `object_filters`, `destination` pass through from the request unchanged

The composed `pb.CreatePolicyRequest` is sent via the existing `s.policy.CreatePolicy`. Success
returns `201` with `policyDTO` — same response shape as `POST /api/v1/policies`, now including
`disabled_at` so the caller can see when the one-time policy expires.

Malformed JSON returns `400` before any backend call, matching `handleCreatePolicy`. Any backend
validation failure (e.g. empty name after prefixing, invalid glob) surfaces via the existing
`writeGRPCError` gRPC-code→HTTP-status mapping — no new validation logic in `api-server` itself.

## `policyDTO` change

```go
type policyDTO struct {
    // ...existing fields unchanged...
    DisabledAt int64 `json:"disabled_at,omitempty"`
}
```

`toPolicyDTO` sets it from `p.GetDisabledAt()` when non-nil (`.AsTime().Unix()`), left as the zero
value (omitted from JSON) when unset — mirroring how `policy-server`'s own `ToProto` only sets the
proto field when `Metadata.DisabledAt` is non-zero. This is a general fix applied to every policy
response (`GET`/`POST`/`PUT` on both `/policies` and `/storage-policies`), not scoped to the new
endpoint.

## Configuration

- New `Config.AdhocPolicyTimeoutSec int`, default `3600` — parsed in `config.go`'s existing
  switch-case (`case "AdhocPolicyTimeoutSec":`), same convention as `BackupWindowGraceSec`.
- Not a CLI flag (`arguments.go` unchanged) — set purely via `local.conf`, per the request ("timeout
  is set via standard config"), matching how `agent` reads `BackupWindowGraceSec`/
  `PolicyFetchIntervalSec` directly from `*config.Config` with no corresponding flag.
- `main.go` converts it to `time.Duration` once at startup and sets it on the `server` struct
  (`srv.adhocPolicyTimeout = time.Duration(conf.AdhocPolicyTimeoutSec) * time.Second`), the same
  post-construction-field-assignment pattern already used for `srv.loki`/`srv.clientManagerAdmin`.

## Testing plan

- `src/cmd/api-server/policies_test.go`: `handleCreateAdhocPolicy` against a fake
  `policyServiceClient` —
  - name gets the `adhoc_` prefix, type is always `"backup"`
  - `backup_window` is always `["* * * * *"]`, `rpo` matches the configured timeout's duration
    string, `disabled_at` is `~now + timeout` (bounded comparison, not exact-equality, given the
    call takes real wall-clock time)
  - `rpo`/`backup_window` set in the request body are ignored, not forwarded
  - response includes `disabled_at`
  - malformed JSON body returns `400`
  - a backend `CreatePolicy` error maps through `writeGRPCError` the same as the existing create
    endpoint
- `src/cmd/api-server/policies_test.go`: regression coverage that `toPolicyDTO` now includes
  `disabled_at` when set and omits it when zero, across the existing create/update/list tests.
- `src/common/config/config_test.go`: `AdhocPolicyTimeoutSec` parses from `local.conf` and defaults
  to `3600` when unset.

## Documentation impact

Per this project's standing documentation rules (feature change, new endpoint):

- `docs/components/api-server.md`: Endpoints section (new endpoint, one-sentence description) and
  Configuration Keys (`AdhocPolicyTimeoutSec`, matching the existing `Sec`-suffixed keys'
  case-sensitive convention, e.g. `BackupWindowGraceSec`).
- `docs/api/rest-v1.md`: new `## POST /api/v1/policies/adhoc` section with example request/response,
  alongside the existing `POST /api/v1/policies` section.
- `CHANGELOG.md`: dated entry summarizing the new endpoint and why (avoids hand-composing
  `backup_window`/`rpo`/`disabled_at` for one-time backups).
- No `docs/protocols/` update — no `.proto` changes.
