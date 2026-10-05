package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	phaseBackupCold = "backup-cold"
	phaseBackupWarm = "backup-warm"
	phaseRestore    = "restore"
)

func checkBinaries(dir string) error {
	for _, name := range []string{"brfs", "bwfs", "rwfs"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s not found in --bin-dir %s", name, dir)
		}
		if !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
			return fmt.Errorf("%s in --bin-dir %s is not an executable file", name, dir)
		}
	}
	return nil
}

func brfsArgs(a *Args, src, dest string) []string {
	args := []string{src, "--destination", dest, "--quiet", "--streams", strconv.Itoa(a.Streams)}
	if a.Window > 0 {
		args = append(args, "--window", strconv.Itoa(a.Window))
	}
	return append(args, a.BrfsArgs...)
}

func rwfsArgs(a *Args, dest string) []string {
	args := []string{"restore", dest, "--rules-stdin", "--quiet", "--streams", strconv.Itoa(a.Streams)}
	return append(args, a.RwfsArgs...)
}

// restoreRules selects the whole source tree (any host) and renames it onto
// the restore directory.
func restoreRules(src, restored string) string {
	rules := map[string]any{"rules": []map[string]any{
		{"host": "", "path": src, "include": true, "dest_path": restored},
	}}
	data, _ := json.Marshal(rules)
	return string(data)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// proc is a long-running child process (bwfs).
type proc struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func startProc(ctx context.Context, bin string, args, env []string, logPath string) (*proc, error) {
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(pctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) } // graceful first
	cmd.WaitDelay = 5 * time.Second                                       // then kill
	if err := cmd.Start(); err != nil {
		cancel()
		logFile.Close()
		return nil, err
	}
	p := &proc{cancel: cancel, done: make(chan struct{})}
	go func() {
		cmd.Wait()
		logFile.Close()
		close(p.done)
	}()
	return p, nil
}

func (p *proc) stop() {
	p.cancel()
	<-p.done
}

func waitListening(ctx context.Context, addr string, timeout time.Duration, died <-chan struct{}) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-died:
			return fmt.Errorf("process exited before listening on %s", addr)
		default:
		}
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("nothing listening on %s after %v", addr, timeout)
}

func tail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

// runTool runs a short-lived client binary to completion, keeping its output
// in logPath and returning its tail on failure.
func runTool(ctx context.Context, logPath, bin string, args, env []string, stdin string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(logPath, out, 0o644)
	if err != nil {
		return fmt.Errorf("%s failed: %w\n%s", filepath.Base(bin), err, tail(out, 2000))
	}
	return nil
}

// RunOnce performs one full cycle in a fresh work directory and store:
// cold backup, warm backup, restore, then a byte comparison of the restored
// tree with the source.
func RunOnce(ctx context.Context, a *Args, idx int, logf func(string, ...any)) (*RunResult, error) {
	work, err := os.MkdirTemp("", "mpbench-*")
	if err != nil {
		return nil, err
	}
	if work, err = filepath.EvalSymlinks(work); err != nil {
		return nil, err
	}
	if a.Keep {
		logf("work directory kept: %s", work)
	} else {
		defer os.RemoveAll(work)
	}

	cfg := filepath.Join(work, "cfg")
	if err := WriteIdentity(cfg); err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	if err := WriteLocalConf(cfg, filepath.Join(work, "logs"), a.Streams, a.ConfLines...); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	env := []string{"MP_CONFIG_PATH=" + cfg}

	src := filepath.Join(work, "src")
	restored := filepath.Join(work, "restored")
	store := filepath.Join(work, "store")
	for _, d := range []string{src, store} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	ds, err := Generate(DatasetSpec{Dir: src, Files: a.Files, Profile: a.Profile, DupRatio: a.DupRatio, Seed: a.Seed})
	if err != nil {
		return nil, fmt.Errorf("dataset: %w", err)
	}
	logf("dataset: %d files, %s", len(ds.Files), humanBytes(ds.TotalBytes))

	port, err := freePort()
	if err != nil {
		return nil, err
	}
	backend := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	bw, err := startProc(ctx, filepath.Join(a.BinDir, "bwfs"),
		[]string{store, "server", "--port", strconv.Itoa(port), "--quiet"}, env, filepath.Join(work, "bwfs.log"))
	if err != nil {
		return nil, fmt.Errorf("start bwfs: %w", err)
	}
	defer bw.stop()
	if err := waitListening(ctx, backend, 15*time.Second, bw.done); err != nil {
		return nil, fmt.Errorf("bwfs: %w (see %s)", err, filepath.Join(work, "bwfs.log"))
	}

	px, err := StartProxy(backend, ProxySpec{RTT: a.RTT, Bandwidth: a.Bandwidth})
	if err != nil {
		return nil, fmt.Errorf("proxy: %w", err)
	}
	defer px.Close()
	dest := "localhost:" + strconv.Itoa(px.Port())

	res := &RunResult{Index: idx, DatasetFiles: len(ds.Files), DatasetBytes: ds.TotalBytes}
	measure := func(name string, fn func() error) error {
		px.ResetCounters()
		start := time.Now()
		if err := fn(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		secs := time.Since(start).Seconds()
		up, down := px.Counters()
		p := newPhaseResult(name, secs, ds.TotalBytes, len(ds.Files), up, down)
		res.Phases = append(res.Phases, p)
		logf("%s: %.2fs, %.2f MB/s, wire up %s down %s", name, p.Seconds, p.MBPerSec, humanBytes(up), humanBytes(down))
		return nil
	}

	brfs := filepath.Join(a.BinDir, "brfs")
	rwfs := filepath.Join(a.BinDir, "rwfs")

	if err := measure(phaseBackupCold, func() error {
		return runTool(ctx, filepath.Join(work, "brfs-cold.log"), brfs, brfsArgs(a, src, dest), env, "")
	}); err != nil {
		return nil, err
	}

	if err := ds.Touch(); err != nil {
		return nil, fmt.Errorf("touch: %w", err)
	}
	if err := measure(phaseBackupWarm, func() error {
		return runTool(ctx, filepath.Join(work, "brfs-warm.log"), brfs, brfsArgs(a, src, dest), env, "")
	}); err != nil {
		return nil, err
	}

	if err := measure(phaseRestore, func() error {
		return runTool(ctx, filepath.Join(work, "rwfs.log"), rwfs, rwfsArgs(a, dest), env, restoreRules(src, restored))
	}); err != nil {
		return nil, err
	}

	if err := CompareTrees(src, restored); err != nil {
		return nil, fmt.Errorf("verify: restored tree does not match the source: %w", err)
	}
	logf("verify: restored tree matches the source")
	return res, nil
}
