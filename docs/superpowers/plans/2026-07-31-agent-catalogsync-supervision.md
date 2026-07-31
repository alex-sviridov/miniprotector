# Agent Catalogsync Supervision + Demo Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `agent` supervise `catalogsync` the same way it already supervises `bwfs` for a `"storage"`-typed policy, then delete the demo's hand-rolled process-sequencing shell script now that both hazards it existed for are gone.

**Architecture:** `storageTask` gains a `Binary` field; `storageTasks()` derives two independent tasks per cached storage policy (one for `bwfs`, one for `catalogsync`) instead of one; `storageManager` becomes fully generic over whichever `(ID, Binary, Args)` tuples it's handed, with zero cross-task coordination — each task is reconciled, crash-restarted, and pruned entirely on its own. `demo/backup-host/entrypoint.sh` shrinks to bootstrap-cert-then-`exec agent serve`.

**Tech Stack:** Go 1.26, `cobra` (unaffected here), `testify` (`assert`/`require`), Docker Compose (demo).

## Global Constraints

- No new coordination/ordering logic between `bwfs` and `catalogsync` — both are independent ensure-running tasks, each reconciled without any knowledge of the other. A `catalogsync` that starts before `bwfs` has created `metadata.db` fails cleanly (`OpenReplicaReader`'s `mode=ro` open) and retries via the existing crash-backoff path — no readiness gate, no ordering.
- `agent` does not create the storage root directory. `bwfs`'s own `storage/filesystem.New` already does this (`os.MkdirAll(filepath.Join(basePath, "chunks"), 0755)`, which creates `basePath` itself as a parent) — confirmed in `src/storage/filesystem/store.go:20-24`. Nothing in this plan touches that file.
- `reconcile.go` and `list.go` are not modified. Both already operate generically on `[]storageTask` (`reconcile.go`'s prune-set union at `src/cmd/agent/reconcile.go:290-303`; `list.go`'s `renderPolicies` at `src/cmd/agent/list.go:99-110`) — returning two tasks per policy instead of one requires no changes to either.
- `bwfs` and `catalogsync` source code is not modified — this plan only changes who starts them and with what arguments.
- Every code task follows TDD: write the failing test, confirm it fails, implement, confirm it passes, commit.

---

## Task 1: `agent` — `storageTask` gains `Binary`; `storageTasks()` derives two tasks per policy

**Files:**
- Modify: `src/cmd/agent/storage.go:1-8` (file header comment), `storage.go:23-37` (`storageTask` struct + `storageTaskID`), `storage.go:46-81` (`storageTasks` function)
- Test: `src/cmd/agent/storage_test.go:16-117` (all `TestStorageTasks_*` tests)

**Interfaces:**
- Consumes: nothing new from other tasks.
- Produces: `storageTask{ID, Binary, Args string/[]string}` (was `{ID, Args}`); `storageTasks(policiesCachePath string, logger *slog.Logger, bwfsBinary, catalogsyncBinary string) ([]storageTask, bool)` (was `storageTasks(policiesCachePath string, logger *slog.Logger)`); new `catalogsyncTaskID(policyName string) string`. Task 2 and Task 3 depend on this exact signature.

- [ ] **Step 1: Write the failing tests — update every `TestStorageTasks_*` call site in `storage_test.go`**

Replace the entire block from `TestStorageTasks_BuildsTaskFromFilesystemConfig` (line 16) through `TestStorageTasks_MultiplePoliciesEachGetTheirOwnTask` (ending line 117) with:

```go
func TestStorageTasks_BuildsTaskFromFilesystemConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "east-1-storage",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
	}]`)

	tasks, ok := storageTasks(path, testLogger(), "bwfs-bin", "catalogsync-bin")
	require.True(t, ok)
	require.Len(t, tasks, 2)

	assert.Equal(t, "storage:east-1-storage", tasks[0].ID)
	assert.Equal(t, "bwfs-bin", tasks[0].Binary)
	assert.Equal(t, []string{"/data/storage", "server", "--port", "9400"}, tasks[0].Args)

	assert.Equal(t, "storage:east-1-storage:catalogsync", tasks[1].ID)
	assert.Equal(t, "catalogsync-bin", tasks[1].Binary)
	assert.Equal(t, []string{"/data/storage"}, tasks[1].Args)
}

