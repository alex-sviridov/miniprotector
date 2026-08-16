# Bootstrap Certificate Renewal: Real TTL + Failure Visibility — Design

## Problem

Investigating a stuck demo stack (all mesh services crash-looping on `tls: expired certificate`)
surfaced a real bug, confirmed against the pinned `smallstep/certificates@v0.30.2` source, not just
the demo's own long-idle volumes:

`certclient bootstrap()` (`src/cmd/certclient/bootstrap.go`) never sets `api.SignRequest.NotAfter`
on its initial `Sign` call. When unset, step-ca falls back to its own hardcoded
`provisioner.DefaultCertValidity = 24 * time.Hour` (`authority/provisioner/sign_options.go:27`,
reconfirmed in `authority/config/config.go:51-52`) — regardless of `BootstrapCertTTLSec`'s configured
~90-day (2160h) intent, which `docs/SECURITY.md`'s own credential table already flags as "parsed and
defaulted but not yet consumed by any request path (tracked follow-up)." Worse, step-ca's `/renew`
never re-derives a duration — `authority/tls.go:396`'s `renewContext` computes
`duration := oldCert.NotAfter.Sub(oldCert.NotBefore)` and reapplies that *original* delta forever. So
a bootstrap credential minted today stays pinned at 24 hours for its entire lineage, no matter how
many times it's renewed.

`agent`'s `bootstrap-refresh` policy runs `certclient renew` on a flat 24-hour interval
(`BootstrapCertRefreshIntervalSec`, `src/common/config/config.go:104`) — meaning renewal isn't
happening with months of safety margin, as the config implies, but is racing the certificate's actual
24-hour lifetime on every single cycle. And because `renew()` (`src/cmd/certclient/renew.go:26-53`)
authenticates the renewal request *by presenting the current, about-to-expire certificate itself*, one
missed cycle is unrecoverable: an already-expired client certificate is rejected by Go's TLS layer
during the handshake, before step-ca's application code ever runs, so there is no "renew a lapsed
cert" path — only a fresh `certclient bootstrap` (a new one-time enrollment token) can recover a node
in that state. Today that failure is also silent: it only shows up in the node's own local logs and
`agent list-policies` output, nowhere central.

## Goals

- A newly-bootstrapped node's certificate actually gets `BootstrapCertTTLSec`'s configured lifetime,
  restoring the intended safety margin between "renewed" and "expired."
- When a node's `bootstrap-refresh` is currently failing, that fact is visible centrally (queryable via
  `api-server`) — not only in the affected node's own, possibly-soon-unreachable logs.
- Minimal moving parts: reuse existing channels, existing schedules, existing credentials. No new
  round trips, no new message types beyond what's strictly needed, no UI work in this round.

## Non-Goals

- **No change to `certclient renew()`.** Confirmed unnecessary: `ca.Client.Renew`'s HTTP request body
  is `http.NoBody` (`ca/client.go:821`, pinned library) — genuinely parameterless — and step-ca always
  copies the *original* cert's duration forward on renewal. Fixing the initial `Sign` request is
  sufficient; every subsequent renewal in that lineage inherits the corrected duration automatically.
- **No retroactive fix for already-enrolled nodes.** A node's cert lineage keeps whatever duration its
  *original* bootstrap `Sign` was granted — today, 24h — until that node runs `certclient bootstrap`
  again with a fresh enrollment token. This design does not build an automatic re-bootstrap mechanism;
  it's a one-time operational step for existing fleets, called out in Documentation Impact below.
- **No web UI work.** The failure status becomes queryable via `api-server` in this round; rendering it
  in `ClientDetailView.vue`/`ClientsListView.vue` is a small, separate follow-on once the data exists —
  the same sequencing this codebase already used for `dest_path` (wire plumbing landed well before any
  UI or executor consumed it).
