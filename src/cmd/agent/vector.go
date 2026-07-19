// vector.go: agent's ownership of the bundled Vector process's binary
// resolution, config generation, and supervision. See
// docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"text/template"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// resolveVectorBinary finds the Vector binary colocated with agent's own
// executable -- unlike realExec's resolution for certclient/policyclient/
// brfs, there is deliberately no $PATH fallback: Vector is a third-party
// tool that may already exist elsewhere on a host for an unrelated
// purpose, and silently picking up a different, unpinned version there
// would be a correctness landmine, not a convenience.
func resolveVectorBinary() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("determine own executable path: %w", err)
	}
	return resolveVectorBinaryIn(filepath.Dir(exePath))
}

// resolveVectorBinaryIn is resolveVectorBinary's testable core.
func resolveVectorBinaryIn(dir string) (string, error) {
	candidate := filepath.Join(dir, "vector")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("vector binary not found at %s (bundled alongside agent, no $PATH fallback): %w", candidate, err)
	}
	return candidate, nil
}

// vectorConfigTemplate is Vector's own config format (YAML). Vector's
// `{{ binary }}` label templating syntax is escaped as a literal string so
// Go's text/template doesn't try to parse it as its own action.
const vectorConfigTemplate = `data_dir: {{ .VarDir }}/vector-data

sources:
  local_logs:
    type: file
    include:
      - "{{ .LogDir }}/*.log"

transforms:
  add_binary_label:
    type: remap
    inputs: ["local_logs"]
    source: |
      parts = split!(.file, "/")
      .binary = replace!(parts[-1], ".log", "")
      parsed, err = parse_json(.message)
      if err == null {
        .job_id = parsed.job_id
        .event = parsed.event
        .status = parsed.status
      }

sinks:
  loki_gateway:
    type: loki
    inputs: ["add_binary_label"]
    endpoint: "https://{{ .LogGatewayHost }}:{{ .LogGatewayPort }}"
    encoding:
      codec: json
    labels:
      binary: "{{"{{ binary }}"}}"
      hostname: "{{ .Hostname }}"
    structured_metadata:
      job_id: "{{"{{ job_id }}"}}"
      event: "{{"{{ event }}"}}"
      status: "{{"{{ status }}"}}"
    tls:
      ca_file: "{{ .CertsDir }}/ca.crt"
      crt_file: "{{ .CertsDir }}/client.crt"
      key_file: "{{ .CertsDir }}/client.key"
    buffer:
      type: disk
      max_size: 268435488
      when_full: drop_newest
`

type vectorConfigData struct {
	LogDir         string
	VarDir         string
	CertsDir       string
	LogGatewayHost string
	LogGatewayPort int
	Hostname       string
}

// renderVectorConfig builds Vector's config from this node's own resolved
// paths and local.conf values -- never a static file, since all of these
// are deployment-specific and only known after agent has parsed its own
// config. hostname becomes every shipped stream's "hostname" label:
// log-gateway authenticates the push (a valid operating certificate is
// required) but never inspects or rewrites the body, so Vector itself must
// be the one to set this label -- see docs/SECURITY.md.
func renderVectorConfig(logDir, varDir, certsDir, logGatewayHost string, logGatewayPort int, hostname string) (string, error) {
	tmpl, err := template.New("vector-config").Parse(vectorConfigTemplate)
	if err != nil {
		return "", fmt.Errorf("parse vector config template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vectorConfigData{
		LogDir:         logDir,
		VarDir:         varDir,
		CertsDir:       certsDir,
		LogGatewayHost: logGatewayHost,
		LogGatewayPort: logGatewayPort,
		Hostname:       hostname,
	}); err != nil {
		return "", fmt.Errorf("render vector config: %w", err)
	}
	return buf.String(), nil
}

// hostnameFromBootstrapCert parses this node's own hostname from its
// bootstrap credential's Subject.CommonName -- mirrors
// cmd/certclient/operatingrefresh.go's helper of the same name exactly;
// duplicated rather than shared since agent and certclient are separate
// binaries with no existing common package for this one-line lookup (this
// codebase's established convention for small per-binary helpers, e.g.
// cmd/log-gateway/e2e_test.go's comment on the same trade-off).
func hostnameFromBootstrapCert(certsDir string) (string, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, "bootstrap.crt"),
		filepath.Join(certsDir, "bootstrap.key"),
	)
	if err != nil {
		return "", fmt.Errorf("load bootstrap credential: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("parse bootstrap certificate: %w", err)
	}
	if leaf.Subject.CommonName == "" {
		return "", fmt.Errorf("bootstrap certificate has no CommonName")
	}
	return leaf.Subject.CommonName, nil
}

// vectorSupervisor owns the lifecycle of agent's bundled Vector process: a
// long-running child, not a due-and-complete Policy exec, so it gets its
// own small supervision loop rather than being shoehorned into
// reconcile.go's due/execute/record cycle. It restarts Vector immediately
// (no backoff) whenever TriggerRestart is called -- the expected,
// roughly-15-minute-interval event of a fresh operating cert landing --
// and with the same jittered backoff() reconcile.go already uses for
// failing policies whenever Vector exits unexpectedly for any other
// reason.
type vectorSupervisor struct {
	binary     string
	configPath string
	logger     *slog.Logger

	mu           sync.Mutex
	cmd          *exec.Cmd
	shuttingDown bool
	restarting   bool

	// onSpawnForTest, when non-nil, is called once per spawn attempt --
	// test-only instrumentation, never set in production.
	onSpawnForTest func()

	// loopDone is closed when superviseLoop returns, giving callers (and
	// tests) a real signal to synchronize on instead of guessing at a
	// sleep duration -- important because ctx cancellation alone must
	// also tear down whatever Vector process is currently running, not
	// just stop future respawns.
	loopDone chan struct{}
}

