// vector.go: agent's ownership of the bundled Vector process's binary
// resolution, config generation, and supervision. See
// docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"text/template"

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
        # Override Vector's own read-time .timestamp with the app's actual
        # log time (slog's "time" field) wherever it parses -- otherwise
        # ordering/display (both keyed on .timestamp downstream) reflect
        # when Vector caught up reading the file, not when the event
        # happened, and those diverge inconsistently across hosts/binaries
        # after any read lag (restart catch-up, disk buffer backpressure).
        if is_string(parsed.time) {
          ts, ts_err = parse_timestamp(parsed.time, "%+")
          if ts_err == null {
            .timestamp = ts
          }
        }
      }

sinks:
  loki_gateway:
    type: loki
    inputs: ["add_binary_label"]
    endpoint: "https://{{ .LogGatewayHost }}:{{ .LogGatewayPort }}"
    encoding:
      codec: text
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
      when_full: block
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

// newVectorSupervisor builds a processSupervisor configured for agent's
// bundled Vector process: no StabilityWindow and no OnOutcome (Vector
// isn't tracked in agent-state.json/list-policies, unlike storage tasks --
// see storage.go's storageManager), and Vector's own stdout/stderr
// rotated to disk via lumberjack, since they were previously silently
// discarded and are the only way to see a Vector-side failure (a config
// problem, a sink healthcheck failure, a buffer error) without manually
// re-running the binary by hand.
func newVectorSupervisor(binary, configPath string, logger *slog.Logger) *processSupervisor {
	args := []string{}
	if configPath != "" {
		args = []string{"--config", configPath}
	}

	var stdout, stderr io.Writer
	if configPath != "" {
		ljLogger := &lumberjack.Logger{
			Filename:   filepath.Join(filepath.Dir(configPath), "vector-output.log"),
			MaxSize:    50, // megabytes
			MaxBackups: 5,
			MaxAge:     14, // days
			Compress:   true,
		}
		stdout, stderr = ljLogger, ljLogger
	}

	return newProcessSupervisor(supervisorConfig{
		Binary:  binary,
		Args:    args,
		Logger:  logger,
		Backoff: defaultBackoffPolicy,
		Stdout:  stdout,
		Stderr:  stderr,
	})
}
