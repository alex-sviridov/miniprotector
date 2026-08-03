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
