package network

import (
	"context"
	"fmt"
	"log/slog"
	"github.com/alex-sviridov/miniprotector/common"
)

// Stream represents one stream of files with persistent connection
type Stream struct {
	streamId   int
	jobId      string
	connection *Connection
	logger     *slog.Logger
}

func NewStream(config *common.Config, ctx context.Context, client *Client) (s *Stream, err error) {

	jobId := ctx.Value("jobId").(string)
	streamId := ctx.Value("streamId").(int)

	// Create a persistent connection for this stream
	connection, err := client.CreateConnection(config, ctx)
	if err != nil {
		return nil, fmt.Errorf("stream %d connection failed: %v", streamId, err)
	}

	s = &Stream{
		streamId:   streamId,
		jobId:      jobId,
		connection: connection,
		logger:     ctx.Value("logger").(*slog.Logger),
	}
	if err := s.StartStream(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Stream) StartStream() error {
	message := fmt.Sprintf("START_STREAM:%s:%d", s.jobId, s.streamId)

	if err := s.connection.SendMessage(message); err != nil {
		return fmt.Errorf("failed to send start stream message: %v", err)
	}

	response, err := s.connection.WaitForResponse()
	if err != nil {
		return fmt.Errorf("failed to receive ACK for stream start: %v", err)
	}

	if response != "START_STREAM_OK" {
		return fmt.Errorf("unexpected response for stream start: %s", response)
	}

	return nil
}

func (s *Stream) CloseStream() {
	s.connection.Close()
}

func (s *Stream) SendMessage(message string) (response string, err error) {
	if err := s.connection.SendMessage(message); err != nil {
		return "", fmt.Errorf("failed to send batch: %v", err)
	}
	return s.connection.WaitForResponse()
}
