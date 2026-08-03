# Backup Policy → Storage Policy Link Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace a backup policy's free-text `destination` with a required `storage_policy_id` reference to a storage policy, resolved to `host:port` live at read time, with deletion blocked while still referenced.

**Architecture:** `policyserver.proto` gains `storage_policy_id` on `Policy`/`CreatePolicyRequest`/`UpdatePolicyRequest` and drops `destination` as writable input. `BackupPolicy` (Go) stores `StoragePolicyID` instead of `Destination`; `Cache.ResolveDestination` looks it up against the live in-memory cache; a small `attachDestination` helper in `server.go` fills `pb.Policy.Destination` in at all four RPCs that return one. `DeletePolicy` refuses to remove a storage policy any backup policy still references. `api-server` passes the new field through unchanged in shape. Three demo fixture files are migrated to the new field.

**Tech Stack:** Go 1.x, gRPC/protobuf (`protoc`, `protoc-gen-go`, `protoc-gen-go-grpc`), `testify` (`assert`/`require`), `google/uuid`.

## Global Constraints

- No backward compatibility is being preserved: existing on-disk backup policy JSON files are migrated outright in this change, not grandfathered — `BackupPolicy.Validate()` requires `storage_policy_id` unconditionally, on both the load path and the write path.
- Every test in `src/cmd/policy-server` and `src/cmd/api-server` must pass: `cd /home/alex/miniprotector/src && go test ./...`.
- `cd /home/alex/miniprotector/src && go vet ./...` must pass (this repo's `make lint`).
- Per this repo's `.claude/CLAUDE.md` doc rules: a `.proto` change requires the matching `docs/protocols/*.md` updated and cross-linked from `README.md`/`docs/components/*.md`; a feature change requires `docs/components/<component>.md` updated; a dated `CHANGELOG.md` entry is required before merging to `main`.
- Regenerate protobuf code with `cd /home/alex/miniprotector && make proto` (wraps `protoc --go_out=... --go-grpc_out=... api/*.proto`) — never hand-edit `*.pb.go`/`*_grpc.pb.go`.
- Design reference: `docs/superpowers/specs/2026-08-03-backup-policy-storage-link-design.md`.

---

## Task 1: Proto schema — `storage_policy_id`, drop writable `destination`

**Files:**
- Modify: `src/api/policyserver.proto`
- Generated (via `make proto`, do not hand-edit): `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go`

**Interfaces:**
- Produces: `pb.Policy.GetStoragePolicyId() string` / `.StoragePolicyId` field (id 15); `pb.CreatePolicyRequest.GetStoragePolicyId() string` / `.StoragePolicyId` field (id 12); `pb.UpdatePolicyRequest.GetStoragePolicyId() string` / `.StoragePolicyId` field (id 12). `pb.Policy.GetDestination()`/`.Destination` (id 7) is unchanged in shape but becomes read-only in practice (no request message carries it anymore). `pb.CreatePolicyRequest`/`pb.UpdatePolicyRequest` no longer have a `Destination` field/getter at all.

- [ ] **Step 1: Edit `Policy` message** — in `src/api/policyserver.proto`, find:

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  // Duration string, e.g. "24h" (time.ParseDuration format). policy-server
  // never parses or evaluates this -- opaque pass-through data.
  string rpo = 5;
  // List of cron expressions (5-field). policy-server never parses or
  // evaluates these -- opaque pass-through data.
  repeated string backup_window = 6;
  string destination = 7;
```

Replace the `string destination = 7;` line with:

```proto
  // Derived, read-only. For a "backup" policy, resolved live from
  // storage_policy_id every time this message is produced -- never stored
  // or settable directly. Unset for a "storage" policy, as before.
  string destination = 7;
```

Then find the end of the message, after `google.protobuf.Timestamp disabled_at = 14;` and its closing `}`. Add a new field just before that closing `}`:

```proto
  google.protobuf.Timestamp disabled_at = 14;
  // "backup" policy only, required. References a "storage"-typed Policy.id;
  // destination is resolved from it live on every read.
  string storage_policy_id = 15;
}
```

- [ ] **Step 2: Edit `CreatePolicyRequest`** — find:

```proto
message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  // Any id set on an entry here is ignored -- object filter IDs are always
  // server-computed from their position in this list.
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  string destination = 6;
  // "backup" or "storage" -- required. Determines which of the fields above
  // (backup) or below (storage) are valid; mixing fields from both types is
  // rejected.
  string type = 7;
  // reserved 8 was "hostname" -- removed, see Policy.reserved 11 above.
  reserved 8;
  reserved "hostname";
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
}
```

Replace it with:

```proto
message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  // Any id set on an entry here is ignored -- object filter IDs are always
  // server-computed from their position in this list.
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  // reserved 6 was "destination" -- removed, destination is never itself
  // writable; it's derived from storage_policy_id. See
  // docs/superpowers/specs/2026-08-03-backup-policy-storage-link-design.md.
  reserved 6;
  reserved "destination";
  // "backup" or "storage" -- required. Determines which of the fields above
  // (backup) or below (storage) are valid; mixing fields from both types is
  // rejected.
  string type = 7;
  // reserved 8 was "hostname" -- removed, see Policy.reserved 11 above.
  reserved 8;
  reserved "hostname";
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
  // "backup" only, required.
  string storage_policy_id = 12;
}
```

- [ ] **Step 3: Edit `UpdatePolicyRequest`** — find:

```proto
message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  // Full replacement of object_filters, not a patch -- reordering or
  // inserting entries changes the affected filters' ids.
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  // A policy's type is immutable via UpdatePolicy -- there is no type field
  // here. port/config are only valid when the policy being updated is
  // already type "storage"; object_filters/rpo/backup_window/destination
  // are only valid when it's already type "backup".
  // reserved 8 was "hostname" -- removed, see Policy.reserved 11 above.
  reserved 8;
  reserved "hostname";
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
}
```

Replace it with:

```proto
message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  // Full replacement of object_filters, not a patch -- reordering or
  // inserting entries changes the affected filters' ids.
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  // reserved 7 was "destination" -- removed, see CreatePolicyRequest above.
  reserved 7;
  reserved "destination";
  // A policy's type is immutable via UpdatePolicy -- there is no type field
  // here. port/config are only valid when the policy being updated is
  // already type "storage"; object_filters/rpo/backup_window/storage_policy_id
  // are only valid when it's already type "backup".
  // reserved 8 was "hostname" -- removed, see Policy.reserved 11 above.
  reserved 8;
  reserved "hostname";
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
  // "backup" only, required.
  string storage_policy_id = 12;
}
```

- [ ] **Step 4: Regenerate**

Run: `cd /home/alex/miniprotector && make proto`
Expected: `Protobuf code generated in src/api/` with no errors.

- [ ] **Step 5: Verify the api package builds on its own**

Run: `cd /home/alex/miniprotector/src && go build ./api/...`
Expected: succeeds (this only proves the generated code itself is valid Go — the rest of the repo will not compile again until Task 2 lands, since `backup_policy.go`/`write.go`/`server.go`/`policies.go` still reference the now-removed `Destination` field on the request types).

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go
git commit -m "$(cat <<'EOF'
feat(proto): add storage_policy_id, drop writable destination

Policy gains storage_policy_id (backup-only, required); Create/UpdatePolicyRequest
drop destination as a writable field. Rest of the repo is updated to match in the
next commit -- this one only touches the wire schema.
EOF
)"
```

---

## Task 2: `policy-server` — schema, resolution, validation, delete-cascade

**Files:**
- Modify: `src/cmd/policy-server/backup_policy.go`
- Modify: `src/cmd/policy-server/cache.go`
- Modify: `src/cmd/policy-server/write.go`
- Modify: `src/cmd/policy-server/server.go`
- Modify (test fixtures only, no behavior change of their own): `src/cmd/policy-server/backup_policy_test.go`, `src/cmd/policy-server/cache_test.go`, `src/cmd/policy-server/write_test.go`, `src/cmd/policy-server/server_test.go`, `src/cmd/policy-server/policy_test.go`

**Interfaces:**
- Consumes: `pb.Policy`/`pb.CreatePolicyRequest`/`pb.UpdatePolicyRequest` with `StoragePolicyId`/`GetStoragePolicyId()` from Task 1.
- Produces: `BackupPolicy.StoragePolicyID string`; `Cache.ResolveDestination(storagePolicyID string) (dest string, ok bool)`; `attachDestination(pp *pb.Policy, cache *Cache)` (in `server.go`); `referencingBackupPolicies(policies []Policy, storagePolicyID string) []string` (in `write.go`).

**Note on TDD granularity:** every file below lives in `package main` (`src/cmd/policy-server`), so the package does not compile again until every production-code step in this task is done — there is no way to get a partial green test run mid-task the way a normal red/green/refactor cycle would. Steps 1–8 make the production and test edits file-by-file; Step 9 is the single build+test checkpoint for the whole task.

- [ ] **Step 1: `backup_policy.go` — schema, `Validate`, `Clone`, `ToProto`**

