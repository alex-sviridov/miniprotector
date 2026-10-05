# Retention Policies and Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the `retention` policy type end to end — policy-server storage/API, `agent` consuming real rules, `api-server` REST, and a reorderable web UI.

**Architecture:** A retention policy is one rule (client_filters + backup_type + path + include + keep_seconds + server-managed priority) in `policies/retention/`. A new `ReorderRetentionPolicies` RPC rewrites priorities atomically from a complete ordered id list. `agent`'s `retentionRulesFrom` seam (Part 2) now maps cached retention policies to `retention.Rule`s. `api-server` proxies; the web UI adds a Retention page.

**Tech Stack:** Go 1.26, protobuf/gRPC (`make proto`), Vue 3 + Pinia + Vitest, Tailwind.

**Spec:** `docs/superpowers/specs/2026-10-05-retention-policies-design.md`

## Global Constraints

- Run Go from `src/`, web tests from `web/` (`npx vitest run`).
- `backup_type` allowed set: `{"filesystem"}`. `path`: absolute, slash-separated, no `..`. `include`: basename globs, a `/` in a pattern is rejected, each must be valid for `path.Match`. `keep_seconds >= 0` (`0` = never expire). `priority >= 1`, server-assigned.
- Create appends at `max(priority)+1`; Update preserves priority; only Reorder changes order; Reorder requires the exact set of existing retention ids.
- `ReorderRetentionPolicies` is `control-plane`-only like the other admin RPCs.
- Docs per `.claude/CLAUDE.md` (protocol doc, component docs, CHANGELOG); commit trailers per the session reminder.

## File Structure

- `src/api/policyserver.proto` (+ pb.go): `RetentionRule`, `Policy.retention`, request fields, Reorder RPC.
- `src/cmd/policy-server/retention_policy.go` (+test): `RetentionPolicy` type; modify `policy.go` (parser map), `write.go` (build/create/update/reorder), `server.go` (`ListPolicies` sort), `authz.go`.
- `src/cmd/policyclient/fetch.go` (+test): carry `retention`.
- `src/cmd/agent/backup.go` (`cachedPolicy.Retention`), `src/cmd/agent/retention.go` (`retentionRulesFrom`).
- `src/cmd/api-server/policies.go`, `server.go` (+tests): DTO + 3 handlers + client interface.
- `web/src/stores/retentionPolicies.js`, `web/src/views/RetentionView.vue`, `web/src/components/retention/RetentionFormModal.vue`, `web/src/utils/retentionRule.js` (+specs); `router.js`, `Sidebar.vue`, `components/icons/IconRetention.vue`.
- Docs: `docs/protocols/policy-server.md`, `docs/components/{policy-server,policyclient,agent,api-server,web}.md`, `docs/api/rest-v1.md`, `docs/ARCHITECTURE.md` (if needed), `CHANGELOG.md`.

---

### Task 1: Proto and the `retention` policy type in policy-server

**Files:** `src/api/policyserver.proto` (+ regenerated pb.go), create `src/cmd/policy-server/retention_policy.go` + `retention_policy_test.go`; modify `policy.go`, `write.go`, `server.go`.

**Interfaces:**
- Produces: `pb.RetentionRule`, `pb.Policy.Retention`, `pb.CreatePolicyRequest.Retention`, `pb.UpdatePolicyRequest.Retention`; `type RetentionPolicy struct { PolicyBase; BackupType string; Path string; Include []string; KeepSeconds int64; Priority int }` implementing `Policy`.

