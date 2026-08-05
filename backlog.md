# Backlog

Ideas and known gaps not scheduled for implementation yet.

## Policy check-in / resolvable-storage-server list

Resolved 2026-08-04: `policy-server` now resolves a backup policy's `destinations` from its storage
policy's live checkin records instead of `client_filters.hostnames[0]`, closing the gap described
below. See
[Design: backup destination from checkin list](docs/superpowers/specs/2026-08-04-backup-destination-checkin-list-design.md).

**Frontend angle (2026-08-03, `backup-policy-form-improvements` final review, updated 2026-08-05):**
`BackupPolicyFormModal.vue`'s destination select still labels a storage policy `"(incomplete)"` based
on whether `client_filters.hostnames[0]` is set — a signal that mattered when hostnames drove
resolution and is stale now that destinations resolve from checkins instead. The remaining real gap is
different: a storage policy nobody has checked in against yet (freshly created, or every checkin aged
past `CheckinRetentionSec`) still resolves to an empty `destinations` list for any backup policy
pointed at it, and the form gives no warning when an operator picks one. Fixing this at the root would
mean the form querying checkin state per storage policy (not yet exposed to the response shape used to
populate this select); short of that, retargeting or dropping the `"(incomplete)"` heuristic and/or
adding an inline warning are both worth reconsidering.

**Related gap, same failure shape:** an empty `rpo` (or empty `backup_window`) also makes a backup
policy silently never run — `agent` skips a policy it can't parse a schedule for
(`src/cmd/agent/backup.go`) rather than erroring at save time. The design deliberately kept `rpo`
optional; tightening this (requiring a non-empty RPO, the same way `storage_policy_id` was made
required) is a real option worth reconsidering, not just a bug to patch.
