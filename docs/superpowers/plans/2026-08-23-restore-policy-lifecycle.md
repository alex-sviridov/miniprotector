# Restore Policy Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a restore/verify task run exactly once ever, correlate a restore policy's whole lifecycle (created → executed → deleted) under one shared `job_id`, and let `policy-server` automatically delete a restore policy once `api-server` reports its job has finished.

**Architecture:** `policy-server` generates a stable `job_id` at `CreatePolicy` time and logs `event="created"` under it; `agent` uses that same id (no longer generates its own) and runs the task once, ever; `api-server` gains its first gRPC server (mTLS, `"control-plane"`-role-gated) exposing `GetPolicyJobStatus`, reusing its existing Loki query machinery; `policy-server` polls that endpoint on a background tick and deletes a policy once its job is finished and a grace period has passed.

**Tech Stack:** Go, gRPC/protobuf (`protoc` + `protoc-gen-go`/`protoc-gen-go-grpc`, already installed), existing `common/mtls`/`common/connection` mTLS helpers, Loki (`lokiQuerier`), `testify` (`assert`/`require`).

## Global Constraints

- Never call `restoreJobID`'s old per-attempt (agent-side, timestamped) generation again — job ids are now generated once, by `policy-server`, at `CreatePolicy` time.
- `agent`'s `event="finish"` logging for restore/verify is **unchanged** — only `event="start"` is retired for these two kinds.
- `GetPolicyJobStatus`'s time window is a **clamp**, never a rejection (`since = max(policy_created_at, now - maxJobsWindow)`) — this is an internal service call, not a browser-facing endpoint, so an over-wide window must still be searched, just narrowed, never refused.
- Every new gRPC RPC this plan adds must be registered in the relevant service's `roleRequirements()` map, restricted to `"control-plane"` — mirroring `cmd/policy-server/authz.go`'s existing pattern exactly.
- Follow `.claude/CLAUDE.md`'s documentation rules: any `.proto` change needs its `docs/protocols/` doc updated/created before commit; any feature/behavior change needs the affected `docs/components/*.md` updated.

---

## File Structure

New files:
- `src/api/jobstatus.proto` — api-server's first gRPC service.
- `src/cmd/api-server/jobstatus_server.go` — `GetPolicyJobStatus` implementation.
- `src/cmd/api-server/jobstatus_server_test.go`
- `src/cmd/api-server/authz.go` — api-server's `roleRequirements()`.
- `src/cmd/policy-server/restore_cleanup.go` — the background sweep.
- `src/cmd/policy-server/restore_cleanup_test.go`
- `docs/protocols/jobstatus.md`

Modified files (by task, below): `src/api/policyserver.proto`, `src/cmd/api-server/{jobs.go,jobs_aggregator.go,server.go,main.go,arguments.go}` + their `_test.go` files, `src/cmd/policy-server/{restore_policy.go,write.go,main.go}` + their `_test.go` files, `src/cmd/agent/{backup.go,restore.go,reconcile.go}` + their `_test.go` files, `docs/components/{api-server,policy-server,agent}.md`, `docs/ARCHITECTURE.md`, `README.md`, `CHANGELOG.md`, `web/e2e/live-job-updates.spec.js`.

---

### Task 1: Proto — `job_id` on `Policy`, new `JobStatusService`

**Files:**
- Modify: `src/api/policyserver.proto`
- Create: `src/api/jobstatus.proto`
- Generated (via `make proto`, do not hand-edit): `src/api/policyserver.pb.go`, `src/api/jobstatus.pb.go`, `src/api/jobstatus_grpc.pb.go`

**Interfaces:**
- Produces: `pb.Policy.JobId` (string field), `pb.JobStatusServiceClient`/`pb.JobStatusServiceServer` interfaces, `pb.GetPolicyJobStatusRequest{JobId string, PolicyCreatedAt *timestamppb.Timestamp}`, `pb.GetPolicyJobStatusResponse{Finished bool, FinishedAt *timestamppb.Timestamp}`, `pb.UnimplementedJobStatusServiceServer`, `pb.JobStatusService_ServiceDesc`.

- [ ] **Step 1: Add `job_id` to the `Policy` message**

In `src/api/policyserver.proto`, inside `message Policy { ... }`, after the existing `bool overwrite = 21;` field, add:

```proto
  // "restore" policy only. Generated once by policy-server at CreatePolicy
  // time (stable for the policy's whole lifecycle -- a restore/verify task
  // now runs exactly once, so there is no per-attempt id to distinguish).
  // Shared with agent (which uses it verbatim as rwfs's --job-id) and with
  // policy-server's own "created"/"deleted" lifecycle log lines, so every
  // Loki line for one restore policy's execution correlates under this one
  // id. Not settable via CreatePolicyRequest -- server-computed, like id
  // (field 8).
  string job_id = 22;
```

- [ ] **Step 2: Create `src/api/jobstatus.proto`**

```proto
syntax = "proto3";

package jobstatusservice;

option go_package = "./proto";

import "google/protobuf/timestamp.proto";

// JobStatusService is api-server's sole gRPC surface (its REST API remains
// the deliberate non-mTLS surface for browsers/admin tools -- see
// docs/components/api-server.md). It answers one question for
// policy-server's restore-policy cleanup sweep: has a given job_id's Loki
// event=finish line been observed? Restricted to the "control-plane" role.
service JobStatusService {
  rpc GetPolicyJobStatus(GetPolicyJobStatusRequest) returns (GetPolicyJobStatusResponse);
}

message GetPolicyJobStatusRequest {
  string job_id = 1;
  // Lower bound for the Loki query. api-server clamps this to at most
  // maxJobsWindow (168h) before "now" -- a policy legitimately older than
  // that is still searched, just over a narrower recent window, since a
  // job that finishes at any point is always inside a future tick's
  // window regardless of how old the policy is.
  google.protobuf.Timestamp policy_created_at = 2;
}

message GetPolicyJobStatusResponse {
  bool finished = 1;
  // Set iff finished is true.
  google.protobuf.Timestamp finished_at = 2;
}
```

- [ ] **Step 3: Generate the Go bindings**

Run: `make proto`
Expected: exits 0, prints "Protobuf code generated in src/api/"; `src/api/jobstatus.pb.go`, `src/api/jobstatus_grpc.pb.go` now exist, and `src/api/policyserver.pb.go` now has a `JobId` field on its generated `Policy` struct.

- [ ] **Step 4: Verify it builds**

Run: `cd src && go build ./...`
Expected: succeeds (nothing yet references the new field/service, so this only proves the generated code itself is valid).

- [ ] **Step 5: Commit**

```bash
git add src/api/policyserver.proto src/api/jobstatus.proto src/api/policyserver.pb.go src/api/jobstatus.pb.go src/api/jobstatus_grpc.pb.go
git commit -m "proto: add Policy.job_id and JobStatusService"
```

---

### Task 2: api-server — `ApplyCreated` and finish-derived `SourceHost` for restore/verify

**Files:**
- Modify: `src/cmd/api-server/jobs.go`
- Test: `src/cmd/api-server/jobs_test.go`

**Interfaces:**
- Consumes: existing `jobEventAccumulator`, `jobEventLine{JobID, Hostname, Timestamp, Status}`, `jobDTO{JobID, Kind, SourceHost, StoreHost *string, StartedAt *int64, FinishedAt *int64, State}`.
- Produces: `(*jobEventAccumulator).ApplyCreated(e jobEventLine) jobDTO` — for later tasks/callers.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/jobs_test.go`:

```go
func TestApplyCreated_SeedsStartedAtAndInProgressWithoutSourceHost(t *testing.T) {
	acc := newJobEventAccumulator()
	got := acc.ApplyCreated(jobEventLine{JobID: "restore:x:1", Hostname: "policy-server-1", Timestamp: 1000, Status: ""})

	assert.Equal(t, "restore", got.Kind)
	assert.Equal(t, "in_progress", got.State)
	require.NotNil(t, got.StartedAt)
	assert.Equal(t, int64(1000), *got.StartedAt)
	assert.Empty(t, got.SourceHost, "created event must never attribute the job to policy-server's own hostname")
}

