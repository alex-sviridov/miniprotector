# Restore Policy Verification Execution — Design

> Builds on `docs/superpowers/specs/2026-08-09-restore-policy-type-design.md` (the `"restore"`
> policy type itself: `client_filters`, `source_store`, `config`) and
> `docs/superpowers/specs/2026-08-10-restore-cart-submission-design.md` (the web UI that actually
> creates one, with `config` = `{"files":[{"source_host","path"}, ...]}`). Both explicitly deferred
> the consumer side: "a future design covers `agent` actually picking up `"restore"`-typed policies
> and executing a restore." This is that work — scoped down to **verification only**, since
> `rwfs restore` (the write-to-filesystem path) doesn't exist yet (see `docs/ARCHITECTURE.md`'s
> `rwfs` row: "list + verify implemented; full restore not yet implemented").

## Problem

`policy-server` can hold a `"restore"`-typed policy and route it to the destination node via
`client_filters`, exactly like backup and storage policies. `agent` already derives dynamic tasks
from cached `"backup"` and `"storage"` policies (`backup.go`, `storage.go`) but has no notion of
`"restore"` at all — a cached restore policy sits inert in `policies-cache.json` forever. Since
actual restore can't be executed yet, the useful thing `agent` *can* do today is prove the
requested files are actually intact and retrievable from `source_store` — an early, real signal
that a future restore of this exact policy would succeed, using a capability (`rwfs verify`) that
already exists.

## Goals

- A `"restore"` policy's `config.files` (the exact, already-resolved file list the restore cart
  produced) gets verified against `source_store` via `rwfs verify`, automatically, the same
  "policy-server holds the directive, agent picks it up and acts" pattern backup/storage already
  use.
- Verification is a **one-shot** action per policy, matching the type's own stated intent ("meant
  to be picked up once") — retried with the existing backoff machinery on failure, but never
  re-run once it succeeds.
- A missing/unreachable file is a real, visible failure — not silently ignored — since every file
  in the list was explicitly requested.
- No `bwfs` change, no wire-protocol change. `bwfs` already serves everything `rwfs verify` needs
  (`ListFiles`, `RestoreFile`); this is purely a new client-side capability of `rwfs` plus a new
  consumer in `agent`.

## Non-Goals (this pass)

- **Actual restore.** `rwfs restore` (writing files back to a destination filesystem) remains
  unbuilt. This design's output is a pass/fail signal an operator can read via
  `agent list-policies` and logs, nothing more.
- **Fixing `config.files`'s size ceiling.** Nothing overrides gRPC's default max message size
  anywhere in this codebase (checked — no `MaxRecvMsgSize`/`MaxSendMsgSize` set), so a single
  `CreatePolicy` call already fails around ~35k–65k file entries in one `config.files` list,
  *before* `agent` is ever involved. A "restore a million files" scenario cannot reach `agent`
  today — it's rejected at `POST /api/v1/restore`. This design carries whatever size already
  successfully got created through one more hop (agent's exec → `rwfs`'s stdin); it does not need
  to handle anything larger, because nothing larger can exist. Redesigning `config` to carry a
  lazily-expanded folder pattern instead of an enumerated file list — the actual fix for
  million-file restores — is separate, larger work, out of scope here.
- **No new concurrency cap.** Restore-verify tasks share `agent`'s existing background-job
  semaphore (`MaxConcurrentBackupJobs`) rather than getting a dedicated cap. Restore verification
  is rare and one-shot; a busy backup window can delay a pending verify (or vice versa) by at most
  one reconcile tick, since an unslotted due task simply stays due. Revisit if this proves to
  matter in practice.
- **No pruning of stale per-task state**, no cross-node coordination — same acceptances
  `2026-07-10-agent-backup-execution-design.md` already made for backup tasks; nothing new here.
- **`list-policies`'s NEXT RUN column will read "due now" for a permanently-succeeded one-shot
  task.** `estimatedNextRun`/`formatNextRun` (`list.go`) have no "never again" concept — once
  `isDue` correctly reports the task not-due (via `Due` returning false post-success),
  `estimatedNextRun` falls through to `NextRun` (unset) then `LastSuccessAt.Add(Interval)`
  (`Interval` is zero for this task kind), i.e. `LastSuccessAt` itself — a past timestamp
  `formatNextRun` renders as "due now" even though the task will never run again. Cosmetically
  confusing but not a functional bug (`STATE`/`LAST SUCCESS` columns already show it succeeded).
  Fixing this properly means teaching `list.go` a "never again" sentinel shared with storage's
  hardcoded `"-"`, which storage tasks get by bypassing this display path entirely — deferred; not
  worth a shared-code change for one column's wording on a rare, one-shot task type.
