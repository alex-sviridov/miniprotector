# Storage Policy Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator create, view, and edit `"storage"`-typed policies through the web UI — currently only `policy-server` itself understands the storage type; nothing above it can create or edit one.

**Architecture:** Add an optional `type` filter to `ListPoliciesRequest` (proto + `policy-server`), thread it through `api-server` as a `?type=` query param, extend `api-server`'s policy DTO/add storage-specific create/update endpoints, then build a dedicated `Storage` section in the web UI (separate Pinia store, one list view, one edit modal) that never touches the existing backup-only `Policies` section.

**Tech Stack:** Go (`policy-server`, `api-server`), protobuf/gRPC, Vue 3 `<script setup>`, Pinia, Vitest + `@vue/test-utils`, `vue-good-table-next`.

## Global Constraints

- Storage `config` JSON uses keys `backend` (storage backend name, e.g. `"filesystem"`) and `root` (filesystem path) — matches the convention already used throughout `policy-server`'s own tests (`storage_policy_test.go`, `server_test.go`), not `type`/`path`.
- A policy's `type` is immutable via `UpdatePolicy` — never send `type` on an update request (matches existing `UpdatePolicyRequest` proto, unchanged here).
- `client_filters` on a storage policy created via the new modal is always `{ hostnames: [], labels: {} }` — `PolicyBase.Matches` already treats empty hostnames as "matches any node", so this is a valid default, not a bug. Client filter editing for storage policies is out of scope.
- Every new/modified `.proto` field requires updating `docs/protocols/policy-server.md` per this repo's `.claude/CLAUDE.md` documentation rules — done in Task 1.
- Follow this repo's existing test patterns exactly: Go tests use `testify` (`assert`/`require`), table-free one-assertion-focus style; Vue tests use Vitest + `@vue/test-utils` + `createTestingPinia({ stubActions: true })` for view tests, `vi.mock('../api/client')` for store tests.

---

## Task 1: Proto — add `type` filter to `ListPoliciesRequest`

**Files:**
- Modify: `src/api/policyserver.proto`
- Modify: `docs/protocols/policy-server.md`
- Generated (do not hand-edit): `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go`

**Interfaces:**
- Produces: `pb.ListPoliciesRequest.GetType() string`, used by Task 2 (`policy-server`) and Task 3 (`api-server`).

- [ ] **Step 1: Edit the proto**

In `src/api/policyserver.proto`, change:

```proto
message ListPoliciesRequest {}
```

to:

```proto
message ListPoliciesRequest {
  // Optional. "backup" or "storage" -- when set, only policies of this type
  // are returned. Empty returns every type (unfiltered, today's behavior).
  string type = 1;
}
```

- [ ] **Step 2: Regenerate the Go bindings**

Run: `make proto`
Expected: regenerates `src/api/policyserver.pb.go` and `src/api/policyserver_grpc.pb.go` with no errors. Confirm with `grep -n "func (x \*ListPoliciesRequest) GetType" src/api/policyserver.pb.go` — should print one match.

- [ ] **Step 3: Update the protocol doc**

In `docs/protocols/policy-server.md`, change the `ListPoliciesRequest {}` line inside the fenced proto block (currently line 24) to match the new proto:

```proto
message ListPoliciesRequest {
  string type = 1;
}
```

Then, in the `## Behavior` section, add a new bullet directly after the existing `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy` bullet (the one starting "`ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy` are the admin surface..."):

```markdown
- `ListPoliciesRequest.type` is an optional filter — `"backup"` or `"storage"` restricts the
  response to that type; empty (the default) returns every type, unchanged from before this field
  existed. A `type` value that matches no loaded policy's `Kind()` returns an empty list, not an
  error — there is no closed enum at this layer, `Kind()` is just whatever string the type
  subfolder produced.
```

- [ ] **Step 4: Build to confirm the generated code compiles**

Run: `cd src && go build ./...`
Expected: exits 0, no errors.

- [ ] **Step 5: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go docs/protocols/policy-server.md
git commit -m "feat(policy-server): add optional type filter to ListPoliciesRequest proto"
```

---

## Task 2: `policy-server` — filter `ListPolicies` by type

**Files:**
- Modify: `src/cmd/policy-server/server.go`
- Test: `src/cmd/policy-server/server_test.go`

**Interfaces:**
- Consumes: `pb.ListPoliciesRequest.GetType() string` (Task 1); `Policy.Kind() string` (existing, `src/cmd/policy-server/policy.go`).
- Produces: `ListPolicies` behavior relied on by Task 3's `api-server` passthrough.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/server_test.go`, after `TestListPolicies_ResponseIncludesType` (end of file):

```go
func TestListPolicies_FilterByTypeReturnsOnlyMatchingType(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	writePolicyFile(t, filepath.Join(dir, "storage"), "east-1.json", `{
		"metadata": {"name": "east-1-storage"},
		"hostname": "storage-east-1.internal",
		"port": 9400,
		"config": {"backend": "filesystem"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{Type: "storage"})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "east-1-storage", resp.Policies[0].Name)
	assert.Equal(t, "storage", resp.Policies[0].Type)
}