func TestApplyFinish_SetsSourceHostForRestoreAndVerifyKinds(t *testing.T) {
	for _, jobID := range []string{"restore:x:1", "verify:x:1"} {
		acc := newJobEventAccumulator()
		got := acc.ApplyFinish(jobEventLine{JobID: jobID, Hostname: "web-01", Timestamp: 2000, Status: "success"})
		assert.Equal(t, "web-01", got.SourceHost, "job_id=%s", jobID)
	}
}

func TestApplyFinish_DoesNotSetSourceHostForOtherKinds(t *testing.T) {
	acc := newJobEventAccumulator()
	got := acc.ApplyFinish(jobEventLine{JobID: "operating-refresh:1752400500", Hostname: "web-01", Timestamp: 2000, Status: "success"})
	assert.Empty(t, got.SourceHost)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestApplyCreated|TestApplyFinish_SetsSourceHostForRestoreAndVerifyKinds' -v`
Expected: FAIL — `ApplyCreated` undefined, and the second test fails because `ApplyFinish` doesn't yet set `SourceHost` for kind restore/verify.

- [ ] **Step 3: Implement**

In `src/cmd/api-server/jobs.go`, add a new method right after `ApplyStart`:

```go
// ApplyCreated folds one event=created line in (policy-server's "restore
// policy created, waiting for client to connect" log) -- like ApplyStart,
// it seeds StartedAt/State, but unlike ApplyStart it never sets SourceHost:
// the line's own hostname is policy-server's, not the node that will
// eventually execute the restore, so attributing the job to it here would
// be wrong. SourceHost for restore/verify comes from the finish line
// instead -- see ApplyFinish.
func (a *jobEventAccumulator) ApplyCreated(e jobEventLine) jobDTO {
	j := a.get(e.JobID)
	ts := e.Timestamp
	j.StartedAt = &ts
	return *j
}
```

Then extend the existing `ApplyFinish` (its `if j.Kind == "backup" { ... }` block):

```go
func (a *jobEventAccumulator) ApplyFinish(e jobEventLine) jobDTO {
	j := a.get(e.JobID)
	ts := e.Timestamp
	j.FinishedAt = &ts
	j.State = e.Status
	if j.Kind == "backup" {
		host := e.Hostname
		j.StoreHost = &host
	}
	if j.Kind == "restore" || j.Kind == "verify" {
		j.SourceHost = e.Hostname
	}
	return *j
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run 'TestApplyCreated|TestApplyFinish' -v`
Expected: PASS

- [ ] **Step 5: Run the full api-server test suite (no regressions)**

Run: `cd src && go test ./cmd/api-server/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_test.go
git commit -m "feat(api-server): ApplyCreated event and finish-derived source_host for restore/verify"
```

---

### Task 3: api-server — widen label selectors, add the `created` query pass everywhere

**Files:**
- Modify: `src/cmd/api-server/jobs.go`, `src/cmd/api-server/jobs_aggregator.go`
- Test: `src/cmd/api-server/jobs_test.go`, `src/cmd/api-server/jobs_aggregator_test.go`

**Interfaces:**
- Consumes: `queryEvent`, `ApplyCreated`/`ApplyStart`/`ApplyFinish` (Task 2).
- Produces: `pairJobEvents(starts, finishes, createds []jobEventLine) []jobDTO` (signature change — both call sites in this task must be updated together).

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/jobs_test.go`:

```go
func TestBinariesForKind_RestoreAndVerifyIncludePolicyServer(t *testing.T) {
	assert.Equal(t, "brfs|bwfs", binariesForKind("backup"))
	assert.Equal(t, "agent", binariesForKind("bootstrap-refresh"))
	assert.Equal(t, "agent", binariesForKind("operating-refresh"))
	assert.Equal(t, "agent", binariesForKind("policy-update"))
	assert.Equal(t, "agent|policy-server", binariesForKind("verify"))
	assert.Equal(t, "agent|policy-server", binariesForKind("restore"))
	assert.Equal(t, "agent|brfs|bwfs|policy-server", binariesForKind(""))
}

func TestPairJobEvents_CreatedLineAloneShowsInProgressWithNoSourceHost(t *testing.T) {
	jobs := pairJobEvents(nil, nil, []jobEventLine{
		{JobID: "restore:x:1", Hostname: "policy-server-1", Timestamp: 500},
	})
	require.Len(t, jobs, 1)
	assert.Equal(t, "in_progress", jobs[0].State)
	assert.Empty(t, jobs[0].SourceHost)
}

func TestPairJobEvents_CreatedThenFinishPopulatesSourceHostAndState(t *testing.T) {
	jobs := pairJobEvents(nil,
		[]jobEventLine{{JobID: "restore:x:1", Hostname: "web-01", Timestamp: 900, Status: "success"}},
		[]jobEventLine{{JobID: "restore:x:1", Hostname: "policy-server-1", Timestamp: 500}},
	)
	require.Len(t, jobs, 1)
	assert.Equal(t, "success", jobs[0].State)
	assert.Equal(t, "web-01", jobs[0].SourceHost)
	require.NotNil(t, jobs[0].StartedAt)
	assert.Equal(t, int64(500), *jobs[0].StartedAt)
}

func TestHandleGetJobLogs_SelectorIncludesPolicyServer(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs|policy-server"} | job_id="restore:x:1"`: {},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/restore:x:1/logs", nil)
	req.SetPathValue("job_id", "restore:x:1")
	w := httptest.NewRecorder()
	srv.handleGetJobLogs(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, fake.calls, "must have queried the widened selector exactly")
}
```

Add to `src/cmd/api-server/jobs_aggregator_test.go` (check its existing imports/helpers first — mirror whatever fake tailer/loki construction it already uses for `newJobAggregator`):

```go
func TestIngestTailMessage_CreatedEventFoldsIntoInProgressState(t *testing.T) {
	a := newJobAggregator(&fakeLokiClient{}, nil, testLogger())
	a.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{
		{
			Stream: map[string]string{"hostname": "policy-server-1"},
			Values: []lokiValue{{
				Timestamp: 500_000_000_000,
				Metadata:  map[string]string{"job_id": "restore:x:1", "event": "created"},
			}},
		},
	}})

	a.mu.Lock()
	got, ok := a.jobs["restore:x:1"]
	a.mu.Unlock()
	require.True(t, ok)
	assert.Equal(t, "in_progress", got.State)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestBinariesForKind_RestoreAndVerify|TestPairJobEvents_Created|TestHandleGetJobLogs_SelectorIncludesPolicyServer|TestIngestTailMessage_CreatedEvent' -v`
Expected: FAIL (compile error on `pairJobEvents` arity, wrong selector strings, unhandled "created" event).

- [ ] **Step 3: Implement — `jobs.go`**

`binariesForKind`:

```go
func binariesForKind(kind string) string {
	switch kind {
	case "backup":
		return "brfs|bwfs"
	case "verify", "restore":
		return "agent|policy-server"
	case "bootstrap-refresh", "operating-refresh", "policy-update":
		return "agent"
	default:
		return "agent|brfs|bwfs|policy-server"
	}
}
```

`pairJobEvents`:

```go
// pairJobEvents groups start/created/finish lines by job_id into one
// jobDTO each. created and start are mutually exclusive per job kind
// (restore/verify use created; every other kind uses start) so applying
// both here is never a real conflict -- whichever is present for a given
// job_id seeds StartedAt/State, and finish always applies last.
func pairJobEvents(starts, finishes, createds []jobEventLine) []jobDTO {
	acc := newJobEventAccumulator()
	for _, e := range createds {
		acc.ApplyCreated(e)
	}
	for _, e := range starts {
		acc.ApplyStart(e)
	}
	for _, e := range finishes {
		acc.ApplyFinish(e)
	}
	return acc.All()
}
```

