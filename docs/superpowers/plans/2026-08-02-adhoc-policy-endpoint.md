# Adhoc Policy Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /api/v1/policies/adhoc` to `api-server` — a convenience endpoint that creates a one-time backup policy by composing `backup_window`/`rpo`/`disabled_at` from a configured timeout, instead of requiring the caller to hand-craft those three fields.

**Architecture:** Pure `api-server` addition — no `.proto` or `policy-server` changes, since `CreatePolicyRequest` already carries `rpo`/`backup_window`/`disabled_at` from the prior `disabled_at` design. The new handler decodes the same `policyInput` shape as `POST /api/v1/policies`, ignores any caller-supplied `rpo`/`backup_window`/`disabled_at`, and always sets `backup_window = ["* * * * *"]`, `rpo = <configured timeout>`, `disabled_at = now + <configured timeout>`, with `adhoc_` prefixed onto the name. Along the way, this plan also closes a round-trip gap the prior `disabled_at` design's own docs flagged as a prerequisite: `PUT /api/v1/policies/{id}` and `PUT /api/v1/storage-policies/{id}` currently always clear a policy's `disabled_at` on any edit, because the REST input DTO never exposed it.

**Tech Stack:** Go, `net/http` (stdlib `ServeMux`), gRPC (`google.golang.org/protobuf/types/known/timestamppb`), `testify` (`assert`/`require`).

## Global Constraints

