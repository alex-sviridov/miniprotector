package connection

import (
	"fmt"
	"path/filepath"
	"regexp"
)

// serviceNameRe matches a fully qualified gRPC service name made of
// non-empty dot-separated identifiers -- deliberately too narrow to contain a
// path separator or "..", since the name becomes a file name.
var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)*$`)

// ValidServiceName reports whether name is safe to use as a socket file name.
func ValidServiceName(name string) bool { return serviceNameRe.MatchString(name) }

// ServicesDir is the directory holding one Unix socket per fronted service.
func ServicesDir(runDir string) string { return filepath.Join(runDir, "services") }

// ServiceSocketPath is where the backend serving gRPC service `service` listens
// and where the listener looks for it.
func ServiceSocketPath(runDir, service string) (string, error) {
	if !ValidServiceName(service) {
		return "", fmt.Errorf("invalid service name %q", service)
	}
	return filepath.Join(ServicesDir(runDir), service+".sock"), nil
}
