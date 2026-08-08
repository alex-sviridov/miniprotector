package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/certmint"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func newTestManagerStore(t *testing.T) *clientmanagerstore.Store {
	t.Helper()
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRunAdd_MintsAndRecordsClient(t *testing.T) {
	store := newTestManagerStore(t)
	var out bytes.Buffer
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		assert.Equal(t, "node-1", hostname)
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(t.Context(), certmint.Options{}, store, args, stubMint, &out)
	require.NoError(t, err)
	assert.Equal(t, "tok-abc\n", out.String())

	got, err := store.GetClient(t.Context(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", got.Hostname)
}

func TestRunAdd_DuplicateHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))

	called := false
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		called = true
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)
	assert.False(t, called, "mint must not be called for a duplicate add")
}

func TestRunAdd_MintFailureDoesNotRecordClient(t *testing.T) {
	store := newTestManagerStore(t)
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		return "", assert.AnError
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)

	_, err = store.GetClient(t.Context(), "node-1")
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunAdd_PassesMintOptsThrough(t *testing.T) {
	store := newTestManagerStore(t)
	wantOpts := certmint.Options{CAURL: "https://ca.internal:9000", Provisioner: "admin@backup.internal"}
	var gotOpts certmint.Options
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		gotOpts = opts
		return "tok-abc", nil
	}

	args := &Arguments{Action: "add", Hostname: "node-1"}
	err := runAdd(t.Context(), wantOpts, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, wantOpts, gotOpts)
}

func TestRunReEnroll_UnknownHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		t.Fatal("mint must not be called for an unknown hostname")
		return "", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "ghost"}
	err := runReEnroll(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	assert.Error(t, err)
}

func TestRunReEnroll_MintsFreshToken(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))
	var out bytes.Buffer
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1"}
	err := runReEnroll(t.Context(), certmint.Options{}, store, args, stubMint, &out)
	require.NoError(t, err)
	assert.Equal(t, "tok-fresh\n", out.String())
}

func TestRunReEnroll_NoSANOverride_ReusesStoredSANsFromAdd(t *testing.T) {
	store := newTestManagerStore(t)
	addSANs := []string{"alias1", "alias2"}
	require.NoError(t, store.AddClient(t.Context(), "node-1", addSANs, time.Now()))

	var gotSANs []string
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		gotSANs = sans
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1"}
	err := runReEnroll(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, addSANs, gotSANs)
}

func TestRunReEnroll_WithSANOverride_UsesOverrideNotStoredSANs(t *testing.T) {
	store := newTestManagerStore(t)
	addSANs := []string{"alias1", "alias2"}
	require.NoError(t, store.AddClient(t.Context(), "node-1", addSANs, time.Now()))

	overrideSANs := []string{"override1"}
	var gotSANs []string
	stubMint := func(hostname string, sans []string, opts certmint.Options) (string, error) {
		gotSANs = sans
		return "tok-fresh", nil
	}

	args := &Arguments{Action: "re-enroll", Hostname: "node-1", SANs: overrideSANs}
	err := runReEnroll(t.Context(), certmint.Options{}, store, args, stubMint, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, overrideSANs, gotSANs)
}
