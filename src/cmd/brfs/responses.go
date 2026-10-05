package main

import (
	"context"
	"fmt"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
)

// responseReader is the single consumer of a stream's Recv. A pipelined
// sender keeps writing requests while replies arrive, so replies are read in
// their own goroutine and handed over through a channel.
//
// The channel must hold every reply that can be outstanding at once (one per
// in-flight chunk, plus a file-level reply or two): if it filled while the
// sender was blocked in Send, bwfs would block sending to us, stop reading,
// and the two would deadlock.
//
// When the stream ends or fails, the reader records why in err and closes the
// channel, so every later read -- by the next file on the same stream -- gets
// the same error again, as repeated Recv calls would.
type responseReader struct {
	ch  chan *pb.FileResponse
	err error // set before ch is closed
}

func newResponseReader(stream pb.BackupService_ProcessBackupStreamClient, window int) *responseReader {
	r := &responseReader{ch: make(chan *pb.FileResponse, window+4)}
	done := stream.Context().Done()
	go func() {
		defer close(r.ch)
		for {
			resp, err := stream.Recv()
			if err != nil {
				r.err = err
				return
			}
			select {
			case r.ch <- resp:
			case <-done:
				r.err = stream.Context().Err()
				return
			}
		}
	}()
	return r
}

func (r *responseReader) closedErr() error {
	return fmt.Errorf("failed to receive response: %w", r.err)
}

// next blocks for the next reply.
func (r *responseReader) next(ctx context.Context) (*pb.FileResponse, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("context cancelled while waiting for response: %w", ctx.Err())
	case resp, ok := <-r.ch:
		if !ok {
			return nil, r.closedErr()
		}
		return resp, nil
	}
}

// poll returns a reply that has already arrived, without blocking.
func (r *responseReader) poll() (resp *pb.FileResponse, ok bool, err error) {
	select {
	case resp, open := <-r.ch:
		if !open {
			return nil, false, r.closedErr()
		}
		return resp, true, nil
	default:
		return nil, false, nil
	}
}

// wait reads replies until one satisfies matcher and discards the rest, like
// connection.WaitForResponse. Skipping is what lets a stream carry on with the
// next file after one failed with replies still in flight.
func (r *responseReader) wait(ctx context.Context, logger *slog.Logger, matcher connection.ResponseMatcher) (any, error) {
	for {
		resp, err := r.next(ctx)
		if err != nil {
			return nil, err
		}
		if result, ok := matcher(resp); ok {
			return result, nil
		}
		logger.Debug("Received unexpected response, continuing to wait")
	}
}
