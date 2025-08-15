package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/workload/filesystem"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/common/wfs"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type RequestHandlerFunc func(context.Context, pb.BackupService_ProcessBackupStreamServer, *wfs.Writer, *pb.FileRequest) error

var handlerMap = map[string]RequestHandlerFunc{
	fmt.Sprintf("%T", &pb.FileRequest_FileInfo{}): handleFileInfoRequest,
}

func handleRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, writer *wfs.Writer, request *pb.FileRequest) error {
	requestType := fmt.Sprintf("%T", request.RequestType)
	handler, ok := handlerMap[requestType]
	if !ok {
		return fmt.Errorf("unknown request type: %s", requestType)
	}
	return handler(ctx, server, writer, request)
}

func handleFileInfoRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, writer *wfs.Writer, req *pb.FileRequest) error {
	fi := req.GetFileInfo()
	if fi == nil {
		return fmt.Errorf("FileRequest_FileInfo has empty FileInfo")
	}
	clientStreamID := req.StreamId
	logger := logging.GetLoggerFromContext(ctx).
		With(slog.String("file_id", fi.FileId)).
		With(slog.Int("stream_id", int(clientStreamID)))

	fileInfo, err := filesystem.DecodeFileInfo(fi.Attributes)
	if err != nil {
		return err
	}

	logger.Debug("Received filename", "attributes", fileInfo.Print())

	fileExists, err := writer.FileExists(fileInfo)
	if err != nil {
		return err
	}

	needed := !fileExists
	logger.Debug("File existence check", "exists", fileExists, "needed", needed)

	// Send back a simple acknowledgment
	response := &pb.FileResponse{
		StreamId: clientStreamID,
		ResponseType: &pb.FileResponse_FileNeeded{
			FileNeeded: &pb.FileNeeded{
				FileId: fi.FileId,
				Needed: needed,
				Host:   fileInfo.Host,
			},
		},
	}
	return server.Send(response)
}
