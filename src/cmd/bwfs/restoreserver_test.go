package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/storage"
)

type fakeChunkSource struct {
	readErr error
	marked  int
}

func (f *fakeChunkSource) ReadChunk([]byte) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return []byte("data"), nil
}

func (f *fakeChunkSource) MarkChunkCorrupted([]byte) error {
	f.marked++
	return nil
}

func TestReadRestoreChunk_MarksOnlyLostChunks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		readErr error
		mark    bool
	}{
		{"corrupt", fmt.Errorf("read chunk: %w", storage.ErrChunkCorrupt), true},
		// Not found keeps marking: it is how a restore that races a backup
		// linking a just-dropped chunk invalidates the affected file.
		{"not found", storage.ErrChunkNotFound, true},
		{"transient I/O", errors.New("open 0000000001.pack: too many open files"), false},
		{"database busy", errors.New("database is locked (5) (SQLITE_BUSY)"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeChunkSource{readErr: tc.readErr}

			_, err := readRestoreChunk(src, quietLogger(), []byte{0xab})

			require.ErrorIs(t, err, tc.readErr)
			if tc.mark {
				assert.Equal(t, 1, src.marked)
			} else {
				assert.Zero(t, src.marked, "a possibly transient error must never drop data")
			}
		})
	}

	src := &fakeChunkSource{}
	data, err := readRestoreChunk(src, quietLogger(), []byte{0xab})
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), data)
	assert.Zero(t, src.marked)
}