func TestStorageTasks_SkipsUnsupportedBackend(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"s3\", \"root\": \"/data/storage\"}"
	}]`)

	tasks, ok := storageTasks(path, testLogger(), "bwfs-bin", "catalogsync-bin")
	assert.True(t, ok, "the file itself was still validly read")
	assert.Empty(t, tasks)
}

func TestStorageTasks_SkipsMissingRoot(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\"}"
	}]`)

	tasks, ok := storageTasks(path, testLogger(), "bwfs-bin", "catalogsync-bin")
	assert.True(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_SkipsUnparseableConfigJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "storage",
		"port": 9400,
		"config": "not json"
	}]`)

	tasks, ok := storageTasks(path, testLogger(), "bwfs-bin", "catalogsync-bin")
	assert.True(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_IgnoresNonStorageType(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "backup",
		"object_filters": [{"path": "/data"}],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)

	tasks, ok := storageTasks(path, testLogger(), "bwfs-bin", "catalogsync-bin")
	assert.True(t, ok)
	assert.Empty(t, tasks, "a cached policy whose type isn't \"storage\" must contribute zero storage tasks")
}

func TestStorageTasks_MissingCacheFileReturnsOkFalse(t *testing.T) {
	tasks, ok := storageTasks(filepath.Join(t.TempDir(), "does-not-exist.json"), testLogger(), "bwfs-bin", "catalogsync-bin")
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_CorruptCacheFileReturnsOkFalse(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `not json`)
	tasks, ok := storageTasks(path, testLogger(), "bwfs-bin", "catalogsync-bin")
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_MultiplePoliciesEachGetTheirOwnTask(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[
		{"name": "a", "type": "storage", "port": 9400, "config": "{\"backend\": \"filesystem\", \"root\": \"/data/a\"}"},
		{"name": "b", "type": "storage", "port": 9401, "config": "{\"backend\": \"filesystem\", \"root\": \"/data/b\"}"}
	]`)

	tasks, ok := storageTasks(path, testLogger(), "bwfs-bin", "catalogsync-bin")
	require.True(t, ok)
	require.Len(t, tasks, 4)
	ids := []string{tasks[0].ID, tasks[1].ID, tasks[2].ID, tasks[3].ID}
	assert.Contains(t, ids, "storage:a")
	assert.Contains(t, ids, "storage:a:catalogsync")
	assert.Contains(t, ids, "storage:b")
	assert.Contains(t, ids, "storage:b:catalogsync")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestStorageTasks -v`
Expected: FAIL — compile error, `storageTasks` called with 4 arguments but declared with 2, and `tasks[0].Binary` undefined.

- [ ] **Step 3: Implement — `storage.go`**

Replace the file header comment (lines 1-8) with:

```go
// storage.go derives agent's ensure-running tasks from cached "storage"-type
// policies -- one task to keep a bwfs server running, and one independent
// task to keep a catalogsync process running against the same root, with no
// coordination between the two (see docs/superpowers/specs/
// 2026-07-31-agent-catalogsync-supervision-design.md for why that's safe:
// catalogsync's read-only sqlite open fails cleanly, not corruptingly, if it
// ever starts before bwfs has created the database, and just gets
// crash-restarted like any other transient exec failure). Like backupTasks
// (backup.go), it relies on policy-server's server-side scoping:
// ClientFilters.Matches applies in GetPolicies before a policy reaches
// policies-cache.json, so anything with Type == "storage" in the cache is
// already scoped to this node.
package main
```

Replace the `storageTask` struct and `storageTaskID` function (lines 23-37) with:

```go
// storageTask is one long-running process this node should be running,
// derived from a cached "storage" policy -- either the bwfs server itself,
// or the catalogsync process replicating its catalog, treated as two
// independent entries with no relationship to each other beyond sharing an
// ID prefix.
type storageTask struct {
	ID     string
	Binary string
	Args   []string
}

// storageTaskID is the stable identifier for one storage policy's bwfs task
// in agent-state.json -- mirrors backup.go's "backup:" prefix convention.
// Like backupTaskID, this assumes policy names are effectively unique
// (the same pre-existing assumption backup tasks already make; not solved
// fresh here).
func storageTaskID(policyName string) string {
	return fmt.Sprintf("storage:%s", policyName)
}