Replace the whole file's `BackupPolicy` struct, `Validate`, `Clone`, and `ToProto` (leave `parseBackupPolicyJSON` and the doc comment above `BackupPolicy` untouched except updating the field list in the comment):

```go
// BackupPolicy is the "backup" policy type: a set of object filters backed
// up on a schedule to a destination bwfs. Its on-disk JSON schema (beyond
// the shared metadata/client_filters PolicyBase already parses) is
// object_filters, rpo, backup_window, and storage_policy_id.
type BackupPolicy struct {
	PolicyBase
	ObjectFilters []ObjectFilter `json:"object_filters"`
	// Duration string, e.g. "24h" (time.ParseDuration format).
	// policy-server never parses or evaluates this -- opaque pass-through
	// data.
	RPO string `json:"rpo"`
	// List of cron expressions (5-field). policy-server never parses or
	// evaluates these -- opaque pass-through data.
	BackupWindow []string `json:"backup_window"`
	// References a "storage"-typed Policy.id. destination is resolved from
	// it live by Cache.ResolveDestination -- never itself stored or set
	// directly.
	StoragePolicyID string `json:"storage_policy_id"`
}
```

```go
// Validate checks the fields an operator can set on a backup policy,
// independent of where it came from (a file on disk or a Create/UpdatePolicy
// RPC request): the fields validateCommon checks, every object_filters
// include/exclude glob pattern must be syntactically valid (path.Match's
// syntax), and storage_policy_id must be non-empty. It cannot check that
// storage_policy_id actually names an existing "storage" policy -- that
// requires the live cache, which isn't available here (this same method
// runs during Cache.Reload's per-file, no-cache-yet load path); referential
// existence is checked separately, only in CreatePolicy/UpdatePolicy where
// a current cache is actually in scope.
func (p *BackupPolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	for _, of := range p.ObjectFilters {
		for _, pattern := range of.Include {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("invalid include pattern %q: %w", pattern, err)
			}
		}
		for _, pattern := range of.Exclude {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("invalid exclude pattern %q: %w", pattern, err)
			}
		}
	}
	if p.StoragePolicyID == "" {
		return fmt.Errorf("storage_policy_id is required")
	}
	return nil
}
```

```go
// Clone deep-copies every reference-typed field so mutating the returned
// value never affects the cached original.
func (p *BackupPolicy) Clone() Policy {
	objectFilters := make([]ObjectFilter, len(p.ObjectFilters))
	for i, f := range p.ObjectFilters {
		objectFilters[i] = ObjectFilter{
			ID:      f.ID,
			Path:    f.Path,
			Include: append([]string(nil), f.Include...),
			Exclude: append([]string(nil), f.Exclude...),
		}
	}
	backupWindow := make([]string, len(p.BackupWindow))
	copy(backupWindow, p.BackupWindow)
	return &BackupPolicy{
		PolicyBase:      p.PolicyBase.clone(),
		ObjectFilters:   objectFilters,
		RPO:             p.RPO,
		BackupWindow:    backupWindow,
		StoragePolicyID: p.StoragePolicyID,
	}
}
```

```go
// ToProto converts to the wire representation GetPolicies/ListPolicies/
// CreatePolicy/UpdatePolicy return. client_filters is only populated when
// includeClientFilters is true -- GetPolicies omits it so a matched node
// never learns another node's targeting rules from a policy that already
// matched its own identity; ListPolicies and the write RPCs include it for
// an operator editing the full policy set. Destination is deliberately left
// unset here -- it's resolved from StoragePolicyID by attachDestination
// (server.go), which every call site producing a pb.Policy invokes right
// after ToProto.
func (p *BackupPolicy) ToProto(includeClientFilters bool) *pb.Policy {
	objectFilters := make([]*pb.ObjectFilter, len(p.ObjectFilters))
	for i, f := range p.ObjectFilters {
		objectFilters[i] = &pb.ObjectFilter{Id: f.ID, Path: f.Path, Include: f.Include, Exclude: f.Exclude}
	}
	pp := &pb.Policy{
		Id:              p.Metadata.ID,
		Name:            p.Metadata.Name,
		CreatedAt:       timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:       timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters:   objectFilters,
		Rpo:             p.RPO,
		BackupWindow:    p.BackupWindow,
		StoragePolicyId: p.StoragePolicyID,
		Type:            p.Type,
	}
	if !p.Metadata.DisabledAt.IsZero() {
		pp.DisabledAt = timestamppb.New(p.Metadata.DisabledAt)
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
```

- [ ] **Step 2: `cache.go` — add `ResolveDestination`**

Add this method anywhere after `FindBySourcePath` in `src/cmd/policy-server/cache.go`:

```go
// ResolveDestination looks up storagePolicyID among the currently-loaded
// policies and, if it names a "storage" policy, returns its "host:port"
// computed from that policy's ClientFilters.Hostnames[0] and Port. ok is
// false if storagePolicyID doesn't resolve to a storage policy at all --
// unknown id, an id belonging to a non-storage policy, or a storage policy
// with no hostname set. Used by attachDestination (server.go) to resolve a
// backup policy's Destination live on every read.
func (c *Cache) ResolveDestination(storagePolicyID string) (string, bool) {
	p, ok := c.FindByID(storagePolicyID)
	if !ok || p.Kind() != "storage" {
		return "", false
	}
	sp, ok := p.(*StoragePolicy)
	if !ok || len(sp.ClientFilters.Hostnames) == 0 {
		return "", false
	}
	return fmt.Sprintf("%s:%d", sp.ClientFilters.Hostnames[0], sp.Port), true
}
```

- [ ] **Step 3: `write.go` — `policyFieldsGetter`, `backupFieldsSet`, `buildPolicy`**

In `src/cmd/policy-server/write.go`, replace:

```go
// backupFieldsSet reports whether any backup-only field is non-default --
// used to reject a request mixing backup fields into a storage policy.
func backupFieldsSet(objectFilters []*pb.ObjectFilter, rpo string, backupWindow []string, destination string) bool {
	return len(objectFilters) > 0 || rpo != "" || len(backupWindow) > 0 || destination != ""
}
```

with:

```go
// backupFieldsSet reports whether any backup-only field is non-default --
// used to reject a request mixing backup fields into a storage policy.
func backupFieldsSet(objectFilters []*pb.ObjectFilter, rpo string, backupWindow []string, storagePolicyID string) bool {
	return len(objectFilters) > 0 || rpo != "" || len(backupWindow) > 0 || storagePolicyID != ""
}
```

Replace:

```go
type policyFieldsGetter interface {
	GetObjectFilters() []*pb.ObjectFilter
	GetRpo() string
	GetBackupWindow() []string
	GetDestination() string
	GetPort() int32
	GetConfig() string
}
```

with:

```go
type policyFieldsGetter interface {
	GetObjectFilters() []*pb.ObjectFilter
	GetRpo() string
	GetBackupWindow() []string
	GetStoragePolicyId() string
	GetPort() int32
	GetConfig() string
}
```

Replace:

```go
func buildPolicy(kind string, base PolicyBase, req policyFieldsGetter) (Policy, error) {
	switch kind {
	case "backup":
		if storageFieldsSet(req.GetPort(), req.GetConfig()) {
			return nil, fmt.Errorf("a backup policy must not set port/config")
		}
		return &BackupPolicy{
			PolicyBase:    base,
			ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
			RPO:           req.GetRpo(),
			BackupWindow:  req.GetBackupWindow(),
			Destination:   req.GetDestination(),
		}, nil
	case "storage":
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), req.GetDestination()) {
			return nil, fmt.Errorf("a storage policy must not set object_filters/rpo/backup_window/destination")
		}
		return &StoragePolicy{
			PolicyBase: base,
			Port:       int(req.GetPort()),
			Config:     json.RawMessage(req.GetConfig()),
		}, nil
	default:
		return nil, fmt.Errorf("unknown policy type %q", kind)
	}
}
```

with:

```go
func buildPolicy(kind string, base PolicyBase, req policyFieldsGetter) (Policy, error) {
	switch kind {
	case "backup":
		if storageFieldsSet(req.GetPort(), req.GetConfig()) {
			return nil, fmt.Errorf("a backup policy must not set port/config")
		}
		return &BackupPolicy{
			PolicyBase:      base,
			ObjectFilters:   fromProtoObjectFilters(req.GetObjectFilters()),
			RPO:             req.GetRpo(),
			BackupWindow:    req.GetBackupWindow(),
			StoragePolicyID: req.GetStoragePolicyId(),
		}, nil
	case "storage":
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), req.GetStoragePolicyId()) {
			return nil, fmt.Errorf("a storage policy must not set object_filters/rpo/backup_window/storage_policy_id")
		}
		return &StoragePolicy{
			PolicyBase: base,
			Port:       int(req.GetPort()),
			Config:     json.RawMessage(req.GetConfig()),
		}, nil
	default:
		return nil, fmt.Errorf("unknown policy type %q", kind)
	}
}
```

- [ ] **Step 4: `write.go` — referential-existence check + resolved response in `CreatePolicy`**

Replace:

```go
	p, err := buildPolicyForCreate(req, now)
	if err != nil {
		s.logger.Error("CreatePolicy: validation failed", "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := p.Validate(); err != nil {
		s.logger.Error("CreatePolicy: validation failed", "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	slug := slugify(p.Meta().Name)
```

with:

```go
	p, err := buildPolicyForCreate(req, now)
	if err != nil {
		s.logger.Error("CreatePolicy: validation failed", "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := p.Validate(); err != nil {
		s.logger.Error("CreatePolicy: validation failed", "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if bp, ok := p.(*BackupPolicy); ok {
		if sp, found := s.cache.FindByID(bp.StoragePolicyID); !found || sp.Kind() != "storage" {
			s.logger.Error("CreatePolicy: storage_policy_id does not reference an existing storage policy", "storage_policy_id", bp.StoragePolicyID)
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("storage policy %q not found", bp.StoragePolicyID))
		}
	}

	slug := slugify(p.Meta().Name)
```

Then replace the function's final two lines:

```go
	s.logger.Info("CreatePolicy", "id", created.Meta().ID, "name", created.Meta().Name, "path", filePath)
	return created.ToProto(true), nil
}
```

with:

```go
	s.logger.Info("CreatePolicy", "id", created.Meta().ID, "name", created.Meta().Name, "path", filePath)
	pp := created.ToProto(true)
	attachDestination(pp, s.cache)
	return pp, nil
}
```

- [ ] **Step 5: `write.go` — referential-existence check + resolved response in `UpdatePolicy`**

Replace:

```go
	p, err := buildPolicyForUpdate(req, existing.Kind(), existing.Meta(), time.Now().UTC())
	if err != nil {
		s.logger.Error("UpdatePolicy: validation failed", "id", req.GetId(), "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := p.Validate(); err != nil {
		s.logger.Error("UpdatePolicy: validation failed", "id", req.GetId(), "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := atomicWriteJSON(existing.Path(), p); err != nil {
```

with:

```go
	p, err := buildPolicyForUpdate(req, existing.Kind(), existing.Meta(), time.Now().UTC())
	if err != nil {
		s.logger.Error("UpdatePolicy: validation failed", "id", req.GetId(), "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := p.Validate(); err != nil {
		s.logger.Error("UpdatePolicy: validation failed", "id", req.GetId(), "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if bp, ok := p.(*BackupPolicy); ok {
		if sp, found := s.cache.FindByID(bp.StoragePolicyID); !found || sp.Kind() != "storage" {
			s.logger.Error("UpdatePolicy: storage_policy_id does not reference an existing storage policy", "id", req.GetId(), "storage_policy_id", bp.StoragePolicyID)
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("storage policy %q not found", bp.StoragePolicyID))
		}
	}

	if err := atomicWriteJSON(existing.Path(), p); err != nil {
```

Then replace the function's final two lines:

```go
	s.logger.Info("UpdatePolicy", "id", updated.Meta().ID, "name", updated.Meta().Name, "path", existing.Path())
	return updated.ToProto(true), nil
}
```

with:

```go
	s.logger.Info("UpdatePolicy", "id", updated.Meta().ID, "name", updated.Meta().Name, "path", existing.Path())
	pp := updated.ToProto(true)
	attachDestination(pp, s.cache)
	return pp, nil
}
```

- [ ] **Step 6: `write.go` — delete-cascade block in `DeletePolicy`, plus helper**

Replace:

```go
// DeletePolicy removes the policy file backing id and reloads the cache.
func (s *policyServerServer) DeletePolicy(ctx context.Context, req *pb.DeletePolicyRequest) (*pb.DeletePolicyResponse, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}

	if err := os.Remove(existing.Path()); err != nil {
```

with:

```go
// referencingBackupPolicies returns the Meta().Name of every backup policy
// in policies whose StoragePolicyID equals storagePolicyID -- used by
// DeletePolicy to block removing a storage policy that's still in use.
func referencingBackupPolicies(policies []Policy, storagePolicyID string) []string {
	var names []string
	for _, p := range policies {
		if bp, ok := p.(*BackupPolicy); ok && bp.StoragePolicyID == storagePolicyID {
			names = append(names, bp.Meta().Name)
		}
	}
	return names
}

// DeletePolicy removes the policy file backing id and reloads the cache.
// Deleting a "storage" policy is refused, with the names of every
// referencing backup policy, if any backup policy's storage_policy_id
// still points at it.
func (s *policyServerServer) DeletePolicy(ctx context.Context, req *pb.DeletePolicyRequest) (*pb.DeletePolicyResponse, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}
	if existing.Kind() == "storage" {
		if inUse := referencingBackupPolicies(s.cache.Policies(), req.GetId()); len(inUse) > 0 {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("storage policy in use by: %s", strings.Join(inUse, ", ")))
		}
	}

	if err := os.Remove(existing.Path()); err != nil {
```

- [ ] **Step 7: `server.go` — `attachDestination`, wire into `GetPolicies`/`ListPolicies`**

Add this function anywhere after `toProtoClientFilters` in `src/cmd/policy-server/server.go`:

```go
// attachDestination resolves pp.Destination for a backup policy from its
// StoragePolicyId, using cache's live state. Called right after ToProto at
// every RPC that returns a pb.Policy (GetPolicies, ListPolicies,
// CreatePolicy, UpdatePolicy). A dangling reference (unknown id, or an id
// that no longer names a storage policy -- only reachable by hand-editing
// policy files outside the write RPCs, since DeletePolicy blocks the
// alternative) leaves pp.Destination unset rather than erroring.
func attachDestination(pp *pb.Policy, cache *Cache) {
	if pp.GetType() != "backup" || pp.GetStoragePolicyId() == "" {
		return
	}
	if dest, ok := cache.ResolveDestination(pp.GetStoragePolicyId()); ok {
		pp.Destination = dest
	}
}
```

Replace, in `GetPolicies`:

```go
		matched = append(matched, p.ToProto(false))
```

with:

```go
		pp := p.ToProto(false)
		attachDestination(pp, s.cache)
		matched = append(matched, pp)
```

Replace, in `ListPolicies`:

```go
		out = append(out, p.ToProto(true))
```

with:

```go
		pp := p.ToProto(true)
		attachDestination(pp, s.cache)
		out = append(out, pp)
```

- [ ] **Step 8: Update every existing test fixture package-wide**

In `src/cmd/policy-server/policy_test.go`, add `"storage_policy_id": "sp-1"` to the JSON body in these three tests (each currently reads `{"metadata": {"name": "..."}}`  for type `"backup"` and expects `require.NoError`):
- `TestParsePolicyFile_SetsTypeFromArgument`: body becomes `{"metadata": {"name": "nightly"}, "storage_policy_id": "sp-1"}`
- `TestParsePolicyFile_ComputesDeterministicPolicyID`: body becomes
  ```json
  {
      "metadata": {"name": "nightly-web-backup"},
      "object_filters": [{"path": "/var/www"}],
      "storage_policy_id": "sp-1"
  }
  ```
- `TestParsePolicyFile_DifferentFilenamesYieldDifferentPolicyIDs`: both `a.json`/`b.json` bodies become `{"metadata": {"name": "same-name"}, "storage_policy_id": "sp-1"}`

(`TestParsePolicyFile_MissingFileFails`, `TestParsePolicyFile_InvalidJSONFails`, `TestParsePolicyFile_UnrecognizedTypeFails` are unaffected — they fail before `Validate()` ever runs.)

In `src/cmd/policy-server/backup_policy_test.go`:

1. `TestParsePolicyFile_ValidPolicyParsesAllFields`: change the JSON body's `"destination": "bwfs-east.internal:8080"` to `"storage_policy_id": "sp-1"`; change the assertion `assert.Equal(t, "bwfs-east.internal:8080", p.Destination)` to `assert.Equal(t, "sp-1", p.StoragePolicyID)`.
2. `TestParsePolicyFile_ObjectFiltersAtDifferentIndicesGetDifferentIDs`: add `,\n\t\t"storage_policy_id": "sp-1"` to the JSON body.
3. `TestParsePolicyFile_ObjectFiltersWithIdenticalPathGetDistinctIDs`: add `"storage_policy_id": "sp-1"` to the JSON body.
4. `TestParsePolicyFile_ObjectFilterOmitsIncludeExclude`: add `"storage_policy_id": "sp-1"` to the JSON body.
5. (`TestParsePolicyFile_InvalidIncludePatternFails`, `_InvalidExcludePatternFails`, `_MissingNameFails`, `_InvalidHostnamePatternFails` are unaffected — they already expect an error for an unrelated reason.)
6. `TestBackupPolicy_ValidateValidPolicyReturnsNil`: add `StoragePolicyID: "sp-1",` to the `BackupPolicy{...}` literal.
7. (`TestBackupPolicy_ValidateMissingNameFails`, `_ValidateInvalidHostnamePatternFails`, `_ValidateInvalidIncludePatternFails`, `_ValidateInvalidExcludePatternFails` are unaffected — still error for their own stated reason.)
8. Add two new tests at the end of the file:

```go
func TestBackupPolicy_ValidateMissingStoragePolicyIDFails(t *testing.T) {
	p := &BackupPolicy{PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}}}
	assert.Error(t, p.Validate())
}

func TestBackupPolicy_ToProtoSetsStoragePolicyIdAndLeavesDestinationUnset(t *testing.T) {
	p := &BackupPolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "nightly"}, Type: "backup"},
		StoragePolicyID: "sp-1",
	}
	pp := p.ToProto(false)
	assert.Equal(t, "sp-1", pp.StoragePolicyId)
	assert.Empty(t, pp.Destination, "Destination is resolved elsewhere (Cache.ResolveDestination via attachDestination), never set directly by ToProto")
}
```

In `src/cmd/policy-server/cache_test.go`:

1. `TestCache_ReloadLoadsValidPolicies`: add `"storage_policy_id": "sp-1"` to both `a.json`/`b.json` bodies (e.g. `{"metadata": {"name": "policy-a"}, "storage_policy_id": "sp-1"}`).
2. `TestCache_ReloadSkipsMalformedFileKeepsGoodOnes`: add `"storage_policy_id": "sp-1"` to `good.json`'s body (`bad.json` is unaffected, it's invalid JSON).
3. `TestCache_ReloadAllFilesFailKeepsPreviousCache`: add `"storage_policy_id": "sp-1"` to `good.json`'s body.
4. `TestCache_ReloadSkipsUnrecognizedTypeSubfolder`: add `"storage_policy_id": "sp-1"` to `a.json`'s body (`b.json` under `other/` is unaffected — skipped regardless of content).
5. `TestCache_ReloadSkipsFileDirectlyUnderPoliciesDir`: add `"storage_policy_id": "sp-1"` to `a.json`'s body (`stray.json` is unaffected — skipped regardless of content).
6. `TestCache_PoliciesReturnsSnapshotCopy`: add `,\n\t\t\t"storage_policy_id": "sp-1"` to the JSON body (after `"backup_window": [...]`), and after the existing `assert.Equal(t, "backup", bp2.Type, ...)` line add:
   ```go
   assert.Equal(t, "sp-1", bp2.StoragePolicyID, "StoragePolicyID must survive the snapshot copy")
   ```
7. `TestCache_FindByIDReturnsMatchingPolicy`: add `"storage_policy_id": "sp-1"` to `a.json`'s body.
8. `TestCache_FindBySourcePathReturnsMatchingPolicy`: add `"storage_policy_id": "sp-1"` to `a.json`'s body.
9. `TestCache_ReloadLoadsBackupAndStoragePoliciesTogether`: add `"storage_policy_id": "sp-1"` to `a.json`'s (backup) body (`b.json`, storage, is unaffected).
10. Add three new tests at the end of the file:

```go
func TestCache_ResolveDestination_ReturnsHostPortForKnownStoragePolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east.json", `{
		"metadata": {"name": "east-storage"},
		"client_filters": {"hostnames": ["bwfs-east.internal"]},
		"port": 8080,
		"config": {}
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	storageID := c.Policies()[0].Meta().ID

	dest, ok := c.ResolveDestination(storageID)
	require.True(t, ok)
	assert.Equal(t, "bwfs-east.internal:8080", dest)
}

func TestCache_ResolveDestination_UnknownIDReturnsFalse(t *testing.T) {
	c := NewCache()
	_, ok := c.ResolveDestination("does-not-exist")
	assert.False(t, ok)
}

func TestCache_ResolveDestination_BackupPolicyIDReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{
		"metadata": {"name": "policy-a"},
		"storage_policy_id": "sp-1"
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	backupID := c.Policies()[0].Meta().ID

	_, ok := c.ResolveDestination(backupID)
	assert.False(t, ok, "an id belonging to a backup policy, not a storage policy, must not resolve")
}
```

In `src/cmd/policy-server/write_test.go`, add this helper right after `newTestWriteServer`:

```go
// createTestStoragePolicy creates a real "storage" policy on srv and
// returns its id, for tests whose backup policy needs a StoragePolicyId
// that actually resolves.
func createTestStoragePolicy(t *testing.T, srv *policyServerServer, hostname string, port int32) string {
	t.Helper()
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "storage-for-" + hostname,
		Type:          "storage",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{hostname}},
		Port:          port,
		Config:        "{}",
	})
	require.NoError(t, err)
	return resp.Id
}
```

Then apply these edits (every `Destination:`/`"destination"` use in a backup-type `CreatePolicyRequest`/`UpdatePolicyRequest`, or a raw backup-type JSON fixture, must go — either to a real id from `createTestStoragePolicy`, or to a dummy non-empty `"storage_policy_id"` string when the test only needs the file to *load*, not to resolve):

1. `TestCreatePolicy_WritesFileAndReturnsPolicyWithID`: add `storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)` right after `srv := newTestWriteServer(t, dir)`; change the request's `Destination: "bwfs:8080",` to `StoragePolicyId: storageID,`; after `assert.Equal(t, "Nightly DB Backup", resp.Name)` add `assert.Equal(t, "bwfs:8080", resp.Destination, "destination must resolve from the referenced storage policy")`.
2. `TestCreatePolicy_SecondCallWithSameNameGetsDistinctFile`: add `storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)` after `srv := newTestWriteServer(t, dir)`; change `req := &pb.CreatePolicyRequest{Name: "dup", Type: "backup", Destination: "bwfs:8080"}` to `req := &pb.CreatePolicyRequest{Name: "dup", Type: "backup", StoragePolicyId: storageID}`.
3. `TestCreatePolicy_ConcurrentCreatesForDifferentNamesBothSurvive`: add `storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)` after `srv := newTestWriteServer(t, dir)`, before the `names := ...` line; inside the goroutine's request, change `Destination: "bwfs:8080",` to `StoragePolicyId: storageID,`.
4. `TestCreatePolicy_ClientFiltersRoundTrip`: add `storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)` after `srv := newTestWriteServer(t, dir)`; add `StoragePolicyId: storageID,` to the request.
5. `TestUpdatePolicy_OverwritesFileKeepsIDAndCreatedAt`: change the on-disk fixture's `"destination": "bwfs:8080"` to `"storage_policy_id": "placeholder"`; after `original := srv.cache.Policies()[0]` add `storageID := createTestStoragePolicy(t, srv, "bwfs", 9090)`; change the `UpdatePolicyRequest`'s `Destination: "bwfs:9090",` to `StoragePolicyId: storageID,`. The existing `assert.Equal(t, "bwfs:9090", resp.Destination)` stays as-is (it now proves live resolution rather than an echoed string).
6. `TestUpdatePolicy_InvalidInputReturnsInvalidArgumentLeavesFileUnchanged`: change the fixture body to `{"metadata": {"name": "nightly"}, "storage_policy_id": "placeholder"}`.
7. `TestDeletePolicy_RemovesFileAndReloads`: change the fixture body to `{"metadata": {"name": "nightly"}, "storage_policy_id": "placeholder"}`.
8. `TestDeletePolicy_LeavesOtherPoliciesIntact`: change both fixture bodies to include `, "storage_policy_id": "placeholder"`.
9. `TestCreatePolicy_ResponseIncludesBackupType`: add `storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)` after `srv := newTestWriteServer(t, dir)`; add `StoragePolicyId: storageID,` to the request.
10. `TestCreatePolicy_StorageTypeWithBackupFieldsRejected`: change `Destination: "bwfs:8080",` to `StoragePolicyId: "sp-1",`.
11. `TestUpdatePolicy_StorageTypeWithBackupFieldsRejected`: change `Destination: "bwfs:8080",` to `StoragePolicyId: "sp-1",`.
12. `TestCreatePolicy_DisabledAtRoundTrips`: add `storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)` after `srv := newTestWriteServer(t, dir)`; change `Destination: "bwfs:8080",` to `StoragePolicyId: storageID,`.
13. `TestCreatePolicy_NoDisabledAtLeavesItUnset`: same pattern as #12.
14. `TestCreatePolicy_PastDisabledAtAcceptedWithoutError`: same pattern as #12.
15. `TestUpdatePolicy_DisabledAtRoundTrips`: add `storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)` after `srv := newTestWriteServer(t, dir)`; change both the `CreatePolicy` and `UpdatePolicy` requests' `Destination: "bwfs:8080",` to `StoragePolicyId: storageID,`.
16. `TestUpdatePolicy_OmittingDisabledAtClearsIt`: same pattern as #15 (both Create and Update calls).

Add five new tests at the end of the file:

```go
func TestCreatePolicy_MissingStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{Name: "no-storage", Type: "backup"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_UnknownStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "orphan",
		Type:            "backup",
		StoragePolicyId: "does-not-exist",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestUpdatePolicy_UnknownStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)
	created, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "nightly",
		Type:            "backup",
		StoragePolicyId: storageID,
	})
	require.NoError(t, err)

	_, err = srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:              created.Id,
		Name:            "nightly",
		StoragePolicyId: "does-not-exist",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestDeletePolicy_StoragePolicyInUseByBackupPolicyRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)
	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "nightly-db-backup",
		Type:            "backup",
		StoragePolicyId: storageID,
	})
	require.NoError(t, err)

	_, err = srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: storageID})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "nightly-db-backup")
	_, statErr := os.Stat(filepath.Join(dir, "storage", "storage-for-bwfs.json"))
	assert.NoError(t, statErr, "storage policy file must remain when the delete is rejected")
}

