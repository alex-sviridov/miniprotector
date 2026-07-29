package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageTasks_BuildsTaskFromFilesystemConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "east-1-storage",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
	}]`)

	tasks, ok := storageTasks(path, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, "storage:east-1-storage", tasks[0].ID)
	assert.Equal(t, []string{"/data/storage", "server", "--port", "9400"}, tasks[0].Args)
}

func TestStorageTasks_SkipsUnsupportedBackend(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"s3\", \"root\": \"/data/storage\"}"
	}]`)

	tasks, ok := storageTasks(path, testLogger())
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

	tasks, ok := storageTasks(path, testLogger())
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

	tasks, ok := storageTasks(path, testLogger())
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

	tasks, ok := storageTasks(path, testLogger())
	assert.True(t, ok)
	assert.Empty(t, tasks, "a cached policy whose type isn't \"storage\" must contribute zero storage tasks")
}

func TestStorageTasks_MissingCacheFileReturnsOkFalse(t *testing.T) {
	tasks, ok := storageTasks(filepath.Join(t.TempDir(), "does-not-exist.json"), testLogger())
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_CorruptCacheFileReturnsOkFalse(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `not json`)
	tasks, ok := storageTasks(path, testLogger())
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_MultiplePoliciesEachGetTheirOwnTask(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[
		{"name": "a", "type": "storage", "port": 9400, "config": "{\"backend\": \"filesystem\", \"root\": \"/data/a\"}"},
		{"name": "b", "type": "storage", "port": 9401, "config": "{\"backend\": \"filesystem\", \"root\": \"/data/b\"}"}
	]`)

	tasks, ok := storageTasks(path, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 2)
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "storage:a")
	assert.Contains(t, ids, "storage:b")
}

func TestStorageSupervisor_StartsAndStopsCleanlyOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	var spawns int64
	sup := newStorageSupervisor(script, nil, testLogger(), func(error) {})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	sup.Start(ctx)

	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, atomic.LoadInt64(&spawns))
	cancel()

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context cancellation")
	}
	assert.EqualValues(t, 1, atomic.LoadInt64(&spawns), "no respawn should happen once ctx is cancelled")
}

func TestStorageSupervisor_RestartsOnUnexpectedExitAndRecordsFailure(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\nexit 1\n"))

	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Millisecond, 30*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	var spawns int64
	var mu sync.Mutex
	var outcomes []error
	sup := newStorageSupervisor(script, nil, testLogger(), func(err error) {
		mu.Lock()
		defer mu.Unlock()
		outcomes = append(outcomes, err)
	})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sup.Start(ctx)

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context timeout")
	}

	assert.GreaterOrEqual(t, atomic.LoadInt64(&spawns), int64(2), "a persistently crashing bwfs must be respawned more than once")

	mu.Lock()
	defer mu.Unlock()
	var sawFailure bool
	for _, err := range outcomes {
		if err != nil {
			sawFailure = true
		}
	}
	assert.True(t, sawFailure, "at least one crash must be recorded as a failure")
}

func TestStorageSupervisor_SuccessfulStartRecordsSuccessImmediately(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	outcomes := make(chan error, 1)
	sup := newStorageSupervisor(script, nil, testLogger(), func(err error) { outcomes <- err })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	select {
	case err := <-outcomes:
		assert.NoError(t, err, "a successful start must record success without waiting for the process to exit")
	case <-time.After(time.Second):
		t.Fatal("onOutcome was never called for a successful start")
	}
	sup.Stop()
}

func TestStorageSupervisor_DeliberateStopDoesNotRecordFailure(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	var mu sync.Mutex
	var outcomes []error
	sup := newStorageSupervisor(script, nil, testLogger(), func(err error) {
		mu.Lock()
		defer mu.Unlock()
		outcomes = append(outcomes, err)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	time.Sleep(100 * time.Millisecond) // let it start (records one nil outcome)

	sup.Stop()
	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after Stop()")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, err := range outcomes {
		assert.NoError(t, err, "a deliberate Stop() must never record a failure outcome")
	}
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

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Args: nil}})

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
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Args: nil}})
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
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Args: []string{"/data/old", "server", "--port", "9400"}}})
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.supervisors) == 1
	}, time.Second, 10*time.Millisecond)
	mgr.mu.Lock()
	firstSup := mgr.supervisors["storage:east-1"]
	mgr.mu.Unlock()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Args: []string{"/data/new", "server", "--port", "9401"}}})

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
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := storageTask{ID: "storage:east-1", Args: []string{"/data", "server", "--port", "9400"}}
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
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{
		{ID: "storage:a", Args: nil},
		{ID: "storage:b", Args: nil},
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