In `handleListJobs`, after the existing `finishLabelSelector` declaration, add:

```go
	// created lines are policy-server's own -- never narrowed by
	// source_host, same reasoning as finishLabelSelector: source_host names
	// the eventual executing node, not policy-server, so narrowing by it
	// here would silently exclude every created line.
	createdLabelSelector := fmt.Sprintf(`{binary=~"%s"}`, binarySelector)
```

Replace:

```go
	jobs := pairJobEvents(starts, finishes)
```

with:

```go
	createds, createdsTruncated, err := queryEvent(r.Context(), s.loki, createdLabelSelector, "created", since, until)
	if err != nil {
		s.logger.Error("handleListJobs: query created events failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "query loki: "+err.Error())
		return
	}

	jobs := pairJobEvents(starts, finishes, createds)
```

And update the final response line:

```go
	writeJSON(w, http.StatusOK, map[string]any{"data": filtered, "truncated": startsTruncated || finishesTruncated || createdsTruncated})
```

In `handleGetJobLogs`, replace every occurrence of the literal `"agent|brfs|bwfs|rwfs"` (four places: the base `labelSelector` and the three `switch` branches) with `"agent|brfs|bwfs|rwfs|policy-server"`.

- [ ] **Step 4: Implement — `jobs_aggregator.go`**

In `ingestTailMessage`, replace:

```go
			if jobID == "" || (event != "start" && event != "finish") {
				continue
			}
```

with:

```go
			if jobID == "" || (event != "start" && event != "finish" && event != "created") {
				continue
			}
```

and replace:

```go
			var updated jobDTO
			if event == "start" {
				updated = acc.ApplyStart(line)
			} else {
				updated = acc.ApplyFinish(line)
			}
```

with:

```go
			var updated jobDTO
			switch event {
			case "start":
				updated = acc.ApplyStart(line)
			case "created":
				updated = acc.ApplyCreated(line)
			default:
				updated = acc.ApplyFinish(line)
			}
```

In `reconcile`, replace:

```go
	const selector = `{binary=~"agent|brfs|bwfs"}`

	starts, _, err := queryEvent(ctx, a.loki, selector, "start", since, until)
	if err != nil {
		return err
	}
	finishes, _, err := queryEvent(ctx, a.loki, selector, "finish", since, until)
	if err != nil {
		return err
	}
	jobs := pairJobEvents(starts, finishes)
```

with:

```go
	const selector = `{binary=~"agent|brfs|bwfs|policy-server"}`

	starts, _, err := queryEvent(ctx, a.loki, selector, "start", since, until)
	if err != nil {
		return err
	}
	finishes, _, err := queryEvent(ctx, a.loki, selector, "finish", since, until)
	if err != nil {
		return err
	}
	createds, _, err := queryEvent(ctx, a.loki, selector, "created", since, until)
	if err != nil {
		return err
	}
	jobs := pairJobEvents(starts, finishes, createds)
```

In `tailLoop`, replace the tail query literal `` `{binary=~"agent|brfs|bwfs"} | job_id=~".+"` `` with `` `{binary=~"agent|brfs|bwfs|policy-server"} | job_id=~".+"` ``.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v -run 'TestBinariesForKind_RestoreAndVerify|TestPairJobEvents_Created|TestHandleGetJobLogs_SelectorIncludesPolicyServer|TestIngestTailMessage_CreatedEvent'`
Expected: PASS

- [ ] **Step 6: Run the full api-server suite**

Run: `cd src && go test ./cmd/api-server/...`
Expected: PASS — this also confirms every existing test using the old `binariesForKind`/selector literals/`pairJobEvents(starts, finishes)` two-arg form was updated; fix any that still reference the old signature or old selector strings before moving on.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_aggregator.go src/cmd/api-server/jobs_test.go src/cmd/api-server/jobs_aggregator_test.go
git commit -m "feat(api-server): widen job queries to include policy-server's created/deleted lines"
```

---

### Task 4: api-server — `GetPolicyJobStatus`

**Files:**
- Create: `src/cmd/api-server/jobstatus_server.go`
- Create: `src/cmd/api-server/jobstatus_server_test.go`
- Modify: `src/cmd/api-server/server.go` (embed `pb.UnimplementedJobStatusServiceServer`)