- **Folder/negative-selection semantics.** These are already fully resolved client-side
  (`web/src/utils/restoreRules.js`'s longest-matching-rule resolution, applied in
  `restoreSubmission.js`'s `submit()` before `config.files` is ever built). By the time a
  `"restore"` policy exists, `config.files` is always a flat list of concrete, individual files —
  never folders, globs, or include/exclude rules. Neither `rwfs` nor `agent` need any rule-
  evaluation logic of their own; this is why exact-path verification (not a path-prefix filter) is
  the right primitive here.

## Architecture

### Data flow

```
web (restore cart, already shipped)
  -> POST /api/v1/restore  { source_store, config: {"files":[{source_host,path},...]} }
  -> policy-server ("restore" policy, client_filters targets the executing node)
       |
       v  GetPolicies (agent's existing policy-update job)
policies-cache.json  (gains source_store; config.files stays opaque JSON, passthrough)
       |
       v  agent serve, every reconcile tick
restoreTasks(): group config.files by source_host
  -> one Policy task per (restore policy, source_host)
       |
       v  due (never yet succeeded) -> background exec
rwfs verify <source_host>: <source_store> --files-stdin --job-id ...
  stdin = {"files":[{source_host,path},...]}  (that host's subset only)
       |
       v  ListFiles(server_name=source_host, path="") + RestoreFile per matched row
bwfs  (existing List/Restore protocol, unchanged)
```

### `rwfs verify` — new `--files-stdin` flag

Today, `rwfs verify` resolves exactly one `[server_name:]path` positional (exact host match,
*prefix* path match) plus a free-text `--filter` substring into a single `ListFiles` call, then
verifies every returned row.

New: a `--files-stdin` bool flag (`src/cmd/rwfs/arguments.go`, `verify.go`). When set, `rwfs
verify`:

1. Reads all of stdin, parses it as `{"files":[{"source_host":"...","path":"..."}, ...]}` — the
   same shape `config.files` already uses, so `agent` pipes a subset of the policy's own JSON
   through with no reshaping.
2. Runs the normal `ListFiles` call as today (agent passes `<source_host>:` as the positional, so
   this stays server-name-scoped, `path=""`).
3. Filters the returned rows to **exact** `(source, path)` matches against the stdin set (this is
   the actual new filtering logic — everything upstream of it is unchanged).
4. For any stdin entry with **zero** matching rows after step 3, synthesizes a failed
   `verifyResult` (`reason: "not found on this store"`) — a behavior change from today's "verify
   whatever matches, silently skip whatever doesn't" default, justified because every entry here
   was explicitly requested, not discovered by a broad filter.

`--filter` and the positional's path segment remain usable alongside `--files-stdin` (composed as
an additional AND filter) but `agent` never sets them — it always passes `<source_host>:` with an
empty path.

### `agent` — restore task derivation (`src/cmd/agent/restore.go`, new)

Mirrors `backup.go`'s shape:

- `cachedPolicy` (backup.go) and `policyclient`'s on-disk `CachedPolicy` (`fetch.go`) both gain
  `SourceStore string` — currently absent from the cache schema entirely; `toCachedPolicies` needs
  `SourceStore: p.GetSourceStore()` added.
- `restoreTasks(policiesCachePath, logger, conf) ([]Policy, bool)`: for every cached policy with
  `Type == "restore"` (skipping `p.disabled(now)`, same as backup/storage), parse `Config` as
  `{"files":[...]}`. A parse failure or empty list skips the policy with a logged error — the same
  fail-safe "skip, don't block the rest" direction `storageTasks` already uses for unparseable
  `config`.