func TestListPolicies_EmptyTypeReturnsEveryType(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	writePolicyFile(t, filepath.Join(dir, "storage"), "east-1.json", `{
		"metadata": {"name": "east-1-storage"},
		"hostname": "storage-east-1.internal",
		"port": 9400,
		"config": {"backend": "filesystem"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Policies, 2)
}

func TestListPolicies_UnknownTypeReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{Type: "quux"})
	require.NoError(t, err)
	assert.Empty(t, resp.Policies)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestListPolicies_FilterByTypeReturnsOnlyMatchingType -v`
Expected: FAIL — returns 2 policies instead of 1 (no filtering implemented yet).

- [ ] **Step 3: Implement the filter**

In `src/cmd/policy-server/server.go`, replace the `ListPolicies` method:

```go
// ListPolicies returns every currently-loaded policy, unfiltered by any
// caller identity -- the admin surface api-server proxies for browsing and
// editing the full policy set. Unlike GetPolicies, it is never called by a
// mesh node itself. If req.Type is set, only policies whose Kind() matches
// are returned; empty Type returns every type, unchanged from before this
// filter existed.
func (s *policyServerServer) ListPolicies(ctx context.Context, req *pb.ListPoliciesRequest) (*pb.ListPoliciesResponse, error) {
	policies := s.cache.Policies()
	var out []*pb.Policy
	for _, p := range policies {
		if req.GetType() != "" && p.Kind() != req.GetType() {
			continue
		}
		out = append(out, p.ToProto(true))
	}
	s.logger.Info("ListPolicies", "type", req.GetType(), "count", len(out))
	return &pb.ListPoliciesResponse{Policies: out}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -v -run TestListPolicies`
Expected: PASS — all `TestListPolicies_*` tests pass, including the three new ones and the two pre-existing ones (`TestListPolicies_ReturnsAllPoliciesRegardlessOfIdentity`, `TestListPolicies_IncludesClientFilters`, `TestListPolicies_ResponseIncludesType`).

- [ ] **Step 5: Run the full policy-server test suite**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go
git commit -m "feat(policy-server): filter ListPolicies by type when requested"
```

---

## Task 3: `api-server` — `?type=` query param and extended `policyDTO`

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Test: `src/cmd/api-server/policies_test.go`

**Interfaces:**
- Consumes: `pb.ListPoliciesRequest{Type: string}` (Task 1/2).
- Produces: `policyDTO` gains `Hostname string`, `Port int32`, `Config string` fields — consumed by Task 5's frontend store/view via the JSON response.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/policies_test.go`, after `TestHandleListPolicies_BackendErrorTranslated`:

```go
func TestHandleListPolicies_PassesTypeQueryParamThrough(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies?type=storage", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleListPolicies_NoTypeParamSendsEmptyType(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}
```

Add after `TestToPolicyDTO_ConvertsTimestampsToUnixSecondsAndClientFilters`:

```go
func TestToPolicyDTO_IncludesStorageFields(t *testing.T) {
	p := &pb.Policy{
		Id:       "s1",
		Name:     "east-1-storage",
		Type:     "storage",
		Hostname: "storage-east-1.internal",
		Port:     9400,
		Config:   `{"backend": "filesystem", "root": "/data/storage"}`,
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, "storage-east-1.internal", dto.Hostname)
	assert.Equal(t, int32(9400), dto.Port)
	assert.Equal(t, `{"backend": "filesystem", "root": "/data/storage"}`, dto.Config)
}
```

To actually assert the query param is forwarded (not just that the response is 200), also update `fakePolicyServiceClient.ListPolicies` to record the last request, and assert on it. Change the fake in `policies_test.go`:

```go
type fakePolicyServiceClient struct {
	listResp     *pb.ListPoliciesResponse
	listErr      error
	lastListReq  *pb.ListPoliciesRequest

	createResp    *pb.Policy
	createErr     error
	lastCreateReq *pb.CreatePolicyRequest

	updateResp    *pb.Policy
	updateErr     error
	lastUpdateReq *pb.UpdatePolicyRequest

	deleteResp    *pb.DeletePolicyResponse
	deleteErr     error
	lastDeleteReq *pb.DeletePolicyRequest
}

func (f *fakePolicyServiceClient) ListPolicies(ctx context.Context, in *pb.ListPoliciesRequest, opts ...grpc.CallOption) (*pb.ListPoliciesResponse, error) {
	f.lastListReq = in
	return f.listResp, f.listErr
}
```

Then rewrite the two new tests to assert on `fake.lastListReq.GetType()`:

```go
func TestHandleListPolicies_PassesTypeQueryParamThrough(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies?type=storage", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastListReq)
	assert.Equal(t, "storage", fake.lastListReq.GetType())
}

func TestHandleListPolicies_NoTypeParamSendsEmptyType(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastListReq)
	assert.Equal(t, "", fake.lastListReq.GetType())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestHandleListPolicies_PassesTypeQueryParamThrough|TestHandleListPolicies_NoTypeParamSendsEmptyType|TestToPolicyDTO_IncludesStorageFields' -v`
Expected: FAIL — `lastListReq` is always nil (fake not yet recording), `dto.Hostname`/`dto.Port`/`dto.Config` don't exist yet (compile error on `policyDTO`).

- [ ] **Step 3: Implement `policyDTO` fields and query param passthrough**

In `src/cmd/api-server/policies.go`, add fields to `policyDTO`:

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
	Hostname      string            `json:"hostname"`
	Port          int32             `json:"port"`
	Config        string            `json:"config"`
}
```

Add to `toPolicyDTO`'s returned struct literal, alongside the existing `Type: p.GetType(),` line:

```go
		Type:      p.GetType(),
		Hostname:  p.GetHostname(),
		Port:      p.GetPort(),
		Config:    p.GetConfig(),
	}