func TestDeletePolicy_UnreferencedStoragePolicySucceeds(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: storageID})

	require.NoError(t, err)
	assert.Empty(t, srv.cache.Policies())
}
```

In `src/cmd/policy-server/server_test.go`:

1. Add `"fmt"` to the import block (alongside the other standard-library imports already there).
2. Add `"storage_policy_id": "sp-1"` to the backup-type JSON fixture in every one of these tests (each currently uses a bare `{"metadata": {"name": "..."}}` or similar with no `storage_policy_id`):  `TestGetPolicies_ReturnsOnlyMatchingPolicies` (`web.json`, `db.json`), `TestGetPolicies_EmptyFiltersMatchEveryone` (`all.json`), `TestGetPolicies_MatchesOnPeerCertLabels` (`db.json`), `TestGetPolicies_MissingJobIDRejected` (`web.json`), `TestListPolicies_ReturnsAllPoliciesRegardlessOfIdentity` (`web.json`, `db.json`), `TestListPolicies_IncludesClientFilters` (`web.json`), `TestGetPolicies_StillOmitsClientFilters` (`web.json`), `TestGetPolicies_ResponseIncludesType` (`web.json`), `TestListPolicies_ResponseIncludesType` (`web.json`), `TestListPolicies_FilterByTypeReturnsOnlyMatchingType` (`web.json` only — `east-1.json` is storage, unaffected), `TestListPolicies_EmptyTypeReturnsEveryType` (`web.json` only), `TestListPolicies_UnknownTypeReturnsEmpty` (`web.json`), `TestGetPolicies_ExcludesPolicyPastItsDisabledAt` (`expired.json`, `active.json`), `TestGetPolicies_IncludesPolicyWithFutureDisabledAt` (`not-yet.json`), `TestListPolicies_IncludesDisabledPolicies` (`expired.json`).

   Example (`TestGetPolicies_ReturnsOnlyMatchingPolicies`'s `web.json`): change
   ```json
   {
       "metadata": {"name": "web-policy"},
       "client_filters": {"hostnames": ["web-*"]}
   }
   ```
   to
   ```json
   {
       "metadata": {"name": "web-policy"},
       "client_filters": {"hostnames": ["web-*"]},
       "storage_policy_id": "sp-1"
   }
   ```
   Apply the same shape of edit (add `"storage_policy_id": "sp-1"` as a sibling of `"metadata"`) to every fixture listed above.

3. Replace `TestGetPolicies_ResponseFieldsRoundTrip` entirely with:

```go
func TestGetPolicies_ResponseFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east.json", `{
		"metadata": {"name": "east-storage"},
		"client_filters": {"hostnames": ["bwfs-east.internal"]},
		"port": 8080,
		"config": {}
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	storageID := c.Policies()[0].Meta().ID

	writePolicyFile(t, filepath.Join(dir, "backup"), "full.json", fmt.Sprintf(`{
		"metadata": {"name": "full-policy", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
		"object_filters": [{"path": "/var/www", "include": ["*.html"], "exclude": ["*.tmp"]}, {"path": "/etc"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"storage_policy_id": %q
	}`, storageID))
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	p := resp.Policies[0]
	assert.Equal(t, "full-policy", p.Name)
	assert.Equal(t, "24h", p.Rpo)
	assert.Equal(t, []string{"0 2 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination, "destination must resolve live from storage_policy_id")
	assert.Equal(t, storageID, p.StoragePolicyId)
	require.Len(t, p.ObjectFilters, 2)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, []string{"*.html"}, p.ObjectFilters[0].Include)
	assert.Equal(t, []string{"*.tmp"}, p.ObjectFilters[0].Exclude)
	assert.Empty(t, p.ObjectFilters[1].Include)
	assert.Empty(t, p.ObjectFilters[1].Exclude)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.CreatedAt.AsTime())
	assert.Equal(t, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), p.UpdatedAt.AsTime())
	assert.NotEmpty(t, p.Id)
	assert.NotEmpty(t, p.ObjectFilters[0].Id)
	assert.NotEmpty(t, p.ObjectFilters[1].Id)
	assert.NotEqual(t, p.ObjectFilters[0].Id, p.ObjectFilters[1].Id)
}
```

4. Add three new tests at the end of the file:

```go
func TestGetPolicies_DanglingStoragePolicyIdLeavesDestinationUnset(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "orphan.json", `{
		"metadata": {"name": "orphan-policy"},
		"storage_policy_id": "does-not-exist"
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Empty(t, resp.Policies[0].Destination)
}

func TestListPolicies_ResolvesDestinationFromStoragePolicyId(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east.json", `{
		"metadata": {"name": "east-storage"},
		"client_filters": {"hostnames": ["bwfs-east.internal"]},
		"port": 8080,
		"config": {}
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	storageID := c.Policies()[0].Meta().ID

	writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", fmt.Sprintf(`{
		"metadata": {"name": "nightly"},
		"storage_policy_id": %q
	}`, storageID))
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "bwfs-east.internal:8080", resp.Policies[0].Destination)
}
```

- [ ] **Step 9: Build and test the whole package**

Run: `cd /home/alex/miniprotector/src && go build ./... && go vet ./cmd/policy-server/... && go test ./cmd/policy-server/... -v`
Expected: build succeeds; every test in `src/cmd/policy-server` passes (existing tests updated in Step 8, new tests added in Steps 1–8 all green). If anything fails, fix it before moving on — do not proceed to Task 3 with a red test suite.

- [ ] **Step 10: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/policy-server/backup_policy.go src/cmd/policy-server/cache.go src/cmd/policy-server/write.go src/cmd/policy-server/server.go src/cmd/policy-server/backup_policy_test.go src/cmd/policy-server/cache_test.go src/cmd/policy-server/write_test.go src/cmd/policy-server/server_test.go src/cmd/policy-server/policy_test.go
git commit -m "$(cat <<'EOF'
feat(policy-server): resolve backup policy destination from storage_policy_id

BackupPolicy now stores StoragePolicyID instead of a free-text Destination;
Cache.ResolveDestination looks it up live against the current cache, and
attachDestination fills it into every RPC that returns a Policy. Both
CreatePolicy/UpdatePolicy require storage_policy_id to reference an existing
storage policy; DeletePolicy refuses to remove one still referenced by a
backup policy.
EOF
)"
```

---

## Task 3: `api-server` — pass `storage_policy_id` through

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/policies_test.go`

**Interfaces:**
- Consumes: `pb.Policy.GetStoragePolicyId()`, `pb.CreatePolicyRequest.StoragePolicyId`, `pb.UpdatePolicyRequest.StoragePolicyId` from Task 1/2.
- Produces: `policyDTO.StoragePolicyID string` (JSON `storage_policy_id`, `omitempty`); `policyInput.StoragePolicyID string` (JSON `storage_policy_id`).

- [ ] **Step 1: `policies.go` — DTO and input struct**

In `src/cmd/api-server/policies.go`, replace:

```go
type policyDTO struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
	ClientFilters clientFiltersDTO  `json:"client_filters"`
	ObjectFilters []objectFilterDTO `json:"object_filters"`
	RPO           string            `json:"rpo"`
	BackupWindow  []string          `json:"backup_window"`
	Destination   string            `json:"destination"`
	Type          string            `json:"type"`
	Port          int32             `json:"port"`
	Config        string            `json:"config"`
	DisabledAt    int64             `json:"disabled_at,omitempty"`
}
```

with:

```go
type policyDTO struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       int64             `json:"updated_at"`
	ClientFilters   clientFiltersDTO  `json:"client_filters"`
	ObjectFilters   []objectFilterDTO `json:"object_filters"`
	RPO             string            `json:"rpo"`
	BackupWindow    []string          `json:"backup_window"`
	Destination     string            `json:"destination"`
	StoragePolicyID string            `json:"storage_policy_id,omitempty"`
	Type            string            `json:"type"`
	Port            int32             `json:"port"`
	Config          string            `json:"config"`
	DisabledAt      int64             `json:"disabled_at,omitempty"`
}
```

Replace, in `toPolicyDTO`:

```go
	dto := policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters: objectFilters,
		RPO:           p.GetRpo(),
		BackupWindow:  p.GetBackupWindow(),
		Destination:   p.GetDestination(),
		Type:          p.GetType(),
		Port:          p.GetPort(),
		Config:        p.GetConfig(),
	}
```

with:

```go
	dto := policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters:   objectFilters,
		RPO:             p.GetRpo(),
		BackupWindow:    p.GetBackupWindow(),
		Destination:     p.GetDestination(),
		StoragePolicyID: p.GetStoragePolicyId(),
		Type:            p.GetType(),
		Port:            p.GetPort(),
		Config:          p.GetConfig(),
	}
```

Replace:

