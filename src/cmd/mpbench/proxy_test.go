package main

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startEcho runs a TCP echo server and returns its address.
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

func startProxy(t *testing.T, spec ProxySpec) *Proxy {
	t.Helper()
	p, err := StartProxy(startEcho(t), spec)
	require.NoError(t, err)
	t.Cleanup(func() { p.Close() })
	return p
}

func TestProxy_PassesBytesUnchangedAndInOrder(t *testing.T) {
	p := startProxy(t, ProxySpec{})
	c, err := net.Dial("tcp", p.Addr())
	require.NoError(t, err)
	defer c.Close()

	want := make([]byte, 300_000)
	_, err = rand.Read(want)
	require.NoError(t, err)
	go func() { c.Write(want) }()

	got := make([]byte, len(want))
	_, err = io.ReadFull(c, got)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(want, got))
}

func TestProxy_OneRoundTripCostsAboutTheRTT(t *testing.T) {
	p := startProxy(t, ProxySpec{RTT: 100 * time.Millisecond})
	c, err := net.Dial("tcp", p.Addr())
	require.NoError(t, err)
	defer c.Close()

	start := time.Now()
	_, err = c.Write([]byte{1})
	require.NoError(t, err)
	_, err = io.ReadFull(c, make([]byte, 1))
	require.NoError(t, err)
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 90*time.Millisecond)
	assert.Less(t, elapsed, 300*time.Millisecond)
}

// Many messages sent back to back must all arrive after about one delay, not
// one delay each: the proxy must not serialize the stream, or it would flatten
// exactly the pipelining gains it exists to measure.
func TestProxy_DoesNotSerializeBackToBackMessages(t *testing.T) {
	const rtt = 100 * time.Millisecond
	const msgs, msgSize = 20, 100
	p := startProxy(t, ProxySpec{RTT: rtt})
	c, err := net.Dial("tcp", p.Addr())
	require.NoError(t, err)
	defer c.Close()

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < msgs; i++ {
			c.Write(make([]byte, msgSize))
			time.Sleep(2 * time.Millisecond) // keep reads separate
		}
	}()
	_, err = io.ReadFull(c, make([]byte, msgs*msgSize))
	require.NoError(t, err)
	wg.Wait()
	elapsed := time.Since(start)

	// serialized it would be ~msgs*rtt/2 = 1s or more
	assert.Less(t, elapsed, 2*rtt)
}

func TestProxy_BandwidthCapBoundsThroughput(t *testing.T) {
	p := startProxy(t, ProxySpec{Bandwidth: 1_000_000}) // 1 MB/s per direction
	c, err := net.Dial("tcp", p.Addr())
	require.NoError(t, err)
	defer c.Close()

	const n = 200_000
	start := time.Now()
	go func() { c.Write(make([]byte, n)) }()
	_, err = io.ReadFull(c, make([]byte, n))
	require.NoError(t, err)
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 180*time.Millisecond)
	assert.Less(t, elapsed, 1500*time.Millisecond)
}

// A bulk transfer must run at the configured rate, not a fraction of it: the
// pacer used to forfeit its sleep overshoot on every block and delivered only
// about 57% of the cap, which made capped benchmarks look bandwidth-starved.
func TestProxy_BandwidthCapIsDeliveredAtFullRate(t *testing.T) {
	const bw = 20_000_000 // 20 MB/s per direction
	const n = 10_000_000  // ideal: 0.5 s
	p := startProxy(t, ProxySpec{Bandwidth: bw})
	c, err := net.Dial("tcp", p.Addr())
	require.NoError(t, err)
	defer c.Close()

	start := time.Now()
	go func() {
		buf := make([]byte, 64*1024)
		for sent := 0; sent < n; sent += len(buf) {
			c.Write(buf)
		}
	}()
	_, err = io.ReadFull(c, make([]byte, n/64/1024*64*1024))
	require.NoError(t, err)
	elapsed := time.Since(start)

	ideal := time.Duration(float64(n) / bw * float64(time.Second))
	assert.Less(t, elapsed, ideal*125/100, "delivered well below the configured rate")
}

func TestProxy_CountsBytesPerDirectionAndResets(t *testing.T) {
	p := startProxy(t, ProxySpec{})
	c, err := net.Dial("tcp", p.Addr())
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Write(make([]byte, 5000))
	require.NoError(t, err)
	_, err = io.ReadFull(c, make([]byte, 5000))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		up, down := p.Counters()
		return up == 5000 && down == 5000
	}, time.Second, 5*time.Millisecond)

	p.ResetCounters()
	up, down := p.Counters()
	assert.Zero(t, up)
	assert.Zero(t, down)
}

func TestProxy_CloseStopsAcceptingAndReleasesConnections(t *testing.T) {
	p, err := StartProxy(startEcho(t), ProxySpec{})
	require.NoError(t, err)
	c, err := net.Dial("tcp", p.Addr())
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, p.Close())

	c.SetReadDeadline(time.Now().Add(time.Second))
	_, err = c.Read(make([]byte, 1))
	assert.Error(t, err, "open connection must be closed by Close")
	_, err = net.DialTimeout("tcp", p.Addr(), 200*time.Millisecond)
	assert.Error(t, err, "listener must be closed")
}
