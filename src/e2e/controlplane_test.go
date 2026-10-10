//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// controlPlaneContainer describes what one control-plane container must be
// running (all of it under agent, which is PID 1) and which TCP ports it may
// listen on. See docs/components/agent.md#running-services and
// docs/components/listener.md.
type controlPlaneContainer struct {
	service string
	// processes are the supervised children, as the path prefix of their
	// command line (agent starts them by absolute path next to itself).
	processes []string
	// ports is the exact set of TCP ports the container listens on, apart
	// from Docker's embedded DNS resolver.
	ports []int
	// restPath is a REST route served by api-server that needs this
	// container's gRPC service, used to prove it works again after a crash.
	restPath string
}

var controlPlane = []controlPlaneContainer{
	{"catalog", []string{"/app/listener", "/app/catalog"}, []int{9300}, "/api/v1/catalog"},
	{"policy-server", []string{"/app/listener", "/app/policy-server"}, []int{9300}, "/api/v1/policies"},
	{"clientmanager-api", []string{"/app/listener", "/app/clientmanager-api", "/app/clientmanager-admin-api"}, []int{9300}, "/api/v1/clients"},
	// api-server's REST port stays its own; only its gRPC services moved
	// behind the listener.
	{"api-server", []string{"/app/listener", "/app/api-server"}, []int{8090, 9300}, "/api/v1/clients"},
	// log-gateway is an HTTP service: no listener, own port.
	{"log-gateway", []string{"/app/log-gateway"}, []int{9400}, ""},
}

// processes returns pid -> command line for every process in the container.
func processes(t *testing.T, service string) map[int]string {
	t.Helper()
	out, err := dockerComposeExec(t, service, "sh", "-c",
		`for p in /proc/[0-9]*; do printf '%s ' "${p#/proc/}"; tr '\0' ' ' < $p/cmdline 2>/dev/null; echo; done`)
	require.NoError(t, err, out)
	procs := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		pidStr, cmd, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		procs[pid] = cmd
	}
	return procs
}

// pidOf returns the pid of the process whose command line starts with prefix
// followed by a space or end of line, or 0 if none.
func pidOf(procs map[int]string, prefix string) int {
	for pid, cmd := range procs {
		if cmd == prefix || strings.HasPrefix(cmd, prefix+" ") {
			return pid
		}
	}
	return 0
}

// listeningPorts returns the sorted TCP ports the container listens on,
// excluding Docker's embedded DNS resolver (127.0.0.11, a random port that
// lives in every container's network namespace but belongs to no process of
// ours).
func listeningPorts(t *testing.T, service string) []int {
	t.Helper()
	out, err := dockerComposeExec(t, service, "sh", "-c", "cat /proc/net/tcp /proc/net/tcp6")
	require.NoError(t, err, out)
	seen := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != "0A" { // 0A = LISTEN
			continue
		}
		addr, port, ok := strings.Cut(f[1], ":")
		if !ok || addr == "0B00007F" { // 127.0.0.11, little-endian hex
			continue
		}
		p, err := strconv.ParseInt(port, 16, 32)
		require.NoError(t, err)
		seen[int(p)] = true
	}
	ports := make([]int, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports
}

// killProcess sends SIGTERM to the process in service whose command line
// starts with prefix. The images have no pkill, so scan /proc.
func killProcess(t *testing.T, service, prefix string) {
	t.Helper()
	script := fmt.Sprintf(`for p in /proc/[0-9]*; do c=$(tr '\0' ' ' < $p/cmdline 2>/dev/null); case "$c" in "%s "*) kill ${p#/proc/};; esac; done`, prefix)
	out, err := dockerComposeExec(t, service, "sh", "-c", script)
	require.NoError(t, err, out)
}