- [ ] **Step 1: Proto.** Add `message RetentionRule` (fields 1–5 per the spec), `RetentionRule retention = 23;` to `Policy`, `= 17` to `CreatePolicyRequest`, `= 13` to `UpdatePolicyRequest`, plus the Reorder RPC and messages from the spec. Run `make proto && cd src && go build ./...`.
- [ ] **Step 2: Failing tests** (`retention_policy_test.go`): parse JSON round-trip; `Validate` rejects empty name, unknown backup_type, relative path, `..` in path, `/` in include glob, malformed glob, negative keep_seconds, priority < 1; accepts keep 0; `ToProto` populates `Type`, `Retention` (all five fields) and honors `includeClientFilters`; `Clone` deep-copies `Include`; `Matches` uses client_filters (inherited from `PolicyBase`).
- [ ] **Step 3: Implement** `retention_policy.go` mirroring `storage_policy.go` (`parseRetentionPolicyJSON`, `Validate`, `Clone`, `ToProto`), register `"retention": parseRetentionPolicyJSON` in `policyParsers`.
- [ ] **Step 4: Write path tests then code** in `write_test.go`: `CreatePolicy` type `retention` → file in `policies/retention/`, priority `1` for the first, `max+1` for later ones, `req.Retention.Priority` ignored; reject mixing backup/storage/restore fields into a retention request and retention into other types; `UpdatePolicy` on a retention policy replaces fields, keeps `priority` and `created_at`; `DeletePolicy` works. Implement in `write.go`: `buildPolicyForCreate` retention branch (priority computed from `s.cache.Policies()` retention max, under `writeMu`), `buildPolicyForUpdate` accepts `retention` (kind allowed; priority copied from the existing `*RetentionPolicy`), and the cross-type field checks (`retentionFieldsSet`).
- [ ] **Step 5: `ListPolicies` ordering test then code:** with `Type: "retention"` results are sorted by `priority` ascending (ties by id); other types unchanged. Implement in `server.go`.
- [ ] **Step 6:** `cd src && go test ./cmd/policy-server/ ./api/...` → PASS. Commit `feat(policy-server): retention policy type`.

### Task 2: `ReorderRetentionPolicies` RPC

**Files:** `src/cmd/policy-server/write.go` (or new `reorder.go`), `authz.go`, tests in `write_test.go`/`authz_test.go`.

**Interfaces:** `func (s *policyServerServer) ReorderRetentionPolicies(ctx, *pb.ReorderRetentionPoliciesRequest) (*pb.ReorderRetentionPoliciesResponse, error)`.

- [ ] **Step 1: Failing tests:** three retention policies A,B,C (priorities 1,2,3); reorder `[C,A,B]` → files rewritten with 1,2,3 in that order, response lists C,A,B, `ListPolicies(retention)` agrees; request missing an id, with an unknown id, or with a duplicate → `InvalidArgument` and no file changed; empty list with zero retention policies succeeds; non-retention id rejected; `roleRequirements` contains the RPC for `control-plane`.
- [ ] **Step 2: Implement:** under `writeMu`, load current retention policies from the cache, validate the id set exactly equals the requested set, then for each policy whose priority changes rewrite its file with `atomicWriteJSON`, reload the cache once, return the new order. Add the `authz.go` entry.
- [ ] **Step 3:** `go test ./cmd/policy-server/` → PASS. Commit `feat(policy-server): ReorderRetentionPolicies RPC`.

### Task 3: `policyclient` and `agent` consume retention rules

**Files:** `src/cmd/policyclient/fetch.go` (+test), `src/cmd/agent/backup.go` (`cachedPolicy`), `src/cmd/agent/retention.go` (+test).

**Interfaces:** `policyclient.RetentionRule`/`CachedPolicy.Retention *RetentionRule` (`json:"retention,omitempty"`, fields `backup_type,path,include,keep_seconds,priority`); `cachedPolicy.Retention *cachedRetention`; `retentionRulesFrom(cached []cachedPolicy) []retention.Rule`.

- [ ] **Step 1: Failing tests:** `policyclient` maps `pb.Policy.Retention` into `CachedPolicy.Retention` (nil when absent). `agent` `retentionRulesFrom`: keeps only `type=="retention"`, `backup_type=="filesystem"`, not disabled; sorts by `(priority, name)`; maps `path→Prefix`, `include`, `keep_seconds`; ignores non-retention policies; empty input → nil. An agent-level test: with two cached retention policies and a backup task for `/data/app`, `prepareRetention`'s written matrix (via `retention.LoadFile`) gives the first matching rule's expiry and falls through to the default row.
- [ ] **Step 2: Implement** the mapping and replace the `retentionRulesFrom` stub body.
- [ ] **Step 3:** `go test ./cmd/policyclient/ ./cmd/agent/` → PASS. Commit `feat(agent): apply retention policies when resolving a job's matrix`.

### Task 4: `api-server` REST

**Files:** `src/cmd/api-server/policies.go`, `server.go` (client interface + routes), `policies_test.go` (+ fake client in the existing test helper).

