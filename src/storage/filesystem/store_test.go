package filesystem

import (
	"testing"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/stretchr/testify/assert"
)

func TestErrChunkNotFoundIsSentinel(t *testing.T) {
	// Verify the sentinel exists and has the right message
	assert.EqualError(t, storage.ErrChunkNotFound, "chunk not found")
}
