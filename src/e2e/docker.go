//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// testingT is the subset of testing.T used by docker helpers.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
	Log(args ...any)
	Logf(format string, args ...any)
	Cleanup(func())
	Errorf(format string, args ...any)
	FailNow()
}

func newDockerClient(t testingT) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	return cli
}

// buildImage builds the e2e Docker image from the repo root.
// repoRoot is the directory containing src/ and src/e2e/Dockerfile.
// Returns the image ID. Caller is responsible for cleanup.
func buildImage(ctx context.Context, t testingT, repoRoot string) string {
	t.Helper()
	cli := newDockerClient(t)
	defer cli.Close()

	// Create build context tar from repoRoot
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	err := addDirToTar(tw, repoRoot, "")
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	resp, err := cli.ImageBuild(ctx, buf, types.ImageBuildOptions{
		Dockerfile: "src/e2e/Dockerfile",
		Remove:     true,
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	// Stream build output to test log; extract image ID
	var imageID string
	dec := json.NewDecoder(resp.Body)
	for dec.More() {
		var msg struct {
			Stream string `json:"stream"`
			Aux    *struct {
				ID string `json:"ID"`
			} `json:"aux"`
			Error string `json:"error"`
		}
		require.NoError(t, dec.Decode(&msg))
		if msg.Error != "" {
			t.Fatalf("docker build error: %s", msg.Error)
		}
		if msg.Stream != "" {
			t.Log("[docker build]", msg.Stream)
		}
		if msg.Aux != nil && msg.Aux.ID != "" {
			imageID = msg.Aux.ID
		}
	}
	require.NotEmpty(t, imageID, "docker build did not return image ID")
	return imageID
}

func addDirToTar(tw *tar.Writer, srcDir, prefix string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		tarPath := filepath.Join(prefix, rel)
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = tarPath
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

// createNetwork creates an isolated bridge network and registers cleanup.
func createNetwork(ctx context.Context, t testingT) string {
	t.Helper()
	cli := newDockerClient(t)

	name := fmt.Sprintf("e2e-test-%d", time.Now().UnixNano())
	resp, err := cli.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge"})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = cli.NetworkRemove(context.Background(), resp.ID)
		cli.Close()
	})
	return resp.ID
}

// startBwfsContainer starts bwfs server and returns the host port it's mapped to.
// storageDir on the host is bind-mounted to /storage inside the container (host-readable,
// required so the test can open metadata.db directly for checksum validation).
// A named Docker volume backs /tmp inside the container for scratch/log space.
func startBwfsContainer(ctx context.Context, t testingT, imageID, networkID, storageDir string) string {
	t.Helper()
	cli := newDockerClient(t)

	hostPort, err := freePort()
	require.NoError(t, err)

	volName := fmt.Sprintf("e2e-scratch-%d", time.Now().UnixNano())
	_, err = cli.VolumeCreate(ctx, volume.CreateOptions{Name: volName})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cli.VolumeRemove(context.Background(), volName, true)
	})

	containerPort := nat.Port("15722/tcp")
	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image:        imageID,
			Cmd:          []string{"/app/bwfs", "/storage", "server", "--port", "15722", "--quiet"},
			ExposedPorts: nat.PortSet{containerPort: struct{}{}},
		},
		&container.HostConfig{
			Binds: []string{
				storageDir + ":/storage",
				volName + ":/tmp",
			},
			PortBindings: nat.PortMap{
				containerPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: hostPort}},
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkID: {NetworkID: networkID, Aliases: []string{"bwfs"}},
			},
		},
		nil,
		"bwfs-server",
	)
	require.NoError(t, err)

	require.NoError(t, cli.ContainerStart(ctx, resp.ID, container.StartOptions{}))

	t.Cleanup(func() {
		stopCtx := context.Background()
		timeout := 5
		_ = cli.ContainerStop(stopCtx, resp.ID, container.StopOptions{Timeout: &timeout})
		_ = cli.ContainerRemove(stopCtx, resp.ID, container.RemoveOptions{Force: true})
		cli.Close()
	})

	return hostPort
}

func freePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	return port, err
}

// waitForBwfs polls gRPC dial until bwfs is ready or timeout expires.
func waitForBwfs(ctx context.Context, hostPort string) error {
	deadline := time.Now().Add(15 * time.Second)
	addr := "127.0.0.1:" + hostPort
	for time.Now().Before(deadline) {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("bwfs at %s did not become ready within 15s", addr)
}

// runBrfsContainer runs brfs as a one-shot container and returns the exit code.
// dataDir on the host is bind-mounted to /testdata inside the container.
// bwfsContainerName is the DNS name of the bwfs container on the shared network.
func runBrfsContainer(ctx context.Context, t testingT, imageID, networkID, dataDir, bwfsContainerName string, streams int) int {
	t.Helper()
	cli := newDockerClient(t)
	defer cli.Close()

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: imageID,
			Cmd: []string{
				"/app/brfs", "/testdata",
				"--destination", fmt.Sprintf("%s:15722", bwfsContainerName),
				"--streams", fmt.Sprintf("%d", streams),
				"--quiet",
			},
		},
		&container.HostConfig{
			Binds: []string{dataDir + ":/testdata:ro"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkID: {NetworkID: networkID},
			},
		},
		nil,
		"",
	)
	require.NoError(t, err)
	defer func() {
		_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	}()

	require.NoError(t, cli.ContainerStart(ctx, resp.ID, container.StartOptions{}))

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case status := <-statusCh:
		if status.Error != nil {
			t.Logf("brfs container error: %s", status.Error.Message)
			logContainerOutput(ctx, t, cli, resp.ID)
		}
		return int(status.StatusCode)
	}
	return -1
}

// runBwfsListContainer runs `bwfs list --output json` and returns stdout.
func runBwfsListContainer(ctx context.Context, t testingT, imageID, networkID, storageDir string) []byte {
	t.Helper()
	cli := newDockerClient(t)
	defer cli.Close()

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: imageID,
			Cmd:   []string{"/app/bwfs", "/storage", "list", "--output", "json"},
		},
		&container.HostConfig{
			Binds: []string{storageDir + ":/storage"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkID: {NetworkID: networkID},
			},
		},
		nil,
		"",
	)
	require.NoError(t, err)
	defer func() {
		_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	}()

	require.NoError(t, cli.ContainerStart(ctx, resp.ID, container.StartOptions{}))

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case status := <-statusCh:
		require.Equal(t, int64(0), status.StatusCode, "bwfs list exited non-zero")
	}

	out, err := cli.ContainerLogs(ctx, resp.ID, container.LogsOptions{ShowStdout: true})
	require.NoError(t, err)
	defer out.Close()
	data, err := io.ReadAll(out)
	require.NoError(t, err)
	// Docker multiplexes stdout/stderr with an 8-byte header per frame; strip it.
	return stripDockerMux(data)
}

// logContainerOutput fetches and logs container stdout+stderr for test debugging.
func logContainerOutput(ctx context.Context, t testingT, cli *client.Client, containerID string) {
	t.Helper()
	out, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return
	}
	defer out.Close()
	data, _ := io.ReadAll(out)
	t.Log("[container logs]", string(stripDockerMux(data)))
}

// stripDockerMux removes the 8-byte per-frame multiplexing header Docker adds to container log streams.
func stripDockerMux(data []byte) []byte {
	var out []byte
	for len(data) >= 8 {
		size := int(data[4])<<24 | int(data[5])<<16 | int(data[6])<<8 | int(data[7])
		data = data[8:]
		if size > len(data) {
			size = len(data)
		}
		out = append(out, data[:size]...)
		data = data[size:]
	}
	return out
}
