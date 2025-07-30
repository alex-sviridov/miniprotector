package network

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync/atomic"

	"github.com/alex-sviridov/miniprotector/common"
)

// MessageHandler defines what the application does with received messages
type MessageHandler interface {
	OnConnectionStart(config *common.Config, ctx context.Context, onnectionID uint32, scanner *bufio.Scanner, writer *bufio.Writer) error
	OnMessage(connectionID uint32, message string) (response string, err error)
	OnConnectionEnd(connectionID uint32) error
}

// Server handles network connections - completely generic
type Server struct {
	port              int
	connectionCounter uint32
	handler           MessageHandler
	logger            *slog.Logger
	socketPath        string
	listener          net.Listener
	config            *common.Config
	ctx               context.Context
	cancel            context.CancelFunc
}

func NewServer(config *common.Config, ctx context.Context, port int, handler MessageHandler) *Server {
	ctx, cancel := context.WithCancel(ctx)

	return &Server{
		port:       port,
		handler:    handler,
		logger:     ctx.Value("logger").(*slog.Logger),
		socketPath: fmt.Sprintf("/tmp/network_%d.sock", port),
		ctx:        ctx,
		config:     config,
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
		s.logger.Debug("Unix socket failed, trying TCP", "Unix Socket", s.socketPath)
		s.listener, err = net.Listen("tcp", fmt.Sprintf(":%d", s.port))
		if err != nil {
			return fmt.Errorf("failed to start server: %v", err)
		}
		s.logger.Info("Server started on TCP port", "TCP Port", s.port)
	} else {
		s.logger.Info("Server started on Unix socket", "socketPath", s.socketPath)
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
				s.logger.Error("Accept failed", "error", err)
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
		s.logger.Debug("Removed socket file", "socketPath", s.socketPath)
	}

	s.logger.Info("Server shutdown complete")
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Generate unique connection ID
	connectionID := atomic.AddUint32(&s.connectionCounter, 1)

	scanner := bufio.NewScanner(conn)
	writer := bufio.NewWriter(conn)

	response := fmt.Sprintf("CONNECTION_ID:%d\n", connectionID)
	writer.WriteString(response)
	writer.Flush()
	ctx := context.WithValue(s.ctx, "connectionId", connectionID)

	// Notify connection start
	if err := s.handler.OnConnectionStart(s.config, ctx, connectionID, scanner, writer); err != nil {
		s.logger.Error("Handler OnConnectionStart failed", "error", err)
		return
	}

	// Process messages
	for scanner.Scan() {
		message := strings.TrimSpace(scanner.Text())

		if message == "CLOSE" {
			break
		}

		// Pass raw message to application
		response, err := s.handler.OnMessage(connectionID, message)
		if err != nil {
			s.logger.Error("Handler OnMessage failed", "error", err)
			return
		}

		// Send response if handler provided one
		if response != "" {
			writer.WriteString(response + "\n")
			writer.Flush()
		}
	}

	// Notify connection end
	if err := s.handler.OnConnectionEnd(connectionID); err != nil {
		s.logger.Error("Handler OnConnectionEnd failed", "error", err)
	}

	if err := scanner.Err(); err != nil {
		s.logger.Error("Scanner error", "error", err)
	}
}
