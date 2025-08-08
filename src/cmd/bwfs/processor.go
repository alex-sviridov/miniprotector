package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common/files"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func (s *BackupStream) handleResponse(stream pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {
	logger := *s.logger

	switch r := req.RequestType.(type) {
	case *pb.FileRequest_FileInfo:
		response, err := s.handleFileRequest(r.FileInfo)
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

func (s *BackupStream) handleFileRequest(fi *pb.FileInfo) (*pb.FileResponse, error) {
	logger := *s.logger
	fileInfo, err := files.DecodeFileInfo(fi.Attributes)
	if err != nil {
		return nil, err
	}

	s.filesProcessed++
	logger.Debug("Received filename",
		"filename", fileInfo.Path,
		"file_number", s.filesProcessed,
		"attributes", fileInfo.Print())

	fileExists, err := s.writer.FileExists(fileInfo)
	if err != nil {
		return nil, err
	}

	var message string
	if fileExists {
		message = fmt.Sprintf("File exists in database: %s", fileInfo.Path)
		logger.Info(message)
	} else {
		message = fmt.Sprintf("File doesn't exist in database: %s", fileInfo.Path)
		if err := s.writer.AddFile(fileInfo, ""); err != nil {
			return nil, err
		}
		logger.Info(message)
	}

	// Send back a simple acknowledgment
	response := &pb.FileResponse{
		StreamId: s.clientStreamID,
		ResponseType: &pb.FileResponse_Result{
			Result: &pb.ProcessingResult{
				Filename: fileInfo.Path,
				Success:  true,
				Message:  message,
			},
		},
	}
	return response, nil
}