**Interfaces:**
- Consumes: `s.loki lokiQuerier`, `queryEvent`, `jobEventLine`, `maxJobsWindow` (all from Task 2/3 and existing `jobs.go`), `pb.GetPolicyJobStatusRequest`/`Response` (Task 1).
- Produces: `(*server).GetPolicyJobStatus(ctx, *pb.GetPolicyJobStatusRequest) (*pb.GetPolicyJobStatusResponse, error)`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/api-server/jobstatus_server_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGetPolicyJobStatus_FinishedReturnsTrueWithTimestamp(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|policy-server"} | job_id="restore:x:1" | event="finish"`: {
			{Stream: map[string]string{"hostname": "web-01"}, Values: []lokiValue{
				{Timestamp: 1_700_000_000_000_000_000, Metadata: map[string]string{"job_id": "restore:x:1", "status": "success"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake

	createdAt := time.Unix(1_699_999_000, 0)
	resp, err := srv.GetPolicyJobStatus(context.Background(), &pb.GetPolicyJobStatusRequest{
		JobId:           "restore:x:1",
		PolicyCreatedAt: timestamppb.New(createdAt),
	})

	require.NoError(t, err)
	assert.True(t, resp.GetFinished())
	assert.Equal(t, int64(1_700_000_000), resp.GetFinishedAt().AsTime().Unix())
}

func TestGetPolicyJobStatus_NotFoundReturnsFalse(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake

	resp, err := srv.GetPolicyJobStatus(context.Background(), &pb.GetPolicyJobStatusRequest{
		JobId:           "restore:x:1",
		PolicyCreatedAt: timestamppb.New(time.Now()),
	})

	require.NoError(t, err)
	assert.False(t, resp.GetFinished())
}

func TestGetPolicyJobStatus_ClampsSinceToMaxJobsWindow(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake

	oldCreatedAt := time.Now().Add(-30 * 24 * time.Hour) // far older than maxJobsWindow (168h)
	_, err := srv.GetPolicyJobStatus(context.Background(), &pb.GetPolicyJobStatusRequest{
		JobId:           "restore:x:1",
		PolicyCreatedAt: timestamppb.New(oldCreatedAt),
	})
	require.NoError(t, err)

	assert.WithinDuration(t, time.Now().Add(-maxJobsWindow), fake.lastStart, 5*time.Second,
		"since must be clamped to maxJobsWindow before now, not the far-older policy_created_at")
}

func TestGetPolicyJobStatus_MissingJobIDRejected(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}

	_, err := srv.GetPolicyJobStatus(context.Background(), &pb.GetPolicyJobStatusRequest{
		PolicyCreatedAt: timestamppb.New(time.Now()),
	})
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestGetPolicyJobStatus -v`
Expected: FAIL — `GetPolicyJobStatus` undefined on `*server`.

- [ ] **Step 3: Implement**

Create `src/cmd/api-server/jobstatus_server.go`:

```go
// src/cmd/api-server/jobstatus_server.go
package main

import (
	"context"
	"fmt"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetPolicyJobStatus answers policy-server's restore-cleanup sweep: has
// job_id's event=finish line been observed? Reuses queryEvent/jobEventLine
// unchanged -- the only new behavior is the job_id-scoped selector and the
// policy_created_at-anchored, maxJobsWindow-clamped time window (see
// docs/superpowers/specs/2026-08-23-restore-policy-lifecycle-design.md).
func (s *server) GetPolicyJobStatus(ctx context.Context, req *pb.GetPolicyJobStatusRequest) (*pb.GetPolicyJobStatusResponse, error) {
	if req.GetJobId() == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id is required")
	}

	until := time.Now()
	since := req.GetPolicyCreatedAt().AsTime()
	if cutoff := until.Add(-maxJobsWindow); since.Before(cutoff) {
		since = cutoff
	}

	selector := fmt.Sprintf(`{binary=~"agent|policy-server"} | job_id="%s"`, req.GetJobId())
	finishes, _, err := queryEvent(ctx, s.loki, selector, "finish", since, until)
	if err != nil {
		return nil, status.Error(codes.Internal, "query loki: "+err.Error())
	}
	if len(finishes) == 0 {
		return &pb.GetPolicyJobStatusResponse{Finished: false}, nil
	}
	return &pb.GetPolicyJobStatusResponse{
		Finished:   true,
		FinishedAt: timestamppb.New(time.Unix(finishes[0].Timestamp, 0)),
	}, nil
}
```

In `src/cmd/api-server/server.go`, add the embedded field to the `server` struct so it satisfies `pb.JobStatusServiceServer`:

```go
type server struct {
	clientManager      clientManagerClient
	clientManagerAdmin clientManagerAdminClient
	catalog            catalogQueryClient
	policy             policyServiceClient
	loki               lokiQuerier
	lokiTail           lokiTailer
	wsTickets          *wsTicketStore
	aggregator         *jobAggregator
	logger             *slog.Logger
	adhocPolicyTimeout time.Duration
	pb.UnimplementedJobStatusServiceServer
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run TestGetPolicyJobStatus -v`
Expected: PASS

- [ ] **Step 5: Run the full api-server suite**

Run: `cd src && go test ./cmd/api-server/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobstatus_server.go src/cmd/api-server/jobstatus_server_test.go src/cmd/api-server/server.go
git commit -m "feat(api-server): implement GetPolicyJobStatus"
```

---

### Task 5: api-server — mTLS gRPC listener, role gate, config, docs

**Files:**
- Create: `src/cmd/api-server/authz.go`
- Modify: `src/cmd/api-server/main.go`, `src/cmd/api-server/arguments.go`, `src/common/config/config.go`
- Modify: `docs/components/api-server.md`, `docs/ARCHITECTURE.md`

**Interfaces:**
- Consumes: `connection.StartServer(ctx, logger, port, certsDir, roleRequirements, register func(*grpc.Server))` (existing, `common/connection/server.go`), `pb.RegisterJobStatusServiceServer`, `pb.JobStatusService_ServiceDesc`.
- Produces: `conf.APIServerJobStatusPort int` (new config key, default `8091`), `Arguments.JobStatusPort int`.

- [ ] **Step 1: Add the config key**

In `src/common/config/config.go`'s `Config` struct, add a field after `APIServerToken`:

```go
	APIServerJobStatusPort           int
```

In `ParseConfig`'s default literal, add:

```go
		APIServerJobStatusPort:           8091,
```

In the `switch key` block, add a case near the other `APIServer*` handling (mirror `CheckinRetentionSec`'s int-parsing shape exactly):

```go
		case "APIServerJobStatusPort":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid APIServerJobStatusPort value at line %d: %s", lineNum, value)
			}
			config.APIServerJobStatusPort = number
			foundFields["APIServerJobStatusPort"] = true
```

- [ ] **Step 2: Add the CLI flag**

In `src/cmd/api-server/arguments.go`, add to `Arguments`:

```go
type Arguments struct {
	Port           int
	JobStatusPort  int
	Token          string
	Debug          bool
}
```

Add the flag and validation:

```go
	cmd.Flags().IntVar(&args.JobStatusPort, "job-status-port", conf.APIServerJobStatusPort, "Port the internal control-plane-only job-status gRPC service listens on")
```

```go
	if err := common.ValidatePort(args.JobStatusPort); err != nil {
		return nil, fmt.Errorf("job-status-port error: %w", err)
	}
```

- [ ] **Step 3: Add `authz.go`**

Create `src/cmd/api-server/authz.go`:

```go
package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is api-server's per-RPC authorization matrix for its
// gRPC surface (its REST API is unauthenticated by role -- see
// docs/SECURITY.md). GetPolicyJobStatus is called only by policy-server's
// restore-cleanup sweep, so it is restricted to the "control-plane" role,
// mirroring cmd/policy-server/authz.go's identical pattern.
func roleRequirements() map[string][]string {
	svc := pb.JobStatusService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + svc + "/GetPolicyJobStatus": {"control-plane"},
	}
}
```

- [ ] **Step 4: Wire the listener into `main.go`**

In `src/cmd/api-server/main.go`, add the import `"google.golang.org/grpc"` and, after the existing `go srv.aggregator.Start(signalCtx)` line, add:

```go
	go func() {
		if err := connection.StartServer(signalCtx, logger, arguments.JobStatusPort, certsDir, roleRequirements(), func(s *grpc.Server) {
			pb.RegisterJobStatusServiceServer(s, srv)
		}); err != nil {
			logger.Error("job-status server failed", "error", err)
		}
	}()
```

- [ ] **Step 5: Verify it builds**

Run: `cd src && go build ./...`
Expected: succeeds.

- [ ] **Step 6: Run the full api-server suite**

Run: `cd src && go test ./cmd/api-server/... ./common/config/...`
Expected: PASS.

- [ ] **Step 7: Update docs**

In `docs/components/api-server.md`: add a section describing the new gRPC listener (`APIServerJobStatusPort`, default `8091`), the `GetPolicyJobStatus` RPC, and that it's the only inbound mTLS surface on api-server (contrast with the REST API's non-mesh nature, already documented there). Add the new config key to its Configuration Keys table (mirror the existing table's row format, e.g. `| \`api_server_job_status_port\` | 8091 | Port the internal control-plane-only job-status gRPC service listens on |`).

In `docs/ARCHITECTURE.md`: update the component table/mermaid diagram to show api-server now accepting one inbound mTLS connection (from policy-server), not only outbound ones — find the existing api-server row/node and add this edge alongside its existing outbound edges to catalog/clientmanager-api/policy-server/log-gateway.

- [ ] **Step 8: Commit**

```bash
git add src/common/config/config.go src/cmd/api-server/main.go src/cmd/api-server/arguments.go src/cmd/api-server/authz.go docs/components/api-server.md docs/ARCHITECTURE.md
git commit -m "feat(api-server): serve JobStatusService over mTLS gRPC"
```

---

### Task 6: policy-server — `RestorePolicy.JobID`

**Files:**
- Modify: `src/cmd/policy-server/restore_policy.go`
- Test: `src/cmd/policy-server/restore_policy_test.go`

**Interfaces:**
- Produces: `RestorePolicy.JobID string` field (JSON tag `job_id`), carried through `Clone()` and `ToProto()`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/restore_policy_test.go` (check its existing imports/style first, e.g. how it constructs a `RestorePolicy{}` literal and calls `ToProto`/`Clone`):

```go
func TestRestorePolicy_ToProtoCarriesJobID(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		JobID:      "restore:x:1700000000",
	}
	pp := p.ToProto(false)
	assert.Equal(t, "restore:x:1700000000", pp.JobId)
}

func TestRestorePolicy_CloneCarriesJobID(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		JobID:      "restore:x:1700000000",
	}
	cloned := p.Clone().(*RestorePolicy)
	assert.Equal(t, "restore:x:1700000000", cloned.JobID)
}

