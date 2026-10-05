package connection

import (
	"sync/atomic"

	"google.golang.org/grpc"
)

// flowWindow is the fixed HTTP/2 flow-control window in bytes, or 0 for
// grpc-go's default, which sizes the window dynamically from measured
// bandwidth-delay product.
var flowWindow atomic.Int32

// SetFlowControlWindow makes every connection dialed, and every server
// started, by this package afterwards use a fixed flow-control window of the
// given size (stream and connection level) instead of grpc-go's dynamic one.
// 0 restores the default. Call it once at startup, after reading config and
// before dialing or serving.
//
// Setting a fixed window turns off grpc-go's automatic sizing, so it only helps
// when it is at least the link's bandwidth-delay product: a window below that
// is slower than the default. See docs/components/mpbench.md for measurements.
func SetFlowControlWindow(bytes int) {
	switch {
	case bytes <= 0:
		flowWindow.Store(0)
	case bytes > 1<<30:
		flowWindow.Store(1 << 30)
	default:
		flowWindow.Store(int32(bytes))
	}
}

func windowDialOptions() []grpc.DialOption {
	w := flowWindow.Load()
	if w == 0 {
		return nil
	}
	return []grpc.DialOption{grpc.WithInitialWindowSize(w), grpc.WithInitialConnWindowSize(w)}
}

func windowServerOptions() []grpc.ServerOption {
	w := flowWindow.Load()
	if w == 0 {
		return nil
	}
	return []grpc.ServerOption{grpc.InitialWindowSize(w), grpc.InitialConnWindowSize(w)}
}
