# Backlog

Ideas and known gaps not scheduled for implementation yet.

## Policy check-in / resolvable-storage-server list

Resolved 2026-08-04: `policy-server` now resolves a backup policy's `destinations` from its storage
policy's live checkin records instead of `client_filters.hostnames[0]`, closing the gap described
below. See
[Design: backup destination from checkin list](docs/superpowers/specs/2026-08-04-backup-destination-checkin-list-design.md).

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
