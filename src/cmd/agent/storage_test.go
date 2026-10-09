package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageTasks_BuildsTaskFromFilesystemConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"id": "pol-east-1",
		"name": "east-1-storage",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
	}]`)

	tasks := storageTasks(mustReadCachedPolicies(t, path), testLogger(), "bwfs-bin", "catalogsync-bin")
	require.Len(t, tasks, 2)

	assert.Equal(t, "storage:east-1-storage", tasks[0].ID)
	assert.Equal(t, "bwfs-bin", tasks[0].Binary)
	assert.Equal(t, []string{"/data/storage", "server", "--port", "9400", "--policy-id", "pol-east-1"}, tasks[0].Args)

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

	tasks := storageTasks(mustReadCachedPolicies(t, path), testLogger(), "bwfs-bin", "catalogsync-bin")
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

	tasks := storageTasks(mustReadCachedPolicies(t, path), testLogger(), "bwfs-bin", "catalogsync-bin")
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

	tasks := storageTasks(mustReadCachedPolicies(t, path), testLogger(), "bwfs-bin", "catalogsync-bin")
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

	tasks := storageTasks(mustReadCachedPolicies(t, path), testLogger(), "bwfs-bin", "catalogsync-bin")
	assert.Empty(t, tasks, "a cached policy whose type isn't \"storage\" must contribute zero storage tasks")
}

func TestStorageTasks_MultiplePoliciesEachGetTheirOwnTask(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[
		{"name": "a", "type": "storage", "port": 9400, "config": "{\"backend\": \"filesystem\", \"root\": \"/data/a\"}"},
		{"name": "b", "type": "storage", "port": 9401, "config": "{\"backend\": \"filesystem\", \"root\": \"/data/b\"}"}
	]`)

	tasks := storageTasks(mustReadCachedPolicies(t, path), testLogger(), "bwfs-bin", "catalogsync-bin")
	require.Len(t, tasks, 4)
	ids := []string{tasks[0].ID, tasks[1].ID, tasks[2].ID, tasks[3].ID}
	assert.Contains(t, ids, "storage:a")
	assert.Contains(t, ids, "storage:a:catalogsync")
	assert.Contains(t, ids, "storage:b")
	assert.Contains(t, ids, "storage:b:catalogsync")
}

// osWriteExecutable writes content to path as an executable file --
// shared test helper so every fake-bwfs.sh fixture above is one line.
func osWriteExecutable(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o755)
}

func TestStorageManager_StartsSupervisorForNewTask(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger(), backoff: defaultBackoffPolicy}
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 20 * time.Millisecond
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

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger(), backoff: defaultBackoffPolicy}
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

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger(), backoff: defaultBackoffPolicy}
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

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger(), backoff: defaultBackoffPolicy}
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

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger(), backoff: defaultBackoffPolicy}
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
	dir := t.TempDir()
	healthyScript := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, healthyScript, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))
	crashingScript := filepath.Join(dir, "fake-catalogsync.sh")
	require.NoError(t, osWriteExecutable(t, crashingScript, "#!/bin/sh\nexit 1\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger(), backoff: defaultBackoffPolicy}
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 20 * time.Millisecond
	mgr.backoff = backoffPolicy{Base: 10 * time.Millisecond, Max: 30 * time.Millisecond}
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

func TestStorageTasks_SkipsDisabledPolicy(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}",
		"disabled_at": "2020-01-01T00:00:00Z"
	}]`)

	tasks := storageTasks(mustReadCachedPolicies(t, path), testLogger(), "bwfs-bin", "catalogsync-bin")

	assert.Empty(t, tasks)
}
