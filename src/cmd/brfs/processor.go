package main

import (
	"context"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func handleResponse(ctx context.Context, stream pb.BackupService_ProcessBackupStreamClient, response *pb.FileResponse) error {
	logger := logging.GetLoggerFromContext(ctx)
	switch r := response.ResponseType.(type) {
	case *pb.FileResponse_Result:
		result := r.Result
		logger.Debug("Server response",
			"message", result.Message,
			"success", result.Success)
	default:
		logger.Error("Received unknown response type", "type", r)
	}
	return nil
}
