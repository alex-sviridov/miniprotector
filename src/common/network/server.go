package network

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"

	"github.com/alex-sviridov/miniprotector/common"
)

// MessageHandler defines what the application does with received messages
type MessageHandler interface {
	OnConnectionStart(connectionID uint32) error
	OnMessage(connectionID uint32, message string) error
	OnConnectionEnd(connectionID uint32) error
}

// Logger interface for the network library
type Logger interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// Server handles network connections - completely generic
type Server struct {
	port              int
	connectionCounter uint32
	handler           MessageHandler
	logger            *common.Logger
	socketPath        string
	listener          net.Listener

	ctx    context.Context
	cancel context.CancelFunc
}

func NewServer(port int, handler MessageHandler, logger *common.Logger) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &Server{
		port:       port,
		handler:    handler,
		logger:     logger,
		socketPath: fmt.Sprintf("/tmp/network_%d.sock", port),
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (s *Server) Start() error {
	// Try Unix socket first
	os.Remove(s.socketPath)

	var err error
	s.listener, err = net.Listen("unix", s.socketPath)
	if err != nil {
		// Fall back to TCP
		s.logger.Info("Unix socket failed, using TCP on port %d", s.port)
		s.listener, err = net.Listen("tcp", fmt.Sprintf(":%d", s.port))
		if err != nil {
			return fmt.Errorf("failed to start server: %v", err)
		}
	} else {
		s.logger.Info("Server started on Unix socket: %s", s.socketPath)
	}

	// Use defer for automatic cleanup
	defer s.Shutdown()

	for {
		// Check if we should stop
		select {
		case <-s.ctx.Done():
			s.logger.Info("Server stopping gracefully")
			return nil
		default:
		}
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				s.logger.Info("Server stopped during accept")
				return nil
			default:
				s.logger.Error("Accept failed: %v", err)
				continue
			}
		}

		go s.handleConnection(conn)
	}
}

// Shutdown properly closes the server and cleans up resources
func (s *Server) Shutdown() {
	s.logger.Info("Shutting down server...")
	s.cancel()
	// Close listener if it exists
	if s.listener != nil {
		s.listener.Close()
		s.logger.Debug("Closed listener")
	}

	// Remove socket file if it exists
	if s.socketPath != "" {
		os.Remove(s.socketPath)
		s.logger.Debug("Removed socket file: %s", s.socketPath)
	}

	s.logger.Info("Server shutdown complete")
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Generate unique connection ID
	connectionID := atomic.AddUint32(&s.connectionCounter, 1)

	scanner := bufio.NewScanner(conn)
	writer := bufio.NewWriter(conn)

	// Notify connection start
	if err := s.handler.OnConnectionStart(connectionID); err != nil {
		s.logger.Error("Handler OnConnectionStart failed: %v", err)
		return
	}

	// Send connection ID to client
	response := fmt.Sprintf("CONNECTION_ID:%d\n", connectionID)
	writer.WriteString(response)
	writer.Flush()

	// Process messages
	for scanner.Scan() {
		message := strings.TrimSpace(scanner.Text())

		if message == "CLOSE" {
			break
		}

		// Pass raw message to application
		if err := s.handler.OnMessage(connectionID, message); err != nil {
			s.logger.Error("Handler OnMessage failed: %v", err)
			return
		}
	}

	// Notify connection end
	if err := s.handler.OnConnectionEnd(connectionID); err != nil {
		s.logger.Error("Handler OnConnectionEnd failed: %v", err)
	}

	if err := scanner.Err(); err != nil {
		s.logger.Error("Scanner error: %v", err)
	}
}
