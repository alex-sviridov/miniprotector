package main

import (
	"context"
	"fmt"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func handleResponse(ctx context.Context, stream pb.BackupService_ProcessBackupStreamClient, response *pb.FileResponse) error {
	logger := logging.GetLoggerFromContext(ctx)
	switch r := response.ResponseType.(type) {
	case *pb.FileResponse_FileNeeded:
		if response.StreamId != ctx.Value("streamId").(int32) {
			return fmt.Errorf("stream ID mismatch: expected %d, received %d", ctx.Value("streamId").(int32), response.StreamId)
		}
		if r.FileNeeded.Host != ctx.Value(common.HostnameContextKey).(string) {
			return fmt.Errorf("wrong hostname recieved: expected %s, received %s", ctx.Value(common.HostnameContextKey).(string), r.FileNeeded.Host)
		}
		if err := handleFileInfoResponse(ctx, response); err != nil {
			return err
		}
	default:
		logger.Error("Received unknown response type", "type", r)
	}
	return nil
}

func handleFileInfoResponse(ctx context.Context, resp *pb.FileResponse) error {
	fi := resp.GetFileNeeded()
	streamId := ctx.Value("streamId").(int32)

	logger := logging.GetLoggerFromContext(ctx).
		With(slog.String("file_id", fi.FileId)).
		With(slog.Int("streamId", int(streamId)))
	logger.Debug("Response", "needed", fi.Needed)

	return nil
}
