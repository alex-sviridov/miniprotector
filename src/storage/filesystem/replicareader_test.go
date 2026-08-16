package filesystem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenReplicaReader_FileVersionsSince_ReturnsNewRowsInOrder(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", "hosta", "/path", "f", []byte("v1"), 100))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-2", "hosta", "/path", "f", []byte("v2"), 100))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-3", "hosta", "/path", "f", []byte("v3"), 100))

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	batch, err := reader.FileVersionsSince(t.Context(), 0, 2)
	require.NoError(t, err)
	require.Len(t, batch, 2)
	assert.Equal(t, "obj-1", batch[0].ObjectID)
	assert.Equal(t, "obj-2", batch[1].ObjectID)

	next, err := reader.FileVersionsSince(t.Context(), batch[1].Seq, 2)
	require.NoError(t, err)
	require.Len(t, next, 1)
	assert.Equal(t, "obj-3", next[0].ObjectID)
}

func TestOpenReplicaReader_FileVersionsSince_EmptyWhenCaughtUp(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", "hosta", "/path", "f", []byte("v1"), 100))

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	batch, err := reader.FileVersionsSince(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, batch, 1)

	caughtUp, err := reader.FileVersionsSince(t.Context(), batch[0].Seq, 10)
	require.NoError(t, err)
	assert.Empty(t, caughtUp)
}

func TestOpenReplicaReader_CannotWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	defer store.Close()

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	err = reader.db.Exec(
		"INSERT INTO file_version_records (object_id, job_id, ctime, created_at) VALUES ('x', 'y', 0, datetime('now'))",
	).Error
	assert.Error(t, err, "a mode=ro connection must reject writes")
}
