package main

import (
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func (s *BackupStream) handleResponse(stream pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {
	logger := *s.logger
	// Handle only FileInfo messages for now
	switch r := req.RequestType.(type) {
	case *pb.FileRequest_FileInfo:
		filename := r.FileInfo.Filename

		if filename == "" {
			logger.Error("Received empty filename")
			return nil
		}

		s.filesProcessed++
		logger.Debug("Received filename",
			"filename", filename,
			"file_number", s.filesProcessed)

		// Send back a simple acknowledgment
		response := &pb.FileResponse{
			StreamId: req.StreamId,
			ResponseType: &pb.FileResponse_Result{
				Result: &pb.ProcessingResult{
					Filename: filename,
					Success:  true,
					Message:  fmt.Sprintf("Got your filename: %s", filename),
				},
			},
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
