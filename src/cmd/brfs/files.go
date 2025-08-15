package main

import (
	"context"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func sendFilesMetadata(ctx context.Context, stream pb.BackupService_ProcessBackupStreamClient, fileList []filesystem.FileInfo) error {
	conf := config.GetConfigFromContext(ctx)
	logger := logging.GetLoggerFromContext(ctx)
	streamId := ctx.Value("streamId").(int32)
	for _, file := range fileList {
		attr, err := filesystem.Encode(&file)
		if err != nil {
			logger.Error("Failed to encode file info", "filename", file.Path, "error", err)
			if conf.StopStreamOnFileError {
				return err
			} else {
				continue
			}
		}
		flogger := logger.With(slog.String("file_path", file.Path))
		flogger.Info("Sending file metadata")
		request := &pb.FileRequest{
			StreamId: streamId, // Simple stream ID
			RequestType: &pb.FileRequest_FileInfo{
				FileInfo: &pb.FileInfo{
					FileId:     file.GetId(),
					Attributes: attr,
				},
			},
		}

		if err := stream.Send(request); err != nil {
			flogger.Error("Failed to send filename", "error", err)
			if conf.StopStreamOnFileError {
				return err
			} else {
				continue
			}
		}
	}
	return nil
}