// TestE2E_ControlPlaneContainersRunOnlyUnderAgent: agent is PID 1 in every
// control-plane container and has started exactly the processes that
// container's local.conf lists as running_service=.
func TestE2E_ControlPlaneContainersRunOnlyUnderAgent(t *testing.T) {
	for _, c := range controlPlane {
		t.Run(c.service, func(t *testing.T) {
			procs := processes(t, c.service)
			require.True(t, strings.HasPrefix(procs[1], "./agent serve"), "PID 1 must be agent, got %q", procs[1])
			for _, want := range c.processes {
				assert.NotZero(t, pidOf(procs, want), "%s must be running under agent", want)
			}
		})
	}
}

// TestE2E_ControlPlanePortsAreListenerOnly: behind the listener, the gRPC
// services open no TCP port of their own -- the only new network surface on a
// fronted host is the listener's :9300.
func TestE2E_ControlPlanePortsAreListenerOnly(t *testing.T) {
	for _, c := range controlPlane {
		t.Run(c.service, func(t *testing.T) {
			assert.Equal(t, c.ports, listeningPorts(t, c.service))
		})
	}
}

// TestE2E_ListenerAnswersUnimplementedForServiceNotOnHost: rwfs speaks the
// list protocol, which only bwfs serves. Pointed at catalog's listener
// (catalog:9300) -- a host that has no such service -- the call must fail
// with the listener's UNIMPLEMENTED, not hang or surface as a bare connection
// error. The same rwfs against the real store still works.
func TestE2E_ListenerAnswersUnimplementedForServiceNotOnHost(t *testing.T) {
	out, err := dockerComposeExec(t, "database", "./rwfs", "list", "catalog:9300")
	require.Error(t, err, "rwfs list against a host without the list service must fail:\n%s", out)
	assert.Contains(t, out, "not served on this host", "output:\n%s", out)

	out, err = dockerComposeExec(t, "database", "./rwfs", "list", "store:8080")
	require.NoError(t, err, "rwfs list against the real store must still work:\n%s", out)
}

// TestE2E_SupervisedServicesRestartAfterCrash: kill the listener, then the
// service itself, in each fronted container; agent must restart both within
// seconds and the REST route that depends on that service must work again.
func TestE2E_SupervisedServicesRestartAfterCrash(t *testing.T) {
	for _, c := range controlPlane {
		if c.restPath == "" {
			continue
		}
		for _, victim := range []string{"/app/listener", c.processes[1]} {
			t.Run(c.service+"/"+strings.TrimPrefix(victim, "/app/"), func(t *testing.T) {
				before := pidOf(processes(t, c.service), victim)
				require.NotZero(t, before, "%s must be running before the crash", victim)

				killProcess(t, c.service, victim)

				// A restarted process has a different pid. agent's backoff
				// for running services is ~1s; allow generous slack.
				require.Eventually(t, func() bool {
					now := pidOf(processes(t, c.service), victim)
					return now != 0 && now != before
				}, 30*time.Second, 500*time.Millisecond, "%s was not restarted", victim)

				require.Eventually(t, func() bool {
					resp := apiRequest(t, http.MethodGet, c.restPath, nil)
					defer resp.Body.Close()
					return resp.StatusCode == http.StatusOK
				}, 30*time.Second, time.Second, "GET %s did not recover", c.restPath)
			})
		}
	}
}

// TestE2E_StorageStatusReachesApiServerThroughListener: bwfs reports its
// status to api-server once a minute over the listener on api-server's host
// (StorageStatusService, "store" role); the reporting host comes from the
// forwarded client certificate. The REST status endpoint must show the store
// online.
func TestE2E_StorageStatusReachesApiServerThroughListener(t *testing.T) {
	id := fetchStoragePolicyID(t, "store")

	require.Eventually(t, func() bool {
		resp := apiRequest(t, http.MethodGet, "/api/v1/storage-policies/"+id+"/status", nil)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return false
		}
		var body struct {
			Reports []struct {
				Hostname string `json:"hostname"`
				State    string `json:"state"`
			} `json:"reports"`
		}
		decodeJSON(t, resp, &body)
		for _, r := range body.Reports {
			if r.Hostname == "store" && r.State == "online" {
				return true
			}
		}
		return false
	}, 120*time.Second, 5*time.Second, "store never appeared online in the storage status")
}
