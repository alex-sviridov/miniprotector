# E2E brfs/bwfs Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Go-native e2e test in `src/e2e/` that builds brfs and bwfs into a Docker image, runs them in containers on an isolated bridge network, and validates backup correctness via size and CRC32 integrity checks.

**Architecture:** A `//go:build e2e` Go package in `src/e2e/` uses the Docker SDK to build a shared image once in `TestMain`, then each test creates an isolated Docker network + bwfs server container + temporary storage dir, runs brfs as a short-lived container, and validates results by parsing `bwfs list` JSON and querying the SQLite `metadata.db` directly from the host. Test data (250 MiB, two subdirectories) is generated on the host with precomputed CRC32-of-chunk-CRC32s checksums matching exactly what bwfs stores. The bwfs storage path is a host bind-mount (`t.TempDir()`) so the host test process can open `metadata.db` directly for checksum validation; a named Docker volume backs `/tmp` scratch space inside the bwfs container (matching the user's "use a volume" request) without breaking host-side DB access.

**Tech Stack:** Go 1.26, `github.com/docker/docker` SDK (v27+), `gorm.io/driver/sqlite`, `gorm.io/gorm`, `github.com/stretchr/testify`, Docker daemon (must be running on host).

## Global Constraints

- Build tag: `//go:build e2e` on all files in `src/e2e/`
- Module: `github.com/alex-sviridov/miniprotector`
- Chunk size: `64 * 1024` bytes (64 KiB) — must match `workload/filesystem.ChunkSize`
- File-level checksum: CRC32-IEEE of each chunk's CRC32-IEEE, fed big-endian into a running `crc32.NewIEEE()` hasher — matches `bwfs/handler.go:feedChecksum`
- bwfs default port inside container: `15722`
- Container working dir: `/app`; config at `/app/.config/local.conf`; binaries at `/app/brfs`, `/app/bwfs`
- Config hardcoded path in binaries: `../.config/local.conf` relative to working dir → `/app/.config/local.conf` ✓
- `bwfs list` uses `NewReadOnly` — can run concurrently with a live bwfs server on the same storage path
- Run: `cd src && go test -v -tags=e2e -timeout=300s ./e2e/...`
- Test data total: ~250 MiB split across `subA/` (~125 MiB) and `subB/` (~125 MiB)
- bwfs storage path: host bind-mount (`t.TempDir()`), not a named volume — required so the host can open `metadata.db` directly for checksum validation (Task 5)
- bwfs container `/tmp` scratch: backed by a named Docker volume (`docker volume create`, mounted at `/tmp` in the bwfs container), cleaned up in `t.Cleanup()`

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `src/e2e/Dockerfile` | Create | Multi-stage image: build brfs+bwfs, runtime with config |
| `src/e2e/config.conf` | Create | Config baked into image (`logfolder=/tmp/log`, port 15722) |
| `src/e2e/docker.go` | Create | Docker SDK helpers: build image, network, containers, logs |
| `src/e2e/testdata.go` | Create | Generate 250 MiB test tree, compute per-file checksums |
| `src/e2e/validate.go` | Create | Parse bwfs list JSON, query SQLite checksums |
| `src/e2e/e2e_test.go` | Create | TestMain (build image), two test functions |
| `src/go.mod` / `src/go.sum` | Modify | Add `github.com/docker/docker` dependency |

---

## Task 1: Dockerfile and config

**Files:**
- Create: `src/e2e/Dockerfile`
- Create: `src/e2e/config.conf`

**Interfaces:**
- Produces: Docker image with `/app/brfs`, `/app/bwfs`, `/app/.config/local.conf`; working dir `/app`

- [ ] **Step 1: Create `src/e2e/config.conf`**

```
default_port=15722
default_streams=4
logfolder=/tmp/log
ClientHashQueryBatchSize=10
ConnectionTimeOutSec=30
FileLockTimeoutSec=300
StopStreamOnFileError=true
```

- [ ] **Step 2: Create `src/e2e/Dockerfile`**

```dockerfile
FROM golang:1.26 AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    protobuf-compiler \
    && rm -rf /var/lib/apt/lists/*

RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

WORKDIR /build/src
COPY src/ .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/brfs /build/bin/bwfs ./
COPY src/e2e/config.conf .config/local.conf
```

- [ ] **Step 3: Verify the Dockerfile builds manually**

From the repo root (parent of `src/`):
```bash
docker build -f src/e2e/Dockerfile -t miniprotector-e2e-test .
```
Expected: image builds successfully, `docker run --rm miniprotector-e2e-test ls /app` shows `brfs  bwfs  .config`.

- [ ] **Step 4: Commit**

```bash
git add src/e2e/Dockerfile src/e2e/config.conf
git commit -m "feat(e2e): add Dockerfile and config for e2e test image"
```

---

## Task 2: Add Docker SDK dependency

**Files:**
- Modify: `src/go.mod`, `src/go.sum`

**Interfaces:**
- Produces: `github.com/docker/docker` available in `src/e2e/` package imports

- [ ] **Step 1: Add the Docker SDK**

```bash
cd src && go get github.com/docker/docker@v27.5.1
```

Expected: `go.mod` gains `require github.com/docker/docker v27.5.1` (or nearest available). Also adds transitive deps (`github.com/docker/go-connections`, etc.).

- [ ] **Step 2: Tidy**

```bash
cd src && go mod tidy
```

Expected: no errors, `go.sum` updated.

- [ ] **Step 3: Commit**

```bash
git add src/go.mod src/go.sum
git commit -m "feat(e2e): add docker SDK dependency"
```

---

## Task 3: Docker lifecycle helpers (`docker.go`)

**Files:**
- Create: `src/e2e/docker.go`

**Interfaces:**
- Produces:
  - `buildImage(ctx context.Context, t *testing.T, repoRoot string) string` — returns image ID
  - `createNetwork(ctx context.Context, t *testing.T) string` — returns network ID, registers `t.Cleanup`
  - `startBwfsContainer(ctx context.Context, t *testing.T, imageID, networkID, storageDir string) string` — returns host port string (e.g. `"32768"`), registers `t.Cleanup`
  - `waitForBwfs(ctx context.Context, hostPort string) error` — polls gRPC until ready
  - `runBrfsContainer(ctx context.Context, t *testing.T, imageID, networkID, dataDir, bwfsHost string, streams int) int` — returns exit code
  - `runBwfsListContainer(ctx context.Context, t *testing.T, imageID, networkID, storageDir string) []byte` — returns stdout bytes

- [ ] **Step 1: Write `src/e2e/docker.go`**

```go
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
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// testingT is the subset of *testing.T used by docker helpers.
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

	resp, err := cli.ImageBuild(ctx, buf, image.BuildOptions{
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
			Image: imageID,
			Cmd:   []string{"/app/bwfs", "/storage", "server", "--port", "15722", "--quiet"},
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
			Cmd:   []string{"/app/bwfs", "/storage", "list", "--output", "json", "--quiet"},
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
```

- [ ] **Step 2: Verify it compiles (no tests yet)**

```bash
cd src && go build -tags=e2e ./e2e/...
```
Expected: no errors (may warn about unused imports if validate/testdata not yet written — that's fine at this step; add a blank `package e2e` file if needed).

- [ ] **Step 3: Commit**

```bash
git add src/e2e/docker.go
git commit -m "feat(e2e): add Docker lifecycle helpers"
```

---

## Task 4: Test data generation (`testdata.go`)

**Files:**
- Create: `src/e2e/testdata.go`

**Interfaces:**
- Produces:
  - `type fileRecord struct { size int64; checksum uint32 }` — per-file manifest entry
  - `generateTestData(t *testing.T, rootDir string) map[string]fileRecord` — creates `rootDir/subA/` and `rootDir/subB/`, returns map keyed by path relative to `rootDir` (e.g. `"subA/file0.bin"`)

- [ ] **Step 1: Write `src/e2e/testdata.go`**

The checksum must match bwfs exactly: for each 64 KiB chunk, compute `crc32.ChecksumIEEE(chunkData)`, then feed that value big-endian into a running `crc32.NewIEEE()` file hasher. The final `Sum32()` of that file hasher is the checksum stored by bwfs.

```go
//go:build e2e

package e2e

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const chunkSize = 64 * 1024 // must match workload/filesystem.ChunkSize

type fileRecord struct {
	size     int64
	checksum uint32 // CRC32-of-chunk-CRC32s, matching bwfs FinalizeFileData
}

// fileSizes defines the sizes of files in each subdirectory.
// 8 files per subdir, total ~125 MiB per subdir = ~250 MiB total.
var fileSizes = []int64{
	4 * 1024 * 1024,  // 4 MiB
	8 * 1024 * 1024,  // 8 MiB
	12 * 1024 * 1024, // 12 MiB
	16 * 1024 * 1024, // 16 MiB
	20 * 1024 * 1024, // 20 MiB
	24 * 1024 * 1024, // 24 MiB
	28 * 1024 * 1024, // 28 MiB
	32 * 1024 * 1024, // 32 MiB
} // sum = 144 MiB per subdir, ~288 MiB total

// generateTestData creates subA/ and subB/ under rootDir, each with 8 files
// of varying sizes. Returns a map from relative path (e.g. "subA/file0.bin")
// to fileRecord with size and CRC32-of-chunk-CRC32s checksum.
func generateTestData(t *testing.T, rootDir string) map[string]fileRecord {
	t.Helper()
	records := make(map[string]fileRecord)
	rng := rand.New(rand.NewSource(42)) // deterministic for reproducibility

	for _, subdir := range []string{"subA", "subB"} {
		dir := filepath.Join(rootDir, subdir)
		require.NoError(t, os.MkdirAll(dir, 0755))
		for i, size := range fileSizes {
			name := fmt.Sprintf("file%d.bin", i)
			rel := filepath.Join(subdir, name)
			path := filepath.Join(rootDir, rel)
			checksum := writeFile(t, path, size, rng)
			records[rel] = fileRecord{size: size, checksum: checksum}
		}
	}
	return records
}

// writeFile writes size bytes of pseudo-random data to path in chunkSize
// increments and returns the CRC32-of-chunk-CRC32s matching bwfs.
func writeFile(t *testing.T, path string, size int64, rng *rand.Rand) uint32 {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	fileHasher := crc32.NewIEEE()
	buf := make([]byte, chunkSize)
	remaining := size

	for remaining > 0 {
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		chunk := buf[:n]
		_, err := rng.Read(chunk)
		require.NoError(t, err)
		_, err = f.Write(chunk)
		require.NoError(t, err)

		chunkCRC := crc32.ChecksumIEEE(chunk)
		// Feed chunk CRC big-endian into file hasher — matches handler.go:feedChecksum
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], chunkCRC)
		fileHasher.Write(b[:])

		remaining -= n
	}
	return fileHasher.Sum32()
}
```

- [ ] **Step 2: Verify compilation**

```bash
cd src && go build -tags=e2e ./e2e/...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add src/e2e/testdata.go
git commit -m "feat(e2e): add test data generator with CRC32 checksum precompute"
```

---

## Task 5: Validation helpers (`validate.go`)

**Files:**
- Create: `src/e2e/validate.go`

**Interfaces:**
- Consumes: `fileRecord` from `testdata.go`
- Produces:
  - `type listRecord struct` with JSON tags matching `bwfs list --output json`
  - `parseListOutput(t *testing.T, data []byte) []listRecord`
  - `assertFilesPresent(t *testing.T, list []listRecord, expected map[string]fileRecord, storagePath string)`
  - `assertFilesAbsent(t *testing.T, list []listRecord, absent map[string]fileRecord)`

- [ ] **Step 1: Write `src/e2e/validate.go`**

`assertFilesPresent` checks each expected file appears in the list with matching `size`, then queries `metadata.db` for the stored `checksum` and asserts it matches the precomputed value.

The `path` field in `bwfs list` JSON comes from `parseFileID` which extracts the path segment from the file ID `fs://hostname:f:/absolute/path:mtime`. The absolute path inside the brfs container is `/testdata/<rel>`. So the list `path` will be `/testdata/subA/file0.bin` etc.

```go
//go:build e2e

package e2e

import (
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type listRecord struct {
	FileDataID string `json:"file_data_id"`
	Source     string `json:"source"`
	Type       string `json:"type"`
	Path       string `json:"path"`
	Timestamp  int64  `json:"timestamp"`
	Size       int64  `json:"size"`
	Chunks     int    `json:"chunks"`
	Versions   int64  `json:"versions"`
	CreatedAt  string `json:"created_at"`
}

func parseListOutput(t *testing.T, data []byte) []listRecord {
	t.Helper()
	var records []listRecord
	require.NoError(t, json.Unmarshal(data, &records), "failed to parse bwfs list JSON: %s", string(data))
	return records
}

// assertFilesPresent verifies every file in expected appears in list with correct
// size, and that the checksum stored in metadata.db matches the precomputed value.
// expected keys are relative paths like "subA/file0.bin"; list paths are absolute
// inside the container like "/testdata/subA/file0.bin".
func assertFilesPresent(t *testing.T, list []listRecord, expected map[string]fileRecord, storagePath string) {
	t.Helper()

	// Index list by path for O(1) lookup
	byPath := make(map[string]listRecord, len(list))
	for _, r := range list {
		byPath[r.Path] = r
	}

	db := openMetadataDB(t, storagePath)

	for rel, want := range expected {
		absPath := "/testdata/" + filepath.ToSlash(rel)
		rec, ok := byPath[absPath]
		if !assert.True(t, ok, "expected path %q not found in bwfs list", absPath) {
			continue
		}
		assert.Equal(t, want.size, rec.Size, "size mismatch for %s", rel)

		// Query the checksum stored by bwfs for this file_data_id
		stored := queryChecksum(t, db, rec.FileDataID)
		assert.Equal(t, want.checksum, stored, "checksum mismatch for %s", rel)
	}
}

// assertFilesAbsent verifies none of the files in absent appear in list.
func assertFilesAbsent(t *testing.T, list []listRecord, absent map[string]fileRecord) {
	t.Helper()
	byPath := make(map[string]struct{}, len(list))
	for _, r := range list {
		byPath[r.Path] = struct{}{}
	}
	for rel := range absent {
		absPath := "/testdata/" + filepath.ToSlash(rel)
		assert.NotContains(t, byPath, absPath, "path %q should not be in bwfs list", absPath)
	}
}

type fileDataRecord struct {
	ID       string `gorm:"column:id"`
	Checksum []byte `gorm:"column:checksum"`
}

func openMetadataDB(t *testing.T, storagePath string) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(storagePath, "metadata.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?mode=ro"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "failed to open metadata.db at %s", dbPath)
	return db
}

func queryChecksum(t *testing.T, db *gorm.DB, fileDataID string) uint32 {
	t.Helper()
	var rec fileDataRecord
	err := db.Table("file_data_records").
		Select("id, checksum").
		Where("id = ?", fileDataID).
		First(&rec).Error
	require.NoError(t, err, "failed to query checksum for file_data_id %s", fileDataID)
	require.Len(t, rec.Checksum, 4, "checksum should be 4 bytes")
	return binary.BigEndian.Uint32(rec.Checksum)
}
```

- [ ] **Step 2: Verify compilation**

```bash
cd src && go build -tags=e2e ./e2e/...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add src/e2e/validate.go
git commit -m "feat(e2e): add bwfs list parsing and SQLite checksum validation"
```

---

## Task 6: Test functions (`e2e_test.go`)

**Files:**
- Create: `src/e2e/e2e_test.go`

**Interfaces:**
- Consumes: all helpers from tasks 3–5
- Produces: `TestMain`, `TestE2E_SingleSubfolderBackup`, `TestE2E_AllFoldersBackup`

- [ ] **Step 1: Write `src/e2e/e2e_test.go`**

```go
//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testImageID string

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Locate repo root (two levels up from src/e2e/)
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	var t fakeT
	testImageID = buildImage(ctx, &t, repoRoot)
	if t.failed {
		fmt.Fprintln(os.Stderr, "failed to build e2e Docker image")
		os.Exit(1)
	}

	code := m.Run()

	// Clean up image
	cli := newDockerClient(&t)
	_ = cli.ImageRemove(context.Background(), testImageID, image.RemoveOptions{Force: true})
	cli.Close()

	os.Exit(code)
}

// fakeT satisfies *testing.T for TestMain where a real *testing.T is unavailable.
type fakeT struct{ failed bool }

func (f *fakeT) Helper()                             {}
func (f *fakeT) Fatalf(format string, args ...any)   { fmt.Fprintf(os.Stderr, format+"\n", args...); f.failed = true }
func (f *fakeT) Log(args ...any)                     { fmt.Println(args...) }
func (f *fakeT) Logf(format string, args ...any)     { fmt.Printf(format+"\n", args...) }
func (f *fakeT) Cleanup(func())                      {}
func (f *fakeT) Errorf(format string, args ...any)   { fmt.Fprintf(os.Stderr, format+"\n", args...) }
func (f *fakeT) FailNow()                            { f.failed = true }

func TestE2E_SingleSubfolderBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Generate test data
	dataDir := t.TempDir()
	allRecords := generateTestData(t, dataDir)

	// Split into subA and subB records
	subARecords := make(map[string]fileRecord)
	subBRecords := make(map[string]fileRecord)
	for rel, rec := range allRecords {
		if len(rel) > 4 && rel[:4] == "subA" {
			subARecords[rel] = rec
		} else {
			subBRecords[rel] = rec
		}
	}

	// Create isolated network and storage
	networkID := createNetwork(ctx, t)
	storageDir := t.TempDir()

	// Start bwfs server
	hostPort := startBwfsContainer(ctx, t, testImageID, networkID, storageDir)
	require.NoError(t, waitForBwfs(ctx, hostPort))

	// Run brfs for subA only, 1 stream
	exitCode := runBrfsContainer(ctx, t, testImageID, networkID,
		filepath.Join(dataDir, "subA"), "bwfs", 1)
	require.Equal(t, 0, exitCode, "brfs exited with non-zero code")

	// Validate with bwfs list
	listJSON := runBwfsListContainer(ctx, t, testImageID, networkID, storageDir)
	t.Logf("bwfs list output: %s", string(listJSON))
	list := parseListOutput(t, listJSON)

	// subA files present with correct size and checksum
	assertFilesPresent(t, list, subARecords, storageDir)
	// subB files absent
	assertFilesAbsent(t, list, subBRecords)
}

func TestE2E_AllFoldersBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Generate test data
	dataDir := t.TempDir()
	allRecords := generateTestData(t, dataDir)

	// Create isolated network and storage
	networkID := createNetwork(ctx, t)
	storageDir := t.TempDir()

	// Start bwfs server
	hostPort := startBwfsContainer(ctx, t, testImageID, networkID, storageDir)
	require.NoError(t, waitForBwfs(ctx, hostPort))

	// Run brfs for all folders, 4 streams
	exitCode := runBrfsContainer(ctx, t, testImageID, networkID, dataDir, "bwfs", 4)
	require.Equal(t, 0, exitCode, "brfs exited with non-zero code")

	// Validate with bwfs list
	listJSON := runBwfsListContainer(ctx, t, testImageID, networkID, storageDir)
	t.Logf("bwfs list output: %s", string(listJSON))
	list := parseListOutput(t, listJSON)

	// All files present with correct size and checksum
	assertFilesPresent(t, list, allRecords, storageDir)
}
```

- [ ] **Step 2: Verify compilation**

```bash
cd src && go build -tags=e2e ./e2e/...
```
Expected: no errors.

- [ ] **Step 3: Fix the fakeT — add missing image import to e2e_test.go**

The `TestMain` uses `image.RemoveOptions`. Add the import:
```go
import (
    // existing imports ...
    "github.com/docker/docker/api/types/image"
)
```

`docker.go` already defines `testingT` (Task 3) and all its functions accept `testingT` rather than `*testing.T`, so `*fakeT` satisfies them without further changes. The `require.NoError` / `require.Equal` calls accept `require.TestingT` which `testingT` is compatible with.

- [ ] **Step 4: Run the tests**

```bash
cd src && go test -v -tags=e2e -timeout=300s -run TestE2E ./e2e/...
```

Expected output (approximate):
```
--- PASS: TestE2E_SingleSubfolderBackup (45.00s)
--- PASS: TestE2E_AllFoldersBackup (90.00s)
PASS
ok      github.com/alex-sviridov/miniprotector/e2e    135.000s
```

If tests fail, check:
- `[docker build]` lines in output for build errors
- `[container logs]` lines for bwfs/brfs runtime errors
- `bwfs list output:` lines for unexpected JSON

- [ ] **Step 5: Commit**

```bash
git add src/e2e/e2e_test.go src/e2e/docker.go
git commit -m "feat(e2e): add TestMain and e2e test functions for brfs/bwfs"
```

---

## Self-Review

**Spec coverage:**
- ✅ Build brfs and bwfs — Task 1 (Dockerfile), Task 2 (dependency)
- ✅ Generate 250 MiB backup load — Task 4 (144 MiB × 2 subdirs ≈ 288 MiB)
- ✅ Backup one subfolder with one stream — `TestE2E_SingleSubfolderBackup`
- ✅ Backup all folders with several streams — `TestE2E_AllFoldersBackup` (4 streams)
- ✅ Validate with bwfs list — Task 5, both test functions
- ✅ tmpfs / temp dirs — `t.TempDir()` used for both data and storage dirs
- ✅ Run brfs and bwfs in containers — Tasks 3 and 6
- ✅ Network connectivity tested — shared Docker bridge network
- ✅ CRC validation (not just size) — `assertFilesPresent` queries `metadata.db`

**Placeholder scan:** No TBDs or vague steps found.

**Type consistency:**
- `fileRecord` defined in Task 4, consumed in Tasks 5 and 6 ✓
- `listRecord` defined in Task 5, consumed in Task 6 ✓
- `testingT` interface defined in Task 3's `docker.go` code from the start; all docker.go functions take `testingT` instead of `*testing.T`, so `TestMain`'s `*fakeT` satisfies them directly ✓
- `startBwfsContainer` names the container `"bwfs-server"` and sets network alias `"bwfs"` (Task 3); `runBrfsContainer` connects to `"bwfs:15722"` ✓ — consistent, no remaining gap
- bwfs storage stays a host bind-mount per the user's clarification (named volume only backs `/tmp` scratch in the bwfs container) — `openMetadataDB` in Task 5 continues to work unmodified ✓
