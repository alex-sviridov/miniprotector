package main

import (
	"fmt"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/files"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type ResponseHandlerFunc func(*BackupStream, pb.BackupService_ProcessBackupStreamServer, *pb.FileRequest) error

var handlerMap = map[string]ResponseHandlerFunc{
	fmt.Sprintf("%T", &pb.FileRequest_FileInfo{}): (*BackupStream).handleFileInfoRequest,
}

func (stream *BackupStream) handleResponse(server pb.BackupService_ProcessBackupStreamServer, request *pb.FileRequest) error {
	requestType := fmt.Sprintf("%T", request.RequestType)
	handler, ok := handlerMap[requestType]
	if !ok {
		return fmt.Errorf("unknown request type: %s", requestType)
	}
	return handler(stream, server, request)
}

func (stream *BackupStream) handleFileInfoRequest(server pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {

	fi := req.GetFileInfo()
	if fi == nil {
		return fmt.Errorf("FileRequest_FileInfo has empty FileInfo")
	}
	clientStreamID := req.StreamId
	logger := stream.logger.With(slog.String("file_id", fi.FileId)).With(slog.Int("stream_id", int(clientStreamID)))

	fileInfo, err := files.DecodeFileInfo(fi.Attributes)
	if err != nil {
		return err
	}

	stream.filesProcessed++
	logger.Debug("Received filename", "file_number", stream.filesProcessed, "attributes", fileInfo.Print())

	fileExists, err := stream.writer.FileExists(fileInfo)
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