- Config keys are parsed case-sensitively against the exact Go struct field name (see `src/common/config/config.go`'s switch statement) — the new key is `AdhocPolicyTimeoutSec`, not a snake_case variant.
- No new CLI flag — the timeout is config-only, matching how `agent` reads `BackupWindowGraceSec`/`PolicyFetchIntervalSec` with no corresponding flag.
- Every REST endpoint change must be reflected in `docs/components/api-server.md` and `docs/api/rest-v1.md`, and a `CHANGELOG.md` entry is required before this branch merges to `main` (per `.claude/CLAUDE.md`).
- Follow this codebase's existing test doubles/patterns exactly: `fakePolicyServiceClient` in `src/cmd/api-server/policies_test.go`, `newServer(cm, catalog, policy, logger)` (pass `nil` for unused clients), `testLogger()` from `clients_test.go`.

---

## Task 1: `AdhocPolicyTimeoutSec` config field

**Files:**
- Modify: `src/common/config/config.go:119-120` (struct field), `:165` (default value), `:399-405` (switch-case parsing, inserted after the `MaxConcurrentBackupJobs` case)
- Test: `src/common/config/config_test.go` (append)

**Interfaces:**
- Produces: `config.Config.AdhocPolicyTimeoutSec int` — read by Task 4's `main.go` wiring. Default `3600` when the key is absent from `local.conf`.

- [ ] **Step 1: Write the failing tests**

Append to `src/common/config/config_test.go`:

```go
func TestParseConfig_AdhocPolicyTimeoutSecDefaultsTo3600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlog_dir=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 3600, conf.AdhocPolicyTimeoutSec)
}

func TestParseConfig_AdhocPolicyTimeoutSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\nAdhocPolicyTimeoutSec=1800\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 1800, conf.AdhocPolicyTimeoutSec)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./src/common/config/... -run TestParseConfig_AdhocPolicyTimeoutSec -v`
Expected: FAIL to compile — `conf.AdhocPolicyTimeoutSec undefined (type *Config has no field or method AdhocPolicyTimeoutSec)`.

- [ ] **Step 3: Add the field, default, and parsing case**

In `src/common/config/config.go`, add the struct field right before the closing `}` of `Config` (after `ClientManagerAdminAPIHost string`, line 119):

```go
	ClientManagerAdminAPIHost        string
	AdhocPolicyTimeoutSec            int
}
```

Add the default in `ParseConfig`'s initial `config := &Config{...}` literal, right after `ConnectionTimeOutSec: 30,` (line 165):

```go
		ConnectionTimeOutSec:             30,
		AdhocPolicyTimeoutSec:            3600,
```

Add the parsing case right after the `MaxConcurrentBackupJobs` case (before `default:`, around line 405):

```go
		case "MaxConcurrentBackupJobs":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid MaxConcurrentBackupJobs value at line %d: %s", lineNum, value)
			}
			config.MaxConcurrentBackupJobs = number
			foundFields["MaxConcurrentBackupJobs"] = true
		case "AdhocPolicyTimeoutSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid AdhocPolicyTimeoutSec value at line %d: %s", lineNum, value)
			}
			config.AdhocPolicyTimeoutSec = number
			foundFields["AdhocPolicyTimeoutSec"] = true
		default:
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./src/common/config/... -v`
Expected: PASS, including all pre-existing config tests (unaffected).

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add AdhocPolicyTimeoutSec, default 3600"
```

---

## Task 2: `policyDTO` gains `disabled_at` (output)

**Files:**
- Modify: `src/cmd/api-server/policies.go:23-36` (`policyDTO` struct), `:38-60` (`toPolicyDTO`)
- Test: `src/cmd/api-server/policies_test.go` (append)

**Interfaces:**
- Produces: `policyDTO.DisabledAt int64` (`json:"disabled_at,omitempty"`, unix seconds, zero/omitted when unset). Consumed by Task 4's adhoc-endpoint tests to verify the response surfaces the computed expiry.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/api-server/policies_test.go`:

```go
func TestToPolicyDTO_IncludesDisabledAtWhenSet(t *testing.T) {
	p := &pb.Policy{
		Id:         "p1",
		Name:       "adhoc-x",
		DisabledAt: timestamppb.New(time.Unix(1754000000, 0)),
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, int64(1754000000), dto.DisabledAt)
}

func TestToPolicyDTO_OmitsDisabledAtWhenUnset(t *testing.T) {
	p := &pb.Policy{Id: "p1", Name: "nightly"}

	dto := toPolicyDTO(p)

	assert.Equal(t, int64(0), dto.DisabledAt)
	data, err := json.Marshal(dto)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "disabled_at")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./src/cmd/api-server/... -run TestToPolicyDTO_.*DisabledAt -v`
Expected: FAIL — `dto.DisabledAt undefined (type policyDTO has no field or method DisabledAt)`.

- [ ] **Step 3: Add the field and populate it**

In `src/cmd/api-server/policies.go`, add to `policyDTO` (after `Config string`):

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

Update `toPolicyDTO` to set it when present:

```go
func toPolicyDTO(p *pb.Policy) policyDTO {
	objectFilters := make([]objectFilterDTO, len(p.GetObjectFilters()))
	for i, f := range p.GetObjectFilters() {
		objectFilters[i] = objectFilterDTO{ID: f.GetId(), Path: f.GetPath(), Include: f.GetInclude(), Exclude: f.GetExclude()}
	}
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
	if p.GetDisabledAt() != nil {
		dto.DisabledAt = p.GetDisabledAt().AsTime().Unix()
	}
	return dto
}
```

No import changes needed in `policies.go` for this task — `policies_test.go` already imports `timestamppb` (see its existing import block). Task 3 adds `time`/`timestamppb` to `policies.go` itself, since that's where the first non-test use appears.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./src/cmd/api-server/... -v`
Expected: PASS, including every pre-existing `policies_test.go` test (`toPolicyDTO`'s existing callers are unaffected — `DisabledAt` defaults to zero/omitted).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "feat(api-server): expose disabled_at on policy REST responses"
```

---

## Task 3: `disabled_at` round-trips through `PUT` (fixes a flagged regression)

**Problem being fixed:** `docs/api/rest-v1.md`'s `PUT /api/v1/policies/{id}` and `PUT /api/v1/storage-policies/{id}` sections already document that any edit through these endpoints silently clears `disabled_at`, because `policyInput`/`storagePolicyInput` never expose it — `handleUpdatePolicy`/`handleUpdateStoragePolicy` always send `UpdatePolicyRequest`/`... ` with `DisabledAt` unset. Those docs call this out as something that "must be fixed before a future adhoc-backup endpoint is built on `disabled_at`" — exactly the endpoint Task 4 adds. Without this fix, an adhoc policy edited via `PUT` before it expires (e.g. a UI tweak to its `client_filters`) would unexpectedly become permanent.

**Files:**
- Modify: `src/cmd/api-server/policies.go` — `policyInput` struct (:99-106), `storagePolicyInput` struct (:175-180), `handleCreatePolicy` (:128-149), `handleUpdatePolicy` (:151-173), `handleCreateStoragePolicy` (:190-209), `handleUpdateStoragePolicy` (:211-231); new `disabledAtToProto` helper
- Test: `src/cmd/api-server/policies_test.go` (append)

**Interfaces:**
- Consumes: nothing new from earlier tasks.
- Produces: `disabledAtToProto(unixSeconds int64) *timestamppb.Timestamp` — a private helper, also usable (but not required) by Task 4. `policyInput.DisabledAt`/`storagePolicyInput.DisabledAt int64` (`json:"disabled_at,omitempty"`) — input fields on both create and update paths for backup and storage policies.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/api-server/policies_test.go`:

```go
func TestHandleCreatePolicy_SetsDisabledAtWhenProvided(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080", "disabled_at": 1754000000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	require.NotNil(t, fake.lastCreateReq.GetDisabledAt())
	assert.Equal(t, int64(1754000000), fake.lastCreateReq.GetDisabledAt().AsTime().Unix())
}

func TestHandleCreatePolicy_OmittedDisabledAtLeavesItUnset(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Nil(t, fake.lastCreateReq.GetDisabledAt())
}

func TestHandleUpdatePolicy_EchoesDisabledAtBack(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080", "disabled_at": 1754000000}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/p1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	require.NotNil(t, fake.lastUpdateReq.GetDisabledAt())
	assert.Equal(t, int64(1754000000), fake.lastUpdateReq.GetDisabledAt().AsTime().Unix())
}

func TestHandleUpdatePolicy_OmittedDisabledAtClearsIt(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/p1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	assert.Nil(t, fake.lastUpdateReq.GetDisabledAt(), "full-replace semantics: omitting disabled_at clears it, same as every other optional field")
}

func TestHandleUpdateStoragePolicy_EchoesDisabledAtBack(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{Id: "s1", Type: "storage"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "east-1-storage",
		"client_filters": {"hostnames": ["storage-east-1.internal"], "labels": {}},
		"port": 9400,
		"config": "{}",
		"disabled_at": 1754000000
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage-policies/s1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	require.NotNil(t, fake.lastUpdateReq.GetDisabledAt())
	assert.Equal(t, int64(1754000000), fake.lastUpdateReq.GetDisabledAt().AsTime().Unix())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./src/cmd/api-server/... -run 'TestHandle(Create|Update)(Storage)?Policy_.*DisabledAt' -v`
Expected: FAIL — `fake.lastCreateReq.GetDisabledAt undefined` is not the failure (that method already exists on the proto); the actual failures are assertion failures, since today's requests always send a nil `DisabledAt` regardless of the input body (`TestHandleCreatePolicy_SetsDisabledAtWhenProvided` and the two "echoes back" tests fail; the two "omitted"/"unset" tests already pass by coincidence since nothing sets `DisabledAt` today).

- [ ] **Step 3: Add the input field and thread it through all four handlers**

In `src/cmd/api-server/policies.go`, add the import needed for the new helper:

```go
import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)
```

Add the input fields:

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

```go
type storagePolicyInput struct {
	Name          string           `json:"name"`
	ClientFilters clientFiltersDTO `json:"client_filters"`
	Port          int32            `json:"port"`
	Config        string           `json:"config"`
	DisabledAt    int64            `json:"disabled_at,omitempty"`
}
```

Add the helper (near `toProtoObjectFiltersInput`):

```go
// disabledAtToProto converts an optional unix-seconds REST input value to
// a proto Timestamp, treating 0 (the zero value of an omitted/absent
// field) as "not set" -- mirrors write.go's disabledAtFromProto on the
// policy-server side, which treats a nil Timestamp the same way.
func disabledAtToProto(unixSeconds int64) *timestamppb.Timestamp {
	if unixSeconds == 0 {
		return nil
	}
	return timestamppb.New(time.Unix(unixSeconds, 0))
}
```

Add `DisabledAt: disabledAtToProto(in.DisabledAt),` to the `pb.CreatePolicyRequest` literal in `handleCreatePolicy`:

```go
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		Type:          "backup",
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters: toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:           in.RPO,
		BackupWindow:  in.BackupWindow,
		Destination:   in.Destination,
		DisabledAt:    disabledAtToProto(in.DisabledAt),
	})
```

Add the same field to the `pb.UpdatePolicyRequest` literal in `handleUpdatePolicy`:

```go
	resp, err := s.policy.UpdatePolicy(r.Context(), &pb.UpdatePolicyRequest{
		Id:            id,
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters: toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:           in.RPO,
		BackupWindow:  in.BackupWindow,
		Destination:   in.Destination,
		DisabledAt:    disabledAtToProto(in.DisabledAt),
	})
```

Add it to `handleCreateStoragePolicy`'s `pb.CreatePolicyRequest` literal:

```go
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		Type:          "storage",
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Port:          in.Port,
		Config:        in.Config,
		DisabledAt:    disabledAtToProto(in.DisabledAt),
	})
```

And to `handleUpdateStoragePolicy`'s `pb.UpdatePolicyRequest` literal:

```go
	resp, err := s.policy.UpdatePolicy(r.Context(), &pb.UpdatePolicyRequest{
		Id:            id,
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Port:          in.Port,
		Config:        in.Config,
		DisabledAt:    disabledAtToProto(in.DisabledAt),
	})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./src/cmd/api-server/... -v`
Expected: PASS, including every pre-existing test in the package.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "fix(api-server): round-trip disabled_at through policy create/update"
```

---

## Task 4: `POST /api/v1/policies/adhoc` endpoint

**Files:**
- Modify: `src/cmd/api-server/server.go` — `server` struct (:47-54), `registerRoutes` (:80-84 region), imports
- Modify: `src/cmd/api-server/policies.go` — new `handleCreateAdhocPolicy`
- Modify: `src/cmd/api-server/main.go` — wire `conf.AdhocPolicyTimeoutSec` onto the server after construction (:95 region)
- Test: `src/cmd/api-server/policies_test.go` (append)

**Interfaces:**
- Consumes: `config.Config.AdhocPolicyTimeoutSec` (Task 1), `toPolicyDTO`'s `DisabledAt` field (Task 2), `disabledAtToProto`/`policyInput`/`decodePolicyInput` (Task 3, reused as-is — the adhoc handler never reads `in.RPO`/`in.BackupWindow`/`in.DisabledAt`).
- Produces: `server.adhocPolicyTimeout time.Duration` field; `POST /api/v1/policies/adhoc` route.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/api-server/policies_test.go`:

```go
func TestHandleCreateAdhocPolicy_ComposesFieldsAndPrefixesName(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1", Name: "adhoc_web-emergency", Type: "backup"}}
	srv := newServer(nil, nil, fake, testLogger())
	srv.adhocPolicyTimeout = time.Hour
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	before := time.Now()
	body := strings.NewReader(`{
		"name": "web-emergency",
		"client_filters": {"hostnames": ["web-*"]},
		"object_filters": [{"path": "/var/www"}],
		"destination": "bwfs:8080"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies/adhoc", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "adhoc_web-emergency", fake.lastCreateReq.GetName())
	assert.Equal(t, "backup", fake.lastCreateReq.GetType())
	assert.Equal(t, []string{"* * * * *"}, fake.lastCreateReq.GetBackupWindow())
	assert.Equal(t, "1h0m0s", fake.lastCreateReq.GetRpo())
	assert.Equal(t, []string{"web-*"}, fake.lastCreateReq.GetClientFilters().GetHostnames())
	require.Len(t, fake.lastCreateReq.GetObjectFilters(), 1)
	assert.Equal(t, "/var/www", fake.lastCreateReq.GetObjectFilters()[0].GetPath())
	assert.Equal(t, "bwfs:8080", fake.lastCreateReq.GetDestination())
	require.NotNil(t, fake.lastCreateReq.GetDisabledAt())
	assert.WithinDuration(t, before.Add(time.Hour), fake.lastCreateReq.GetDisabledAt().AsTime(), 5*time.Second)
}

func TestHandleCreateAdhocPolicy_IgnoresCallerSuppliedScheduleFields(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	srv.adhocPolicyTimeout = time.Hour
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web-emergency",
		"destination": "bwfs:8080",
		"rpo": "5m",
		"backup_window": ["0 2 * * *"],
		"disabled_at": 1
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies/adhoc", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "1h0m0s", fake.lastCreateReq.GetRpo())
	assert.Equal(t, []string{"* * * * *"}, fake.lastCreateReq.GetBackupWindow())
	assert.NotEqual(t, int64(1), fake.lastCreateReq.GetDisabledAt().AsTime().Unix())
}

func TestHandleCreateAdhocPolicy_ReturnsPolicyDTOWithDisabledAt(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "p1", Name: "adhoc_web-emergency", Type: "backup",
		DisabledAt: timestamppb.New(time.Unix(1754000000, 0)),
	}}
	srv := newServer(nil, nil, fake, testLogger())
	srv.adhocPolicyTimeout = time.Hour
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies/adhoc", strings.NewReader(`{"name": "web-emergency", "destination": "bwfs:8080"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, float64(1754000000), respBody["disabled_at"])
}

