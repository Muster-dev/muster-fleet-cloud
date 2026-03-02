package tunnel

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/Muster-dev/muster-fleet-cloud/internal/protocol"
)

// Client manages a WebSocket connection to the relay.
type Client struct {
	relayURL string
	token    string
	identity string // "org_id/name"

	conn *WSConn
	mu   sync.Mutex
}

// NewClient creates a tunnel client.
func NewClient(relayURL, token, orgID, name string) *Client {
	return &Client{
		relayURL: relayURL,
		token:    token,
		identity: orgID + "/" + name,
	}
}

// Connect establishes a WebSocket connection to the relay.
func (c *Client) Connect() error {
	url := c.relayURL + "/v1/tunnel"

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+c.token)

	conn, err := Dial(url, headers)
	if err != nil {
		return fmt.Errorf("connect to relay %s: %w", c.relayURL, err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	log.Printf("connected to relay: %s", c.relayURL)
	return nil
}

// Close closes the WebSocket connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// SendFrame encodes and sends a protocol frame.
func (c *Client) SendFrame(f *protocol.Frame) error {
	data := protocol.Encode(f)

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	return conn.Write(data)
}

// ReadFrame reads and decodes a protocol frame.
func (c *Client) ReadFrame() (*protocol.Frame, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	data, err := conn.Read()
	if err != nil {
		return nil, fmt.Errorf("read frame: %w", err)
	}

	return protocol.Decode(data)
}

// Identity returns the client's identity string.
func (c *Client) Identity() string {
	return c.identity
}

// IsConnected returns whether the client has an active connection.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}