func newVectorSupervisor(binary, configPath string, logger *slog.Logger) *vectorSupervisor {
	return &vectorSupervisor{binary: binary, configPath: configPath, logger: logger}
}

// Start launches the supervise loop in its own goroutine and returns
// immediately; the loop itself runs until ctx is done, at which point the
// currently-running Vector process (if any) is also signalled to exit --
// ctx cancellation is a real shutdown path, not just a "stop respawning"
// switch.
func (v *vectorSupervisor) Start(ctx context.Context) {
	v.loopDone = make(chan struct{})
	go func() {
		defer close(v.loopDone)
		v.superviseLoop(ctx)
	}()
}

// TriggerRestart signals the currently-running Vector process to exit
// (SIGTERM) and marks the next respawn as deliberate, so the supervise
// loop skips the crash-backoff delay for it.
func (v *vectorSupervisor) TriggerRestart() {
	v.mu.Lock()
	cmd := v.cmd
	v.restarting = true
	v.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

// Stop signals the currently-running Vector process to exit and tells the
// supervise loop not to respawn it.
func (v *vectorSupervisor) Stop() {
	v.mu.Lock()
	v.shuttingDown = true
	cmd := v.cmd
	v.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (v *vectorSupervisor) superviseLoop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		if err := v.spawnAndWait(ctx); err != nil {
			v.logger.Error("vector process error", "error", err)
		}

		v.mu.Lock()
		shuttingDown := v.shuttingDown
		deliberate := v.restarting
		v.restarting = false
		v.mu.Unlock()

		if shuttingDown || ctx.Err() != nil {
			return
		}
		if deliberate {
			failures = 0
			continue
		}

		failures++
		v.logger.Error("vector exited unexpectedly, restarting with backoff", "failures", failures)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff(failures)):
		}
	}
}

// spawnAndWait starts Vector and blocks until it exits. If ctx is cancelled
// while Vector is still running, it is sent SIGTERM so cancellation is a
// real shutdown signal for the child process, not just a marker that stops
// future respawns -- without this, a caller that only cancels ctx (rather
// than also calling Stop) would leak both the Vector process and this
// goroutine forever, since cmd.Wait() would never return.
//
// cmd.Start() runs under v.mu so v.cmd is updated atomically with the
// process actually starting: without this, TriggerRestart/Stop could read
// v.cmd in the gap between a successful Start() and the assignment below,
// see the previous (already-exited) cmd, and signal a no-op instead of the
// live process -- self-healing for TriggerRestart (the restarting flag
// just persists to the next exit) but a real leak for Stop, the same class
// of bug as the ctx-cancellation case above. cmd.Start() itself is a
// fork/exec that returns as soon as the child is launched -- it does not
// block the way cmd.Wait() does -- so holding the lock across it only
// costs a concurrent TriggerRestart/Stop a fork/exec-duration wait, not a
// process-lifetime one. v.cmd is only assigned on success, so a failed
// Start() never leaves v.cmd pointing at a cmd that never ran; it leaves
// the previous (already-exited) cmd in place, which is harmless since
// TriggerRestart/Stop signaling it is already a no-op.
func (v *vectorSupervisor) spawnAndWait(ctx context.Context) error {
	args := []string{}
	if v.configPath != "" {
		args = []string{"--config", v.configPath}
	}
	cmd := exec.Command(v.binary, args...)

	// Vector's own stdout/stderr are otherwise silently discarded -- the
	// only signal this supervisor previously surfaced on a failure was
	// its own "exit status N", with no way to see Vector's own error
	// message (e.g. a config problem, a sink healthcheck failure, a
	// buffer error) without manually re-running the binary by hand.
	// Rotated the same way common/logging rotates every other binary's
	// own log (lumberjack, same size/backup/age/compress values) --
	// unbounded growth here would otherwise be a real disk-fill risk on
	// a long-running node that's crash-looping or repeatedly failing its
	// sink healthcheck, both of which write continuously. Best-effort:
	// nothing about this affects Vector supervision itself.
	if v.configPath != "" {
		ljLogger := &lumberjack.Logger{
			Filename:   filepath.Join(filepath.Dir(v.configPath), "vector-output.log"),
			MaxSize:    50, // megabytes
			MaxBackups: 5,
			MaxAge:     14, // days
			Compress:   true,
		}
		defer ljLogger.Close()
		cmd.Stdout = ljLogger
		cmd.Stderr = ljLogger
	}

	v.mu.Lock()
	err := cmd.Start()
	if err == nil {
		v.cmd = cmd
	}
	v.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start vector: %w", err)
	}
	if v.onSpawnForTest != nil {
		v.onSpawnForTest()
	}

	waitDone := make(chan struct{})
	defer close(waitDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
		case <-waitDone:
		}
	}()

	return cmd.Wait()
}