- **No generic "any agent task's failure" reporting.** Scoped specifically to `bootstrap-refresh` — the
  one task whose failure is both unrecoverable-without-intervention and silent. `operating-refresh`
  failures degrade more gracefully (per `docs/SECURITY.md`'s existing revocation-latency discussion)
  and aren't in scope here.
- **No provisioner claim beyond `--x509-max-dur`.** The client will always send an explicit `NotAfter`
  going forward, so the provisioner's *default* duration claim is never consulted for this traffic —
  only the *max* (ceiling) needs raising.

## Architecture

### Part A: request the correct TTL, and let the CA actually grant it

**`src/cmd/certclient/bootstrap.go`** — `bootstrap` gains a `ttlSec int` parameter:

```go
func bootstrap(token string, client signer, certsDir string, ttlSec int) error {
	req, pk, err := ca.CreateSignRequest(token)
	if err != nil {
		return fmt.Errorf("create sign request: %w", err)
	}
	req.NotAfter = api.NewTimeDuration(time.Now().Add(time.Duration(ttlSec) * time.Second))
	// ...unchanged from here (TemplateData, Sign, writeIdentity)
}
```

This mirrors `cmd/issuer/mintsign.go:42`'s existing pattern exactly — no new technique, just applying
one already-used line to a request that was missing it.

**`src/cmd/certclient/main.go`** — the `"bootstrap"` case passes `conf.BootstrapCertTTLSec` (already
loaded into `conf` before this call site — no new config plumbing needed):

```go
if err := bootstrap(tok, client, certsDir, conf.BootstrapCertTTLSec); err != nil {
```

**`deploy/control-plane/ca/entrypoint.sh`** — its `step ca provisioner update` line currently sets only
`--x509-template`. Add `--x509-max-dur=2200h` (matching the demo's own existing value — comfortably
above the 2160h/90-day request, never so wide it invites an operator setting `BootstrapCertTTLSec`
absurdly high without noticing a ceiling exists):

```sh
step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/leaf.tpl --x509-max-dur=2200h
```

`demo/ca/entrypoint.sh` needs **no change** — it already sets `--x509-max-dur=2200h`, already
comfortably above the fixed request. Only production was missing the ceiling.

### Part B: report `bootstrap-refresh` failures through the one channel guaranteed to still work

**Why `GetPolicies` and not a new endpoint:** `agent` knows the failure (it's in `PolicyState`, held
in-process) but doesn't dial out itself — only its subprocess binaries (`certclient`, `policyclient`)
do. `policyclient fetch` already calls `policy-server`'s `GetPolicies` every
`PolicyFetchIntervalSec` (15 min), already authenticated with the *operating* credential — a
credential on an independent renewal schedule from the bootstrap one, so it typically stays valid for
up to `OperatingCertTTLSec` (1h) *after* `bootstrap-refresh` starts failing. That's the reporting
window: piggyback on a call that's already happening, on a credential that's still good, rather than
opening any new channel.

**`src/api/policyserver.proto`** — `GetPoliciesRequest` gains two flat, optional fields (no new message
type — this reports exactly one thing, so the shape says exactly one thing):

```proto
message GetPoliciesRequest {
  // Set only when this node's bootstrap-refresh task is currently failing
  // (agent-state.json's "bootstrap-refresh" entry has a non-empty
  // last_error). Empty means either healthy or nothing to report.
  string bootstrap_refresh_last_error      = 1;
  int64  bootstrap_refresh_last_attempt_at = 2; // unix seconds; 0 = not reported
}
```

**`src/cmd/policyclient/fetch.go`** — immediately before building today's `GetPoliciesRequest{}`, a new
best-effort helper reads `agent-state.json` (same `VarPath` resolution both binaries already share via
`config.Config`) and looks up the single `"bootstrap-refresh"` key:

```go
// bootstrapRefreshFailure does a best-effort read of agent-state.json's
// "bootstrap-refresh" entry -- the one piece of agent's local state this
// binary has any reason to look at. A missing or unparseable file (agent
// not yet run, or a races-with-write read) is not an error here: it means
// "nothing to report," the same fail-safe direction agent's own readCache
// already takes for the identical file. This must never block the actual
// GetPolicies call that follows.
func bootstrapRefreshFailure(varPath string) (lastError string, lastAttemptAt int64) {
	data, err := os.ReadFile(filepath.Join(varPath, "agent-state.json"))
	if err != nil {
		return "", 0
	}
	var cache map[string]struct {
		LastAttemptAt *time.Time `json:"last_attempt_at"`
		LastError     string     `json:"last_error,omitempty"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return "", 0
	}
	entry, ok := cache["bootstrap-refresh"]
	if !ok || entry.LastError == "" {
		return "", 0
	}
	if entry.LastAttemptAt != nil {
		lastAttemptAt = entry.LastAttemptAt.Unix()
	}
	return entry.LastError, lastAttemptAt
}
```

The request construction becomes:

```go
lastErr, lastAt := bootstrapRefreshFailure(conf.VarPath)
resp, err := client.GetPolicies(ctx, &pb.GetPoliciesRequest{
	BootstrapRefreshLastError:     lastErr,
	BootstrapRefreshLastAttemptAt: lastAt,
})
```

**`src/storage/policyserver`** — one new table, two data columns, mirroring `CheckinRecord`'s existing
shape and upsert pattern exactly:

```go
// NodeCertStatus is the most recently reported bootstrap-refresh failure
// for hostname, if any -- separate from CheckinRecord (which is scoped to
// (PolicyID, Hostname) pairs, tracking which policies a node is actively
// polling) because this is a node-wide property with no policy_id to key
// on: bootstrap-refresh is agent's own built-in task, never a policy
// fetched from policy-server.
type NodeCertStatus struct {
	Hostname      string `gorm:"primaryKey"`
	LastError     string
	LastAttemptAt time.Time
}
```

```go
// RecordCertStatus upserts hostname's current bootstrap-refresh status on
// every GetPolicies call, healthy or not -- lastError empty overwrites a
// prior failure, so recovery is visible (paired with LastAttemptAt
// advancing) rather than a stale failure lingering forever with no way to
// tell "still broken" from "fixed weeks ago." Combined with the existing
// checkin's last_seen_at (updated by the same call), the two together
// distinguish all three states an operator cares about: recent checkin +
// empty LastError = healthy; recent checkin + non-empty LastError =
// actively failing but still reporting (the grace window); stale checkin
// = gone dark, LastError/LastAttemptAt frozen at whatever was last known
// before that.
func (s *Store) RecordCertStatus(ctx context.Context, hostname, lastError string, lastAttemptAt time.Time) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hostname"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_error", "last_attempt_at"}),
	}).Create(&NodeCertStatus{Hostname: hostname, LastError: lastError, LastAttemptAt: lastAttemptAt}).Error
}
```

**`src/cmd/policy-server/server.go`** — `GetPolicies`'s existing handler already resolves `hostname`
from the verified mTLS identity before its current `RecordCheckin` call (`server.go:85`). Immediately
alongside it:

```go
if err := s.certStatus.RecordCertStatus(ctx, hostname, req.GetBootstrapRefreshLastError(),
	time.Unix(req.GetBootstrapRefreshLastAttemptAt(), 0)); err != nil {
	s.logger.Error("record cert status failed", "hostname", hostname, "error", err)
	// non-fatal: GetPolicies must still succeed and return policies
}
```

Called unconditionally, mirroring the existing `RecordCheckin` call right next to it — not gated on
`LastError` being non-empty, so a recovery (an empty-error report overwriting a stale failure) is
captured too. Non-fatal by design — a status-recording failure must never block the actual policy
fetch, the same fail-safe posture as `bootstrapRefreshFailure`'s own read. A node that never sends the
fields at all (an old, unpatched binary) reports proto3's zero values, indistinguishable from "healthy"
— acceptable, since an unpatched node predates this feature entirely and its checkin `last_seen_at`
still functions unaffected.

**`src/api/policyserver.proto`** — confirmed no existing RPC fits: `RecordCheckin`
(`server.go:84-85`) only runs *inside* the per-matched-policy loop in `GetPolicies`, so a host with no
matching policies gets no checkin row at all today — piggybacking cert status onto
`PolicyCheckin`/`Policy.checkins` would silently drop exactly the hosts most likely to be
mid-enrollment-trouble. One small new RPC on the existing `PolicyService`:

```proto
service PolicyService {
  // ...existing RPCs unchanged...
  rpc GetNodeCertStatus(GetNodeCertStatusRequest) returns (NodeCertStatus);
}

