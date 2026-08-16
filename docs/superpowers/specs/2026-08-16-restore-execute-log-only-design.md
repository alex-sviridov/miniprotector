# Restore Execution — Log-Only First Slice — Design

> **Builds on:** `docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md` (`RestoreRule`
> schema, the ported `resolveRestoreFile` precedence logic), `docs/superpowers/specs/
> 2026-08-13-restore-destination-rename-design.md` (`dest_path`), `docs/superpowers/specs/
> 2026-08-14-restore-verify-execute-split-design.md` (the `mode`/`overwrite` UI/API split — today
> `mode: "restore"` is validated by `api-server` and then always rejected with `501`, never reaching
> `policy-server`), and `docs/superpowers/specs/2026-08-15-restore-file-version-resolution-design.md`
> (`not_before`/`not_after`, the streaming `ResolveRestoreFiles` RPC, and the rule-precedence
> tie-break `resolve.go`'s `restoreResolver` already implements). This design is the first round that
> makes `mode: "restore"` actually do something past `api-server` — but still not write a single
> byte to disk.

## Problem

Every piece of machinery needed to know *which files, at which version, under which new path* a
restore policy selects already exists and is exercised today by `rwfs verify --rules-stdin`:
rule precedence (`rules.go`), timeframe-scoped resolution against the real store
(`ResolveRestoreFiles`, `resolve.go`), and the `dest_path` rename field, which has been carried
end-to-end through the schema since 2026-08-13 without any consumer ever reading it. What doesn't
exist is any path from `mode: "restore"` to that machinery — `api-server` rejects it with `501`
before `policy-server` ever sees it, so there is no way to ask "what would a restore of this policy
actually do?"

We want that question answerable end to end — policy through `agent` through `rwfs` — without yet
building the part that writes files, so that the next round (real file writes) has a proven
resolution/dispatch path to build on, exactly the way `dest_path`'s wire plumbing preceded any
executor reading it.

## Goals

- `mode: "restore"` reaches `policy-server` and creates a real, one-shot `"restore"`-typed policy,
  the same way `mode: "verify"` always has.
- `agent` distinguishes a verify policy from a restore policy and dispatches the right `rwfs`
  subcommand for each.
- A new `rwfs restore` subcommand resolves a restore policy's rules against the live store — same
  precedence, same timeframe scoping, same not-found semantics `verify --rules-stdin` already has —
  and logs, per file: its source path and its computed destination path (`dest_path`'s rename rule
  applied, including the folder-rule prefix-substitution case). It also logs the policy's
  `overwrite` setting once per run. It writes nothing and calls `RestoreFile` on nothing.
- Task identity makes the two kinds of restore policy visually distinct without reading policy
  detail: a verify policy's task/job IDs move to a `verify:` prefix; a restore policy's task/job IDs
  use `restore:`.

## Non-Goals (this round)

- **No actual file write.** `rwfs restore` this round is exec + resolve + log, nothing else. A
  future round adds the write path, `overwrite`-conflict handling against files that already exist
  at the destination, and whatever new transport `bwfs`/`rwfs` need for that.
- **No restore-side concurrency flags.** `--streams`/`--retries` exist on `verify` because it does
  per-file network I/O (`RestoreFile`) that can fail and retry. `restore` this round only consumes
  the existing `ResolveRestoreFiles` stream and logs — there's nothing to parallelize yet.
- **No UI confirmation dialog, no destination-conflict preview.** The Restore button already exists
  (2026-08-14 design); clicking it is still harmless after this change, since nothing is written.
  A "this will overwrite N existing files" preview needs a real write path to be able to check
  existence against, so it's deferred with it.
- **No change to `dest_path`/`not_before`/`not_after`'s validation or wire shape.** Reused exactly
  as `verify --rules-stdin` already consumes them.
- **No change to `ListFiles`/`RestoreFile`/the restore RPC protocol.** `rwfs restore` only calls
  `ResolveRestoreFiles`, already shipped.

## Architecture

### 1. Proto: `mode`/`overwrite` on the restore policy

`src/api/policyserver.proto` — next free field numbers on both messages (`Policy` last used 19,
`CreatePolicyRequest` last used 14):

```proto
message Policy {
  // ...existing fields unchanged...
  repeated RestoreRule rules = 19;
  // "restore" policy only. "" or "verify" behaves exactly as every restore
  // policy does today (agent runs rwfs verify, writes nothing). "restore"
  // is the new log-only-for-now execution path (rwfs restore, see below).
  // A restore policy JSON file written before this field existed has no
  // "mode" key at all and is unaffected -- absent is read as "verify",
  // the same interpretation the type already had.
  string mode = 20;
  // "restore" policy only. Carried through and logged by rwfs restore;
  // has no effect when mode is "verify" or unset (the web UI already
  // sends this checkbox unconditionally on every submit, per the
  // 2026-08-14 design -- it is simply inert for a verify submission).
  bool overwrite = 21;
}

message CreatePolicyRequest {
  // ...existing fields unchanged...
  repeated RestoreRule rules = 14;
  string mode      = 15;
  bool   overwrite = 16;
}
```

Regenerate `src/api/policyserver.pb.go` via `make proto`.

### 2. `policy-server`

`src/cmd/policy-server/restore_policy.go`:

- `RestorePolicy` struct gains `Mode string \`json:"mode,omitempty"\`` and `Overwrite bool
  \`json:"overwrite,omitempty"\``.
- `Validate()` gains: `if p.Mode != "" && p.Mode != "verify" && p.Mode != "restore" { return
  fmt.Errorf("mode must be 'verify' or 'restore'") }`. No validation ties `Overwrite` to `Mode` —
  see the proto comment above.
- `ToProto` passes `Mode`/`Overwrite` through to `pb.Policy` alongside the existing fields.
- `Clone` needs no change — both are plain scalar fields, already covered by the struct copy.

`src/cmd/policy-server/write.go`'s `buildPolicyForCreate` restore branch (~line 211) gains
`Mode: req.GetMode(), Overwrite: req.GetOverwrite()` on the constructed `&RestorePolicy{...}`.

No change to `attachDestination`/`GetPolicies`/`ListPolicies` beyond what `ToProto` already carries
— `mode`/`overwrite` need no live resolution, unlike `destinations`.

### 3. `api-server`

`src/cmd/api-server/policies.go`'s `handleCreateRestore` (~line 342): remove the `if mode ==
"restore" { 501 }` branch entirely. Keep the `mode` default-to-`"verify"` and the `400` for an
unrecognized value. Add `Mode: mode, Overwrite: in.Overwrite` to the `CreatePolicyRequest{...}` —
both modes now build and send the same request shape, differing only in that field.

`policyDTO` (~line 39) and `toPolicyDTO` (~line 58) gain `Mode string \`json:"mode,omitempty"\`` /
`Overwrite bool \`json:"overwrite,omitempty"\`` so a `GET`/`ListPolicies` response round-trips them,
same treatment `dest_path` got on `ruleDTO`.

`src/cmd/api-server/jobs.go`:

- `validJobKinds` (~line 23) gains `"verify": true` alongside the existing `"restore": true` (which
  now means restore-execution, not verify — see §4's ID rename).
- `binariesForKind`'s case list (~line 51) becomes `case "bootstrap-refresh",
  "operating-refresh", "policy-update", "verify", "restore": return "agent"` — unchanged mapping,
  just naming both kinds that route through agent's own wrapper log.
- The `400` message at ~line 215 (`"kind must be one of backup, bootstrap-refresh,
  operating-refresh, policy-update, restore"`) gains `verify`.

### 4. `agent`

`src/cmd/policyclient/fetch.go`: `CachedPolicy` (~line 69) gains `Mode string
\`json:"mode,omitempty"\`` / `Overwrite bool \`json:"overwrite,omitempty"\``. `toCachedPolicies`
(~line 192) sets both from `p.GetMode()`/`p.GetOverwrite()`.

`src/cmd/agent/backup.go`'s `cachedPolicy` (~line 31, the duplicated subset `agent` itself reads)
gains the same two fields.

`src/cmd/agent/restore.go` is rewritten to branch on mode:

- `restoreTaskID(policyName string, mode string) string` — returns `fmt.Sprintf("verify:%s",
  policyName)` when `mode != "restore"`, else `fmt.Sprintf("restore:%s", policyName)`.
- `restoreJobID` gets the same mode parameter, same prefix logic, `:<unix-timestamp>` suffix
  unchanged.
- `restoreTasks`'s per-policy body picks `Args` based on mode:
  - `mode != "restore"` (unset or `"verify"`): `Args: []string{"verify", p.Destinations[0],
    "--rules-stdin", "--job-id", jobID}` — byte-for-byte what's dispatched today, only the ID
    prefix changed.
  - `mode == "restore"`: `Args: []string{"restore", p.Destinations[0], "--rules-stdin",
    "--job-id", jobID}`, with `--overwrite` appended when `p.Overwrite` is true.
- `Due`, `Background`, `Stdin` (the marshaled `rulesStdinPayload`), the empty-`Destinations`/empty-
  `Rules` skip checks, and the one-shot semantics are all unchanged and shared by both branches.

`src/cmd/agent/restore_test.go` and `src/cmd/agent/reconcile_test.go` (~line 605) currently assert
`ID: "restore:web01-emergency"`/`ID: "restore:x"` for what is, after this rename, verify behavior —
these become `verify:web01-emergency` / `verify:x`; new cases are added asserting the `restore:`
prefix and `restore` args/`--overwrite` threading for `mode: "restore"`.

### 5. `rwfs`: new `restore` subcommand

`src/cmd/rwfs/resolve.go`: `restoreResolver.Feed` changes from `Feed(row *pb.FileRow, filterIndex
int32) bool` to `Feed(row *pb.FileRow, filterIndex int32) (dispatch bool, ruleIndex int)`, returning
`r.filterToRuleIndex[filterIndex]` (the winning rule's index) alongside today's bool. `verify.go`'s
one call site (~line 149, inside the `ResolveRestoreFiles` consumption goroutine) becomes `if
dispatch, _ := resolver.Feed(...); dispatch { workCh <- resp.GetRow() }` — verify has no use for the
rule index, it only needs the file content.

`src/cmd/rwfs/rules.go` gains:

```go
// restoreDestPath computes row's destination path under rule's dest_path
// rename, if any. Works uniformly for a file-level rule (rowPath ==
// rule.Path exactly, a straight swap) and a folder-level rule (rowPath is
// always a rule.Path-prefixed descendant, by construction of
// longestMatchingFolderRuleIndex's ancestor-chain match) -- the "single
// replacement prefix for the whole folder" semantics
// docs/superpowers/specs/2026-08-13-restore-destination-rename-design.md
// already specified for a future executor to interpret.
func restoreDestPath(rule RestoreRule, rowPath string) string {
	if rule.DestPath == "" || rule.DestPath == rule.Path {
		return rowPath
	}
	return rule.DestPath + strings.TrimPrefix(rowPath, rule.Path)
}
```

New `src/cmd/rwfs/restore.go`, structured as `runRestore`/`runRestoreWithConn` mirroring
`verify.go`'s `runVerify`/`runVerifyWithConn` split (production entry point vs. a
bufconn-dialable body for tests):

- Always requires `--rules-stdin` — `parseArguments` rejects `restore` without it (§ below), so
  `runRestore` can assume `rulesStdin` is true and skip the plain-`ListFiles` branch `verify` still
  has.
- Reuses `parseRulesStdin`, `buildRestoreFilters`, `newRestoreResolver` unchanged.
- Opens `ResolveRestoreFiles` the same way `verify` does, but the consumption loop replaces the
  worker-pool dispatch with direct, sequential handling per arriving row: `dispatch, ruleIndex :=
  resolver.Feed(resp.GetRow(), resp.GetFilterIndex())`; if `dispatch`, compute `destPath :=
  restoreDestPath(rules[ruleIndex], row.GetPath())` and log it (unless `--quiet`):
  ```go
  logger.Info("resolved",
      "source", row.GetSource(),
      "path", row.GetPath(),
      "dest_path", destPath,
  )
  ```
- One `logger.Info("restore starting", "overwrite", overwrite, "rules", len(rules), "target",
  fmt.Sprintf("%s:%d", host, port))` line before the stream opens.
- Reuses `resolver.NotFound()` and the exact warning/error shape `verify` already has (`logger.Warn
  ("verification failed", ...)` text is verify-specific — `restore.go` uses `"resolution failed"`
  instead, same fields, same "file-level rule matching nothing on the resolved store is an error,
  folder-level is not" semantics) — and the same final `fmt.Errorf("%d file(s) failed resolution",
  warnings)` non-nil-on-failure contract, so `agent`'s existing one-shot success/failure handling
  needs no changes.
- Final `logger.Info("summary", "resolved", total, "warnings", warnings)` line.

`src/cmd/rwfs/arguments.go`:

- `Arguments` gains `Overwrite bool // restore only`.
- New `restoreCmd` cobra command, `Use: "restore [[server_name:]path] <bwfs_host:port>"` structurally
  identical to `verifyCmd`'s `Run` (sets `args.Action = "restore"`), but only registers
  `--rules-stdin`, `--overwrite`, `--quiet`, `--job-id`, `--debug` (no `--filter`, `--streams`,
  `--retries` — see Non-Goals).
- Validation gains: `if args.Action == "restore" && !args.RulesStdin { return nil,
  fmt.Errorf("restore requires --rules-stdin") }`.
- The `serverName == "" && !args.RulesStdin` hostname-default logic (shared with `list`/`verify`)
  already does the right thing for `restore` unchanged, since `restore` always sets `RulesStdin`.
- The "a subcommand is required" error message gains `restore`.

`src/cmd/rwfs/main.go` gains a `case "restore":` calling `runRestore(logger, arguments.BwfsHost,
arguments.BwfsPort, arguments.Overwrite, os.Stdin, arguments.Quiet, certsDir, jobID)`.

### 6. `web`

`web/src/stores/restoreSubmission.js`: each pushed result gains `mode` (the value `submit` was
called with) — `results.push({ storeHost, status: 'success', policy, mode })` — so the view can
render success copy correctly per-mode. The error-path push is unchanged (mode is irrelevant to an
error line).

`web/src/views/RestoreView.vue`: the success branch becomes:

```html
<span v-if="result.status === 'success'">
  Started {{ result.mode === 'restore' ? 'restore' : 'verification' }} policy
  {{ result.policy.name }} from {{ result.storeHost }}
</span>
```

## Data Flow (end to end)

```
web: user checks "Overwrite existing files", clicks Restore
  -> submission.submit(destinationHost, { mode: 'restore', overwrite: true })
  -> (unchanged fan-out) per store: POST /api/v1/restore
       { ..., rules, mode: 'restore', overwrite: true }

api-server: handleCreateRestore forwards mode/overwrite (no longer 501s)
  -> pb.CreatePolicyRequest{ ..., Mode: "restore", Overwrite: true }
  -> policy-server.CreatePolicy -> RestorePolicy{Mode:"restore", Overwrite:true, Rules:[...]}
       -> written to policies/restore/*.json

agent: GetPolicies -> policies-cache.json (mode/overwrite passthrough)
  -> restoreTasks(): p.Mode == "restore"
       ID = restore:<policy>, JobID = restore:<policy>:<ts>
       Args = ["restore", destinations[0], "--rules-stdin", "--job-id", jobID, "--overwrite"]
       Stdin = {"rules": [...]}

rwfs restore --rules-stdin --overwrite --job-id ...:
  parse rules -> ResolveRestoreFiles(filters) [streaming]
  -> per row: precedence tie-break (resolver.Feed) -> restoreDestPath(rule, row.Path)
  -> logger.Info("resolved", source, path, dest_path)   -- nothing written, no RestoreFile call
  -> summary log, non-zero exit iff a file-level rule matched nothing
```

## Error Handling

- `mode` present but not `"verify"`/`"restore"` — unchanged `400` at `api-server`, plus the same
  check now also independently enforced at `policy-server`'s `Validate()` (defense in depth, same
  as every other restore field).
- `restore` invoked without `--rules-stdin` — `rwfs` argument error, process exits before dialing
  anything (mirrors today's `--streams`/`--retries` argument-shaped validation).
- A file-level rule resolving to zero rows — resolution failure for that rule, non-zero exit,
  `agent`'s existing failure-backoff retries it next eligible tick. A folder-level rule resolving to
  zero rows is not a failure.
- `ResolveRestoreFiles` stream error — `runRestore` fails the whole run, same as `verify` today.
- Everything else (dangling `storage_policy_id`, empty `Destinations`, empty `Rules`, `SIGTERM`
  mid-run) is unchanged from the existing verify-task handling, now shared by both branches.

## Testing

- `policy-server`: `restore_policy_test.go` — `Validate` accepts `""`/`"verify"`/`"restore"`,
  rejects anything else; `ToProto` carries `mode`/`overwrite`; `CreatePolicy` end-to-end sets both
  from the request.
- `api-server`: `policies_test.go` — `mode: "restore"` now returns `201` and the policy client mock
  **was** called (inverts the existing `..._RestoreModeReturns501AndSkipsBackend` test — rename and
  rewrite it); `toPolicyDTO` round-trips `mode`/`overwrite`; `jobs_test.go` — `kindFromJobID` on a
  `verify:...` id returns `"verify"`, on a `restore:...` id returns `"restore"`; `binariesForKind`
  covers both.
- `agent`: `restore_test.go` — existing assertions renamed `restore:` → `verify:` for the
  mode-unset/`"verify"` case; new cases for `mode: "restore"` assert the `restore:` ID prefix,
  `["restore", ...]` args, and `--overwrite` present iff `p.Overwrite`; `reconcile_test.go`'s
  `restore:x` fixture (~line 605) renamed to match whichever behavior it's actually exercising.
- `rwfs`: `resolve_test.go` — `Feed`'s new second return value is the correct winning rule index
  across the existing precedence-tie-break cases. New `restore_test.go` — `restoreDestPath` for a
  file rule, a folder rule with a rename, and a folder rule with no rename (identity); `runRestore`
  over a fake streaming client logs the right `(source, path, dest_path)` triples and calls no
  `RestoreFile`-shaped RPC; not-found semantics match `verify`'s (file-level fails, folder-level
  doesn't); `--rules-stdin`-omitted argument error.
- `web`: `restoreSubmission.spec.js` — result entries carry `mode`; `RestoreView.spec.js` — success
  copy differs between a `verify` and a `restore` result.
- Integration: extend the existing e2e harness with a `mode: "restore"` submission against a real
  backed-up file, asserting the job's log contains a `"resolved"` line with the expected
  `dest_path` and that no file appears at the destination (nothing was written).

## Documentation Impact

Per `.claude/CLAUDE.md`'s protocol-change and feature-change rules (this round touches `.proto` and
regenerated `*.pb.go`):

- **`docs/protocols/policy-server.md`** — `Policy`/`CreatePolicyRequest` proto blocks gain
  `mode`/`overwrite`; the `"restore"` policy prose section documents both fields and what `mode:
  "restore"` currently does (log-only).
- **`docs/components/policy-server.md`** — same, in the "Policy types and directory layout"
  section.
- **`docs/components/api-server.md`** / **`docs/api/rest-v1.md`** — `POST /api/v1/restore`: `mode:
  "restore"` now returns `201` (not `501`), with a note that it currently only logs its resolved
  file list.
- **`docs/components/rwfs.md`** — new `## restore` section (mirroring the existing `## verify`
  section's structure), explicitly scoped as log-only, no write, no `RestoreFile` call this round.
- **`docs/protocols/restore.md`** — CLI→RPC mapping gains `rwfs restore --rules-stdin`'s mapping
  (`ResolveRestoreFiles` only, no `RestoreFile`).
- **`docs/components/agent.md`** — "Policy-driven restore verification" section split into
  "Policy-driven restore verification" (updated for the `verify:` prefix) and a new "Policy-driven
  restore execution" section (`restore:` prefix, log-only scope, `--overwrite` threading); the
  "Logging and correlation" section's job-id prefix list updated.
- **`docs/ARCHITECTURE.md`** — `agent`'s role line updated to mention the new log-only restore path
  alongside restore verification.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.

## Relationship to Prior Work

This is the first design in the restore-policy line whose primary contribution is *behavior* rather
than *schema* — every field it needs (`dest_path`, `not_before`/`not_after`, the streaming
resolution RPC, the rule-precedence tie-break) was already carried through the stack, unconsumed, by
the three designs listed at the top. `mode`/`overwrite` are the only new schema this design adds,
and only at the policy level (not per-rule). The next round past this one is the actual write path
— `rwfs restore` gaining the ability to open a destination file and stream chunks into it, and
`overwrite` gaining a real meaning (skip vs. clobber an existing file) — which this design's task
dispatch, rule resolution, and destination-path computation are built to hand off to unchanged.
