# Policy Server Deployment Wiring — Design

> Follow-up to `docs/superpowers/specs/2026-07-10-policy-server-design.md`, whose implementation
> (`src/cmd/policy-server`) is already built and merged on this branch. That spec's own Non-Goals
> explicitly deferred a client-side consumer of `GetPolicies` — this doc does not revisit that; it
> only wires the already-complete binary into both Docker Compose deployment environments, so it
> can actually run as a real, enrolled node.

## Problem

`policy-server` exists as a binary with full test coverage but no deployment story: it isn't built
into a container, isn't a service in either `deploy/control-plane/docker-compose.yml` or
`demo/docker-compose.yml`, and isn't part of either environment's enrollment sequence. It cannot
currently be run anywhere except by hand.

## Approach

Mirror `catalog`'s existing deployment pattern exactly — the closest sibling component: also a
small, database-or-file-backed-locally gRPC service, also enrolled and certificate-managed like any
ordinary node (own `agent`/`certclient`/`issuer` cert refresh cycle, no special bootstrap path), and
already proven to work correctly in both environments. `policy-server` differs from `catalog` in
exactly one structural way worth naming: it has no local SQLite database (`STORAGE_PATH`) — its
persistent state is the operator-authored `policies/` directory instead, which `policy-server`'s own
`main.go` already `os.MkdirAll`s on startup, so no entrypoint-side directory setup is needed beyond
what the volume/bind-mount itself provides.

### `deploy/control-plane/` (production-reference environment)

- New `deploy/control-plane/policy-server/Dockerfile`: same multi-stage pattern as
  `deploy/control-plane/catalog/Dockerfile` (`golang:1.26` builder running
  `make policy-server certclient agent`, `debian:bookworm-slim` runtime) — minus the `sqlite3`
  apt package, which only `catalog` needs.
- New `deploy/control-plane/policy-server/entrypoint.sh`: same bootstrap-or-renew +
  `agent serve &` + wait-for-`client.crt` pattern as catalog's entrypoint, ending in
  `exec ./policy-server --debug="${DEBUG:-false}"` — no positional argument, since
  `policy-server`'s CLI (`cobra.NoArgs`) takes none, unlike catalog's `$STORAGE_PATH`.
- New `deploy/control-plane/policy-server/local.conf`: same shape as catalog's own `local.conf`
  (`ca_host`, `issuer_host`/`issuer_port`, `ReconcileIntervalSec`,
  `BootstrapCertRefreshIntervalSec`, `OperatingCertFetchIntervalSec`), with `policy_server_port`
  in place of `catalog_port`.
- New service block in `deploy/control-plane/docker-compose.yml`: mirrors catalog's exactly —
  `depends_on: [step-ca, issuer]`, bind-mounted `./policy-server/data:/data` (so an operator edits
  policy JSON files directly on the host at `deploy/control-plane/policy-server/data/policies/`,
  no container exec required) and `./policy-server/local.conf:/data/local.conf:ro`, `ports:
  ["9300:9300"]`.
- `deploy/control-plane/README.md` gains a policy-server section (enrollment command, port),
  mirroring catalog's existing section.

### `demo/` (self-contained lab environment)

- `demo/docker-compose.yml` gains a `policy-server` service reusing the same
  `deploy/control-plane/policy-server/Dockerfile` (build context `..`), following catalog's demo
  pattern exactly: named volume `policy-server-data:/data` (not a host bind mount — matches how
  catalog's demo `/data/storage` is also not host-visible; an operator would `docker compose exec`
  to manage policy files in the demo), shared `./local.conf:/data/local.conf:ro`.
- `demo/up.sh` gains one line, `enroll policy-server`, alongside the existing `enroll catalog`.
- `demo/README.md` gains a policy-server walkthrough step, in the same style as the existing
  `docker compose exec catalog ./agent list-policies` step — proving enrollment and cert refresh
  succeeded (`docker compose exec policy-server ./agent list-policies`), plus a short
  demonstration of the `policies/.changed` reload mechanism (write a policy file into the
  container, touch `.changed`, show a log line confirming reload) since that's policy-server's own
  distinctive, otherwise-invisible behavior.

### Documentation completeness

`docs/ARCHITECTURE.md`'s separate "Control Plane vs. Agents" table and its Docker-images
enumeration currently omit `policy-server` (a gap already flagged as Minor, non-blocking, by the
final whole-branch code review of the `policy-server` implementation itself) — this pass closes
that gap as part of the same documentation sweep, since this deployment work is what actually makes
`policy-server` a real Docker-image-producing control-plane member. `CHANGELOG.md` gets a new entry
for this deployment-wiring change.

## Non-Goals (unchanged from the parent spec)

- No client-side consumer of `GetPolicies` — nothing in either compose environment calls the RPC.
  This pass only makes the server reachable and enrollable, exactly like every other control-plane
  service in this repo before its own eventual consumer exists.
- No changes to `policy-server`'s own application code (`src/cmd/policy-server/`) — this is
  deployment-only.

## Testing

- `demo/up.sh`'s full walkthrough re-run end-to-end (cold `make demo-down && make demo-up`),
  confirming `policy-server` reaches steady `Up`, enrolls successfully, and its
  `agent list-policies` output shows both policies healthy — mirrors the verification pattern
  already established for every other demo-lab component.
- `deploy/control-plane`'s `docker compose config` (or an equivalent syntax/build check) confirming
  the new service block is well-formed and builds.
