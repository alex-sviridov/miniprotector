package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestVariants_NoSweepIsOneVariantWithTheScalarSettings(t *testing.T) {
	a := &Args{Streams: 4, Window: 8, RTT: 50 * time.Millisecond}
	vs := Variants(a)

	assert.Len(t, vs, 1)
	assert.Equal(t, "run", vs[0].Label)
	assert.Equal(t, 4, vs[0].Args.Streams)
	assert.Equal(t, 8, vs[0].Args.Window)
	assert.Equal(t, 50*time.Millisecond, vs[0].Args.RTT)
}

func TestVariants_CartesianProductInStableOrder(t *testing.T) {
	a := &Args{Streams: 4, Window: 8, SweepStreams: []int{2, 4}, SweepWindows: []int{1, 8}}
	vs := Variants(a)

	assert.Len(t, vs, 4)
	var labels []string
	for _, v := range vs {
		labels = append(labels, v.Label)
	}
	assert.Equal(t, []string{
		"streams=2 window=1", "streams=2 window=8", "streams=4 window=1", "streams=4 window=8",
	}, labels)
	assert.Equal(t, 2, vs[0].Args.Streams)
	assert.Equal(t, 1, vs[0].Args.Window)
	assert.Equal(t, 8, vs[3].Args.Window)
}

func TestVariants_SweepOverridesScalarAndKeepsUnsweptDimensions(t *testing.T) {
	a := &Args{Streams: 4, Window: 8, RTT: 20 * time.Millisecond, SweepRTTs: []time.Duration{0, 50 * time.Millisecond}}
	vs := Variants(a)

	assert.Len(t, vs, 2)
	assert.Equal(t, "rtt=0s", vs[0].Label)
	assert.Equal(t, "rtt=50ms", vs[1].Label)
	for _, v := range vs {
		assert.Equal(t, 4, v.Args.Streams, "unswept dimension keeps its scalar value")
		assert.Equal(t, 8, v.Args.Window)
	}
}

func TestVariants_DoNotShareMutableStateWithTheOriginal(t *testing.T) {
	a := &Args{Streams: 4, SweepWindows: []int{1, 2}}
	vs := Variants(a)
	vs[0].Args.Streams = 99
	assert.Equal(t, 4, a.Streams)
	assert.Equal(t, 4, vs[1].Args.Streams)
}