```

Update `handleListPolicies` to read and forward the query param:

```go
func (s *server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	resp, err := s.policy.ListPolicies(r.Context(), &pb.ListPoliciesRequest{Type: r.URL.Query().Get("type")})
	if err != nil {
		s.logger.Error("handleListPolicies: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	policies := make([]policyDTO, len(resp.GetPolicies()))
	for i, p := range resp.GetPolicies() {
		policies[i] = toPolicyDTO(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": policies})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v -run 'TestHandleListPolicies|TestToPolicyDTO'`
Expected: PASS, all listed tests green.

- [ ] **Step 5: Run the full api-server test suite**

Run: `cd src && go test ./cmd/api-server/...`
Expected: PASS, no regressions (existing `TestHandleListPolicies_ReturnsDataEnvelope` etc. still pass since `fakePolicyServiceClient.ListPolicies` behavior is additive).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "feat(api-server): forward type query param on GET /policies, extend policyDTO with storage fields"
```

---

## Task 4: `api-server` — storage policy create/update endpoints

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/server.go`
- Test: `src/cmd/api-server/policies_test.go`

**Interfaces:**
- Produces: `POST /api/v1/storage-policies`, `PUT /api/v1/storage-policies/{id}` — consumed by Task 6's frontend `storagePolicies` store.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/policies_test.go`, at the end of the file:

```go
func TestHandleCreateStoragePolicy_ReturnsCreatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "s1", Name: "east-1-storage", Type: "storage",
		Hostname: "storage-east-1.internal", Port: 9400, Config: `{"backend": "filesystem"}`,
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "east-1-storage",
		"hostname": "storage-east-1.internal",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\"}"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "storage", fake.lastCreateReq.GetType())
	assert.Equal(t, "east-1-storage", fake.lastCreateReq.GetName())
	assert.Equal(t, "storage-east-1.internal", fake.lastCreateReq.GetHostname())
	assert.Equal(t, int32(9400), fake.lastCreateReq.GetPort())
	assert.Equal(t, `{"backend": "filesystem"}`, fake.lastCreateReq.GetConfig())

	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "storage-east-1.internal", respBody["hostname"])
}

func TestHandleCreateStoragePolicy_MalformedJSONReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-policies", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, fake.lastCreateReq, "backend must not be called on malformed input")
}

func TestHandleCreateStoragePolicy_BackendValidationErrorReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{createErr: status.Error(codes.InvalidArgument, "hostname is required")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-policies", strings.NewReader(`{"name": "x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateStoragePolicy_ReturnsUpdatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{
		Id: "s1", Name: "east-1-storage-renamed", Type: "storage",
		Hostname: "storage-east-2.internal", Port: 9401, Config: `{"backend": "filesystem"}`,
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "east-1-storage-renamed",
		"hostname": "storage-east-2.internal",
		"port": 9401,
		"config": "{\"backend\": \"filesystem\"}"
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage-policies/s1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	assert.Equal(t, "s1", fake.lastUpdateReq.GetId())
	assert.Equal(t, "storage-east-2.internal", fake.lastUpdateReq.GetHostname())
	assert.Equal(t, int32(9401), fake.lastUpdateReq.GetPort())
}

func TestHandleUpdateStoragePolicy_UnknownIDReturns404(t *testing.T) {
	fake := &fakePolicyServiceClient{updateErr: status.Error(codes.NotFound, "policy \"ghost\" not found")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage-policies/ghost", strings.NewReader(`{"name": "x", "hostname": "h", "port": 1, "config": "{}"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleCreateStoragePolicy -v`
Expected: FAIL — `404 page not found` (route doesn't exist yet).

- [ ] **Step 3: Implement the handlers**

In `src/cmd/api-server/policies.go`, add after `handleUpdatePolicy`:

```go
type storagePolicyInput struct {
	Name          string           `json:"name"`
	ClientFilters clientFiltersDTO `json:"client_filters"`
	Hostname      string           `json:"hostname"`
	Port          int32            `json:"port"`
	Config        string           `json:"config"`
}

func decodeStoragePolicyInput(r *http.Request) (storagePolicyInput, error) {
	var in storagePolicyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return storagePolicyInput{}, err
	}
	return in, nil
}

func (s *server) handleCreateStoragePolicy(w http.ResponseWriter, r *http.Request) {
	in, err := decodeStoragePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		Type:          "storage",
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Hostname:      in.Hostname,
		Port:          in.Port,
		Config:        in.Config,
	})
	if err != nil {
		s.logger.Error("handleCreateStoragePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(resp))
}

func (s *server) handleUpdateStoragePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in, err := decodeStoragePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.UpdatePolicy(r.Context(), &pb.UpdatePolicyRequest{
		Id:            id,
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Hostname:      in.Hostname,
		Port:          in.Port,
		Config:        in.Config,
	})
	if err != nil {
		s.logger.Error("handleUpdateStoragePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(resp))
}
```

In `src/cmd/api-server/server.go`, add two routes to `registerRoutes`, directly after the existing policy routes:

```go
	mux.HandleFunc("GET /api/v1/policies", s.handleListPolicies)
	mux.HandleFunc("GET /api/v1/policies/{id}", s.handleGetPolicy)
	mux.HandleFunc("POST /api/v1/policies", s.handleCreatePolicy)
	mux.HandleFunc("PUT /api/v1/policies/{id}", s.handleUpdatePolicy)
	mux.HandleFunc("DELETE /api/v1/policies/{id}", s.handleDeletePolicy)
	mux.HandleFunc("POST /api/v1/storage-policies", s.handleCreateStoragePolicy)
	mux.HandleFunc("PUT /api/v1/storage-policies/{id}", s.handleUpdateStoragePolicy)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v -run 'TestHandleCreateStoragePolicy|TestHandleUpdateStoragePolicy'`
Expected: PASS.

- [ ] **Step 5: Run the full api-server test suite**

Run: `cd src && go test ./cmd/api-server/...`
Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/server.go src/cmd/api-server/policies_test.go
git commit -m "feat(api-server): add POST/PUT storage-policies endpoints"
```

---

## Task 5: Documentation for the backend changes

**Files:**
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/api-server.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- None (docs only).

- [ ] **Step 1: Update `docs/components/policy-server.md`**

Find the sentence "`ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy`) that `api-server` proxies as REST" near the top of the file (around line 6) and leave it as-is (unaffected). Then find the "Policy types and directory layout" section and add a sentence after the existing paragraph ending "...a request that sets fields belonging to the other type is rejected.":

```markdown
`ListPolicies` additionally accepts an optional `type` filter — `"backup"` or `"storage"` restricts
the response to that type; empty returns every type, unchanged from before this filter existed.
```

- [ ] **Step 2: Update `docs/components/api-server.md`**

Replace the paragraph starting "`policy-server` also supports a `"storage"` policy type..." (lines 36-44) with:

```markdown
`policy-server` also supports a `"storage"` policy type (`hostname`/`port`/`config`).
`GET /policies` accepts an optional `?type=backup|storage` query parameter to filter by type;
without it, every policy of every type is returned, each with `hostname`/`port`/`config` populated
in the response DTO when applicable (empty/zero for a `"backup"`-typed policy, and vice versa for
`rpo`/`destination`/`object_filters`). Creating or updating a storage policy uses a separate pair of
endpoints, `POST /storage-policies` and `PUT /storage-policies/{id}`, since a storage policy's input
shape (`hostname`/`port`/`config`) shares nothing with a backup policy's
(`object_filters`/`rpo`/`backup_window`/`destination`) beyond `name`/`client_filters`. `GET
/policies/{id}` and `DELETE /policies/{id}` are shared across both types — both operations are
already type-agnostic, looking a policy up or removing it by `id` alone.
```

- [ ] **Step 3: Update `docs/api/rest-v1.md`**

Add a `type` query parameter note directly under the `## GET /api/v1/policies` heading, replacing the first paragraph:

```markdown
## `GET /api/v1/policies`

Returns every policy, unfiltered by any client identity (unlike `policy-server`'s own `GetPolicies`
RPC, which every mesh node calls and which is scoped to its own matching policies). Not paginated.
Accepts an optional `?type=backup` or `?type=storage` query parameter to restrict the response to
one policy type; omitted returns every type.
```

Then update the example response's fields (after `"destination": "bwfs-east.internal:8080"`) — add the storage fields so the shape is documented even though this particular example is a backup policy:

```json
{
  "data": [
    {
      "id": "b1f2c3d4-...",
      "name": "nightly-web-backup",
      "created_at": 1752400000,
      "updated_at": 1752400010,
      "client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
      "object_filters": [
        {"id": "a9e8d7c6-...", "path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}
      ],
      "rpo": "24h",
      "backup_window": ["0 2 * * *", "0 20 * * *"],
      "destination": "bwfs-east.internal:8080",
      "type": "backup",
      "hostname": "",
      "port": 0,
      "config": ""
    }
  ]
}
```

Then add two new sections directly after the existing `## DELETE /api/v1/policies/{id}` section (before `## GET /api/v1/jobs`):

```markdown
## `POST /api/v1/storage-policies`

Creates a new `"storage"`-typed policy. Body:

```json
{
  "name": "east-1-storage",
  "client_filters": {"hostnames": [], "labels": {}},
  "hostname": "storage-east-1.internal",
  "port": 9400,
  "config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
}
```

`config` is a JSON string, not a nested object — `policy-server` treats it as opaque, pass-through
text; the web UI is the one that gives it the `backend`/`root` shape shown above. `201` with the
created policy on success. `400` if `name` is empty, `hostname` is empty, `port` isn't in `[1,
65535]`, or `config` isn't well-formed JSON — no file is written when validation fails.

## `PUT /api/v1/storage-policies/{id}`

Replaces an existing storage policy's editable fields — same body shape as `POST`, full replacement
rather than a partial patch. `200` with the updated policy; `id`, `created_at`, and `type` never
change. `400` on the same validation failures as `POST`. `404` if `id` doesn't match any policy.
```

- [ ] **Step 4: Add the changelog entry**

Prepend to `CHANGELOG.md`, above the existing `## 2026-07-28 — policy-server: add storage policy type` entry:

```markdown
## 2026-07-28 — api-server/web: storage policy create/edit support

`api-server`'s `GET /policies` now accepts a `?type=` filter, and two new endpoints —
`POST /storage-policies` / `PUT /storage-policies/{id}` — let a caller create and edit `"storage"`
-typed policies, which `policy-server` has supported since the previous entry but which nothing
above it could write until now. The web UI gained a dedicated `Storage` section (`/storage`) —
list, create, and edit via a modal — kept fully separate from the existing backup-only `Policies`
section, which now requests `?type=backup` explicitly so it never renders a storage policy's blank
`rpo`/`destination` fields.

```

- [ ] **Step 5: Commit**

```bash
git add docs/components/policy-server.md docs/components/api-server.md docs/api/rest-v1.md CHANGELOG.md
git commit -m "docs: document ListPolicies type filter and storage-policies endpoints"
```

---

## Task 6: Frontend — `storagePolicies` store, and scope `usePoliciesStore` to backup

**Files:**
- Create: `web/src/stores/storagePolicies.js`
- Create: `web/src/stores/storagePolicies.spec.js`
- Modify: `web/src/stores/policies.js`
- Modify: `web/src/stores/policies.spec.js`

**Interfaces:**
- Produces: `useStoragePoliciesStore()` with state `{ list, byId, loading, error }` and actions `fetchAll()`, `fetchOne(id)`, `create(input)`, `update(id, input)`, `remove(id)` — consumed by Task 8 (`StorageView.vue`).

- [ ] **Step 1: Write the failing store test for `usePoliciesStore.fetchAll`'s new query param**

In `web/src/stores/policies.spec.js`, change the existing `fetchAll` test's assertion:

```js
  it('fetchAll populates the list from the API', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 'p1', name: 'nightly' }] })
    const policies = usePoliciesStore()

    await policies.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/policies?type=backup')
    expect(policies.list).toEqual([{ id: 'p1', name: 'nightly' }])
    expect(policies.loading).toBe(false)
    expect(policies.error).toBeNull()
  })
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npx vitest run src/stores/policies.spec.js -t "fetchAll populates the list from the API"`
Expected: FAIL — `apiFetch` was called with `'/policies'`, not `'/policies?type=backup'`.

- [ ] **Step 3: Update `usePoliciesStore.fetchAll`**

In `web/src/stores/policies.js`, change:

```js
    async fetchAll() {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/policies')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
```

to:

```js
    async fetchAll() {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/policies?type=backup')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
```

- [ ] **Step 4: Run the policies store spec to verify it passes**

Run: `cd web && npx vitest run src/stores/policies.spec.js`
Expected: PASS, all tests including the updated one.

- [ ] **Step 5: Write the failing test file for the new `storagePolicies` store**

Create `web/src/stores/storagePolicies.spec.js`:

```js
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useStoragePoliciesStore } from './storagePolicies'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('storagePolicies store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetchAll populates the list from the API filtered to storage', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 's1', name: 'east-1-storage' }] })
    const storagePolicies = useStoragePoliciesStore()

    await storagePolicies.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/policies?type=storage')
    expect(storagePolicies.list).toEqual([{ id: 's1', name: 'east-1-storage' }])
    expect(storagePolicies.loading).toBe(false)
    expect(storagePolicies.error).toBeNull()
  })

  it('fetchAll records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const storagePolicies = useStoragePoliciesStore()

    await storagePolicies.fetchAll()

    expect(storagePolicies.error).toBe('boom')
    expect(storagePolicies.list).toEqual([])
  })

  it('fetchOne fetches and caches a storage policy by id', async () => {
    apiFetch.mockResolvedValue({ id: 's1', name: 'east-1-storage' })
    const storagePolicies = useStoragePoliciesStore()

    const first = await storagePolicies.fetchOne('s1')
    const second = await storagePolicies.fetchOne('s1')

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(apiFetch).toHaveBeenCalledWith('/policies/s1')
    expect(first).toEqual({ id: 's1', name: 'east-1-storage' })
    expect(second).toEqual(first)
  })

  it('create posts to /storage-policies and adds the result to list and byId', async () => {
    const created = { id: 's2', name: 'east-2-storage' }
    apiFetch.mockResolvedValue(created)
    const storagePolicies = useStoragePoliciesStore()

    const input = { name: 'east-2-storage', hostname: 'h', port: 9400, config: '{}' }
    const result = await storagePolicies.create(input)

    expect(apiFetch).toHaveBeenCalledWith('/storage-policies', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(created)
    expect(storagePolicies.list).toEqual([created])
    expect(storagePolicies.byId.s2).toEqual(created)
  })

  it('create records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('hostname is required'))
    const storagePolicies = useStoragePoliciesStore()

    await expect(storagePolicies.create({ name: 'x' })).rejects.toThrow('hostname is required')
    expect(storagePolicies.error).toBe('hostname is required')
    expect(storagePolicies.list).toEqual([])
  })

  it('update puts to /storage-policies/{id} and replaces the entry in list and byId', async () => {
    const original = { id: 's1', name: 'east-1-storage' }
    const updated = { id: 's1', name: 'east-1-storage-renamed' }
    apiFetch.mockResolvedValueOnce({ data: [original] })
    const storagePolicies = useStoragePoliciesStore()
    await storagePolicies.fetchAll()

    apiFetch.mockResolvedValueOnce(updated)
    const input = { name: 'east-1-storage-renamed', hostname: 'h', port: 9400, config: '{}' }
    const result = await storagePolicies.update('s1', input)

    expect(apiFetch).toHaveBeenCalledWith('/storage-policies/s1', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(updated)
    expect(storagePolicies.list).toEqual([updated])
    expect(storagePolicies.byId.s1).toEqual(updated)
  })

  it('update records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('port must be between 1 and 65535'))
    const storagePolicies = useStoragePoliciesStore()

    await expect(storagePolicies.update('s1', { name: 'x' })).rejects.toThrow('port must be between 1 and 65535')
    expect(storagePolicies.error).toBe('port must be between 1 and 65535')
  })

  it('remove deletes via /policies/{id} and drops the entry from list and byId', async () => {
    apiFetch.mockResolvedValueOnce({ data: [{ id: 's1', name: 'east-1-storage' }] })
    const storagePolicies = useStoragePoliciesStore()
    await storagePolicies.fetchAll()
    storagePolicies.byId.s1 = { id: 's1', name: 'east-1-storage' }

    apiFetch.mockResolvedValueOnce(null)
    await storagePolicies.remove('s1')

    expect(apiFetch).toHaveBeenCalledWith('/policies/s1', { method: 'DELETE' })
    expect(storagePolicies.list).toEqual([])
    expect(storagePolicies.byId.s1).toBeUndefined()
  })

  it('remove records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('policy not found'))
    const storagePolicies = useStoragePoliciesStore()

    await expect(storagePolicies.remove('missing')).rejects.toThrow('policy not found')
    expect(storagePolicies.error).toBe('policy not found')
  })
})
```

- [ ] **Step 6: Run it to verify it fails**

Run: `cd web && npx vitest run src/stores/storagePolicies.spec.js`
Expected: FAIL — module `./storagePolicies` doesn't exist.

- [ ] **Step 7: Implement the store**

Create `web/src/stores/storagePolicies.js`:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

export const useStoragePoliciesStore = defineStore('storagePolicies', {
  state: () => ({
    list: [],
    byId: {},
    loading: false,
    error: null,
  }),
  actions: {
    async fetchAll() {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/policies?type=storage')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
    async fetchOne(id) {
      if (this.byId[id]) {
        this.error = null
        return this.byId[id]
      }
      return withRequest(this, async () => {
        const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`)
        this.byId[id] = policy
        return policy
      })
    },
    async create(input) {
      return withRequest(this, async () => {
        const policy = await apiFetch('/storage-policies', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        this.list.push(policy)
        this.byId[policy.id] = policy
        return policy
      })
    },
    async update(id, input) {
      return withRequest(this, async () => {
        const policy = await apiFetch(`/storage-policies/${encodeURIComponent(id)}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        const idx = this.list.findIndex((p) => p.id === id)
        if (idx !== -1) this.list[idx] = policy
        this.byId[id] = policy
        return policy
      })
    },
    async remove(id) {
      return withRequest(this, async () => {
        await apiFetch(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' })
        this.list = this.list.filter((p) => p.id !== id)
        delete this.byId[id]
      })
    },
  },
})
```

- [ ] **Step 8: Run both spec files to verify they pass**

Run: `cd web && npx vitest run src/stores/storagePolicies.spec.js src/stores/policies.spec.js`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add web/src/stores/storagePolicies.js web/src/stores/storagePolicies.spec.js web/src/stores/policies.js web/src/stores/policies.spec.js
git commit -m "feat(web): add storagePolicies store, scope policies store to type=backup"
```

---

## Task 7: Frontend — `StorageEditModal.vue`

**Files:**
- Create: `web/src/components/storage/StorageEditModal.vue`
- Create: `web/src/components/storage/StorageEditModal.spec.js`

**Interfaces:**
- Consumes: nothing from earlier tasks (pure presentational component).
- Produces: props `{ policy: Object|null }`; emits `close` (no payload) and `save` (payload: `{ name, hostname, port, config, client_filters }` where `config` is a JSON string `{"backend": "filesystem", "root": "<path>"}`). Consumed by Task 8 (`StorageView.vue`).

- [ ] **Step 1: Write the failing test**

Create `web/src/components/storage/StorageEditModal.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import StorageEditModal from './StorageEditModal.vue'

describe('StorageEditModal', () => {
  it('renders empty fields in create mode', () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    expect(wrapper.find('[data-test="storage-name-input"]').element.value).toBe('')
    expect(wrapper.find('[data-test="storage-hostname-input"]').element.value).toBe('')
    expect(wrapper.find('[data-test="storage-port-input"]').element.value).toBe('')
    expect(wrapper.find('[data-test="storage-path-input"]').element.value).toBe('')
  })

  it('pre-fills fields from the policy prop in edit mode', () => {
    const wrapper = mount(StorageEditModal, {
      props: {
        policy: {
          id: 's1',
          name: 'east-1-storage',
          hostname: 'storage-east-1.internal',
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage"}',
        },
      },
    })
    expect(wrapper.find('[data-test="storage-name-input"]').element.value).toBe('east-1-storage')
    expect(wrapper.find('[data-test="storage-hostname-input"]').element.value).toBe('storage-east-1.internal')
    expect(wrapper.find('[data-test="storage-port-input"]').element.value).toBe('9400')
    expect(wrapper.find('[data-test="storage-path-input"]').element.value).toBe('/data/storage')
  })

  it('emits close when the Cancel button is clicked', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('[data-test="storage-cancel"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('emits close on Escape', () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('does not emit save when required fields are blank', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
    expect(wrapper.text()).toContain('required')
  })

  it('emits save with the built payload on valid submit', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('[data-test="storage-name-input"]').setValue('east-1-storage')
    await wrapper.find('[data-test="storage-hostname-input"]').setValue('storage-east-1.internal')
    await wrapper.find('[data-test="storage-port-input"]').setValue('9400')
    await wrapper.find('[data-test="storage-path-input"]').setValue('/data/storage')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0]).toEqual({
      name: 'east-1-storage',
      hostname: 'storage-east-1.internal',
      port: 9400,
      config: JSON.stringify({ backend: 'filesystem', root: '/data/storage' }),
      client_filters: { hostnames: [], labels: {} },
    })
  })

  it('rejects a port outside 1-65535', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('[data-test="storage-name-input"]').setValue('x')
    await wrapper.find('[data-test="storage-hostname-input"]').setValue('h')
    await wrapper.find('[data-test="storage-port-input"]').setValue('70000')
    await wrapper.find('[data-test="storage-path-input"]').setValue('/data')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toBeUndefined()
    expect(wrapper.text()).toContain('port')
  })
})
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npx vitest run src/components/storage/StorageEditModal.spec.js`
Expected: FAIL — module doesn't exist.

- [ ] **Step 3: Implement the component**

Create `web/src/components/storage/StorageEditModal.vue`:

```vue
<!-- web/src/components/storage/StorageEditModal.vue -->
<script setup>
import { reactive, onMounted, onBeforeUnmount } from 'vue'
import BaseButton from '../ui/BaseButton.vue'

const props = defineProps({
  policy: { type: Object, default: null },
})
const emit = defineEmits(['close', 'save'])

function parseConfig(configText) {
  try {
    return JSON.parse(configText || '{}')
  } catch {
    return {}
  }
}

const form = reactive({
  name: props.policy?.name || '',
  hostname: props.policy?.hostname || '',
  port: props.policy ? String(props.policy.port) : '',
  storageType: parseConfig(props.policy?.config).backend || 'filesystem',
  path: parseConfig(props.policy?.config).root || '',
})

const errors = reactive({ message: '' })

function close() {
  emit('close')
}

function onKeydown(event) {
  if (event.key === 'Escape') close()
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})

function submit() {
  errors.message = ''
  const port = Number(form.port)

  if (!form.name.trim()) {
    errors.message = 'Name is required.'
    return
  }
  if (!form.hostname.trim()) {
    errors.message = 'Hostname is required.'
    return
  }
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    errors.message = 'Port must be a number between 1 and 65535.'
    return
  }
  if (!form.path.trim()) {
    errors.message = 'Filesystem path is required.'
    return
  }

  emit('save', {
    name: form.name.trim(),
    hostname: form.hostname.trim(),
    port,
    config: JSON.stringify({ backend: form.storageType, root: form.path.trim() }),
    client_filters: { hostnames: [], labels: {} },
  })
}
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="close">
    <div class="bg-white rounded p-4 max-w-lg w-full">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-lg font-semibold">{{ policy ? 'Edit Storage Policy' : 'New Storage Policy' }}</h2>
        <BaseButton variant="secondary" data-test="storage-cancel" @click="close">Cancel</BaseButton>
      </div>
      <p v-if="errors.message" class="text-red-600 mb-4">{{ errors.message }}</p>
      <form @submit.prevent="submit" class="space-y-4">
        <div>
          <label class="block font-medium mb-1">Name</label>
          <input data-test="storage-name-input" v-model="form.name" class="w-full border rounded px-2 py-1" />
        </div>
        <div>
          <label class="block font-medium mb-1">Hostname</label>
          <input data-test="storage-hostname-input" v-model="form.hostname" class="w-full border rounded px-2 py-1" />
        </div>
        <div>
          <label class="block font-medium mb-1">Port</label>
          <input data-test="storage-port-input" v-model="form.port" type="number" class="w-full border rounded px-2 py-1" />
        </div>
        <div>
          <label class="block font-medium mb-1">Storage Type</label>
          <select data-test="storage-type-select" v-model="form.storageType" class="w-full border rounded px-2 py-1">
            <option value="filesystem">filesystem</option>
          </select>
        </div>
        <div v-if="form.storageType === 'filesystem'">
          <label class="block font-medium mb-1">Filesystem Path</label>
          <input data-test="storage-path-input" v-model="form.path" class="w-full border rounded px-2 py-1" />
        </div>
        <BaseButton type="submit" variant="primary">
          {{ policy ? 'Save Changes' : 'Create Storage Policy' }}
        </BaseButton>
      </form>
    </div>
  </div>
</template>
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && npx vitest run src/components/storage/StorageEditModal.spec.js`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/storage/StorageEditModal.vue web/src/components/storage/StorageEditModal.spec.js
git commit -m "feat(web): add StorageEditModal component"
```

---

## Task 8: Frontend — `StorageView.vue`, router, and nav

**Files:**
- Create: `web/src/views/StorageView.vue`
- Create: `web/src/views/StorageView.spec.js`
- Modify: `web/src/router.js`
- Modify: `web/src/components/Sidebar.vue`

**Interfaces:**
- Consumes: `useStoragePoliciesStore()` (Task 6); `StorageEditModal` props/emits (Task 7).
- Produces: route `{ name: 'storage', path: '/storage' }`.

- [ ] **Step 1: Write the failing test**

Create `web/src/views/StorageView.spec.js`:

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import StorageView from './StorageView.vue'
import { useStoragePoliciesStore } from '../stores/storagePolicies'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { storagePolicies: state } })
  const wrapper = mount(StorageView, { global: { plugins: [pinia] } })
  return { wrapper, storagePolicies: useStoragePoliciesStore() }
}

describe('StorageView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('calls fetchAll on mount', () => {
    const { storagePolicies } = mountView({ list: [], loading: false, error: null })
    expect(storagePolicies.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('renders each storage policy in the table', () => {
    const { wrapper } = mountView({
      list: [
        {
          id: 's1',
          name: 'east-1-storage',
          hostname: 'storage-east-1.internal',
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage"}',
        },
      ],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('east-1-storage')
    expect(wrapper.text()).toContain('storage-east-1.internal')
    expect(wrapper.text()).toContain('9400')
    expect(wrapper.text()).toContain('filesystem')
  })

  it('shows an empty-state message when there are no storage policies', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.text()).toContain('No storage policies defined yet.')
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('opens the modal in create mode when "New Storage Policy" is clicked', async () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    await wrapper.find('[data-test="storage-new"]').trigger('click')
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).props('policy')).toBeNull()
  })

  it('opens the modal in edit mode when a row is clicked', async () => {
    const { wrapper } = mountView({
      list: [{ id: 's1', name: 'east-1-storage', hostname: 'h', port: 9400, config: '{}' }],
      loading: false,
      error: null,
    })
    await wrapper.find('[data-test="storage-edit-s1"]').trigger('click')
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).props('policy')).toEqual({
      id: 's1',
      name: 'east-1-storage',
      hostname: 'h',
      port: 9400,
      config: '{}',
    })
  })

  it('calls create and closes the modal on save in create mode', async () => {
    const { wrapper, storagePolicies } = mountView({ list: [], loading: false, error: null })
    storagePolicies.create.mockResolvedValue({ id: 's2', name: 'new-storage' })
    await wrapper.find('[data-test="storage-new"]').trigger('click')

    const payload = { name: 'new-storage', hostname: 'h', port: 1, config: '{}', client_filters: { hostnames: [], labels: {} } }
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('save', payload)

    expect(storagePolicies.create).toHaveBeenCalledWith(payload)
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(false)
  })

  it('calls update and closes the modal on save in edit mode', async () => {
    const { wrapper, storagePolicies } = mountView({
      list: [{ id: 's1', name: 'east-1-storage', hostname: 'h', port: 9400, config: '{}' }],
      loading: false,
      error: null,
    })
    storagePolicies.update.mockResolvedValue({ id: 's1', name: 'renamed' })
    await wrapper.find('[data-test="storage-edit-s1"]').trigger('click')

    const payload = { name: 'renamed', hostname: 'h', port: 9400, config: '{}', client_filters: { hostnames: [], labels: {} } }
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('save', payload)

    expect(storagePolicies.update).toHaveBeenCalledWith('s1', payload)
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(false)
  })

  it('closes the modal without saving on close', async () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    await wrapper.find('[data-test="storage-new"]').trigger('click')
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(false)
  })

  it('deletes a storage policy after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, storagePolicies } = mountView({
      list: [{ id: 's1', name: 'east-1-storage', hostname: 'h', port: 9400, config: '{}' }],
      loading: false,
      error: null,
    })

    await wrapper.find('[data-test="storage-delete-s1"]').trigger('click')

    expect(storagePolicies.remove).toHaveBeenCalledWith('s1')
  })

  it('does not delete when the confirm dialog is dismissed', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { wrapper, storagePolicies } = mountView({
      list: [{ id: 's1', name: 'east-1-storage', hostname: 'h', port: 9400, config: '{}' }],
      loading: false,
      error: null,
    })

    await wrapper.find('[data-test="storage-delete-s1"]').trigger('click')

    expect(storagePolicies.remove).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npx vitest run src/views/StorageView.spec.js`
Expected: FAIL — module doesn't exist.

- [ ] **Step 3: Implement `StorageView.vue`**

Create `web/src/views/StorageView.vue`:

```vue
<script setup>
import { onMounted, ref } from 'vue'
import { useStoragePoliciesStore } from '../stores/storagePolicies'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import StorageEditModal from '../components/storage/StorageEditModal.vue'

const storagePolicies = useStoragePoliciesStore()
const showModal = ref(false)
const editingPolicy = ref(null)

onMounted(() => {
  storagePolicies.fetchAll()
})

function storageBackend(configText) {
  try {
    return JSON.parse(configText).backend || '—'
  } catch {
    return '—'
  }
}

function openCreate() {
  editingPolicy.value = null
  showModal.value = true
}

function openEdit(policy) {
  editingPolicy.value = policy
  showModal.value = true
}

function closeModal() {
  showModal.value = false
  editingPolicy.value = null
}

async function save(payload) {
  if (editingPolicy.value) {
    await storagePolicies.update(editingPolicy.value.id, payload)
  } else {
    await storagePolicies.create(payload)
  }
  closeModal()
}

function confirmDelete(id) {
  if (window.confirm('Delete this storage policy?')) {
    storagePolicies.remove(id)
  }
}

const columns = [
  { label: 'Name', field: 'name', sortable: true },
  { label: 'Hostname', field: 'hostname', sortable: true },
  { label: 'Port', field: 'port', sortable: true },
  { label: 'Storage Type', field: 'storageType', sortable: false },
  { label: '', field: 'actions', sortable: false },
]
</script>

<template>
  <div>
    <PageHeader title="Storage">
      <template #actions>
        <BaseButton data-test="storage-new" variant="primary" @click="openCreate">
          New Storage Policy
        </BaseButton>
      </template>
    </PageHeader>
    <StatusMessage
      :loading="storagePolicies.loading"
      :error="storagePolicies.error"
      :empty="storagePolicies.list.length === 0"
      empty-text="No storage policies defined yet."
    >
      <DataTable :columns="columns" :rows="storagePolicies.list">
        <template #table-row="{ column, row }">
          <button
            v-if="column.field === 'name'"
            :data-test="`storage-edit-${row.id}`"
            class="text-blue-600 hover:underline"
            @click="openEdit(row)"
          >
            {{ row.name }}
          </button>
          <span v-else-if="column.field === 'storageType'">{{ storageBackend(row.config) }}</span>
          <BaseButton
            v-else-if="column.field === 'actions'"
            :data-test="`storage-delete-${row.id}`"
            variant="danger"
            @click="confirmDelete(row.id)"
          >
            Delete
          </BaseButton>
          <span v-else>{{ row[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
    <StorageEditModal v-if="showModal" :policy="editingPolicy" @close="closeModal" @save="save" />
  </div>
</template>
```

Note: `StorageEditModal` needs a `name` option for the `findComponent({ name: ... })` lookups in the test to work with `<script setup>` — Vue's compiler infers the component name from the filename in dev tooling, but to be explicit and test-safe, confirm this resolves; if `findComponent({ name: 'StorageEditModal' })` fails to match, switch the two test lookups to `wrapper.findComponent(StorageEditModal)` importing the component directly instead (this is a known `@vue/test-utils` quirk with `<script setup>` SFC name inference — Vite's `vue` plugin does set `__name` from the filename by default, so the name-string lookup should work, but if Step 4 fails on this specifically, make this swap rather than adding an explicit `defineOptions({ name: ... })`, since no other component in this codebase does that).

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && npx vitest run src/views/StorageView.spec.js`
Expected: PASS. If any `findComponent({ name: 'StorageEditModal' })` assertion fails to locate the component, apply the fallback noted in Step 3 (import `StorageEditModal` directly in the spec and use `wrapper.findComponent(StorageEditModal)` in place of the name-string form), then re-run.

- [ ] **Step 5: Add the route**

In `web/src/router.js`, add after the `policy-edit` route:

```js
    { path: '/policies/:id/edit', name: 'policy-edit', component: () => import('./views/PolicyFormView.vue') },
    { path: '/storage', name: 'storage', component: () => import('./views/StorageView.vue') },
```

- [ ] **Step 6: Add the nav link**

In `web/src/components/Sidebar.vue`, add after the Policies link:

```html
    <router-link :to="{ name: 'policies' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Policies
    </router-link>
    <router-link :to="{ name: 'storage' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Storage
    </router-link>
```

- [ ] **Step 7: Update `router.spec.js`'s exact route-name list**

`web/src/router.spec.js` asserts the router's route names against a hardcoded `EXPECTED_NAMES` array — it will fail as soon as the `storage` route exists unless updated. In `web/src/router.spec.js`, add `'storage'` to the `EXPECTED_NAMES` array:

```js
const EXPECTED_NAMES = [
  'home',
  'clients',
  'client-new',
  'client-detail',
  'catalog',
  'policies',
  'policy-new',
  'policy-detail',
  'policy-edit',
  'storage',
  'jobs',
  'job-detail',
]
```

- [ ] **Step 8: Run the full web test suite**

Run: `cd web && npx vitest run`
Expected: PASS, no regressions across the whole suite, including `router.spec.js`.

- [ ] **Step 9: Commit**

```bash
git add web/src/views/StorageView.vue web/src/views/StorageView.spec.js web/src/router.js web/src/components/Sidebar.vue web/src/router.spec.js
git commit -m "feat(web): add Storage view, route, and nav link"
```

---

## Task 9: Documentation for the web UI changes

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md` (amend the entry from Task 5 if not yet merged, or add detail if it already landed separately — see Step 2)

**Interfaces:**
- None (docs only).

- [ ] **Step 1: Update `docs/components/web.md`**

Change the summary line near the top (currently "lists enrolled clients, browses catalog entries, manages backup policies (list/create/edit/delete), and browses fleet-wide jobs and their logs.") to:

```markdown
A small browser UI over [api-server](./api-server.md)'s REST API — lists enrolled clients,
browses catalog entries, manages backup policies and storage policies (list/create/edit/delete for
each, in separate sections), and browses fleet-wide jobs and their logs. **Not a mesh member:**
unlike every other control-plane component, `web` has no mTLS identity of its own; it's a static Vue
single-page app served by nginx, which reverse-proxies `/api/*` to `api-server` so the browser's
calls stay same-origin (no CORS changes were needed on `api-server`).
```

In the `## Pages` list, add a new bullet directly after the `/policies/:id/edit` bullet (before `/jobs`):

```markdown
- `/storage` — every storage policy (name, hostname, port, storage type), with a "New Storage
  Policy" action and a click-to-edit name column, both opening the same `StorageEditModal` (fields:
  name, hostname, port, storage type — `filesystem` only today — and, when `filesystem` is selected,
  a filesystem path). Kept fully separate from `/policies`: its own store
  (`stores/storagePolicies.js`), its own component folder (`components/storage/`), and no detail or
  form routes of its own — list and modal only. `/policies` itself now requests only `type=backup`
  policies, so a storage policy never appears there.
```

- [ ] **Step 2: Confirm the changelog entry from Task 5 covers this**

Task 5's `CHANGELOG.md` entry already describes the web UI's `Storage` section (see Task 5 Step 4). Re-read it now that the UI is built: confirm it still accurately describes what was built (list/create/edit via modal, separate from `/policies`). If Task 5 was completed and committed before this task, no further changelog edit is needed here — skip to Step 3. If the wording needs adjusting to match what was actually built, edit that same entry in place now (do not add a second entry for the same date/feature).

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document the Storage web UI section"
```

---

## Final verification

- [ ] **Step 1: Run the full Go test suite**

Run: `cd src && go build ./... && go test ./...`
Expected: PASS, no errors, no regressions in any package.

- [ ] **Step 2: Run the full web test suite**

Run: `cd web && npx vitest run`
Expected: PASS, no regressions.

- [ ] **Step 3: Manually verify in a browser**

Follow the `## Local development` instructions in `docs/components/web.md` to run the dev server against a locally running `api-server` (see `docs/components/api-server.md` and `make control-plane-up`). Navigate to `/storage`, create a storage policy through the modal, confirm it appears in the list, edit it, confirm the change persists on reload, delete it, confirm it's removed. Separately confirm `/policies` (backup) still works unaffected and never shows the storage policy created above.