func TestHandleCreateAdhocPolicy_MalformedJSONReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{}
	srv := newServer(nil, nil, fake, testLogger())
	srv.adhocPolicyTimeout = time.Hour
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies/adhoc", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, fake.lastCreateReq)
}

func TestHandleCreateAdhocPolicy_BackendValidationErrorReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{createErr: status.Error(codes.InvalidArgument, "metadata.name is required")}
	srv := newServer(nil, nil, fake, testLogger())
	srv.adhocPolicyTimeout = time.Hour
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies/adhoc", strings.NewReader(`{"name": "x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./src/cmd/api-server/... -run TestHandleCreateAdhocPolicy -v`
Expected: FAIL to compile — `srv.adhocPolicyTimeout undefined (type *server has no field or method adhocPolicyTimeout)`, and no route exists for `/api/v1/policies/adhoc` (would 404 once it compiles).

- [ ] **Step 3: Add the server field, route, and handler**

In `src/cmd/api-server/server.go`, add `"time"` to the import block and add the field to `server`:

```go
import (
	"context"
	"log/slog"
	"net/http"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
)
```

```go
type server struct {
	clientManager      clientManagerClient
	clientManagerAdmin clientManagerAdminClient
	catalog            catalogQueryClient
	policy             policyServiceClient
	loki               lokiQuerier
	logger             *slog.Logger
	adhocPolicyTimeout time.Duration
}
```

Add the route right after the existing `POST /api/v1/policies` line in `registerRoutes`:

```go
	mux.HandleFunc("POST /api/v1/policies", s.handleCreatePolicy)
	mux.HandleFunc("POST /api/v1/policies/adhoc", s.handleCreateAdhocPolicy)
	mux.HandleFunc("PUT /api/v1/policies/{id}", s.handleUpdatePolicy)
