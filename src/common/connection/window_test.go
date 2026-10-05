package connection

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFlowControlWindow_DefaultAddsNoOptions(t *testing.T) {
	SetFlowControlWindow(0)
	assert.Empty(t, windowDialOptions())
	assert.Empty(t, windowServerOptions())
}

func TestFlowControlWindow_SetAddsStreamAndConnectionOptions(t *testing.T) {
	t.Cleanup(func() { SetFlowControlWindow(0) })
	SetFlowControlWindow(4 << 20)
	assert.Len(t, windowDialOptions(), 2)
	assert.Len(t, windowServerOptions(), 2)
	assert.Equal(t, int32(4<<20), flowWindow.Load())
}

func TestFlowControlWindow_NegativeAndHugeValuesAreSafe(t *testing.T) {
	t.Cleanup(func() { SetFlowControlWindow(0) })
	SetFlowControlWindow(-5)
	assert.Equal(t, int32(0), flowWindow.Load())
	SetFlowControlWindow(1 << 40)
	assert.Equal(t, int32(1<<30), flowWindow.Load())
}
