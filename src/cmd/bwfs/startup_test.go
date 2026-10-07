package main

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/storage"
)

type startupStore struct {
	storage.BackupStore
	res *storage.VacuumResult
	err error
}

func (s *startupStore) Vacuum() (*storage.VacuumResult, error) { return s.res, s.err }

func TestStartupVacuum_ReclaimFailureOnlyWarns(t *testing.T) {
	var buf bytes.Buffer
	store := &startupStore{
		res: &storage.VacuumResult{OrphanedChunksRemoved: 3},
		err: fmt.Errorf("%w: segment 1: permission denied", storage.ErrReclaimIncomplete),
	}

	require.NoError(t, startupVacuum(jsonLogger(&buf), store))

	assert.Contains(t, buf.String(), `"level":"WARN"`)
	assert.Contains(t, buf.String(), "segment 1: permission denied")
	assert.Contains(t, buf.String(), `"orphaned_chunks_removed":3`)
}

func TestStartupVacuum_DatabaseFailureIsFatal(t *testing.T) {
	var buf bytes.Buffer
	store := &startupStore{err: errors.New("disk I/O error")}

	assert.ErrorContains(t, startupVacuum(jsonLogger(&buf), store), "disk I/O error")
}
