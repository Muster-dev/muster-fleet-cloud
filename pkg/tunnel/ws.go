package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// DefaultMaxMessageBytes caps a single WebSocket message (one MTP frame).
// Large enough for pushed hook scripts, small enough that a peer cannot make
// us buffer arbitrary amounts of memory.
const DefaultMaxMessageBytes = 1 << 20 // 1 MiB

// DefaultWriteTimeout bounds how long a single frame write may block.
const DefaultWriteTimeout = 15 * time.Second

// DefaultDialTimeout bounds TCP connect + TLS + WebSocket handshake.
const DefaultDialTimeout = 20 * time.Second

// ErrClosed is returned by Read when the peer closed the connection.
var ErrClosed = errors.New("connection closed")

// WSConn wraps golang.org/x/net/websocket for binary frame I/O.
// Writes are serialized and bounded by a write deadline.
type WSConn struct {
	conn         *websocket.Conn
	remoteAddr   string
	writeMu      sync.Mutex
	writeTimeout time.Duration
	closeOnce    sync.Once
	closed       chan struct{}
}

func wrap(conn *websocket.Conn, remoteAddr string, maxMessage int) *WSConn {
	conn.PayloadType = websocket.BinaryFrame
	if maxMessage <= 0 {
		maxMessage = DefaultMaxMessageBytes
	}
	conn.MaxPayloadBytes = maxMessage
	return &WSConn{
		conn:         conn,
		remoteAddr:   remoteAddr,
		writeTimeout: DefaultWriteTimeout,
		closed:       make(chan struct{}),
	}
}

// Dial connects to a WebSocket server with DefaultDialTimeout.
func Dial(rawURL string, headers http.Header) (*WSConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultDialTimeout)
	defer cancel()
	return DialContext(ctx, rawURL, headers)
}

// DialContext connects to a WebSocket server. TLS certificates are verified
// for wss:// URLs.
func DialContext(ctx context.Context, rawURL string, headers http.Header) (*WSConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	origin := "http://" + u.Host + "/"
	if u.Scheme == "wss" {
		origin = "https://" + u.Host + "/"
	}

	config, err := websocket.NewConfig(rawURL, origin)
	if err != nil {
		return nil, fmt.Errorf("websocket config: %w", err)
	}
	for k, vals := range headers {
		for _, v := range vals {
			config.Header.Set(k, v)
		}
	}

	conn, err := config.DialContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}
	return wrap(conn, u.Host, DefaultMaxMessageBytes), nil
}

// ServerOptions configures AcceptHandler.
type ServerOptions struct {
	// MaxMessageBytes caps incoming messages (default DefaultMaxMessageBytes).
	MaxMessageBytes int
	// RemoteAddr extracts the client address from the upgrade request
	// (default: r.RemoteAddr). Used for logging and rate limiting.
	RemoteAddr func(r *http.Request) string
	// Allow is called before the upgrade; returning false rejects the request
	// with 429 (used for per-IP connection rate limiting).
	Allow func(r *http.Request) bool
}

// AcceptHandler returns an http.Handler that upgrades to WebSocket and calls fn.
// Any Origin is accepted: authentication happens in-band with a token, never
// with ambient browser credentials, so cross-site WebSocket hijacking does not
// apply.
func AcceptHandler(fn func(ws *WSConn), opts ...ServerOptions) http.Handler {
	var o ServerOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	srv := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(conn *websocket.Conn) {
			addr := conn.Request().RemoteAddr
			if o.RemoteAddr != nil {
				addr = o.RemoteAddr(conn.Request())
			}
			ws := wrap(conn, addr, o.MaxMessageBytes)
			defer ws.Close()
			fn(ws)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.Allow != nil && !o.Allow(r) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		srv.ServeHTTP(w, r)
	})
}

// Write sends one binary message, bounded by the write timeout.
func (w *WSConn) Write(data []byte) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if w.writeTimeout > 0 {
		_ = w.conn.SetWriteDeadline(time.Now().Add(w.writeTimeout))
	}
	_, err := w.conn.Write(data)
	return err
}

// Read reads one binary message. Messages larger than the configured maximum
// return an error and should be treated as fatal for the connection.
func (w *WSConn) Read() ([]byte, error) {
	var msg []byte
	err := websocket.Message.Receive(w.conn, &msg)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrClosed
		}
		if errors.Is(err, websocket.ErrFrameTooLarge) {
			return nil, fmt.Errorf("message exceeds %d bytes", w.conn.MaxPayloadBytes)
		}
		return nil, err
	}
	return msg, nil
}

// SetReadDeadline sets the deadline for the next Read (zero clears it).
func (w *WSConn) SetReadDeadline(t time.Time) error {
	return w.conn.SetReadDeadline(t)
}

// SetReadTimeout sets the read deadline to now+d (d<=0 clears it).
func (w *WSConn) SetReadTimeout(d time.Duration) error {
	if d <= 0 {
		return w.conn.SetReadDeadline(time.Time{})
	}
	return w.conn.SetReadDeadline(time.Now().Add(d))
}

// Close closes the connection. Safe to call multiple times.
func (w *WSConn) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.closed)
		err = w.conn.Close()
	})
	return err
}

// Done is closed when Close has been called.
func (w *WSConn) Done() <-chan struct{} { return w.closed }

// RemoteAddr returns the peer address (as resolved by ServerOptions.RemoteAddr
// on the server side, or the dialed host on the client side).
func (w *WSConn) RemoteAddr() string { return w.remoteAddr }

// Raw returns the underlying websocket.Conn.
func (w *WSConn) Raw() *websocket.Conn { return w.conn }

// IsTimeout reports whether err is a network timeout (e.g. a read deadline).
func IsTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
