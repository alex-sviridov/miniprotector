package main

import (
	"context"
	"fmt"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/listformat"
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

	client := pb.NewListServiceClient(conn)

	ctx, cancel := context.WithTimeout(jobid.Outgoing(context.Background(), jobID), 30*time.Second)
	defer cancel()

	resp, err := client.ListFiles(ctx, &pb.ListRequest{
		ServerName: serverName,
		Path:       pathFilter,
		Filter:     filter,
	})
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}

	rows := make([]listformat.Row, len(resp.Rows))
	for i, r := range resp.Rows {
		createdAt, _ := time.Parse(time.RFC3339, r.CreatedAt)
		rows[i] = listformat.Row{
			FileUUID:  r.FileUuid,
			Source:    r.Source,
			Type:      r.Type,
			Path:      r.Path,
			Timestamp: r.Timestamp,
			Size:      r.Size,
			Chunks:    int(r.Chunks),
			Versions:  r.Versions,
			CreatedAt: createdAt,
		}
	}

	switch output {
	case "json":
		return listformat.RenderJSON(rows)
	default:
		return listformat.RenderTable(rows)
	}
}
