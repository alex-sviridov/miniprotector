package network

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/alex-sviridov/miniprotector/common"
)

// Client handles network communication - completely generic
type Client struct {
	host   string
	port   int
	logger *common.Logger
}

func NewClient(host string, port int, logger *common.Logger) *Client {
	return &Client{
		host:   host,
		port:   port,
		logger: logger,
	}
}

// Connection represents a persistent network connection
type Connection struct {
	id     uint32
	writer *bufio.Writer
	reader *bufio.Reader
	conn   net.Conn
	logger *common.Logger
}

func (c *Connection) WaitForResponse() (string, error) {
	response, err := c.reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	response = strings.TrimSpace(response)
	c.logger.Debug("Received response: %s", response)
	return response, nil
}

func (c *Connection) SendMessage(message string) error {
	_, err := c.writer.WriteString(message + "\n")
	if err != nil {
		return err
	}
	// Flush immediately to ensure message is sent
	err = c.writer.Flush()
	if err == nil {
		c.logger.Debug("Sent message: %s", message)
	}
	return err
}

func (c *Connection) Close() error {
	c.writer.WriteString("CLOSE\n")
	c.writer.Flush()
	return c.conn.Close()
}

func (c *Connection) GetID() uint32 {
	return c.id
}

// CreateConnection opens a persistent connection
func (c *Client) CreateConnection(config *common.Config, ctx context.Context) (*Connection, error) {
	// Connect to server
	netConn, err := c.connect()
	if err != nil {
		return nil, fmt.Errorf("connection failed: %v", err)
	}

	scanner := bufio.NewScanner(netConn)
	writer := bufio.NewWriter(netConn)
	reader := bufio.NewReader(netConn)

	// Read connection ID
	if !scanner.Scan() {
		netConn.Close()
		return nil, fmt.Errorf("no response from server")
	}

	response := scanner.Text()
	var connectionID uint32
	_, err = fmt.Sscanf(response, "CONNECTION_ID:%d", &connectionID)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("invalid response: %s", response)
	}

	// Create connection wrapper
	conn := &Connection{
		id:     connectionID,
		writer: writer,
		reader: reader,
		conn:   netConn,
		logger: ctx.Value("logger").(*common.Logger),
	}
	conn.logger.Info("Connected with ID: %d", connectionID)

	return conn, nil
}

func (c *Client) connect() (net.Conn, error) {
	// Try Unix socket first if localhost
	if c.isLocalhost() {
		socketPath := fmt.Sprintf("/tmp/network_%d.sock", c.port)
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			c.logger.Debug("Connected via Unix socket")
			return conn, nil
		}
	}

	// Fall back to TCP
	address := fmt.Sprintf("%s:%d", c.host, c.port)
	conn, err := net.Dial("tcp", address)
	if err == nil {
		c.logger.Debug("Connected via TCP to %s", address)
	}
	return conn, err
}

func (c *Client) isLocalhost() bool {
	return c.host == "localhost" || c.host == "127.0.0.1" || c.host == ""
}