func TestRestorePolicy_JobIDRoundTripsThroughJSON(t *testing.T) {
	p := &RestorePolicy{JobID: "verify:x:1700000000"}
	data, err := json.Marshal(p)
	require.NoError(t, err)

	parsed, err := parseRestorePolicyJSON(data)
	require.NoError(t, err)
	assert.Equal(t, "verify:x:1700000000", parsed.(*RestorePolicy).JobID)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestRestorePolicy_.*JobID -v`
Expected: FAIL — `JobID` undefined on `RestorePolicy`, `pp.JobId` always empty.

- [ ] **Step 3: Implement**

In `src/cmd/policy-server/restore_policy.go`, add the field to the struct:

```go
type RestorePolicy struct {
	PolicyBase
	StoragePolicyID string        `json:"storage_policy_id"`
	Rules           []RestoreRule `json:"rules"`
	Mode            string        `json:"mode,omitempty"`
	Overwrite       bool          `json:"overwrite,omitempty"`
	// JobID correlates this policy's whole lifecycle (created -> executed
	// -> deleted) under one Loki job_id, shared with agent (used verbatim
	// as rwfs's --job-id) and with policy-server's own "created"/"deleted"
	// lifecycle log lines. Generated once, in CreatePolicy, never
	// regenerated -- a restore/verify task now runs exactly once, so
	// there is no retry to distinguish with a fresh id. See
	// docs/superpowers/specs/2026-08-23-restore-policy-lifecycle-design.md.
	JobID string `json:"job_id,omitempty"`
}
```

Update `Clone()`:

```go
func (p *RestorePolicy) Clone() Policy {
	rules := make([]RestoreRule, len(p.Rules))
	copy(rules, p.Rules)
	return &RestorePolicy{
		PolicyBase:      p.PolicyBase.clone(),
		StoragePolicyID: p.StoragePolicyID,
		Rules:           rules,
		Mode:            p.Mode,
		Overwrite:       p.Overwrite,
		JobID:           p.JobID,
	}
}
```

Update `ToProto()`'s `pb.Policy{...}` literal, adding `JobId: p.JobID,` alongside the existing `Mode`/`Overwrite` fields.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -run TestRestorePolicy_.*JobID -v`
Expected: PASS

- [ ] **Step 5: Run the full policy-server suite**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/restore_policy.go src/cmd/policy-server/restore_policy_test.go
git commit -m "feat(policy-server): add RestorePolicy.JobID"
```

---

### Task 7: policy-server — `CreatePolicy` generates `JobID` and logs `event=created`

**Files:**
- Modify: `src/cmd/policy-server/write.go`
- Test: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: `RestorePolicy.JobID` (Task 6), `restoreTaskID`-style prefix convention (`"restore:"` for `Mode == "restore"`, `"verify:"` otherwise).
- Produces: `restorePolicyJobID(name, mode string, now time.Time) string` (policy-server's own copy of the naming scheme agent's now-deleted `restoreJobID` used).

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/write_test.go`:

```go
func TestCreatePolicy_RestoreGeneratesJobIDWithVerifyPrefix(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "web01-emergency",
		Type:            "restore",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/var/www", Include: true}},
	})

	require.NoError(t, err)
	require.NotEmpty(t, resp.JobId)
	assert.True(t, strings.HasPrefix(resp.JobId, "verify:web01-emergency:"), "got %q", resp.JobId)
}

func TestCreatePolicy_RestoreModeGeneratesJobIDWithRestorePrefix(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "web01-actual-restore",
		Type:            "restore",
		Mode:            "restore",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/var/www", Include: true}},
	})

	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(resp.JobId, "restore:web01-actual-restore:"), "got %q", resp.JobId)
}

func TestCreatePolicy_RestoreLogsCreatedEventUnderTheSameJobID(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	logger, buf := testLoggerWithBuffer()
	srv := NewPolicyServerServer(c, dir, logger, newTestCheckinStore(t))
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "web01-emergency",
		Type:            "restore",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/var/www", Include: true}},
	})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"event":"created"`)
	assert.Contains(t, out, resp.JobId)
	assert.Contains(t, out, "waiting for client to connect")
}

func TestCreatePolicy_NonRestorePolicyLogsNoCreatedEvent(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	logger, buf := testLoggerWithBuffer()
	srv := NewPolicyServerServer(c, dir, logger, newTestCheckinStore(t))

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "east", Type: "storage", Port: 8080, Config: `{}`,
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"bwfs-east"}},
	})
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), `"event":"created"`)
}
```

Add `testLoggerWithBuffer` to `src/cmd/policy-server/write_test.go` (it doesn't exist in this package yet — mirror `cmd/agent/reconcile_test.go`'s exact implementation):

```go
func testLoggerWithBuffer() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}
```

(`bytes` is likely already imported in `write_test.go` for other reasons — add the import if not.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run 'TestCreatePolicy_Restore.*JobID|TestCreatePolicy_Restore.*Created|TestCreatePolicy_NonRestore' -v`
Expected: FAIL — `resp.JobId` empty, no `event":"created"` logged.

- [ ] **Step 3: Implement**

In `src/cmd/policy-server/write.go`, add the job-id generator near `slugify`:

```go
// restorePolicyJobID builds the stable job_id for a restore policy's whole
// lifecycle -- generated once, here, at CreatePolicy time. Prefix
// determines the job's "kind" everywhere downstream (agent, api-server's
// kindFromJobID): "restore:" for mode=="restore" (rwfs restore), "verify:"
// for every other mode (rwfs verify) -- mirrors the prefix convention
// cmd/agent/restore.go's restoreTaskID already uses for the task id.
func restorePolicyJobID(name, mode string, now time.Time) string {
	if mode == "restore" {
		return fmt.Sprintf("restore:%s:%d", name, now.UnixNano())
	}
	return fmt.Sprintf("verify:%s:%d", name, now.UnixNano())
}
```

(`now.UnixNano()`, not `now.Unix()`, since — unlike agent's old per-attempt id, which only needed second-level uniqueness against a human-driven retry cadence — this is generated once per `CreatePolicy` call and two calls in the same process could otherwise land in the same second under test or heavy load.)

In `buildPolicyForCreate`'s `if req.GetType() == "restore" { ... }` branch, set the field when constructing the `&RestorePolicy{...}` literal:

```go
		return &RestorePolicy{
			PolicyBase:      base,
			StoragePolicyID: req.GetStoragePolicyId(),
			Rules:           rules,
			Mode:            req.GetMode(),
			Overwrite:       req.GetOverwrite(),
			JobID:           restorePolicyJobID(req.GetName(), req.GetMode(), now),
		}, nil
```

In `CreatePolicy`, right after the existing final `s.logger.Info("CreatePolicy", "id", created.Meta().ID, "name", created.Meta().Name, "path", filePath)` line, add:

```go
	if rp, ok := created.(*RestorePolicy); ok {
		s.logger.Info("restore policy created, waiting for client to connect",
			"policy", rp.Meta().ID, "job_id", rp.JobID, "event", "created")
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -run 'TestCreatePolicy_Restore.*JobID|TestCreatePolicy_Restore.*Created|TestCreatePolicy_NonRestore' -v`
Expected: PASS

- [ ] **Step 5: Run the full policy-server suite**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go
git commit -m "feat(policy-server): generate restore policy JobID and log its creation"
```

---

### Task 8: policy-server — the cleanup sweep

**Files:**
- Create: `src/cmd/policy-server/restore_cleanup.go`
- Create: `src/cmd/policy-server/restore_cleanup_test.go`

**Interfaces:**
- Consumes: `s.cache.Policies() []Policy` (existing), `RestorePolicy.JobID`/`.Meta()` (Task 6), `s.DeletePolicy` (existing, in-process call), `pb.JobStatusServiceClient` (Task 1).
- Produces: `(*policyServerServer).sweepRestorePolicies(ctx, jobStatus pb.JobStatusServiceClient, gracePeriod time.Duration, logger *slog.Logger)`, `(*policyServerServer).runRestoreCleanup(ctx, jobStatus pb.JobStatusServiceClient, interval, gracePeriod time.Duration, logger *slog.Logger)`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/policy-server/restore_cleanup_test.go`:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeJobStatusClient struct {
	resp  *pb.GetPolicyJobStatusResponse
	err   error
	calls []*pb.GetPolicyJobStatusRequest
}

func (f *fakeJobStatusClient) GetPolicyJobStatus(ctx context.Context, req *pb.GetPolicyJobStatusRequest, opts ...grpc.CallOption) (*pb.GetPolicyJobStatusResponse, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func createTestRestorePolicy(t *testing.T, srv *policyServerServer, name, storageID string) *pb.Policy {
	t.Helper()
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            name,
		Type:            "restore",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/var/www", Include: true}},
	})
	require.NoError(t, err)
	return resp
}