message GetNodeCertStatusRequest {
  string hostname = 1; // required
}

// hostname with no reported status ever returns a NodeCertStatus with
// empty LastError and a zero LastAttemptAt -- not an error.
message NodeCertStatus {
  string hostname                           = 1;
  string last_error                         = 2; // "" = healthy or never reported
  google.protobuf.Timestamp last_attempt_at = 3;
}
```

Narrowed from an earlier draft that had `hostname` optional ("empty = all known hosts") and a
`repeated`-wrapping response: the only caller in this round is a single per-host detail lookup, so the
RPC returns exactly one `NodeCertStatus` for exactly one required `hostname` — no list wrapper, no
unused "list all hosts" mode. A bulk listing can be added later if a consumer actually needs one.

**`src/cmd/policy-server`** — new handler backed by a new `Store.CertStatusForHost` read method
(mirroring `CheckinsForPolicy`'s existing shape), called once per `GetPolicies` request as described
above — not from inside the per-policy loop.

**`src/cmd/api-server`** — one new small `GET` route proxying `GetNodeCertStatus`, following this
codebase's existing route-registration pattern. This is a genuine new piece — correcting the earlier,
too-optimistic "no new REST route" — but stays to exactly one RPC and one route; nothing else in this
design changes as a result.

## Data Flow

```
certclient bootstrap (one-time enrollment):
  reads conf.BootstrapCertTTLSec -> req.NotAfter = now + TTL -> Sign
  -> step-ca grants exactly the requested duration (now that --x509-max-dur allows it)

