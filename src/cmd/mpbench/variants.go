package main

import (
	"fmt"
	"strings"
	"time"
)

// Variant is one configuration to measure: a full copy of the arguments with
// the swept dimensions overridden.
type Variant struct {
	Label string
	Args  Args
}

// Variants expands the sweep flags into the cartesian product of the swept
// dimensions (rtt, then streams, then window, in that nesting order). With no
// sweep it is a single variant carrying the scalar settings. The label names
// only the swept dimensions.
func Variants(a *Args) []Variant {
	rtts, sweepRTT := a.SweepRTTs, len(a.SweepRTTs) > 0
	if !sweepRTT {
		rtts = []time.Duration{a.RTT}
	}
	streams, sweepStreams := a.SweepStreams, len(a.SweepStreams) > 0
	if !sweepStreams {
		streams = []int{a.Streams}
	}
	windows, sweepWindow := a.SweepWindows, len(a.SweepWindows) > 0
	if !sweepWindow {
		windows = []int{a.Window}
	}

	var out []Variant
	for _, r := range rtts {
		for _, s := range streams {
			for _, w := range windows {
				v := *a
				v.RTT, v.Streams, v.Window = r, s, w
				var parts []string
				if sweepRTT {
					parts = append(parts, fmt.Sprintf("rtt=%v", r))
				}
				if sweepStreams {
					parts = append(parts, fmt.Sprintf("streams=%d", s))
				}
				if sweepWindow {
					parts = append(parts, fmt.Sprintf("window=%d", w))
				}
				label := strings.Join(parts, " ")
				if label == "" {
					label = "run"
				}
				out = append(out, Variant{Label: label, Args: v})
			}
		}
	}
	return out
}