func TestSweepRestorePolicies_DeletesWhenFinishedPastGracePeriod(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	p := createTestRestorePolicy(t, srv, "web01-emergency", storageID)

	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{
		Finished:   true,
		FinishedAt: timestamppb.New(time.Now().Add(-time.Hour)),
	}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, testLogger())

	_, ok := srv.cache.FindByID(p.Id)
	assert.False(t, ok, "policy must be deleted")
	require.Len(t, fake.calls, 1)
	assert.Equal(t, p.JobId, fake.calls[0].JobId)
}

func TestSweepRestorePolicies_SkipsWhenNotFinished(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	p := createTestRestorePolicy(t, srv, "web01-emergency", storageID)

	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{Finished: false}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, testLogger())

	_, ok := srv.cache.FindByID(p.Id)
	assert.True(t, ok, "policy must survive")
}

func TestSweepRestorePolicies_SkipsWithinGracePeriod(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	p := createTestRestorePolicy(t, srv, "web01-emergency", storageID)

	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{
		Finished:   true,
		FinishedAt: timestamppb.New(time.Now()), // just finished
	}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Hour, testLogger())

	_, ok := srv.cache.FindByID(p.Id)
	assert.True(t, ok, "policy must survive until the grace period elapses")
}

func TestSweepRestorePolicies_SkipsOnQueryError(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	p := createTestRestorePolicy(t, srv, "web01-emergency", storageID)

	fake := &fakeJobStatusClient{err: assert.AnError}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, testLogger())

	_, ok := srv.cache.FindByID(p.Id)
	assert.True(t, ok, "a query failure must never delete the policy")
}

func TestSweepRestorePolicies_IgnoresNonRestorePolicies(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	_ = createTestStoragePolicy(t, srv, "bwfs-east", 8080)

	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{Finished: true, FinishedAt: timestamppb.New(time.Now().Add(-time.Hour))}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, testLogger())

	assert.Empty(t, fake.calls, "must never query job status for a non-restore policy")
	_, err := os.Stat(filepath.Join(dir, "storage", "storage-for-bwfs-east.json"))
	require.NoError(t, err, "the storage policy must be untouched")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestSweepRestorePolicies -v`
Expected: FAIL — `sweepRestorePolicies` undefined.

- [ ] **Step 3: Implement**

Create `src/cmd/policy-server/restore_cleanup.go`:

```go
// restore_cleanup.go runs policy-server's own background sweep for
// restore-type policies: on a fixed tick, ask api-server whether each
// cached restore policy's one-shot job has finished, and delete the
// policy once it has, past a grace period. Mirrors checkin.go's
// runCheckinCleanup shape. See
// docs/superpowers/specs/2026-08-23-restore-policy-lifecycle-design.md.
package main

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// runRestoreCleanup runs sweepRestorePolicies every interval until ctx is
// done.
func (s *policyServerServer) runRestoreCleanup(ctx context.Context, jobStatus pb.JobStatusServiceClient, interval, gracePeriod time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepRestorePolicies(ctx, jobStatus, gracePeriod, logger)
		}
	}
}

