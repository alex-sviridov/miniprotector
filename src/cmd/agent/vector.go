// vector.go: agent's ownership of the bundled Vector process's binary
// resolution, config generation, and supervision. See
// docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
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
      .binary = replace!(path.strip_dir!(.file), ".log", "")

sinks:
  loki_gateway:
    type: loki
    inputs: ["add_binary_label"]
    endpoint: "https://{{ .LogGatewayHost }}:{{ .LogGatewayPort }}"
    encoding:
      codec: json
    labels:
      binary: "{{"{{ binary }}"}}"
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
}

// renderVectorConfig builds Vector's config from this node's own resolved
// paths and local.conf values -- never a static file, since all of these
// are deployment-specific and only known after agent has parsed its own
// config.
func renderVectorConfig(logDir, varDir, certsDir, logGatewayHost string, logGatewayPort int) (string, error) {
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
	}); err != nil {
		return "", fmt.Errorf("render vector config: %w", err)
	}
	return buf.String(), nil
}
