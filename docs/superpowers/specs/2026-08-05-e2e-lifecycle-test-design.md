# Design: E2E Lifecycle Test — Revoke/Reissue, Policy, Job, Catalog

## Problem

The current `src/e2e` suite (`docs/superpowers/specs/2026-07-27-e2e-tests-rewrite-design.md`) is a
single smoke test: it confirms the demo web UI answers HTTP 200 and nothing more. It proves the
stack is up, but exercises none of the product's actual behavior — no client credential lifecycle,
no policy-driven backup, no job or catalog visibility. We want a second e2e test that walks a
realistic operator flow end to end against the same already-running demo lab (`make demo-up`):
revoke a client's certificate and confirm it's locked out, reissue it, create a fast-recurring
backup policy, watch a real backup job run, and confirm the resulting entry lands in the catalog.

## Scope

Add one new test function to the existing `src/e2e` package (same `//go:build e2e` tag, same
package, same "demo lab must already be running" precondition as `TestE2E_WebUIAvailable`). No
changes to how the demo lab itself is started or torn down. The new test does introduce one
capability the existing suite deliberately avoided — shelling out to `docker compose exec` against
specific already-running containers — because two of the four steps (forcing an immediate
credential refresh, forcing an immediate policy fetch) have no host-reachable API and are only
triggerable by running a CLI already bundled on the target node.

Target node: **`database`**, an already-enrolled node in `demo/docker-compose.yml` with existing
sample data at `/var/lib/dbdata` (mounted from `demo/sample-data/db`). No new node, container, or
docker-compose service is created.

## Why an existing node, not a newly-enrolled one

A literal "add a new client" flow would need a new docker-compose service or `docker compose run`
against the `backup-host` build target — infrastructure the demo doesn't currently define for a
spare/generic node. That's out of scope here: this suite tests product behavior, not demo
topology. Revoking and reissuing `database`'s certificate exercises the same enrollment/credential
machinery (`issuer`, `client-manager`, `certclient`) without needing new infrastructure.

## Design

One test, four sequential sub-steps sharing state (target hostname, policy ID, job ID). Steps are
written as `t.Run` subtests for readability, but each depends on the previous one's outcome — they
run in order and a failure in an earlier subtest makes later ones meaningless.

### Step 1: Revoke → verify locked out → reissue

```
POST /api/v1/clients/database/revoke
docker compose -f demo/docker-compose.yml exec -T database certclient operating-refresh   # expect non-zero exit
POST /api/v1/clients/database/unrevoke
docker compose -f demo/docker-compose.yml exec -T database certclient operating-refresh   # expect success
```

`database` already holds a valid operating certificate (TTL up to `OperatingCertTTLSec`, default
1h) at test start, so simply revoking it wouldn't observably break anything until that certificate
expires on its own. Running `certclient operating-refresh` directly forces a *new* certificate
request, which `issuer` refuses outright for a revoked hostname (`docs/components/issuer.md`) — the
locked-out assertion is the command's non-zero exit code. Re-running it after `unrevoke` confirms
the node can obtain a fresh certificate again.

### Step 2: Create a 1-minute backup policy

```json
POST /api/v1/policies
{
  "name": "e2e-lifecycle-<unique-suffix>",
  "client_filters": {"hostnames": ["database"]},
  "object_filters": [{"path": "/var/lib/dbdata"}],
  "rpo": "1m",
  "backup_window": ["* * * * *"],
  "storage_policy_id": "<demo's existing store storage-policy id>"
}
```

`storage_policy_id` is resolved at test runtime, not hardcoded: `GET /api/v1/policies?type=storage`,
find the entry whose `name` is `"store"` (the demo's one storage policy — see
`demo/policy-server/policies/storage/store.json`), and use its `id`. Hardcoding the UUID would break
silently if the demo's storage policy is ever recreated with a new ID.

followed immediately by:

```
docker compose -f demo/docker-compose.yml exec -T database policyclient fetch
```

The policy name carries a unique suffix (e.g. a timestamp) so repeated test runs never collide.
`agent`'s own `policy-update` policy only refreshes its cache every `PolicyFetchIntervalSec`
(default 900s / 15 minutes) — far too slow for a test — so the test forces the fetch directly via
the same CLI `agent` would otherwise run on its own schedule. Once the cache is refreshed, the
policy's backup task becomes due on `database`'s very next reconcile tick (`ReconcileIntervalSec`,
30s in the demo), since it has never run before and its `backup_window` is open every minute.

### Step 3: Access the job

Poll `GET /api/v1/jobs?kind=backup&source_host=database&since=<step-2 timestamp>` until an entry
whose `job_id` embeds our policy name reaches `"state": "success"`, or a timeout elapses. Record
its `job_id`.

### Step 4: Access the catalog entry

Poll `GET /api/v1/catalog?source_host=database&job_names=e2e-lifecycle-<unique-suffix>` until at
least one entry appears, or a timeout elapses. Catalog replication (`catalogsync`) polls every
`CatalogSyncPollIntervalSec` (default 5s) after the job completes, so this should resolve quickly
once step 3 confirms success.

## Error handling and cleanup

`t.Cleanup`, registered before step 1 runs and executed regardless of test outcome:

- `POST /api/v1/clients/database/unrevoke` (unconditional — guarantees a failure partway through
  step 1 never leaves `database` locked out for any other use of the demo lab).
- `DELETE /api/v1/policies/{id}` for the policy created in step 2, if it was created.

Each polling step (3 and 4) uses a bounded timeout with a clear failure message identifying which
condition never became true (e.g. "no successful backup job for source_host=database, policy
e2e-lifecycle-<suffix> within 60s") — not a bare timeout error, matching the existing suite's
convention of self-explanatory failures (`docs/superpowers/specs/2026-07-27-e2e-tests-rewrite-design.md`).

## Supporting changes

- **`Makefile`**: `test-e2e`'s `-timeout` flag increases from `30s` to `120s` — this test is
  realistically tens of seconds (revoke/reissue is fast, but steps 2–4 wait on real reconcile/sync
  intervals), not the sub-second smoke check the current timeout was sized for.
- **`README.md`**: no changes — the documented precondition (`make demo-up` first) doesn't change.
- **No changes** to `docs/components/*.md`, `docs/ARCHITECTURE.md`, or `docs/protocols/` — this adds
  test coverage, not product behavior.
- **`CHANGELOG.md`**: an entry is added when this change merges to `main`, per project convention.

## Testing the change itself

- With the demo lab up (`make demo-up`) and no test running: `make test-e2e` passes both
  `TestE2E_WebUIAvailable` and the new lifecycle test.
- Interrupting the new test mid-run (e.g. `Ctrl-C` or a forced failure) and re-running `make
  test-e2e` should still pass — the unique policy-name suffix avoids `409` collisions, and
  `database` should never be left revoked between runs (verify manually once during
  implementation: `GET /api/v1/clients/database` shows `"revoked": false` after a deliberately
  failed run).