// sweepRestorePolicies checks every cached "restore"-type policy against
// api-server's job status and deletes the ones whose job finished more
// than gracePeriod ago. A query failure for one policy is logged and
// skipped -- retried on the next tick, never fatal to the sweep as a
// whole (same best-effort direction as DeletePolicy's own check-in
// cleanup, write.go).
func (s *policyServerServer) sweepRestorePolicies(ctx context.Context, jobStatus pb.JobStatusServiceClient, gracePeriod time.Duration, logger *slog.Logger) {
	for _, p := range s.cache.Policies() {
		rp, ok := p.(*RestorePolicy)
		if !ok {
			continue
		}

		resp, err := jobStatus.GetPolicyJobStatus(ctx, &pb.GetPolicyJobStatusRequest{
			JobId:           rp.JobID,
			PolicyCreatedAt: timestamppb.New(rp.Meta().CreatedAt),
		})
		if err != nil {
			logger.Error("restore cleanup: GetPolicyJobStatus failed", "policy", rp.Meta().ID, "job_id", rp.JobID, "error", err)
			continue
		}
		if !resp.GetFinished() {
			continue
		}
		if time.Since(resp.GetFinishedAt().AsTime()) < gracePeriod {
			continue
		}

		logger.Info("restore policy deleted as job found", "policy", rp.Meta().ID, "job_id", rp.JobID, "event", "deleted")
		if _, err := s.DeletePolicy(ctx, &pb.DeletePolicyRequest{Id: rp.Meta().ID}); err != nil {
			logger.Error("restore cleanup: DeletePolicy failed", "policy", rp.Meta().ID, "error", err)
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -run TestSweepRestorePolicies -v`
Expected: PASS

- [ ] **Step 5: Run the full policy-server suite**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/restore_cleanup.go src/cmd/policy-server/restore_cleanup_test.go
git commit -m "feat(policy-server): sweep and delete restore policies whose job has finished"
```

---

### Task 9: policy-server — wire the sweep, config, docs

**Files:**
- Modify: `src/cmd/policy-server/main.go`, `src/common/config/config.go`
- Modify: `docs/components/policy-server.md`
- Modify: `deploy/control-plane/policy-server/local.conf`

**Interfaces:**
- Consumes: `connection.Connect(host, port, timeoutSec, certsDir) (*grpc.ClientConn, error)` (existing), `pb.NewJobStatusServiceClient(conn)`, `runRestoreCleanup` (Task 8).
- Produces: `conf.APIServerHost string`, `conf.APIServerJobStatusPort int` (read directly by policy-server, same key api-server itself defined in Task 5 — one shared config key, two consumers, matching how `PolicyServerHost`/`PolicyServerPort` are already shared between api-server-as-client and policy-server-as-server), `conf.RestoreCleanupIntervalSec int` (default `300`), `conf.RestoreCleanupGracePeriodSec int` (default `900`).

- [ ] **Step 1: Add the config keys**

In `src/common/config/config.go`'s `Config` struct, add:

```go
	APIServerHost                    string
	RestoreCleanupIntervalSec        int
	RestoreCleanupGracePeriodSec     int
```

(`APIServerJobStatusPort` already exists from Task 5 — no change needed there, just a second consumer.)

In `ParseConfig`'s default literal, add:

```go
		RestoreCleanupIntervalSec:        300,
		RestoreCleanupGracePeriodSec:     900,
```

In the `switch key` block, add:

```go
		case "api_server_host":
			config.APIServerHost = value
			foundFields["api_server_host"] = true
		case "RestoreCleanupIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid RestoreCleanupIntervalSec value at line %d: %s", lineNum, value)
			}
			config.RestoreCleanupIntervalSec = number
			foundFields["RestoreCleanupIntervalSec"] = true
		case "RestoreCleanupGracePeriodSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid RestoreCleanupGracePeriodSec value at line %d: %s", lineNum, value)
			}
			config.RestoreCleanupGracePeriodSec = number
			foundFields["RestoreCleanupGracePeriodSec"] = true
```

- [ ] **Step 2: Wire the client and goroutine in `main.go`**

In `src/cmd/policy-server/main.go`, add imports `pb "github.com/alex-sviridov/miniprotector/api"` (if not already present under a different alias — check first) and confirm `"github.com/alex-sviridov/miniprotector/common/connection"` is imported (it already is, for `connection.StartServer`). After the existing `checkins, err := checkinstore.New(varDir)` block and before `srv := NewPolicyServerServer(...)`, add:

```go
	jobStatusConn, err := connection.Connect(conf.APIServerHost, conf.APIServerJobStatusPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Error("connect to api-server job-status service failed", "error", err)
		os.Exit(1)
	}
	defer jobStatusConn.Close()
	jobStatusClient := pb.NewJobStatusServiceClient(jobStatusConn)
```

After the existing `go runCheckinCleanup(...)` line, add:

```go
	go srv.runRestoreCleanup(signalCtx, jobStatusClient,
		time.Duration(conf.RestoreCleanupIntervalSec)*time.Second,
		time.Duration(conf.RestoreCleanupGracePeriodSec)*time.Second,
		logger)
```

- [ ] **Step 3: Verify it builds**

Run: `cd src && go build ./...`
Expected: succeeds.

- [ ] **Step 4: Run the full policy-server and config suites**

Run: `cd src && go test ./cmd/policy-server/... ./common/config/...`
Expected: PASS.

- [ ] **Step 5: Update deploy config**

In `deploy/control-plane/policy-server/local.conf`, add (near the other `*_host`/`*_port` entries):

```
# Where policy-server's restore-cleanup sweep asks api-server whether a
# restore policy's job has finished.
api_server_host=api-server
```

(`APIServerJobStatusPort` uses its own default of 8091 unless overridden -- add `APIServerJobStatusPort=8091` explicitly only if this deployment already overrides other ports in this file; otherwise the default is sufficient and no line is needed.)

- [ ] **Step 6: Update docs**

In `docs/components/policy-server.md`: add a section on the restore policy lifecycle — `JobID` generation and the `event="created"` line at `CreatePolicy` time, the `runRestoreCleanup` sweep (what it does, its two new config keys, what `event="deleted"` means), and that this makes policy-server both a gRPC server (`PolicyService`, existing) and, for this one purpose, a gRPC client of `api-server`. Add the three new config keys (`api_server_host`, `RestoreCleanupIntervalSec`, `RestoreCleanupGracePeriodSec`) to its Configuration Keys table, matching the existing table's format.

- [ ] **Step 7: Commit**

```bash
git add src/common/config/config.go src/cmd/policy-server/main.go deploy/control-plane/policy-server/local.conf docs/components/policy-server.md
git commit -m "feat(policy-server): wire the restore-cleanup sweep into main"
```

---

### Task 10: agent — use the policy-provided `JobID`, run once ever

**Files:**
- Modify: `src/cmd/agent/backup.go`, `src/cmd/agent/restore.go`
- Test: `src/cmd/agent/restore_test.go`

**Interfaces:**
- Consumes: `cachedPolicy` (existing, `backup.go`), `restoreTaskID` (existing, unchanged — still the `agent-state.json` `PolicyState` key), `PolicyState.LastAttemptAt *time.Time` (already exists, `cache.go`, already set on every attempt by `reconcile.go`'s `recordOutcome` — no change needed there).
- Produces: `cachedPolicy.JobID string` (new field).

- [ ] **Step 1: Add the field to `cachedPolicy`**

In `src/cmd/agent/backup.go`, add to the `cachedPolicy` struct, next to the other restore-only fields:

```go
	// "restore" policy only, empty for every other type. Generated once by
	// policy-server at CreatePolicy time and used verbatim as the
	// dispatched rwfs exec's --job-id -- see restore.go's restoreTasks.
	JobID string `json:"job_id,omitempty"`
```

- [ ] **Step 2: Update the failing/changed tests in `restore_test.go`**

Replace `TestRestoreTasks_OneTaskPerRestorePolicy`'s fixture and assertions:

```go
func TestRestoreTasks_OneTaskPerRestorePolicy(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{
			Name: "web01-emergency", Type: "restore", JobID: "verify:web01-emergency:1700000000",
			Destinations: []string{"bwfs-1:8080"},
			Rules:        []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		},
		{Name: "nightly", Type: "backup"}, // must contribute zero restore tasks
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, "verify:web01-emergency", tasks[0].ID)
	assert.Equal(t, "rwfs", tasks[0].Binary)
	assert.Equal(t, "verify:web01-emergency:1700000000", tasks[0].JobID, "job id must come from the cached policy, not be generated here")
	assert.Equal(t, []string{"verify", "bwfs-1:8080", "--rules-stdin", "--job-id", tasks[0].JobID}, tasks[0].Args)
	assert.True(t, tasks[0].Background)

	var payload struct {
		Rules []RestoreRule `json:"rules"`
	}
	require.NoError(t, json.Unmarshal(tasks[0].Stdin, &payload))
	assert.Equal(t, []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}}, payload.Rules)
}
```

Replace `TestRestoreTasks_DueUntilFirstSuccessThenNeverAgain` with:

```go
func TestRestoreTasks_DueUntilFirstAttemptThenNeverAgain(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{Name: "x", Type: "restore", JobID: "verify:x:1700000000", Destinations: []string{"bwfs-1:8080"}, Rules: []RestoreRule{{Path: "/x", Include: true}}},
	})
	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)

	now := time.Now()
	assert.True(t, tasks[0].Due(PolicyState{}, now), "never attempted is due")
	attempted := now.Add(-time.Minute)
	assert.False(t, tasks[0].Due(PolicyState{LastAttemptAt: &attempted}, now), "attempted once (even if it failed) is never due again")
}
```

Update `TestRestoreTasks_RestoreModeUsesRestorePrefixAndRestoreSubcommand` and `TestRestoreTasks_RestoreModeWithoutOverwriteOmitsFlag`'s fixtures to set `JobID: "restore:web01-actual-restore:1700000000"` and change their `strings.HasPrefix(tasks[0].JobID, ...)` assertion to an exact `assert.Equal(t, "restore:web01-actual-restore:1700000000", tasks[0].JobID)`.

The `strings` import in `restore_test.go` may become unused after these edits — check and remove it if so (`goimports`/`go vet` will flag it).

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestRestoreTasks -v`
Expected: FAIL — `tasks[0].JobID` still generated by `restoreJobID`, `Due` still checks `LastSuccessAt`.

- [ ] **Step 4: Implement**

In `src/cmd/agent/restore.go`, delete the now-unused `restoreJobID` function entirely (its job is now done once, by policy-server, in Task 7).

In `restoreTasks`, replace:

```go
		jobID := restoreJobID(p.Name, p.Mode, time.Now())
```

with:

```go
		jobID := p.JobID
```

Replace the `Due` function in the `tasks = append(tasks, Policy{...})` literal:

```go
			Due: func(s PolicyState, now time.Time) bool {
				return s.LastAttemptAt == nil
			},
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run TestRestoreTasks -v`
Expected: PASS

- [ ] **Step 6: Run the full agent suite**

Run: `cd src && go test ./cmd/agent/...`
Expected: PASS — this also catches any other place still referencing `restoreJobID`.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/agent/backup.go src/cmd/agent/restore.go src/cmd/agent/restore_test.go
git commit -m "feat(agent): use policy-provided JobID, run restore/verify tasks exactly once"
```

---

### Task 11: agent — retire `event=start` for restore/verify, docs

**Files:**
- Modify: `src/cmd/agent/reconcile.go`
- Test: `src/cmd/agent/reconcile_test.go`
- Modify: `docs/components/agent.md`

**Interfaces:**
- Consumes: `isBackupPolicy` (existing, exact pattern to mirror), `logExecStart`/`logExecCompletion` (existing).
- Produces: `isRestorePolicy(p Policy) bool`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/agent/reconcile_test.go`:

```go
func TestLogExecOutcome_RestorePolicyOmitsStartButKeepsFinish(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "restore:web01-emergency", JobID: "restore:web01-emergency:1700000000"}

	logExecStart(logger, p)
	logExecCompletion(logger, p, nil, 250*time.Millisecond)

	out := buf.String()
	assert.NotContains(t, out, `"event":"start"`, "policy-server's own created event is the start marker now")
	assert.Contains(t, out, `"event":"finish"`)
	assert.Contains(t, out, `"status":"success"`)
}

func TestLogExecOutcome_VerifyPolicyOmitsStartButKeepsFinish(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "verify:web01-emergency", JobID: "verify:web01-emergency:1700000000"}

	logExecStart(logger, p)
	logExecCompletion(logger, p, errors.New("boom"), time.Second)

	out := buf.String()
	assert.NotContains(t, out, `"event":"start"`)
	assert.Contains(t, out, `"event":"finish"`)
	assert.Contains(t, out, `"status":"failure"`)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestLogExecOutcome_.*PolicyOmitsStart -v`
Expected: FAIL — both currently log `event=start`.

- [ ] **Step 3: Implement**

In `src/cmd/agent/reconcile.go`, add next to `isBackupPolicy`:

```go
// isRestorePolicy reports whether p is a restore/verify task -- their
// event=start marker comes from policy-server's own "created" log line
// now (see restore_cleanup.go/write.go), not from agent, so agent must
// not also emit one. Unlike isBackupPolicy, event=finish is unaffected --
// agent's own completion line remains the sole finish signal for these
// kinds.
func isRestorePolicy(p Policy) bool {
	return strings.HasPrefix(p.ID, "restore:") || strings.HasPrefix(p.ID, "verify:")
}
```

Update `logExecStart`:

```go
func logExecStart(logger *slog.Logger, p Policy) {
	if isBackupPolicy(p) || isRestorePolicy(p) {
		logger.Info("policy execution started", "policy", p.ID, "binary", p.Binary, "job_id", p.JobID)
		return
	}
	logger.Info("policy execution started", "policy", p.ID, "binary", p.Binary, "job_id", p.JobID, "event", "start")
}
```

`logExecCompletion` is **unchanged** — it must keep logging `event="finish"` for restore/verify exactly as today.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run TestLogExecOutcome -v`
Expected: PASS

- [ ] **Step 5: Run the full agent suite**

Run: `cd src && go test ./cmd/agent/...`
Expected: PASS

- [ ] **Step 6: Update docs**

In `docs/components/agent.md`: update the section describing agent's start/finish logging (near where it currently documents `event=start`/`event=finish` for dispatched execs, and near backup's existing carve-out) to note that restore/verify tasks also omit `event=start` (policy-server's own `created` line is the start marker now — cross-reference `docs/components/policy-server.md`), that they now run at most once ever regardless of outcome (no more retry-until-success), and update anything describing the old per-attempt `restoreJobID` generation to reflect that `job_id` now comes from policy-server.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/reconcile_test.go docs/components/agent.md
git commit -m "feat(agent): retire agent's own start event for restore/verify tasks"
```

---

### Task 12: Documentation wrap-up

**Files:**
- Create: `docs/protocols/jobstatus.md`
- Modify: `README.md`, `docs/ARCHITECTURE.md` (verify Task 5's edit is complete now that the full picture exists), `CHANGELOG.md`

**Interfaces:** None — pure documentation.

- [ ] **Step 1: Write `docs/protocols/jobstatus.md`**

Mirror `docs/protocols/policy-server.md`'s structure (service overview, message field tables, CLI→RPC mapping if applicable, design decisions). Cover: `JobStatusService.GetPolicyJobStatus` — request/response fields, the `since`/`until` clamping rule (`policy_created_at` vs. `maxJobsWindow`), the `"control-plane"`-only role restriction, and that it is api-server's only gRPC surface (its REST API is separately documented and remains non-mTLS).

- [ ] **Step 2: Cross-link it**

In `README.md`'s Documentation section, add a line for the new protocol doc alongside the existing protocol doc links (e.g. next to `docs/protocols/policy-server.md`'s entry).

In `docs/components/api-server.md` and `docs/components/policy-server.md`'s See Also sections (both already touched in Tasks 5/9 — verify, don't duplicate the edit), link to `docs/protocols/jobstatus.md`.

- [ ] **Step 3: Verify `docs/ARCHITECTURE.md` is complete**

Re-read the diagram/table edit made in Task 5 now that policy-server's client side (Task 9) also exists — confirm the edge is described as policy-server → api-server (not just "api-server accepts inbound"), matching the actual caller.

- [ ] **Step 4: Add the CHANGELOG entry**

Add a dated entry (most recent first, matching the file's existing format) summarizing: restore/verify tasks now run exactly once; policy-server now generates and logs under a shared `job_id`, correlating creation/execution/deletion in the Jobs UI; policy-server automatically deletes a restore policy once its job finishes, via a new mTLS `GetPolicyJobStatus` call to api-server's first gRPC listener.

- [ ] **Step 5: Commit**

```bash
git add docs/protocols/jobstatus.md README.md docs/ARCHITECTURE.md docs/components/api-server.md docs/components/policy-server.md CHANGELOG.md
git commit -m "docs: restore policy lifecycle (jobstatus protocol, architecture, changelog)"
```

---

### Task 13: e2e — verify the lifecycle end to end

**Files:**
- Modify: `web/e2e/live-job-updates.spec.js`

**Interfaces:**
- Consumes: existing e2e fixtures — check `web/e2e/helpers/test.js`'s `trackPolicy` fixture (referenced in this repo's recent e2e cleanup work) and whatever restore-cart-submission helper `restore-cart.spec.js`/`restore-verify.spec.js` already use, to submit a restore policy the same way those specs do.

- [ ] **Step 1: Read the existing spec for its conventions**

Open `web/e2e/live-job-updates.spec.js` and `web/e2e/restore-verify.spec.js` (or equivalent) to find: how a restore policy is currently submitted end to end in e2e (likely via the restore cart UI flow or a direct API helper), how the Jobs page's live-updating list is currently asserted against (`data-test` selectors), and the `trackPolicy` fixture's exact signature — reuse these verbatim rather than inventing new helpers.

- [ ] **Step 2: Write the new test**

Add a test asserting: after submitting a restore (verify-mode is sufficient — no real destination needed), the Jobs list shows the job as `in_progress` before the target node has run anything (poll/assert on the `state` shown for that `job_id`), and — once the demo environment's agent actually executes it and the configured grace period elapses — the policy is gone from `GET /api/v1/policies?type=restore` (or equivalent). Use `trackPolicy` for any policy this test creates that might outlive a shortened test-grace-period, so a slow/failed run still cleans up.

- [ ] **Step 3: Run it against the demo environment**

Run: `make demo-up` (if not already running), then `cd web && npx playwright test live-job-updates.spec.js`
Expected: PASS. If the default grace period (15 minutes, from `RestoreCleanupGracePeriodSec`'s default of 900) is impractical for a fast e2e run, note in the test that the demo environment's `local.conf` should override `RestoreCleanupGracePeriodSec` to a short value (e.g. 5) for e2e — apply that override to `demo/policy-server`'s config if such a directory/file exists (check `demo/policy-server/` first).

- [ ] **Step 4: Commit**

```bash
git add web/e2e/live-job-updates.spec.js
git commit -m "test(e2e): verify restore policy lifecycle shows in_progress at creation and cleans up after completion"
```

---

## Self-Review Notes

- **Spec coverage:** every Goal in the design doc maps to a task — run-once-ever (Task 10), central "has it finished" signal (Tasks 1, 4, 8), automatic deletion + grace period (Task 8), `in_progress` from creation (Tasks 2, 3, 7), shared `job_id` correlation (Tasks 1, 6, 7, 10). All four Documentation Impact bullets are covered (Tasks 5, 9, 11, 12). The two Open Questions (approval/authz, and exact grace-period/interval defaults) are deliberately left as defaults chosen in Task 9 rather than fixed in the design — consistent with the spec's own deferral.
- **Placeholder scan:** no TBD/TODO; every step has real code or an exact command.
- **Type consistency:** `JobID`/`JobId` naming is deliberate, not a typo — Go struct fields use `JobID` (Go convention) throughout (`RestorePolicy.JobID`, `cachedPolicy.JobID`), while the generated protobuf field is `JobId` (protoc-gen-go's `job_id` → `JobId` casing) and is only ever referenced as `resp.JobId`/`req.GetJobId()` in code that talks to the proto type directly (Tasks 6 Step 1, 7, 8) — verified against this codebase's existing convention (e.g. `StoragePolicyID` field vs. generated `StoragePolicyId`/`GetStoragePolicyId()`, both already present in `restore_policy.go`).