certclient renew (daily, agent's bootstrap-refresh policy):
  presents current bootstrap.crt -> step-ca's /renew copies that cert's own
  (now-correct) duration forward -> stays correct for the lineage's lifetime

  [failure path, e.g. missed cycles eventually lapse the cert]
  renew fails -> agent's PolicyState for "bootstrap-refresh" records LastError/LastAttemptAt
  in agent-state.json (agent's own existing behavior, unchanged)

policyclient fetch (every 15 min, agent's policy-update policy):
  best-effort read of agent-state.json's "bootstrap-refresh" entry
  -> if LastError set, include it on the already-happening GetPolicies call
  -> policy-server: hostname resolved from verified mTLS identity (operating cert,
     independently still valid for up to OperatingCertTTLSec after bootstrap starts failing)
  -> RecordCertStatus upserts NodeCertStatus once per call (not per matched policy)
  -> api-server: new GET route proxying policy-server's new GetNodeCertStatus RPC
     (UI rendering: follow-on, not this round)
```

## Error Handling

- `bootstrapRefreshFailure`'s file read/parse failure → `("", 0)`, silently — never blocks
  `GetPolicies`.
- `policy-server`'s `RecordCertStatus` failure → logged, `GetPolicies` still returns policies normally.
- A node that never reports (too old to have the fix, or `agent-state.json` genuinely absent) simply
  never has a `NodeCertStatus` row — `api-server`'s new field is absent/omitted for it, not a zero
  value implying "known healthy."
- `--x509-max-dur` set too low relative to `BootstrapCertTTLSec` → step-ca clamps the grant to the
  provisioner's ceiling rather than erroring; worth a one-line note in the CHANGELOG entry so a future
  `BootstrapCertTTLSec` increase doesn't silently stop taking effect.

## Security Considerations

- No new attack surface: `GetPolicies`'s two new fields ride the same already-mTLS-authenticated call;
  `hostname` for `RecordCertStatus` comes from the verified peer identity (`server.go`'s existing
  pattern for `RecordCheckin`), never from the request body — a node can only ever report status for
  itself.
- `bootstrap_refresh_last_error` is a Go error string (from `renew()`'s own `fmt.Errorf` wrapping) —
  worth a quick check during implementation that nothing in that error chain could leak a credential
  or key material; on inspection, `renew()`'s errors are all connection/parse-shaped
  (`"renew request: %w"`, `"load existing identity: %w"`), not secret-bearing, so plain pass-through is
  fine.
- No change to the two-tier credential model, the `EKUIssuerCaller` confinement, or revocation
  semantics documented in `docs/SECURITY.md` — this only makes an existing, already-correct trust
  boundary's failure mode observable, and fixes a duration bug within it.

## Testing

- `cmd/certclient`: `TestBootstrap_SetsRequestedNotAfter` — a fake `signer` capturing the `*api.SignRequest`
  it received, asserting `NotAfter` reflects the passed `ttlSec`.
- `cmd/policyclient`: `TestBootstrapRefreshFailure_ReadsAgentState` (present, non-empty `last_error`),
  `TestBootstrapRefreshFailure_MissingFileReturnsEmpty`, `TestBootstrapRefreshFailure_HealthyEntryReturnsEmpty`
  (entry present but `last_error` empty), `TestFetch_IncludesBootstrapRefreshStatusInRequest` (confirms
  the constructed `GetPoliciesRequest` carries it through).
- `storage/policyserver`: `TestRecordCertStatus_UpsertsByHostname` (second call overwrites, doesn't
  duplicate), `TestRecordCertStatus_EmptyErrorOverwritesPriorFailure` (the recovery case — this is the
  one the self-review pass exists to catch: a healthy report must actually clear a stale failure, not
  leave it stuck), mirroring `CheckinRecord`'s existing test pattern.
- `cmd/policy-server`: `TestGetPolicies_RecordsCertStatusOnEveryCall` (both a failing and a healthy
  report land in the store), `TestGetPolicies_RecordsCertStatusEvenWithNoMatchingPolicies` (the exact
  gap that ruled out piggybacking on `RecordCheckin`), `TestGetPolicies_SucceedsEvenIfCertStatusRecordFails`
  (non-fatal path), `TestGetNodeCertStatus_ReturnsRecordedStatus`, `TestGetNodeCertStatus_FiltersByHostname`.
- `cmd/api-server`: new route's handler test — proxies the RPC, DTO shape round-trips present/absent
  cases.
- Manual/integration: rebuild the demo stack fresh (`make demo-down && make demo-up`), confirm a newly
  enrolled node's `bootstrap.crt` `NotAfter` is ~90 days out (`openssl x509 -enddate -noout` against
  the cert in the container), not 24h.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule:

- **`docs/SECURITY.md`** — the credential table's "tracked follow-up" note for `BootstrapCertTTLSec`
  gets removed/updated now that it's consumed; add a short note on the new failure-visibility path
  alongside the existing revocation-latency discussion.
- **`docs/components/certclient.md`** — `bootstrap`'s description gains the `NotAfter`/TTL behavior.
- **`docs/components/policyclient.md`** — note the best-effort `bootstrap-refresh` status read/report.
- **`docs/components/policy-server.md`** — note `GetPolicies` now also records cert-renewal status
  alongside checkins.
- **`docs/components/api-server.md`** — note the new field on the relevant client-info response.
- **`docs/protocols/policy-server.md`** — `GetPoliciesRequest`'s proto block gains the two new fields;
  new section for `GetNodeCertStatus` (request/response shape, "empty hostname = all known hosts").
- **Operator-facing rollout note** (`CHANGELOG.md` or a short addition to `docs/SECURITY.md`): existing
  enrolled nodes keep their current (24h-lineage) bootstrap credential until re-bootstrapped; this fix
  is forward-only, not retroactive.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.

No `docs/ARCHITECTURE.md` change — no new component, no topology change, same services, same RPC.

## Relationship to Prior Work

Closes the gap `docs/SECURITY.md`'s two-tier credential model doc already named as a "tracked
follow-up." Builds on that doc's existing analysis of the bootstrap/operating credential split and its
already-documented `issuer`-unreachable failure mode — this design adds the missing piece for the
*other* failure mode (bootstrap itself lapsing) that doc didn't yet cover.
