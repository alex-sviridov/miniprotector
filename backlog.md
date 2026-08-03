# Backlog

Ideas and known gaps not scheduled for implementation yet.

## Policy check-in / resolvable-storage-server list

`policy-server`'s `storage_policy_id` referential check (see
`docs/superpowers/specs/2026-08-03-backup-policy-storage-link-design.md`) only verifies that a
backup policy's `storage_policy_id` names an existing `"storage"`-typed policy — not that the
referenced storage policy actually resolves to a usable destination. A storage policy targeted
purely by labels (no `client_filters.hostnames`) is valid per `StoragePolicy.Validate()`, passes
the referential check, and yields an empty, silently-defaulting `destination` for any backup
policy that references it (`common.ParseDestination("", "localhost", defaultPort)` falls back to
`localhost` rather than erroring). This was surfaced by the final review of the
`storage_policy_id` change and deliberately left as-is rather than tightened, in favor of a more
fundamental fix below.

Idea: give clients a check-in mechanism — when a client receives its policies from
`policy-server`, it reports back which storage servers it actually stored/synthesized, so
`policy-server` can maintain a live, client-confirmed list of available storage servers per
storage policy (rather than trusting `client_filters.hostnames` as a static, unverified guess).
This would let `policy-server` (or a downstream consumer) resolve a backup policy's destination
from storage servers actually known to be reachable, instead of a glob pattern or label match that
was never confirmed to resolve to anything real.

**Frontend angle (2026-08-03, `backup-policy-form-improvements` final review):** the same gap is
now visible from `BackupPolicyFormModal.vue`'s destination select — an "(incomplete)" storage
policy (missing a hostname) is deliberately still selectable, by design (see
`docs/superpowers/specs/2026-08-03-backup-policy-form-improvements-design.md`), but nothing in the
form warns an operator that picking one produces a backup policy whose `destination` resolves to
empty; the failure only surfaces later, as a failed backup job. A check-in mechanism (above) would
fix this at the root; short of that, the form could show an inline warning when the selected
storage policy is incomplete.

**Related gap, same failure shape:** an empty `rpo` (or empty `backup_window`) also makes a backup
policy silently never run — `agent` skips a policy it can't parse a schedule for
(`src/cmd/agent/backup.go`) rather than erroring at save time. The design deliberately kept `rpo`
optional; tightening this (requiring a non-empty RPO, the same way `storage_policy_id` was made
required) is a real option worth reconsidering, not just a bug to patch.
