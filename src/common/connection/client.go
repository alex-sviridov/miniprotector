package connection

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

func dial(creds credentials.TransportCredentials, host string, port int) (*grpc.ClientConn, error) {
	// Configure keepalive for connection health monitoring
	keepaliveParams := keepalive.ClientParameters{
		Time:                10 * time.Second, // Send ping every 10 seconds
		Timeout:             3 * time.Second,  // Wait 3 seconds for pong response
		PermitWithoutStream: true,             // Send pings even when no active streams
	}

	// grpc.NewClient itself never blocks or dials -- it just builds the
	// channel. Connection attempts happen lazily on first RPC (or via an
	// explicit conn.Connect()) and are retried forever using gRPC's own
	// backoff, independent of whatever timeout the caller applies to any
	// single RPC.
	conn, err := grpc.NewClient(
		fmt.Sprintf("%s:%d", host, port),
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepaliveParams),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	return conn, nil
}

func dialWithCredentials(creds credentials.TransportCredentials, host string, port, timeout int) (*grpc.ClientConn, error) {
	conn, err := dial(creds, host, port)
	if err != nil {
		return nil, err
	}

	if err := checkConnection(conn, timeout); err != nil {
		conn.Close() // Close only on connection failure
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	// Connection remains open; caller wraps it with the generated client it needs.
	return conn, nil
}

// Connect dials host:port, presenting certsDir/client.crt and
// certsDir/client.key as this node's mTLS identity.
func Connect(host string, port, timeout int, certsDir string) (*grpc.ClientConn, error) {
	creds, err := mtls.LoadClientCredentials(certsDir, host)
	if err != nil {
		return nil, fmt.Errorf("failed to load client credentials: %w", err)
	}
	return dialWithCredentials(creds, host, port, timeout)
}

// ConnectWithIdentity is Connect, parameterized on which cert/key filenames
// to present -- used by callers authenticating with an identity other than
// the standard client.crt/client.key pair.
func ConnectWithIdentity(host string, port, timeout int, certsDir, certFile, keyFile string) (*grpc.ClientConn, error) {
	creds, err := mtls.LoadClientCredentialsWithIdentity(certsDir, certFile, keyFile, host)
	if err != nil {
		return nil, fmt.Errorf("failed to load client credentials: %w", err)
	}
	return dialWithCredentials(creds, host, port, timeout)
}

// DialNonBlocking dials host:port, presenting certsDir/client.crt and
// certsDir/client.key as this node's mTLS identity, and returns the
// ClientConn immediately without waiting for connectivity. Use this instead
// of Connect for long-running processes where the peer may not be up yet
// (or may restart independently) and every call already has its own
// retry/backoff -- gRPC keeps attempting to connect/reconnect for the life
// of the returned ClientConn, so a peer that starts later, or comes back
// after an outage, recovers on its own with no action from the caller.
// Connect remains the right choice for short-lived commands that need to
// fail fast when the peer is unreachable.
func DialNonBlocking(host string, port int, certsDir string) (*grpc.ClientConn, error) {
	creds, err := mtls.LoadClientCredentials(certsDir, host)
	if err != nil {
		return nil, fmt.Errorf("failed to load client credentials: %w", err)
	}
	return dial(creds, host, port)
}

func checkConnection(conn *grpc.ClientConn, timeoutSec int) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	conn.Connect()

	// Wait for connection to be ready
	for {
		state := conn.GetState()

		switch state {
		case connectivity.Ready:
			return nil
		case connectivity.TransientFailure, connectivity.Shutdown:
			return fmt.Errorf("connection failed, state: %v", state)
		default:
			// Wait for state change
			if !conn.WaitForStateChange(ctx, state) {
				return fmt.Errorf("connection timeout")
			}
		}
	}
}

// ResponseMatcher defines a function that checks if a response matches what we're waiting for
type ResponseMatcher func(*pb.FileResponse) (any, bool)

// WaitForResponse waits for a specific response type from the stream with per-operation timeout.
// It continuously receives responses until finding one that matches the provided matcher.
// Uses a per-operation timeout to prevent hanging on server logic issues.
// Returns the matched response or an error if timeout occurs or stream fails.
func WaitForResponse(ctx context.Context, logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, matcher ResponseMatcher, operationTimeout time.Duration) (any, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled while waiting for response: %w", ctx.Err())
		default:
		}

		response, err := stream.Recv()
		if err != nil {
			return nil, fmt.Errorf("failed to receive response: %w", err)
		}

		if result, matches := matcher(response); matches {
			return result, nil
		}

		// Continue waiting - log unexpected responses for debugging
		responseType := "unknown"
		if response.GetFileNeeded() != nil {
			responseType = "FileNeeded"
		} else if response.GetChunkNeeded() != nil {
			responseType = "ChunkNeeded"
		} else if response.GetChunkResult() != nil {
			responseType = "ChunkResult"
		} else if response.GetResult() != nil {
			responseType = "FileResult"
		}
		logger.Debug("Received unexpected response, continuing to wait", "response_type", responseType)
	}
}

// FileNeeded creates a matcher for FileNeeded responses with the expected file ID.
// Returns *pb.FileNeeded when a matching response is found.
func FileNeeded(expectedFileId string) ResponseMatcher {
	return func(response *pb.FileResponse) (any, bool) {
		if fileNeeded := response.GetFileNeeded(); fileNeeded != nil {
			if fileNeeded.FileId == expectedFileId {
				return fileNeeded, true
			}
		}
		return nil, false
	}
}

// ChunkNeeded creates a matcher for ChunkNeeded responses with the expected chunk hash.
// Returns *pb.ChunkNeeded when a matching response is found.
func ChunkNeeded(expectedHash []byte) ResponseMatcher {
	return func(response *pb.FileResponse) (any, bool) {
		if chunkNeeded := response.GetChunkNeeded(); chunkNeeded != nil {
			if bytes.Equal(chunkNeeded.Hash, expectedHash) {
				return chunkNeeded, true
			}
		}
		return nil, false
	}
}

// ChunkResult creates a matcher for ChunkResult responses with the expected chunk hash.
// Returns *pb.ChunkResult when a matching response is found.
func ChunkResult(expectedHash []byte) ResponseMatcher {
	return func(response *pb.FileResponse) (any, bool) {
		if result := response.GetChunkResult(); result != nil {
			if bytes.Equal(result.Hash, expectedHash) {
				return result, true
			}
		}
		return nil, false
	}
}

// FileResult creates a matcher for FileProcessingResult responses with the expected file ID.
// Returns *pb.FileProcessingResult when a matching response is found.
func FileResult(expectedFileId string) ResponseMatcher {
	return func(response *pb.FileResponse) (any, bool) {
		if result := response.GetResult(); result != nil {
			if result.FileId == expectedFileId {
				return result, true
			}
		}
		return nil, false
	}
}

