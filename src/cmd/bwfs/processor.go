package main

import (
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/files"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func (s *BackupStream) handleResponse(stream pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {
	logger := *s.logger

	switch r := req.RequestType.(type) {
	case *pb.FileRequest_FileInfo:
		response, err := s.handleFileInfoRequest(req)
		if err != nil {
			return err
		}
		if err := stream.Send(response); err != nil {
			logger.Error("Error sending response", "error", err)
			return err
		}

	default:
		logger.Error("Received unknown message type", "message_type", r)
	}
	return nil
}

func (s *BackupStream) handleFileInfoRequest(req *pb.FileRequest) (*pb.FileResponse, error) {

	fi := req.GetFileInfo()
	clientStreamID := req.StreamId
	logger := *s.logger.
		With(slog.String("file_id", fi.FileId)).
		With(slog.Int("streamId", int(clientStreamID)))

	fileInfo, err := files.DecodeFileInfo(fi.Attributes)
	if err != nil {
		return nil, err
	}

	s.filesProcessed++
	logger.Debug("Received filename",
		"file_number", s.filesProcessed,
		"attributes", fileInfo.Print())

	fileExists, err := s.writer.FileExists(fileInfo)
	if err != nil {
		return nil, err
	}

	var needed bool
	if fileExists {
		needed = false
		logger.Debug("File exists in database")
	} else {
		needed = true
		logger.Debug("File doesn't exist in database")
	}

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
	return response, nil
}