// catalogsyncTaskID mirrors storageTaskID's "storage:<name>" convention with
// a suffix, so the two tasks derived from one storage policy are
// related-but-distinct IDs in agent-state.json / list-policies -- prune and
// storageManager.reconcile treat them as two ordinary, independent entries.
func catalogsyncTaskID(policyName string) string {
	return storageTaskID(policyName) + ":catalogsync"
}
```

Replace the `storageTasks` function (lines 46-81, i.e. from its doc comment through its closing brace) with:

```go
// storageTasks derives two ensure-running tasks per cached "storage" policy
// -- one for bwfs, one for catalogsync -- valid at the instant it's called;
// callers that need to notice policies-cache.json changing over time
// (agent serve's reconcile loop) must call this fresh every tick rather than
// caching its result once.
//
// ok=false mirrors backupTasks's contract: it means this tick's read of
// policiesCachePath failed, and callers must never treat that as "there are
// zero storage tasks."
//
// A policy whose config doesn't parse as a filesystem-backend JSON object,
// or whose root is empty, is skipped entirely (contributing neither task)
// with a logged error -- the same fail-safe "skip, don't block the rest"
// direction backupTasks already uses for an unparseable rpo or missing
// backup_window.
func storageTasks(policiesCachePath string, logger *slog.Logger, bwfsBinary, catalogsyncBinary string) ([]storageTask, bool) {
	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []storageTask
	for _, p := range cachedPolicies {
		if p.Type != "storage" {
			continue
		}
		var cfg storageConfig
		if err := json.Unmarshal([]byte(p.Config), &cfg); err != nil || cfg.Backend != "filesystem" || cfg.Root == "" {
			logger.Error("storage policy has unsupported or unparseable config, skipping", "policy", p.Name)
			continue
		}
		tasks = append(tasks,
			storageTask{
				ID:     storageTaskID(p.Name),
				Binary: bwfsBinary,
				Args:   []string{cfg.Root, "server", "--port", strconv.Itoa(int(p.Port))},
			},
			storageTask{
				ID:     catalogsyncTaskID(p.Name),
				Binary: catalogsyncBinary,
				Args:   []string{cfg.Root},
			},
		)
	}
	return tasks, true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run TestStorageTasks -v`
Expected: PASS — all 7 `TestStorageTasks_*` tests green.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/storage.go src/cmd/agent/storage_test.go
git commit -m "feat(agent): derive an independent catalogsync task alongside each storage task"
```

---

## Task 2: `agent` — `storageManager` becomes generic over each task's `Binary`

**Files:**
- Modify: `src/cmd/agent/storage.go:263-284` (`storageManager` struct + `newStorageManager`), `storage.go:294-324` (`reconcile`)
- Test: `src/cmd/agent/storage_test.go` — every `TestStorageManager_*` test (lines 347-486 in the pre-Task-1 file; shifted slightly after Task 1's edits, locate by function name) plus one new test

**Interfaces:**
- Consumes: `storageTask{ID, Binary, Args}` from Task 1.
- Produces: `newStorageManager(logger *slog.Logger) *storageManager` (was `newStorageManager(binary string, logger *slog.Logger)`). Task 3 depends on this exact signature.

- [ ] **Step 1: Write the failing tests — update every `TestStorageManager_*` call site**

In `storage_test.go`, update every occurrence of `newStorageManager(script, testLogger())` to `newStorageManager(testLogger())`, and add `Binary: script` to every `storageTask{...}` literal in these tests. Concretely, replace `TestStorageManager_StartsSupervisorForNewTask` through `TestStorageManager_StopAllStopsEverySupervisor` with:

```go
func TestStorageManager_StartsSupervisorForNewTask(t *testing.T) {
	origWindow := storageStabilityWindow
	storageStabilityWindow = 20 * time.Millisecond
	defer func() { storageStabilityWindow = origWindow }()

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Binary: script, Args: nil}})

	require.Eventually(t, func() bool {
		return rs.get("storage:east-1").LastSuccessAt != nil
	}, time.Second, 10*time.Millisecond, "a newly-appeared task must get a running supervisor recorded as successful")

	mgr.StopAll()
}

func TestStorageManager_StopsSupervisorForRemovedTask(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Binary: script, Args: nil}})
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.supervisors) == 1
	}, time.Second, 10*time.Millisecond)

	mgr.reconcile(ctx, rs, nil) // task no longer present

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	assert.Empty(t, mgr.supervisors, "a supervisor for a removed task must be stopped and dropped")
}

func TestStorageManager_RestartsSupervisorWhenArgsChange(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Binary: script, Args: []string{"/data/old", "server", "--port", "9400"}}})
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.supervisors) == 1
	}, time.Second, 10*time.Millisecond)
	mgr.mu.Lock()
	firstSup := mgr.supervisors["storage:east-1"]
	mgr.mu.Unlock()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Binary: script, Args: []string{"/data/new", "server", "--port", "9401"}}})

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.supervisors, 1)
	assert.NotSame(t, firstSup, mgr.supervisors["storage:east-1"], "a task whose args changed must get a fresh supervisor")
}

func TestStorageManager_DoesNotDoubleStartAlreadySupervisedTask(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := storageTask{ID: "storage:east-1", Binary: script, Args: []string{"/data", "server", "--port", "9400"}}
	mgr.reconcile(ctx, rs, []storageTask{task})
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.supervisors) == 1
	}, time.Second, 10*time.Millisecond)
	mgr.mu.Lock()
	firstSup := mgr.supervisors["storage:east-1"]
	mgr.mu.Unlock()

	mgr.reconcile(ctx, rs, []storageTask{task}) // same task, second tick

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	assert.Same(t, firstSup, mgr.supervisors["storage:east-1"], "an unchanged task must not be restarted")
}

func TestStorageManager_StopAllStopsEverySupervisor(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{
		{ID: "storage:a", Binary: script, Args: nil},
		{ID: "storage:b", Binary: script, Args: nil},
	})
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.supervisors) == 2
	}, time.Second, 10*time.Millisecond)

	mgr.mu.Lock()
	dones := make([]chan struct{}, 0, len(mgr.supervisors))
	for _, sup := range mgr.supervisors {
		dones = append(dones, sup.loopDone)
	}
	mgr.mu.Unlock()

	mgr.StopAll()

	for _, done := range dones {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("StopAll did not stop every supervisor")
		}
	}
}

// TestStorageManager_TasksSuperviseFullyIndependently proves there is no
// coordination between two tasks derived from the same policy (e.g. bwfs and
// catalogsync): one crash-looping task's failures never affect its sibling,
// and the healthy one is never restarted or delayed by the other's backoff.
func TestStorageManager_TasksSuperviseFullyIndependently(t *testing.T) {
	origWindow := storageStabilityWindow
	storageStabilityWindow = 20 * time.Millisecond
	defer func() { storageStabilityWindow = origWindow }()

	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Millisecond, 30*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	dir := t.TempDir()
	healthyScript := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, healthyScript, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))
	crashingScript := filepath.Join(dir, "fake-catalogsync.sh")
	require.NoError(t, osWriteExecutable(t, crashingScript, "#!/bin/sh\nexit 1\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{
		{ID: "storage:east-1", Binary: healthyScript, Args: nil},
		{ID: "storage:east-1:catalogsync", Binary: crashingScript, Args: nil},
	})

	require.Eventually(t, func() bool {
		return rs.get("storage:east-1").LastSuccessAt != nil
	}, time.Second, 10*time.Millisecond, "the healthy task must start and stay up")

	require.Eventually(t, func() bool {
		return rs.get("storage:east-1:catalogsync").ConsecutiveFailures >= 2
	}, time.Second, 10*time.Millisecond, "the crash-looping task must keep failing and restarting on its own")

	assert.Empty(t, rs.get("storage:east-1").LastError, "the healthy sibling task must never be affected by the other task's failures")
	mgr.StopAll()
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestStorageManager -v`
Expected: FAIL — compile error, `newStorageManager` called with 1 argument but declared with 2, and `Binary` field undefined on `storageTask` composite literals (it exists after Task 1, but `newStorageManager(testLogger())`'s arity mismatch is the actual failure).

- [ ] **Step 3: Implement — `storage.go`**

Replace the `storageManager` struct and `newStorageManager` (lines 263-284) with:

```go
// storageManager holds one storageSupervisor per current storage task,
// keyed by task ID, and reconciles that set against agent's latest read of
// policies-cache.json every tick (see reconcile.go's run(), which calls
// reconcile once per loop iteration). It has no knowledge of what any given
// task actually runs -- bwfs, catalogsync, or anything else -- it only ever
// sees (ID, Binary, Args) tuples and supervises whatever it's handed.
type storageManager struct {
	logger *slog.Logger

	mu          sync.Mutex
	supervisors map[string]*storageSupervisor
	args        map[string][]string // last-started args, to detect a changed task
}

func newStorageManager(logger *slog.Logger) *storageManager {
	return &storageManager{
		logger:      logger,
		supervisors: map[string]*storageSupervisor{},
		args:        map[string][]string{},
	}
}
```

In `reconcile` (lines 294-324), change the supervisor-construction line inside the "start a supervisor for every newly-appeared task" loop from:

```go
		sup := newStorageSupervisor(m.binary, t.Args, m.logger, func(err error) {
```

to:

```go
		sup := newStorageSupervisor(t.Binary, t.Args, m.logger, func(err error) {
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run "TestStorageManager|TestStorageSupervisor" -v`
Expected: PASS — all `TestStorageManager_*` and `TestStorageSupervisor_*` tests green (the latter are unaffected by this change but confirm nothing broke).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/storage.go src/cmd/agent/storage_test.go
git commit -m "feat(agent): make storageManager generic over each task's binary"
```

---

## Task 3: `agent` — wire both binaries into `main.go`, update `integration_test.go`, update `docs/components/agent.md` and `docs/ARCHITECTURE.md`

**Files:**
- Modify: `src/cmd/agent/main.go:82-136`
- Modify: `src/cmd/agent/integration_test.go:68-114` (the storage-task end-to-end test)
- Modify: `docs/components/agent.md:112-141`
- Modify: `docs/ARCHITECTURE.md:96-99`, `docs/ARCHITECTURE.md:133-166`

**Interfaces:**
- Consumes: `storageTasks(path, logger, bwfsBinary, catalogsyncBinary)` and `newStorageManager(logger)` from Tasks 1-2.
- Produces: nothing new consumed by later tasks — this is the last agent-code task.

- [ ] **Step 1: Write the failing test — update the storage-task integration test**

In `src/cmd/agent/integration_test.go`, replace `TestRun_StorageTaskFromRealCacheFileStartsAndPrunesBwfsSupervisor` (lines 68-114) with:

```go
func TestRun_StorageTaskFromRealCacheFileStartsAndPrunesStorageSupervisors(t *testing.T) {
	origWindow := storageStabilityWindow
	storageStabilityWindow = 20 * time.Millisecond
	defer func() { storageStabilityWindow = origWindow }()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	policiesCachePath := filepath.Join(dir, "policies-cache.json")

	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"), 0o755))

	cacheJSON := `[{
		"name": "east-1-storage",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
	}]`
	require.NoError(t, os.WriteFile(policiesCachePath, []byte(cacheJSON), 0o644))

	// Both binary slots point at the same fake script -- this test proves
	// the reconcile-loop wiring (both tasks start, both prune together), not
	// bwfs/catalogsync's real behavior, which is covered by their own
	// packages.
	storageTasksFunc := func() ([]storageTask, bool) { return storageTasks(policiesCachePath, testLogger(), script, script) }
	mgr := newStorageManager(testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec,
			func() ([]Policy, bool) { return nil, true }, 2, nil, storageTasksFunc, mgr)
	}()

	require.Eventually(t, func() bool {
		cache, err := readCache(cachePath)
		return err == nil &&
			cache["storage:east-1-storage"].LastSuccessAt != nil &&
			cache["storage:east-1-storage:catalogsync"].LastSuccessAt != nil
	}, time.Second, 10*time.Millisecond, "both the bwfs and catalogsync tasks must start and record success")

	// Remove the policy from the cache -- both tasks must be pruned from
	// agent-state.json and both supervisors stopped.
	require.NoError(t, os.WriteFile(policiesCachePath, []byte(`[]`), 0o644))

	require.Eventually(t, func() bool {
		cache, err := readCache(cachePath)
		return err == nil && len(cache) == 0
	}, time.Second, 10*time.Millisecond, "removed storage tasks must be pruned from agent-state.json")

	cancel()
	<-done
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/agent/... -run TestRun_StorageTaskFromRealCacheFile -v`
Expected: FAIL — compile error, `storageTasks`/`newStorageManager` called with the old (pre-Task-1/2) call shape is now fixed in the test, but `main.go` still calls them with the old signatures, so `go build`/`go vet` for the package fails first.

- [ ] **Step 3: Implement — `main.go`**

In the `case "serve":` branch, replace:

```go
		bwfsBinary := resolveExecPath("bwfs")
		storageMgr := newStorageManager(bwfsBinary, logger)
		storageTasksFunc := func() ([]storageTask, bool) {
			return storageTasks(policiesCachePath, logger)
		}
```

with:

```go
		bwfsBinary := resolveExecPath("bwfs")
		catalogsyncBinary := resolveExecPath("catalogsync")
		storageMgr := newStorageManager(logger)
		storageTasksFunc := func() ([]storageTask, bool) {
			return storageTasks(policiesCachePath, logger, bwfsBinary, catalogsyncBinary)
		}
```

In the `case "list-policies":` branch, replace:

```go
	case "list-policies":
		allPolicies, _ := policiesFunc()
		// list-policies never executes anything -- a silent logger here
		// keeps storageTasks' own skip-with-log warnings out of stdout's
		// table, matching this command's existing read-only, no-noise
		// character.
		silentLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
		storageTaskList, _ := storageTasks(policiesCachePath, silentLogger)
```

with:

```go
	case "list-policies":
		allPolicies, _ := policiesFunc()
		// list-policies never executes anything -- a silent logger here
		// keeps storageTasks' own skip-with-log warnings out of stdout's
		// table, matching this command's existing read-only, no-noise
		// character.
		silentLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
		storageTaskList, _ := storageTasks(policiesCachePath, silentLogger, resolveExecPath("bwfs"), resolveExecPath("catalogsync"))
```

- [ ] **Step 4: Run the full agent test suite to verify everything passes**

Run: `cd src && go build ./... && go test ./cmd/agent/... -v`
Expected: PASS — full build succeeds, every test in the package (including Tasks 1-2's updated tests and this task's integration test) is green.

- [ ] **Step 5: Update `docs/components/agent.md`**

Replace the "Storage-policy supervision" section (lines 112-141) with:

```markdown
## Storage-policy supervision

Every reconcile tick, alongside deriving backup tasks, `agent` also derives two independent
**ensure-running** tasks per cached policy whose `type` is `"storage"` — unlike a backup task (or
the three static policies), neither is a due/execute/complete unit: one is "this `bwfs server`
process should be running," the other is "this `catalogsync` process should be running," each
checked and corrected every tick rather than scheduled on an interval. There is no per-node
targeting check here — `policy-server`'s `GetPolicies` already scoped `policies-cache.json` to this
node via `client_filters` (the same mechanism a backup policy uses), so every `"storage"`-typed
policy in the cache is already meant for this node.

A storage policy's `config` is opaque JSON to `policy-server`, but `agent` interprets one shape:
`{"backend": "filesystem", "root": "/data/storage"}`. Any other or missing `backend` value is
skipped with a logged error (contributing neither task), the same fail-safe direction as an
unparseable `rpo` or missing `backup_window` for backup tasks. A matching policy becomes two
processes: `bwfs <root> server --port <port>` and `catalogsync <root>`.

The two tasks are supervised entirely independently, with no ordering or coordination between them
— not even at first startup. `catalogsync` opens `bwfs`'s database read-only
(`mode=ro`), which fails cleanly rather than corrupting anything if `catalogsync` happens to start
before `bwfs` has created it; that failure is handled by the same crash-restart-with-backoff path
described below, no differently than any other transient exec failure.

Each task is supervised under its own ID (`storage:<policy-name>` for `bwfs`,
`storage:<policy-name>:catalogsync` for `catalogsync`, mirroring the `backup:` prefix convention): a
start is recorded as success (not "exited successfully" — neither is expected to exit on its own)
only once the process has stayed running for a short stability window (a few seconds) — a crash
faster than that never resets the failure count, so a persistently crash-looping process accumulates
failures instead of bouncing back to "1 failure" on every restart. An unexpected exit is recorded as
a failure with the same jittered `backoff()` reconcile.go already uses elsewhere, and a policy that's
edited (port/path changed) or removed causes both running processes to be stopped (`SIGTERM`, a
graceful drain for `bwfs` — see [bwfs](./bwfs.md) — and for `catalogsync`, which already honors it)
and, for an edit, fresh ones started with the new arguments; a `Stop()` issued while a supervisor is
sitting out a crash-backoff wait takes effect immediately rather than waiting out the remaining
backoff. `agent list-policies` shows each supervised task as its own additional row, reusing the
same STATE/FAILURES/ERROR columns as everything else, with `NEXT RUN` always `-` since there's no
schedule to estimate.

See [Design: agent storage-policy supervision](../superpowers/specs/2026-07-28-agent-storage-supervision-design.md)
and [Design: agent catalogsync supervision](../superpowers/specs/2026-07-31-agent-catalogsync-supervision-design.md).
```

- [ ] **Step 6: Update `docs/ARCHITECTURE.md`**

Replace lines 96-99:

```markdown
`agent` additionally supervises a `bwfs server` process for every cached `"storage"`-typed policy
targeting this node (ensure-running, not scheduled — see
[agent](components/agent.md#storage-policy-supervision)), the first actual consumer of the
`"storage"` policy type. Each policy's (and backup task's, and storage task's) outcome is tracked in
```

with:

```markdown
`agent` additionally supervises a `bwfs server` process and a `catalogsync` process, independently
of each other, for every cached `"storage"`-typed policy targeting this node (ensure-running, not
scheduled — see [agent](components/agent.md#storage-policy-supervision)), the first actual consumer
of the `"storage"` policy type. Each policy's (and backup task's, and storage task's) outcome is
tracked in
```

In the mermaid diagram, replace:

```mermaid
    %% Storage-policy supervision -- agent ensures bwfs is running (not a
    %% scheduled job, unlike agent's backup tasks above)
    bwfsAgent -.->|supervises: start/crash-restart/stop| bwfs
```

with:

```mermaid
    %% Storage-policy supervision -- agent ensures bwfs and catalogsync are
    %% each independently running (not a scheduled job, unlike agent's
    %% backup tasks above; the two supervised processes have no ordering or
    %% coordination between them)
    bwfsAgent -.->|supervises: start/crash-restart/stop| bwfs
    bwfsAgent -.->|supervises: start/crash-restart/stop| catalogsync
```

- [ ] **Step 7: Commit**

```bash
git add src/cmd/agent/main.go src/cmd/agent/integration_test.go docs/components/agent.md docs/ARCHITECTURE.md
git commit -m "feat(agent): supervise catalogsync alongside bwfs for storage policies"
```

---

## Task 4: Demo — shrink `entrypoint.sh`, drop `STORAGE_PATH`, add the `store` storage policy, update `demo/README.md`

**Files:**
- Modify: `demo/backup-host/entrypoint.sh` (full rewrite)
- Modify: `demo/docker-compose.yml:181-198` (`store` service)
- Create: `demo/policy-server/policies/storage/store.json`
- Modify: `demo/README.md:1-10`, `demo/README.md:41-51`

**Interfaces:**
- Consumes: the agent binary built by Task 3 (this task doesn't touch Go code, only the demo's runtime configuration around it).
- Produces: nothing consumed by a later task — this is the last functional task.

- [ ] **Step 1: Rewrite `demo/backup-host/entrypoint.sh`**

Replace the entire file with:

```sh
#!/bin/sh
set -e

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart) of the long-lived bootstrap credential -- same
# pattern as deploy/control-plane/catalog/entrypoint.sh.
if [ -f /data/certs/bootstrap.crt ]; then
    ./certclient renew
else
    ./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent owns everything from here: its own cert renewal, and -- for a node
# targeted by a "storage" policy (see demo/policy-server/policies/storage/)
# -- starting and supervising bwfs and catalogsync itself once its reconcile
# loop picks that policy up. There's nothing left for this script to
# sequence: agent's own operating-refresh always completes before its
# policy-update (same tick), so nothing agent-spawned can ever race agent's
# own cert setup, and bwfs/catalogsync are independent ensure-running tasks
# agent reconciles on its own -- see docs/superpowers/specs/
# 2026-07-31-agent-catalogsync-supervision-design.md.
exec ./agent serve
```

- [ ] **Step 2: Verify the script's syntax**

Run: `sh -n demo/backup-host/entrypoint.sh`
Expected: no output, exit code 0.

- [ ] **Step 3: Remove `STORAGE_PATH` from the `store` service in `demo/docker-compose.yml`**

Replace (within the `store:` service block, lines 181-198):

```yaml
  store:
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: backup-host
    depends_on:
      - ca
      - issuer
      - catalog
      - log-gateway
    volumes:
      - store-data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - STORAGE_PATH=/data/storage
    restart: unless-stopped
```

with:

```yaml
  store:
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: backup-host
    depends_on:
      - ca
      - issuer
      - catalog
      - log-gateway
      - policy-server
    volumes:
      - store-data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    restart: unless-stopped
```

(`STORAGE_PATH` is gone because nothing reads it anymore; `policy-server` is added to `depends_on`
because `store` now needs a storage policy from it to ever start `bwfs`/`catalogsync` — `up.sh`
already enrolls `policy-server` before `store` today, but this makes the dependency explicit for
anyone running `docker compose up` directly instead of through `up.sh`.)

- [ ] **Step 4: Create `demo/policy-server/policies/storage/store.json`**

```json
{
  "metadata": {
    "name": "store",
    "created_at": "2026-07-31T00:00:00Z",
    "updated_at": "2026-07-31T00:00:00Z"
  },
  "client_filters": {
    "hostnames": ["store"]
  },
  "port": 8080,
  "config": {"backend": "filesystem", "root": "/data/storage"}
}
```

- [ ] **Step 5: Update `demo/README.md`'s intro paragraph**

Replace (lines 1-10):

```markdown
# Demo Lab Environment

A self-contained `docker compose` stack — CA, `issuer`, `catalog`, `policy-server`, and three
backup-capable nodes (`database`, `webserver`, `store`) — mutually enrolled via mTLS, brought up
with one script. Unlike [`deploy/control-plane`](../deploy/control-plane/README.md), this never
touches your host filesystem beyond this directory (except `demo/policy-server/policies/backup/`,
which you're meant to edit — see "Backup policies" below): every secret and every byte of state
lives in
Docker-managed named volumes, and no port is published to the host. Everything is reached via
`docker compose exec`.
```

with:

```markdown
# Demo Lab Environment

A self-contained `docker compose` stack — CA, `issuer`, `catalog`, `policy-server`, and three
backup-capable nodes (`database`, `webserver`, `store`) — mutually enrolled via mTLS, brought up
with one script. Unlike [`deploy/control-plane`](../deploy/control-plane/README.md), this never
touches your host filesystem beyond this directory (except `demo/policy-server/policies/`, which
you're meant to edit — see "Backup policies" and "Storage policy" below): every secret and every
byte of state lives in Docker-managed named volumes, and no port is published to the host.
Everything is reached via `docker compose exec`.
```

- [ ] **Step 6: Add a "Storage policy" section to `demo/README.md`**

Replace the end of the existing "Backup policies" section (lines 41-51, ending right before the "Confirm each node resolves..." paragraph) — i.e. insert a new section directly after the existing policy table and before the "Confirm each node resolves the policies..." paragraph:

```markdown
## Backup policies

`policy-server` ships with three example policies (`demo/policy-server/policies/backup/`), each
demonstrating a different way `client_filters` can select clients:

| Policy | Selects | Backs up |
|---|---|---|
| `audit-logs` | `database` and `webserver`, by explicit hostname list | `/var/log/audit` |
| `database-backup` | `database`, by hostname | `/var/lib/dbdata` |
| `webserver-backup` | any client labeled `role=web` (only `webserver`, here) | `/var/www/html` |

## Storage policy

`store` doesn't run `bwfs`/`catalogsync` unconditionally — like every other node, it just runs
`agent`, which starts and supervises both processes once it picks up the one storage policy shipped
in `demo/policy-server/policies/storage/store.json` (targets `store` by hostname, port `8080`, root
`/data/storage` — matching what every example backup policy's `destination: "store:8080"` expects).
That pickup happens on `agent`'s next reconcile tick after enrollment, so expect a roughly
`ReconcileIntervalSec`-long (30s in this demo) delay after `make demo-up` before
`docker compose -f demo/docker-compose.yml logs -f store` shows either process starting.

Confirm each node resolves the policies meant for it (`catalog`, `policy-server`, and `store` run
`policy-update` too, like every `agent`-managed node, but match none of these three policies — their
own `list-policies` output is still worth checking, just to confirm the job itself is succeeding
rather than failing on a missing `policy_server_host`):
```

- [ ] **Step 7: Manually verify the demo stack**

Run: `make demo-down && make demo-up`
Wait roughly a minute after it reports the stack is up, then run:
`docker compose -f demo/docker-compose.yml exec store ./agent list-policies`
Expected: the output includes both `storage:store` and `storage:store:catalogsync` rows with `STATE`
`ok`. Then run:
`docker compose -f demo/docker-compose.yml exec database ./brfs /var/lib/dbdata --destination store:8080`
Expected: the backup completes successfully, confirming `bwfs` is really up and reachable on `8080`.

- [ ] **Step 8: Commit**

```bash
git add demo/backup-host/entrypoint.sh demo/docker-compose.yml demo/policy-server/policies/storage/store.json demo/README.md
git commit -m "feat(demo): drop the backup-host entrypoint's manual bwfs/catalogsync sequencing"
```

---

## Task 5: `CHANGELOG.md` entry and final full verification

**Files:**
- Modify: `CHANGELOG.md:1-5`

**Interfaces:**
- Consumes: nothing new — this is a documentation-only closing task.
- Produces: nothing — terminal task.

- [ ] **Step 1: Add the changelog entry**

Insert immediately after the header (before the existing `## 2026-07-28 — agent: supervise bwfs for storage policies` entry, i.e. as the new first dated entry):

```markdown
## 2026-07-31 — agent: supervise catalogsync alongside bwfs; demo drops its process-sequencing shell script

`agent` now supervises a `catalogsync` process the same way it already supervises `bwfs` for a
`"storage"`-typed policy: two fully independent ensure-running tasks per policy, with no ordering or
coordination between them — a `catalogsync` that starts before `bwfs` has created its database
simply fails cleanly and gets crash-restarted, like any other transient exec failure.

The demo's `backup-host` containers (`database`, `webserver`, `store`) no longer run a shell script
that hand-starts and sequences multiple processes around cert-readiness and startup-ordering races;
both hazards it existed for are gone once `agent` owns the whole lifecycle, so the entrypoint is now
just "bootstrap a certificate, then run `agent serve`." `store`'s `bwfs`/`catalogsync` now come up
via a `"storage"`-typed policy (`demo/policy-server/policies/storage/store.json`) instead of an
env-var-gated branch in that script.
```

- [ ] **Step 2: Run the full test suite and build**

Run: `cd src && go build ./... && go vet ./... && go test ./...`
Expected: PASS — clean build, no vet warnings, every test across the module green.

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog entry for agent catalogsync supervision and demo simplification"
```

---

## Self-Review Notes

- **Spec coverage:** Every section of `2026-07-31-agent-catalogsync-supervision-design.md` maps to a
  task — `storageTask`/`storageTasks` (Task 1), `storageManager` (Task 2), `main.go` wiring +
  `agent list-policies`/`reconcile.go` (confirmed unchanged, Task 3's Global Constraints note), demo
  entrypoint/compose/policy file (Task 4), docs (`agent.md`/`ARCHITECTURE.md` in Task 3,
  `demo/README.md` in Task 4, `CHANGELOG.md` in Task 5).
- **Placeholder scan:** none found — every step has literal file contents or literal shell/Go
  commands, no "similar to Task N" references.
- **Type consistency:** `storageTask{ID, Binary, Args}` (Task 1) is used identically in every later
  task's code and tests; `storageTasks(path, logger, bwfsBinary, catalogsyncBinary)` and
  `newStorageManager(logger)` signatures introduced in Tasks 1-2 are used unchanged in Task 3's
  `main.go` and `integration_test.go` edits.
