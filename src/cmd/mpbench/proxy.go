package main

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type ProxySpec struct {
	RTT       time.Duration // full round trip; each direction is delayed by RTT/2
	Bandwidth int64         // bytes per second per direction; 0 = unlimited
}

// Proxy is a userspace TCP proxy that adds a fixed propagation delay and an
// optional bandwidth cap in each direction and counts the bytes it forwards.
// TLS passes through opaquely.
type Proxy struct {
	ln       net.Listener
	target   string
	delay    time.Duration
	bw       int64
	up, down atomic.Int64 // up = client -> server

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// StartProxy listens on 127.0.0.1 (random port) and forwards to target.
func StartProxy(target string, spec ProxySpec) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{ln: ln, target: target, delay: spec.RTT / 2, bw: spec.Bandwidth, conns: map[net.Conn]struct{}{}}
	p.wg.Add(1)
	go p.acceptLoop()
	return p, nil
}

func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// Port returns the TCP port the proxy listens on.
func (p *Proxy) Port() int { return p.ln.Addr().(*net.TCPAddr).Port }

// Counters returns bytes forwarded client->server (up) and server->client
// (down) since the last ResetCounters.
func (p *Proxy) Counters() (up, down int64) { return p.up.Load(), p.down.Load() }

func (p *Proxy) ResetCounters() {
	p.up.Store(0)
	p.down.Store(0)
}

func (p *Proxy) Close() error {
	p.mu.Lock()
	p.closed = true
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()
	err := p.ln.Close()
	p.wg.Wait()
	return err
}

func (p *Proxy) register(cs ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	for _, c := range cs {
		p.conns[c] = struct{}{}
	}
	return true
}

func (p *Proxy) unregister(cs ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range cs {
		delete(p.conns, c)
	}
}

func (p *Proxy) acceptLoop() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go p.handle(c)
	}
}

func (p *Proxy) handle(client net.Conn) {
	defer p.wg.Done()
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		client.Close()
		return
	}
	if !p.register(client, server) {
		client.Close()
		server.Close()
		return
	}
	defer func() {
		p.unregister(client, server)
		client.Close()
		server.Close()
	}()

	var inner sync.WaitGroup
	inner.Add(2)
	go func() { defer inner.Done(); p.pipe(server, client, &p.up) }()
	go func() { defer inner.Done(); p.pipe(client, server, &p.down) }()
	inner.Wait()
}

// paceBurst is how far the bandwidth pacer may run ahead of (or behind) the
// ideal schedule; it absorbs timer overshoot without changing the average rate.
const paceBurst = 5 * time.Millisecond

type block struct {
	data []byte
	at   time.Time
}

func sleepUntil(t time.Time) {
	if d := time.Until(t); d > 0 {
		time.Sleep(d)
	}
}

// pipe copies src to dst. The reader stamps every block with its arrival time
// and a separate writer releases it at arrival+delay, so blocks that arrive
// together leave together: the delay is propagation, not a per-read stall.
// With a bandwidth cap, each block then occupies the link for len/bandwidth.
func (p *Proxy) pipe(dst, src net.Conn, counter *atomic.Int64) {
	q := make(chan block, 1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var nextFree time.Time
		for b := range q {
			sleepUntil(b.at.Add(p.delay))
			if p.bw > 0 {
				// nextFree is when the link finishes sending everything
				// so far. A sleep always overshoots a little; the pacer
				// carries that lag forward (up to paceBurst of credit
				// after an idle spell) rather than forgetting it, and
				// only sleeps once it is more than paceBurst ahead.
				// Resetting nextFree to now on every block forfeited each
				// overshoot and delivered only ~57% of the cap.
				now := time.Now()
				if nextFree.Before(now.Add(-paceBurst)) {
					nextFree = now.Add(-paceBurst)
				}
				nextFree = nextFree.Add(time.Duration(float64(len(b.data)) / float64(p.bw) * float64(time.Second)))
				if time.Until(nextFree) > paceBurst {
					sleepUntil(nextFree.Add(-paceBurst))
				}
			}
			if _, err := dst.Write(b.data); err != nil {
				for range q { // keep the reader from blocking
				}
				return
			}
			counter.Add(int64(len(b.data)))
		}
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	buf := make([]byte, 32*1024)
	var readErr error
	for {
		n, err := src.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			q <- block{data: data, at: time.Now()}
		}
		if err != nil {
			readErr = err
			break
		}
	}
	close(q)
	<-done
	if readErr != io.EOF {
		dst.Close() // a reset or a Close: propagate to the other side
	}
}