```

In `src/cmd/api-server/policies.go`, add the handler after `handleCreatePolicy`:

```go
// handleCreateAdhocPolicy creates a one-time backup policy: same input
// shape as POST /api/v1/policies, but backup_window/rpo/disabled_at are
// always computed from s.adhocPolicyTimeout rather than read from the
// request body (any caller-supplied values for those three fields are
// silently ignored) -- backup_window opens every minute so the policy is
// due as soon as a matched node next polls, rpo equals the timeout so it
// fires at most once per node, and disabled_at = now+timeout removes the
// policy (pruning matched nodes' state for it) once every node has had a
// chance to receive and run it.
func (s *server) handleCreateAdhocPolicy(w http.ResponseWriter, r *http.Request) {
	in, err := decodePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          "adhoc_" + in.Name,
		Type:          "backup",
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters: toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:           s.adhocPolicyTimeout.String(),
		BackupWindow:  []string{"* * * * *"},
		Destination:   in.Destination,
		DisabledAt:    timestamppb.New(time.Now().UTC().Add(s.adhocPolicyTimeout)),
	})
	if err != nil {
		s.logger.Error("handleCreateAdhocPolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(resp))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./src/cmd/api-server/... -v`
Expected: PASS, including every pre-existing test in the package.

- [ ] **Step 5: Wire the config value in `main.go`**

In `src/cmd/api-server/main.go`, right after the `srv := newServer(...)` line (around line 95), add:

```go
	srv := newServer(pb.NewClientManagerServiceClient(cmConn), pb.NewCatalogServiceClient(catalogConn), pb.NewPolicyServiceClient(policyConn), logger)
	srv.clientManagerAdmin = pb.NewClientManagerAdminServiceClient(cmAdminConn)
	srv.loki = newCachingLokiClient(newHTTPLokiClient(lokiBaseURL, lokiHTTPClient), 10*time.Second)
	srv.adhocPolicyTimeout = time.Duration(conf.AdhocPolicyTimeoutSec) * time.Second
```

Run: `go build ./...`
Expected: builds cleanly (no dedicated test for `main.go`'s wiring, consistent with `srv.loki`/`srv.clientManagerAdmin` also being untested at this call site — a build check is the only verification available here).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/server.go src/cmd/api-server/policies.go src/cmd/api-server/main.go src/cmd/api-server/policies_test.go
git commit -m "feat(api-server): add POST /api/v1/policies/adhoc"
```

---

## Task 5: Documentation and changelog

**Files:**
- Modify: `docs/components/api-server.md` (Endpoints, Configuration Keys)
- Modify: `docs/api/rest-v1.md` (new section; update `POST`/`PUT` sections that now handle `disabled_at`)
- Modify: `CHANGELOG.md`

No test cycle — this is documentation-only, required by `.claude/CLAUDE.md`'s "Feature Changes" and "Changelog" rules before merging to `main`.

- [ ] **Step 1: Update `docs/components/api-server.md`**

In the **Endpoints** section, after the existing storage-policy paragraph (ending "...removing it by `id` alone."), add:

```markdown
`POST /policies/adhoc` creates a one-time backup policy from the same fields as an ordinary create
(`name`/`client_filters`/`object_filters`/`destination`) — `api-server` computes `backup_window`
(every minute), `rpo`, and `disabled_at` itself from the `AdhocPolicyTimeoutSec` config value, so a
caller never composes those three fields by hand to get a "run once on every matched node, then
expire" policy. See [Design: adhoc policy endpoint](../superpowers/specs/2026-08-02-adhoc-policy-endpoint-design.md).
```

In the **Configuration Keys** list, add:

```markdown
- `AdhocPolicyTimeoutSec` — how long a `POST /policies/adhoc`-created policy stays active (its `rpo`
  and how far past `now` its `disabled_at` is set) before disabling itself *(default: 3600)*
```

- [ ] **Step 2: Update `docs/api/rest-v1.md`**

In the `## POST /api/v1/policies` section, after the existing `400` sentence, add:

```markdown
An optional integer `disabled_at` (Unix seconds) may also be included; once that time passes,
`GetPolicies` stops serving the policy. Omit it (or send `0`) for a policy that's never disabled.
```

Replace the `## PUT /api/v1/policies/{id}` section's last two sentences (the "not yet exposed" warning) with:

```markdown
`disabled_at` round-trips like every other field: since this is a full replacement, an existing
`disabled_at` survives an edit only if the request echoes it back explicitly (the same way
`client_filters` already must be) — omitting it (or sending `0`) clears it.
```

Add a new section immediately after `## DELETE /api/v1/policies/{id}` (before `## POST /api/v1/storage-policies`):

````markdown
## `POST /api/v1/policies/adhoc`

Creates a one-time backup policy without composing `backup_window`/`rpo`/`disabled_at` by hand.
Body — same shape as `POST /api/v1/policies`; `rpo`/`backup_window`/`disabled_at` are accepted for
compatibility but always ignored:

```json
{
  "name": "web-emergency",
  "client_filters": {"hostnames": ["web-*"]},
  "object_filters": [{"path": "/var/www"}],
  "destination": "bwfs-east.internal:8080"
}
```

`api-server` prefixes the name with `adhoc_`, sets `backup_window` to `["* * * * *"]` (open every
minute), and sets `rpo` and `disabled_at` from the configured `AdhocPolicyTimeoutSec` (default
`3600`/1h) — `rpo` as a duration string equal to the timeout, `disabled_at` as `now + timeout`. Every
matched node runs the backup exactly once, the next time it polls within that window, and the policy
disables itself (pruning matched nodes' state for it) once the timeout passes — no follow-up
`DELETE` required. `201` with the created policy (including `disabled_at`) on success; same
`400`/malformed-JSON handling as `POST /api/v1/policies`.
````

Replace the `## PUT /api/v1/storage-policies/{id}` section's "not yet exposed" warning with the same round-trip sentence used above for `PUT /api/v1/policies/{id}`.

- [ ] **Step 3: Add a `CHANGELOG.md` entry**

At the top of `CHANGELOG.md`, above the existing `## 2026-08-02 — policy-server: generic disabled_at field on every policy type` entry:

```markdown
## 2026-08-02 — api-server: adhoc (one-time) backup policy endpoint

`POST /api/v1/policies/adhoc` creates a one-time backup policy from the same fields as an ordinary
create (name, client filters, object filters, destination) -- `api-server` composes `backup_window`
(every minute), `rpo`, and `disabled_at` itself from a new `AdhocPolicyTimeoutSec` config value
(default 1h), so a caller never hand-crafts those three fields to get a "run once on every matched
node, then expire" policy. Also fixes a gap the prior `disabled_at` work flagged: `PUT
/api/v1/policies/{id}` and `PUT /api/v1/storage-policies/{id}` now round-trip `disabled_at` --
previously any edit through either endpoint silently cleared it.

```

- [ ] **Step 4: Verify the full test suite and a clean build**

Run: `go test ./... && go build ./...`
Expected: PASS / clean build.

- [ ] **Step 5: Commit**

```bash
git add docs/components/api-server.md docs/api/rest-v1.md CHANGELOG.md
git commit -m "docs: document the adhoc policy endpoint and disabled_at round-trip fix"
```
