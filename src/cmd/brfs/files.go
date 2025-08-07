package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/gofrs/flock"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

type FileState int

const (
	StateQueued FileState = iota
	StateCompleted
)

type FileStateUpdate struct {
	Filename string
	State    FileState
}

type FileOpenHandle struct {
	File *os.File
	Lock *flock.Flock
}


func sendFilesMetadata(ctx context.Context, stream pb.BackupService_ProcessBackupStreamClient, fileList []files.FileInfo) error {
	conf := config.GetConfigFromContext(ctx)
	logger := logging.GetLoggerFromContext(ctx)
	streamId := ctx.Value("streamId").(int32)

	for _, file := range fileList {
		flogger := logger.With(slog.String("filename", file.Path))
		flogger.Info("Sending file metadata")
		request := &pb.FileRequest{
			StreamId: streamId, // Simple stream ID
			RequestType: &pb.FileRequest_FileInfo{
				FileInfo: &pb.FileInfo{
					Filename: file.Path,
				},
			},
		}

		if err := stream.Send(request); err != nil {
			flogger.Error("Failed to send filename", "filename", file.Path, "error", err)
			if conf.StopStreamOnFileError {
				return err
			}
		}
	}
	return nil
}