```go
type policyInput struct {
	Name          string              `json:"name"`
	ClientFilters clientFiltersDTO    `json:"client_filters"`
	ObjectFilters []objectFilterInput `json:"object_filters"`
	RPO           string              `json:"rpo"`
	BackupWindow  []string            `json:"backup_window"`
	Destination   string              `json:"destination"`
	DisabledAt    int64               `json:"disabled_at,omitempty"`
}
```

with:

```go
type policyInput struct {
	Name            string              `json:"name"`
	ClientFilters   clientFiltersDTO    `json:"client_filters"`
	ObjectFilters   []objectFilterInput `json:"object_filters"`
	RPO             string              `json:"rpo"`
	BackupWindow    []string            `json:"backup_window"`
	StoragePolicyID string              `json:"storage_policy_id"`
	DisabledAt      int64               `json:"disabled_at,omitempty"`
}
```

- [ ] **Step 2: `policies.go` — the three handlers building a backup `CreatePolicyRequest`/`UpdatePolicyRequest`**

In `handleCreatePolicy`, replace `Destination:   in.Destination,` with `StoragePolicyId: in.StoragePolicyID,`.

In `handleCreateAdhocPolicy`, replace `Destination:   in.Destination,` with `StoragePolicyId: in.StoragePolicyID,`.

In `handleUpdatePolicy`, replace `Destination:   in.Destination,` with `StoragePolicyId: in.StoragePolicyID,`.

(`storagePolicyInput`, `decodeStoragePolicyInput`, `handleCreateStoragePolicy`, `handleUpdateStoragePolicy` are unaffected — a storage policy never had a `destination`.)

- [ ] **Step 3: `policies_test.go` — rename input field, add DTO coverage**

In `src/cmd/api-server/policies_test.go`, apply these edits (request-body JSON literals and their matching assertions; `pb.Policy{...}` literals used only as canned *responses* from the fake client — e.g. in `TestHandleListPolicies_ReturnsDataEnvelope` — are untouched, `Destination` stays valid there):

1. `TestHandleCreatePolicy_ReturnsCreatedPolicy`: change body to `strings.NewReader(`{"name": "nightly", "storage_policy_id": "sp-1"}`)`; change `assert.Equal(t, "bwfs:8080", fake.lastCreateReq.GetDestination())` to `assert.Equal(t, "sp-1", fake.lastCreateReq.GetStoragePolicyId())`.
2. `TestHandleUpdatePolicy_ReturnsUpdatedPolicy`: change body to `strings.NewReader(`{"name": "nightly-renamed", "storage_policy_id": "sp-2"}`)`; change `assert.Equal(t, "bwfs:9090", fake.lastUpdateReq.GetDestination())` to `assert.Equal(t, "sp-2", fake.lastUpdateReq.GetStoragePolicyId())`.
3. `TestToPolicyDTO_ConvertsTimestampsToUnixSecondsAndClientFilters`: add `StoragePolicyId: "sp-1",` to the input `pb.Policy{...}` literal; add `assert.Equal(t, "sp-1", dto.StoragePolicyID)` after the existing `assert.Equal(t, "backup", dto.Type)` line.
4. `TestHandleCreateAdhocPolicy_ComposesFieldsAndPrefixesName`: change the body's `"destination": "bwfs:8080"` to `"storage_policy_id": "sp-1"`; change `assert.Equal(t, "bwfs:8080", fake.lastCreateReq.GetDestination())` to `assert.Equal(t, "sp-1", fake.lastCreateReq.GetStoragePolicyId())`.
5. `TestHandleCreateAdhocPolicy_IgnoresCallerSuppliedScheduleFields`: change the body's `"destination": "bwfs:8080"` to `"storage_policy_id": "sp-1"`.
6. `TestHandleCreateAdhocPolicy_ReturnsPolicyDTOWithDisabledAt`: change the body's `"destination": "bwfs:8080"` to `"storage_policy_id": "sp-1"`.
7. `TestHandleCreatePolicy_SetsDisabledAtWhenProvided`: change the body's `"destination": "bwfs:8080"` to `"storage_policy_id": "sp-1"`.
8. `TestHandleCreatePolicy_OmittedDisabledAtLeavesItUnset`: change the body's `"destination": "bwfs:8080"` to `"storage_policy_id": "sp-1"`.
9. `TestHandleUpdatePolicy_EchoesDisabledAtBack`: change the body's `"destination": "bwfs:8080"` to `"storage_policy_id": "sp-1"`.
10. `TestHandleUpdatePolicy_OmittedDisabledAtClearsIt`: change the body's `"destination": "bwfs:8080"` to `"storage_policy_id": "sp-1"`.

- [ ] **Step 4: Build and test**

Run: `cd /home/alex/miniprotector/src && go build ./... && go vet ./cmd/api-server/... && go test ./cmd/api-server/... -v`
Expected: build succeeds, every test in `src/cmd/api-server` passes.

- [ ] **Step 5: Full-repo build and test**

Run: `cd /home/alex/miniprotector/src && go build ./... && go vet ./... && go test ./...`
Expected: everything in the repo builds, vets clean, and every test passes — this is the first point since Task 1 where the whole repo, not just one package, is verified.

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "$(cat <<'EOF'
feat(api-server): pass storage_policy_id through instead of destination

Mirrors policy-server's schema change one layer up: policyInput/policyDTO
gain storage_policy_id, replacing destination as create/update input; the
response DTO still surfaces destination (now server-derived, read-only)
alongside it.
EOF
)"
```

---

## Task 4: Migrate demo fixtures

**Files:**
- Modify: `demo/policy-server/policies/backup/audit-logs.json`
- Modify: `demo/policy-server/policies/backup/database-backup.json`
- Modify: `demo/policy-server/policies/backup/webserver-backup.json`

Do **not** touch `demo/policy-server/policies/backup/hosts.json` (untracked, currently unreadable in this environment — unrelated pre-existing state, out of scope) or `demo/policy-server/policies/storage/store.json` (unchanged; only its `id`, computed from its filename, is being referenced now instead of its `host:port` being copied by hand).

**Interfaces:**
- Consumes: `store.json`'s deterministic policy id, computed by the same `uuid.NewSHA1(policyIDNamespace, []byte(filepath.Join(policyType, filepath.Base(filePath))))` `parsePolicyFile` (`src/cmd/policy-server/policy.go`) uses at load time, with `policyIDNamespace = uuid.MustParse("6f1c3a2e-8b4d-4e11-9a7c-2d5f8e0b1c34")` (same file).

- [ ] **Step 1: Recompute `store.json`'s id authoritatively**

Run:

```bash
cd /home/alex/miniprotector/src && cat > /tmp/store_id.go <<'EOF'
package main

import (
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
)

func main() {
	ns := uuid.MustParse("6f1c3a2e-8b4d-4e11-9a7c-2d5f8e0b1c34")
	id := uuid.NewSHA1(ns, []byte(filepath.Join("storage", "store.json")))
	fmt.Println(id.String())
}
EOF
go run /tmp/store_id.go
rm /tmp/store_id.go
```

Expected output: `93dd1442-461e-571f-95f6-21a5022c7af5` — this is `store.json`'s policy id (the same computation `parsePolicyFile` runs whenever `policy-server` loads it). If the output differs, use whatever value this command actually prints in the next step instead of the value below — it's the authoritative one.

- [ ] **Step 2: Rewrite the three demo backup fixtures**

In `demo/policy-server/policies/backup/audit-logs.json`, `demo/policy-server/policies/backup/database-backup.json`, and `demo/policy-server/policies/backup/webserver-backup.json`, replace this line (present verbatim, identically, in all three files):

```json
  "destination": "store:8080"
```

with:

```json
  "storage_policy_id": "93dd1442-461e-571f-95f6-21a5022c7af5"
```

(Using whichever id Step 1 actually printed.) Every other line in all three files is unchanged.

- [ ] **Step 3: Verify the migrated fixtures actually load and resolve**

Add a temporary one-off test that points a scratch `Cache` at the real demo directory, run it, then delete it — it must not be committed:

```bash
cd /home/alex/miniprotector/src/cmd/policy-server && cat > /tmp/demo_verify_test.go <<'EOF'
package main

import (
	"testing"
)

func TestDemoFixturesLoadAndResolve(t *testing.T) {
	c := NewCache()
	if err := c.Reload("/home/alex/miniprotector/demo/policy-server/policies", testLogger()); err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	for _, p := range c.Policies() {
		bp, ok := p.(*BackupPolicy)
		if !ok {
			continue
		}
		dest, ok := c.ResolveDestination(bp.StoragePolicyID)
		if !ok {
			t.Errorf("%s: storage_policy_id %q did not resolve", bp.Meta().Name, bp.StoragePolicyID)
			continue
		}
		if dest != "store:8080" {
			t.Errorf("%s: resolved destination %q, want \"store:8080\"", bp.Meta().Name, dest)
		}
	}
}
EOF
cp /tmp/demo_verify_test.go ./zz_demo_verify_test.go
go test -run TestDemoFixturesLoadAndResolve -v ./...
rm ./zz_demo_verify_test.go /tmp/demo_verify_test.go
```

Expected: the test passes, with one subtest-equivalent check per migrated backup policy (`audit-logs`, `database-backup`, `webserver-backup`), each resolving to `store:8080`. The scratch test file is deleted immediately after — it must not be committed.

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector
git add demo/policy-server/policies/backup/audit-logs.json demo/policy-server/policies/backup/database-backup.json demo/policy-server/policies/backup/webserver-backup.json
git commit -m "$(cat <<'EOF'
chore(demo): migrate backup policy fixtures to storage_policy_id

Replaces each fixture's raw "destination": "store:8080" with a
storage_policy_id referencing storage/store.json's own deterministic id --
resolves to the same "store:8080" at read time, now derived instead of
hand-copied.
EOF
)"
```

