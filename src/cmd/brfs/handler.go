package main

import (
	"context"
	"fmt"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

type ResponseHandlerFunc func(ctx context.Context, response *pb.FileResponse, ch chan<- BackupResult) error

var handlerMap = map[string]ResponseHandlerFunc{
	fmt.Sprintf("%T", &pb.FileResponse_FileNeeded{}): handleFileInfoResponse,
}

func handleResponse(ctx context.Context, response *pb.FileResponse, ch chan<- BackupResult) error {
	responseType := fmt.Sprintf("%T", response.ResponseType)
	handler, ok := handlerMap[responseType]
	if !ok {
		return fmt.Errorf("unknown response type: %s", responseType)
	}
	return handler(ctx, response, ch)
}

func handleFileInfoResponse(ctx context.Context, resp *pb.FileResponse, ch chan<- BackupResult) error {
	fi := resp.GetFileNeeded()
	if fi == nil {
		return fmt.Errorf("FileResponse_FileNeeded has empty FileInfo")
	}
	streamId := ctx.Value("streamId").(int32)

	if fi.Host != ctx.Value(common.HostnameContextKey).(string) {
		return fmt.Errorf("wrong hostname received: expected %s, received %s", ctx.Value(common.HostnameContextKey).(string), fi.Host)
	}
	logger := logging.GetLoggerFromContext(ctx).
		With(slog.String("file_id", fi.FileId)).
		With(slog.Int("stream_id", int(streamId)))
	logger.Debug("Response", "needed", fi.Needed)

	ch <- BackupResult{
		Filename: fi.FileId,
		Success:  fi.Needed,
	}

	return nil
}
