package main

import (
	"context"
	"fmt"
	"io"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/listformat"
	"google.golang.org/grpc"
)

// runList lists a remote bwfs store's files. jobID rides the ListFiles RPC
// as outgoing job-id metadata, so bwfs's log for this exact call is
// correlatable back to this process's local log -- the same convention brfs
// and policyclient already follow.
func runList(host string, port int, serverName, pathFilter, filter, output, certsDir, jobID string) error {
	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	return runListWithConn(conn, serverName, pathFilter, filter, output, jobID)
}

// runListWithConn is runList's body, parameterized on an already-dialed
// conn -- split out purely so tests can exercise it over a bufconn dial
// without duplicating anything past the transport-level connect (runList
// itself is the only production caller). See list_test.go's
// runListWithDialer. ListFiles is watchdog-protected the same way
// ResolveRestoreFiles is (Task 4): an idle timeout, not a total-duration
// one, so a large legitimate listing is never penalized for taking a
// while, only a genuinely stalled stream is.
func runListWithConn(conn *grpc.ClientConn, serverName, pathFilter, filter, output, jobID string) error {
	client := pb.NewListServiceClient(conn)

	watchdogCtx, touch, stop := withStallWatchdog(jobid.Outgoing(context.Background(), jobID), streamIdleTimeout)
	defer stop()

	stream, err := client.ListFiles(watchdogCtx, &pb.ListRequest{
		ServerName: serverName,
		Path:       pathFilter,
		Filter:     filter,
	})
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}

	var rows []listformat.Row
	for {
		row, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Discard whatever was collected so far -- no partial
			// table/JSON output, matching the old unary call's
			// all-or-nothing behavior.
			return fmt.Errorf("list files: %w", err)
		}
		touch()
		createdAt, _ := time.Parse(time.RFC3339, row.CreatedAt)
		rows = append(rows, listformat.Row{
			FileUUID:  row.FileUuid,
			Source:    row.Source,
			Type:      row.Type,
			Path:      row.Path,
			Timestamp: row.Timestamp,
			Size:      row.Size,
			Chunks:    int(row.Chunks),
			Versions:  row.Versions,
			CreatedAt: createdAt,
		})
	}

	switch output {
	case "json":
		return listformat.RenderJSON(rows)
	default:
		return listformat.RenderTable(rows)
	}
}
