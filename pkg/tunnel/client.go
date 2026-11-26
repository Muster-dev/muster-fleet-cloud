package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
)

// AuthTimeout bounds how long the client waits for AUTH_RESPONSE.
const AuthTimeout = 15 * time.Second

// AuthError is returned when the relay rejects the credentials. It is
// permanent: retrying with the same token will not help.
type AuthError struct {
	Code    string
	Message string
}

func (e *AuthError) Error() string {
	if e.Message == "" {
		return "authentication rejected by relay"
	}
	return "authentication rejected by relay: " + e.Message
}

// IsAuthError reports whether err is (or wraps) an *AuthError.
func IsAuthError(err error) bool {
	var ae *AuthError
	return errors.As(err, &ae)
}

// Options configures a Client.
type Options struct {
	// ClientType is protocol.ClientAgent (default) or protocol.ClientCLI.
	ClientType string
	// Name is the requested agent name (agents only; CLI names are assigned
	// by the relay).
	Name string
	// OrgID is optional; if set the relay verifies it matches the token's org.
	OrgID string
	// Version is reported to the relay.
	Version string
	// Logf receives connection log lines (default log.Printf; set to a no-op
	// to silence).
	Logf func(format string, args ...any)
}

// Client manages a WebSocket connection to the relay.
type Client struct {
	urls RelayURLs
	opts Options

	mu    sync.Mutex
	token string
	conn  *WSConn
	auth  *protocol.AuthResponse
}

// NewClient creates a tunnel client. relayURL may be wss://host,
// https://host or include the /v1/tunnel path.
func NewClient(relayURL, token string, opts Options) (*Client, error) {
	urls, err := NormalizeRelayURL(relayURL)
	if err != nil {
		return nil, err
	}
	if opts.ClientType == "" {
		opts.ClientType = protocol.ClientAgent
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	return &Client{urls: urls, opts: opts, token: token}, nil
}

// URLs returns the normalized relay URLs.
func (c *Client) URLs() RelayURLs { return c.urls }

// SetToken replaces the credential used for subsequent connections (e.g.
// after enrollment returns a node token).
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

// Connect establishes a WebSocket connection to the relay.
func (c *Client) Connect() error {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultDialTimeout)
	defer cancel()
	return c.ConnectContext(ctx)
}

// ConnectContext establishes a WebSocket connection to the relay.
// The token is NOT sent as an HTTP header (it would end up in proxy access
// logs); it is sent in-band in AUTH_REQUEST.
func (c *Client) ConnectContext(ctx context.Context) error {
	conn, err := DialContext(ctx, c.urls.Tunnel(), nil)
	if err != nil {
		return fmt.Errorf("connect to relay %s: %w", c.urls.WS, err)
	}
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.conn = conn
	c.auth = nil
	c.mu.Unlock()
	return nil
}

// Authenticate sends AUTH_REQUEST and waits for AUTH_RESPONSE. On success it
// returns the relay's response (org, assigned name, optional node token).
// A rejection is returned as *AuthError.
func (c *Client) Authenticate() (*protocol.AuthResponse, error) {
	c.mu.Lock()
	token := c.token
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	payload, err := json.Marshal(protocol.AuthRequest{
		Token:      token,
		ClientType: c.opts.ClientType,
		OrgID:      c.opts.OrgID,
		Name:       c.opts.Name,
		Version:    c.opts.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal auth payload: %w", err)
	}

	frame := protocol.NewFrame(protocol.MsgAuthRequest, NewRequestID(), c.opts.Name, protocol.RelayID, 0, payload)
	if err := c.SendFrame(frame); err != nil {
		return nil, fmt.Errorf("send AUTH_REQUEST: %w", err)
	}

	_ = conn.SetReadTimeout(AuthTimeout)
	resp, err := c.ReadFrame()
	_ = conn.SetReadTimeout(0)
	if err != nil {
		return nil, fmt.Errorf("read AUTH_RESPONSE: %w", err)
	}
	if resp.MsgType != protocol.MsgAuthResponse {
		return nil, fmt.Errorf("expected AUTH_RESPONSE, got %s", protocol.MsgTypeName(resp.MsgType))
	}

	var ar protocol.AuthResponse
	if err := json.Unmarshal(resp.Payload, &ar); err != nil {
		return nil, fmt.Errorf("parse AUTH_RESPONSE: %w", err)
	}
	if !ar.OK {
		return nil, &AuthError{Code: ar.Code, Message: ar.Message}
	}

	c.mu.Lock()
	c.auth = &ar
	c.mu.Unlock()
	return &ar, nil
}

// Close closes the WebSocket connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// SendFrame encodes and sends a protocol frame. Safe for concurrent use.
func (c *Client) SendFrame(f *protocol.Frame) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	return conn.Write(protocol.Encode(f))
}

// ReadFrame reads and decodes a protocol frame. Not safe for concurrent use.
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

// SetReadTimeout sets a read deadline of now+d on the current connection
// (d<=0 clears it).
func (c *Client) SetReadTimeout(d time.Duration) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.SetReadTimeout(d)
	}
}

// Identity returns the name the relay knows this connection by (the
// relay-assigned name after authentication, else the requested name).
func (c *Client) Identity() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.auth != nil && c.auth.Name != "" {
		return c.auth.Name
	}
	return c.opts.Name
}

// OrgID returns the authenticated org (empty before authentication).
func (c *Client) OrgID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.auth != nil {
		return c.auth.OrgID
	}
	return ""
}

// IsConnected returns whether the client has an active connection.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// Heartbeat sends a HEARTBEAT to the relay every interval until ctx is done
// or a send fails. It keeps idle connections alive through proxies and lets
// the relay detect dead peers.
func (c *Client) Heartbeat(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f := protocol.NewFrame(protocol.MsgHeartbeat, NewRequestID(), c.Identity(), protocol.RelayID, 0, nil)
			if err := c.SendFrame(f); err != nil {
				return
			}
		}
	}
}

// NewRequestID returns a random 16-byte request ID.
func NewRequestID() [16]byte {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return id
}