---

## Task 5: Documentation and changelog

**Files:**
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/api-server.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `CHANGELOG.md`

(`README.md` is unaffected — `docs/protocols/policy-server.md` is already cross-linked from its Documentation section; no new doc file is being added. `docs/ARCHITECTURE.md` is unaffected — no topology/data-flow change.)

- [ ] **Step 1: `docs/protocols/policy-server.md` — proto block**

Replace the `message Policy { ... }` block:

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string id = 8;
  ClientFilters client_filters = 9;
  string type = 10;
  reserved 11; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 12;
  string config = 13;
  google.protobuf.Timestamp disabled_at = 14;
}
```

with:

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7; // derived, read-only -- see below
  string id = 8;
  ClientFilters client_filters = 9;
  string type = 10;
  reserved 11; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 12;
  string config = 13;
  google.protobuf.Timestamp disabled_at = 14;
  string storage_policy_id = 15; // backup policy only, required
}
```

Replace the `message CreatePolicyRequest { ... }` block:

```proto
message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  string destination = 6;
  string type = 7;
  reserved 8; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
}
```

with:

```proto
message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  reserved 6; reserved "destination"; // removed -- never itself writable, see below
  string type = 7;
  reserved 8; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
  string storage_policy_id = 12; // backup policy only, required
}
```

Replace the `message UpdatePolicyRequest { ... }` block:

```proto
message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  reserved 8; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
}
```

with:

```proto
message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  reserved 7; reserved "destination"; // removed -- never itself writable, see below
  reserved 8; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
  string storage_policy_id = 12; // backup policy only, required
}
```

- [ ] **Step 2: `docs/protocols/policy-server.md` — Behavior bullets**

Replace:

```markdown
- `port`/`config` are only meaningful on a `"storage"`-typed policy -- unset/zero on a
  `"backup"`-typed one, and vice versa for `object_filters`/`rpo`/`backup_window`/`destination`.
```

with:

```markdown
- `port`/`config` are only meaningful on a `"storage"`-typed policy -- unset/zero on a
  `"backup"`-typed one, and vice versa for `object_filters`/`rpo`/`backup_window`/`storage_policy_id`.
  `destination` is never itself a settable field on either type -- see below.
```

Replace:

```markdown
- `rpo` and `backup_window` are opaque, pass-through strings — `policy-server` never parses or
  evaluates either. `destination` is likewise opaque, pass-through data — `policy-server` never
  validates or connects to it.
```

with:

```markdown
- `rpo` and `backup_window` are opaque, pass-through strings — `policy-server` never parses or
  evaluates either. `destination` is derived, read-only: a `"backup"` policy instead carries
  `storage_policy_id`, a required reference to a `"storage"`-typed policy's `id`.
  `GetPolicies`/`ListPolicies`/`CreatePolicy`/`UpdatePolicy` all resolve it live to that storage
  policy's `client_filters.hostnames[0]:port` before responding, so `destination` always reflects
  the referenced storage policy's *current* settings, never a stale copy. It's left unset if the
  reference doesn't resolve (an id that doesn't exist, or no longer names a storage policy) --
  reachable only by hand-editing policy files outside the write RPCs, since `DeletePolicy` refuses
  to remove a storage policy still referenced by any backup policy.
```

Replace:

```markdown
- `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy` are the admin surface `api-server`
  proxies for browsing and editing the full policy set — never called by a mesh node. Unlike
  `GetPolicies`, `ListPolicies`'s response (and `Create`/`UpdatePolicy`'s echoed-back result)
  includes `client_filters`. `Create`/`UpdatePolicy` validate the same way `parsePolicyFile` does
  (non-empty `metadata.name`, syntactically valid glob patterns) before writing anything; a write
  that fails validation returns `INVALID_ARGUMENT` and touches no file. `Update`/`Delete` address a
  policy by its `id`; `Update` keeps the on-disk filename (and therefore the `id`) unchanged,
  overwriting only the file's content. Every write reloads `policy-server`'s own in-memory cache
  synchronously before responding, bypassing the `.changed` sentinel entirely — that remains solely
  the mechanism for an operator's own manual, possibly multi-file, batch edits.
```

with:

```markdown
- `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy` are the admin surface `api-server`
  proxies for browsing and editing the full policy set — never called by a mesh node. Unlike
  `GetPolicies`, `ListPolicies`'s response (and `Create`/`UpdatePolicy`'s echoed-back result)
  includes `client_filters`. `Create`/`UpdatePolicy` validate the same way `parsePolicyFile` does
  (non-empty `metadata.name`, syntactically valid glob patterns) before writing anything; a write
  that fails validation returns `INVALID_ARGUMENT` and touches no file. For a `"backup"` policy,
  both also require `storage_policy_id` to be non-empty and to name a currently-loaded `"storage"`
  policy, checked against the live cache at write time — something `Validate()` alone can't check,
  since it never sees the rest of the loaded set. `DeletePolicy` on a `"storage"` policy fails with
  `INVALID_ARGUMENT`, naming the offending policies, if any `"backup"` policy still references it.
  `Update`/`Delete` address a policy by its `id`; `Update` keeps the on-disk filename (and therefore
  the `id`) unchanged, overwriting only the file's content. Every write reloads `policy-server`'s own
  in-memory cache synchronously before responding, bypassing the `.changed` sentinel entirely — that
  remains solely the mechanism for an operator's own manual, possibly multi-file, batch edits.
```

- [ ] **Step 3: `docs/protocols/policy-server.md` — See Also**

Add a new bullet to the `## See Also` list, after the `[Design: Storage Policy Type]` line:

```markdown
- [Design: link backup policies to storage policies by id](../superpowers/specs/2026-08-03-backup-policy-storage-link-design.md)
```

- [ ] **Step 4: `docs/components/policy-server.md` — schema paragraph**

Replace:

```markdown
A `"backup"` policy describes what to back up and where: `object_filters`, `rpo`, `backup_window`,
`destination`. A `"storage"` policy describes how a future storage server should be configured:
`port` and an opaque `config` JSON blob `policy-server` validates is well-formed but never
interprets. Targeting which node runs it is `client_filters` — the same mechanism a backup policy
already uses — not a field specific to this type; see
[Design: agent storage-policy supervision](../superpowers/specs/2026-07-28-agent-storage-supervision-design.md),
which is the first actual consumer of `storage`-typed policies.
```

with:

```markdown
A `"backup"` policy describes what to back up and, via `storage_policy_id`, where: `object_filters`,
`rpo`, `backup_window`, and a required reference to a `"storage"`-typed policy's `id`. Its
`destination` (the resolved `host:port` `bwfs` target) is never itself stored or settable — it's
computed live from the referenced storage policy every time `policy-server` returns the policy, so
editing that storage policy's `client_filters.hostnames`/`port` updates every backup policy linked to
it with no re-save needed. A `"storage"` policy describes how a future storage server should be
configured: `port` and an opaque `config` JSON blob `policy-server` validates is well-formed but
never interprets. Targeting which node runs it is `client_filters` — the same mechanism a backup
policy already uses — not a field specific to this type; see
[Design: agent storage-policy supervision](../superpowers/specs/2026-07-28-agent-storage-supervision-design.md),
which is the first actual consumer of `storage`-typed policies. See
[Design: link backup policies to storage policies by id](../superpowers/specs/2026-08-03-backup-policy-storage-link-design.md).
```

- [ ] **Step 5: `docs/components/policy-server.md` — Policy files and hot reload paragraph**

Replace:

```markdown
duration string, e.g. `"24h"`), `backup_window` (a list of cron expressions, e.g.
`["0 2 * * *", "0 20 * * *"]`), and `destination` (a `host:port` string, the target `bwfs` for this
policy's backups). A `"storage"` policy instead has `port` and `config` (an opaque JSON object,
```

with:

```markdown
duration string, e.g. `"24h"`), `backup_window` (a list of cron expressions, e.g.
`["0 2 * * *", "0 20 * * *"]`), and `storage_policy_id` (the `id` of a `"storage"`-typed policy --
required). `destination`, unlike every other backup-policy field, is never read from the on-disk
JSON: it's computed at read time from the storage policy `storage_policy_id` names. A `"storage"`
policy instead has `port` and `config` (an opaque JSON object,
```

- [ ] **Step 6: `docs/components/policy-server.md` — writes paragraph**

Replace:

```markdown
Writes made through `CreatePolicy`/`UpdatePolicy`/`DeletePolicy` bypass this sentinel-and-fsnotify
path entirely: each validates its input, atomically writes (or removes) the affected file, then
calls the same `Reload` directly, in-process, before the RPC responds. An operator hand-editing
files on disk and the write RPCs can coexist — both funnel through the same `Reload`/validation
logic — but there's no locking between them beyond the atomic-rename write itself.
```

with:

```markdown
Writes made through `CreatePolicy`/`UpdatePolicy`/`DeletePolicy` bypass this sentinel-and-fsnotify
path entirely: each validates its input, atomically writes (or removes) the affected file, then
calls the same `Reload` directly, in-process, before the RPC responds. An operator hand-editing
files on disk and the write RPCs can coexist — both funnel through the same `Reload`/validation
logic — but there's no locking between them beyond the atomic-rename write itself. Deleting a
`"storage"` policy is rejected if any `"backup"` policy's `storage_policy_id` still points at it —
an operator must repoint or delete those backup policies first.
```

- [ ] **Step 7: `docs/components/api-server.md`**

Replace:

```markdown
`policy-server` also supports a `"storage"` policy type (`port`/`config`).
`GET /policies` accepts an optional `?type=backup|storage` query parameter to filter by type;
without it, every policy of every type is returned, each with `port`/`config` populated
in the response DTO when applicable (zero for a `"backup"`-typed policy, and vice versa for
`rpo`/`destination`/`object_filters`). Creating or updating a storage policy uses a separate pair of
endpoints, `POST /storage-policies` and `PUT /storage-policies/{id}`, since a storage policy's input
shape (`port`/`config`) shares nothing with a backup policy's
(`object_filters`/`rpo`/`backup_window`/`destination`) beyond `name`/`client_filters` — which is also
how a storage policy targets a node (there is no separate `hostname` field; set
`client_filters.hostnames` the same way a backup policy would). `GET
/policies/{id}` and `DELETE /policies/{id}` are shared across both types — both operations are
already type-agnostic, looking a policy up or removing it by `id` alone.

`POST /policies/adhoc` creates a one-time backup policy from the same fields as an ordinary create
(`name`/`client_filters`/`object_filters`/`destination`) — `api-server` computes `backup_window`
(every minute), `rpo`, and `disabled_at` itself from the `AdhocPolicyTimeoutSec` config value, so a
caller never composes those three fields by hand to get a "run once on every matched node, then
expire" policy. See [Design: adhoc policy endpoint](../superpowers/specs/2026-08-02-adhoc-policy-endpoint-design.md).
```

with:

```markdown
`policy-server` also supports a `"storage"` policy type (`port`/`config`).
`GET /policies` accepts an optional `?type=backup|storage` query parameter to filter by type;
without it, every policy of every type is returned, each with `port`/`config` populated
in the response DTO when applicable (zero for a `"backup"`-typed policy, and vice versa for
`rpo`/`storage_policy_id`/`object_filters`). A `"backup"` policy's `destination` in the response DTO
is always derived by `policy-server` from its `storage_policy_id` — it's never itself part of the
create/update input. Creating or updating a storage policy uses a separate pair of
endpoints, `POST /storage-policies` and `PUT /storage-policies/{id}`, since a storage policy's input
shape (`port`/`config`) shares nothing with a backup policy's
(`object_filters`/`rpo`/`backup_window`/`storage_policy_id`) beyond `name`/`client_filters` — which is
also how a storage policy targets a node (there is no separate `hostname` field; set
`client_filters.hostnames` the same way a backup policy would). `GET
/policies/{id}` and `DELETE /policies/{id}` are shared across both types — both operations are
already type-agnostic, looking a policy up or removing it by `id` alone.

`POST /policies/adhoc` creates a one-time backup policy from the same fields as an ordinary create
(`name`/`client_filters`/`object_filters`/`storage_policy_id`) — `api-server` computes `backup_window`
(every minute), `rpo`, and `disabled_at` itself from the `AdhocPolicyTimeoutSec` config value, so a
caller never composes those three fields by hand to get a "run once on every matched node, then
expire" policy. See [Design: adhoc policy endpoint](../superpowers/specs/2026-08-02-adhoc-policy-endpoint-design.md)
and [Design: link backup policies to storage policies by id](../superpowers/specs/2026-08-03-backup-policy-storage-link-design.md).
```

- [ ] **Step 8: `docs/api/rest-v1.md` — `GET /api/v1/policies` example**

Replace the JSON example's:

```json
      "rpo": "24h",
      "backup_window": ["0 2 * * *", "0 20 * * *"],
      "destination": "bwfs-east.internal:8080",
      "type": "backup",
```

with:

```json
      "rpo": "24h",
      "backup_window": ["0 2 * * *", "0 20 * * *"],
      "destination": "bwfs-east.internal:8080",
      "storage_policy_id": "b2c3d4e5-...",
      "type": "backup",
```

- [ ] **Step 9: `docs/api/rest-v1.md` — `POST /api/v1/policies` body and prose**

Replace:

```json
{
  "name": "nightly-web-backup",
  "client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
  "object_filters": [{"path": "/var/www", "include": ["*.html"], "exclude": ["*.tmp"]}],
  "rpo": "24h",
  "backup_window": ["0 2 * * *"],
  "destination": "bwfs-east.internal:8080"
}
```

with:

```json
{
  "name": "nightly-web-backup",
  "client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
  "object_filters": [{"path": "/var/www", "include": ["*.html"], "exclude": ["*.tmp"]}],
  "rpo": "24h",
  "backup_window": ["0 2 * * *"],
  "storage_policy_id": "b2c3d4e5-..."
}
```

Replace the following prose:

```markdown
`201` with the created policy (including its server-assigned `id` and each object filter's `id`) on
success. `400` if `name` is empty or slugifies to nothing (no alphanumeric characters), or any
`include`/`exclude`/hostname entry isn't a syntactically valid glob pattern — no file is written
when validation fails.
```

with:

```markdown
`201` with the created policy (including its server-assigned `id` and each object filter's `id`) on
success. `400` if `name` is empty or slugifies to nothing (no alphanumeric characters), any
`include`/`exclude`/hostname entry isn't a syntactically valid glob pattern, `storage_policy_id` is
empty, or `storage_policy_id` doesn't name an existing storage policy — no file is written when
validation fails. The response's `destination` is always derived from `storage_policy_id`, never
something this body sets directly.
```

- [ ] **Step 10: `docs/api/rest-v1.md` — `POST /api/v1/policies/adhoc` body**

Replace:

```json
{
  "name": "web-emergency",
  "client_filters": {"hostnames": ["web-*"]},
  "object_filters": [{"path": "/var/www"}],
  "destination": "bwfs-east.internal:8080"
}
```

with:

```json
{
  "name": "web-emergency",
  "client_filters": {"hostnames": ["web-*"]},
  "object_filters": [{"path": "/var/www"}],
  "storage_policy_id": "b2c3d4e5-..."
}
```

- [ ] **Step 11: `CHANGELOG.md`**

Add a new dated section at the top, right after the `## 2026-08-02 —` entries and before any older ones (match the file's existing "most recent first" ordering and heading style: `## YYYY-MM-DD — <component>: <short summary>` followed by one prose paragraph):

```markdown
## 2026-08-03 — policy-server/api-server: link backup policies to storage policies by id

A backup policy's `destination` (the target `bwfs`, `"host:port"`) is no longer typed in by hand — it's now derived live from a required `storage_policy_id` reference to a storage policy, resolved to that storage policy's `client_filters.hostnames[0]:port` on every read. This removes the drift risk of a free-text destination silently going stale after a storage policy's hostname or port changes, and gives the (separately planned) web form a real value to select from instead of guessing via string-matching. `CreatePolicy`/`UpdatePolicy` now require `storage_policy_id` to reference an existing storage policy; `DeletePolicy` refuses to remove a storage policy still referenced by any backup policy. This is a breaking change: `destination` is no longer accepted as create/update input (for either policy type), and every on-disk backup policy JSON file needs a `storage_policy_id` — the three demo fixtures under `demo/policy-server/policies/backup/` are migrated as part of this change; no backward-compatibility path is provided for hand-maintained files elsewhere.
```

- [ ] **Step 12: Commit**

```bash
cd /home/alex/miniprotector
git add docs/protocols/policy-server.md docs/components/policy-server.md docs/components/api-server.md docs/api/rest-v1.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: document storage_policy_id link and derived destination

Updates the policy-server protocol/component docs, api-server component doc,
and REST v1 reference for storage_policy_id replacing destination as
backup-policy input; adds the CHANGELOG entry for this feature branch.
EOF
)"
```

---

## Final check

- [ ] Run the full suite one more time end to end: `cd /home/alex/miniprotector/src && go build ./... && go vet ./... && go test ./...` — expect all green.
- [ ] Run `git log --oneline -6` and confirm five commits landed in order: proto, policy-server, api-server, demo fixtures, docs.
- [ ] Confirm `git status` is clean (no stray scratch files like `/tmp/idcalc.go`, `zz_demo_verify_test.go` left in the working tree — the plan's own steps delete these, but double-check).