- Group `files` by `source_host`. One `Policy` task per `(restore policy, source_host)` pair:
  - `ID`: `restore:<policy-name>:<slug(sourceHost)>`
  - `JobID`: `restore:<policy-name>:<slug(sourceHost)>:<unix-timestamp>`
  - `Binary`: `"rwfs"`; `Args`: `["verify", sourceHost+":", policy.SourceStore, "--files-stdin",
    "--job-id", jobID]`
  - `Stdin`: `{"files":[...]}` re-marshaled to just that host's subset
  - `Background: true` — a verify run can take a while and must never delay
    `bootstrap-refresh`/`operating-refresh`
  - `Due: func(s PolicyState, now time.Time) bool { return s.LastSuccessAt == nil }` — one-shot:
    `isDue`'s existing failure-backoff path handles retries on failure with zero new scheduling
    code; once `LastSuccessAt` is set, this task is never due again for as long as its ID stays in
    the cache (i.e., for as long as the policy — restore policies are deletable, just not
    updatable — still exists and still contains that host's files).

### `Policy`/`reconcile.go`: threading stdin through

The one structural change to existing code. `runner` becomes
`func(ctx context.Context, binary string, args []string, stdin []byte) error`; `realExec` sets
`cmd.Stdin = bytes.NewReader(stdin)` only when `stdin != nil`. `Policy` (`policy.go`) gains a
`Stdin []byte` field — zero-value (empty) for every existing policy/task, so this is strictly
additive for `bootstrap-refresh`/`operating-refresh`/`policy-update`/backup tasks. Both dispatch
paths in `run()` (synchronous and background) pass `p.Stdin` through to `execute`.

### Wiring (`main.go`)

`restoreTasksFunc` is folded into the same `policiesFunc` closure `backupTasks` already
contributes to, used by both `agent serve` and `agent list-policies` — no separate code path.

## Configuration

No new config keys. Restore-verify tasks reuse the existing `MaxConcurrentBackupJobs` background
semaphore (see Non-Goals).

## Error Handling

- **`policies-cache.json` missing or unparseable**: zero restore tasks derived that tick, `ok=false`
  — same contract `backupTasks`/`storageTasks` already use; never mistaken for "every restore task
  was removed" by `prune`.
- **Malformed/empty `config.files`**: policy skipped, logged — same direction as storage's
  unparseable `config`.
- **A requested file not found on `source_store`**: verification failure for that host's task
  (surfaced by `rwfs`, see above) — recorded as an ordinary task failure with per-task backoff;
  other hosts' tasks (same or different policy) are unaffected.
- **`source_store` unreachable**: ordinary exec failure (connection error), backoff, retried next
  eligible tick like any other task.
- **`config.files` larger than what `CreatePolicy` already permits**: cannot happen — see Non-Goals.
- **Concurrency cap reached**: a due task that can't acquire a slot this tick simply stays due,
  reconsidered next tick — not recorded as a failure.
- **`agent serve` receives `SIGTERM` mid-verify**: in-flight `rwfs verify` is terminated via the
  shared shutdown context, same as an in-flight `brfs` backup today; the task is retried (still not
  succeeded) on next startup.

## Testing

- `rwfs`: `--files-stdin` parses stdin and filters `ListFiles` rows to exact matches; a stdin entry
  with no matching row is reported as a failure with the expected reason and affects exit code;
  existing (no-flag) filter behavior is unchanged; malformed stdin JSON is a clear startup error.
- `agent`: `restoreTasks` — one task per distinct `source_host` in `config.files`; malformed/empty
  `config` skips the policy with a logged error and contributes no task; `disabled_at` in the past
  contributes no task; a policy/host pair disappearing from the cache is pruned via the existing
  `prune()` path with no restore-specific code.
- `agent`: one-shot `Due` semantics — never-succeeded is due; a failure schedules backoff via the
  existing `isDue`/`backoff()` path unchanged; a success makes the task permanently not-due (never
  re-dispatched on a later tick with the same cache entry).
- `agent`/`reconcile.go`: `Stdin` threading — a fake `runner` observes the exact bytes passed for
  both the synchronous and background dispatch paths; every pre-existing `Policy` (empty `Stdin`)
  is unaffected.
- Integration (extends the existing e2e harness): a real `policy-server` serving a `"restore"`
  policy whose `config.files` names real, previously-backed-up files on a real `bwfs`, and a real
  `agent serve` — confirms `rwfs verify` actually runs, succeeds, and the task never re-runs on a
  subsequent tick. A second case with one file deliberately absent confirms that task fails and
  retries with backoff.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule:

- **`docs/components/agent.md`** (exists) — new "Policy-driven restore verification" section
  mirroring "Policy-driven backup execution": task derivation, one-shot semantics, job-id/logging
  conventions, `list-policies` rows.
- **`docs/components/rwfs.md`** (exists) — document `--files-stdin`: stdin JSON shape, exact-match
  filtering, not-found-is-a-failure behavior.
- **`docs/protocols/restore.md`** (exists) — extend the "CLI → RPC Mapping" section with the
  `--files-stdin` case; no wire-protocol (`.proto`) change, so no protocol-field documentation is
  needed beyond that mapping.
- **`docs/ARCHITECTURE.md`** — update `agent`'s role line/paragraph to mention restore-policy
  verification alongside backup execution and storage supervision.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.

## Relationship to Prior Work

This is the piece both the 2026-08-09 restore-policy-type design and the 2026-08-10 restore-cart-
submission design explicitly deferred. Before this work, a restore policy is authored, served,
fetched, and cached — and then nothing happens. After this work, an authored restore policy results
in a real, tracked, one-shot verification that the exact files a future restore would need are
actually present and intact on `source_store` — closing the loop from "operator submits a restore
cart" to "agent has proven it's verifiable" without executing the (not yet built) restore itself.