**Interfaces:** `policyDTO.Retention *retentionDTO`; `POST /api/v1/retention-policies`, `PUT /api/v1/retention-policies/{id}`, `POST /api/v1/retention-policies/reorder`; `policyServiceClient.ReorderRetentionPolicies`.

- [ ] **Step 1: Failing handler tests** (follow the storage-policy tests): create/update forward `Type: "retention"` and the `Retention` fields, return 201/200 with `retention` in the DTO; invalid JSON → 400; backend `InvalidArgument` → 400 via `writeGRPCError`; reorder forwards `ids` and returns `{"data":[…]}` in order; `GET /policies?type=retention` DTO carries `retention`.
- [ ] **Step 2: Implement** DTO, input types, three handlers, routes, interface method; update every fake `policyServiceClient` in tests.
- [ ] **Step 3:** `go test ./cmd/api-server/` → PASS. Commit `feat(api-server): retention policy REST endpoints`.

### Task 5: Web UI

**Files:** see File Structure. Follow the storage page/store patterns.

**Interfaces:** `useRetentionPoliciesStore()` with `list` (ordered), `loading`, `error`, `fetchAll()`, `create(input)`, `update(id, input)`, `remove(id)`, `reorder(ids)` (optimistic: reorders `list`, calls the endpoint, reverts + rethrows on failure, refetches when the server reports a stale set); `retentionRule.js`: `validateRetentionForm(form) → {field: message}`, `toPayload(form)`, `fromPolicy(policy)`, `daysFromSeconds`, `formatKeep(seconds)`.

- [ ] **Step 1: Failing specs:** `retentionRule.spec.js` (validation of path/glob/days/forever, payload/round-trip, `formatKeep(0)=="Forever"`, `formatKeep(7*86400)=="7 days"`, `1 day`), `retentionPolicies.spec.js` (fetch uses `/policies?type=retention` and keeps server order; create posts to `/retention-policies`; update PUTs; remove DELETEs `/policies/{id}`; reorder POSTs `{ids}` and on failure restores the old order and sets `error`), `RetentionFormModal.spec.js` (renders fields, shows validation errors and does not emit `save` until valid, emits the expected payload, "Keep forever" disables the days input and sends 0, prefills in edit mode, shows `serverError`), `RetentionView.spec.js` (rows in order with keep/path/type, fixed default row last, move-up/move-down buttons call `reorder` with the new id order and are disabled at the ends, drag-and-drop dispatch calls `reorder`, delete confirms, empty state, error state), router and sidebar specs updated for the new route/item.
- [ ] **Step 2: Implement** store, utils, modal, view (drag handle using native HTML5 `draggable` + `dragstart/dragover/drop`, with buttons as the accessible fallback), `IconRetention.vue`, route `/retention`, sidebar entry.
- [ ] **Step 3:** `cd web && npx vitest run` → PASS; `npx vite build` → builds. Commit `feat(web): retention policies page with reorder`.

### Task 6: Docs, changelog, verification

- [ ] **Step 1:** Update `docs/protocols/policy-server.md` (proto additions, retention section, reorder RPC semantics), `docs/components/policy-server.md`, `policyclient.md`, `agent.md` (retention matrix now uses real rules), `api-server.md` and `docs/api/rest-v1.md` (3 endpoints + DTO field), `web.md` (Retention page), cross-links; `ARCHITECTURE.md` only if the data flow text needs it.
- [ ] **Step 2:** CHANGELOG entry "Retention policies and web UI" (2026-10-05).
- [ ] **Step 3:** `make test` and `cd web && npx vitest run`; `make lint` (only the pre-existing `cmd/brfs` vet warning may remain).
- [ ] **Step 4:** Commit `docs: retention policies — protocol, components, api, web, changelog`.

## Self-Review

- **Spec coverage:** type + validation → T1; priority server-managed, reorder RPC, authz → T1/T2; agent consumption → T3; REST → T4; UI incl. reorder, fixed default row, validation → T5; docs → T6.
- **Placeholders:** none; test lists name concrete behaviours; exact code is written task-by-task against the signatures above.
- **Type consistency:** `RetentionRule`/`Retention` names and field set (`backup_type, path, include, keep_seconds, priority`) are identical across proto, Go types, cache JSON, DTO and the web payload.
