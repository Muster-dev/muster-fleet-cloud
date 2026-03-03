package tunnel

import (
	"fmt"
	"io"
	"net/http"

	"golang.org/x/net/websocket"
)

// WSConn wraps golang.org/x/net/websocket for binary frame I/O.
type WSConn struct {
	conn *websocket.Conn
}

// Dial connects to a WebSocket server.
func Dial(url string, headers http.Header) (*WSConn, error) {
	origin := "http://localhost/"

	config, err := websocket.NewConfig(url, origin)
	if err != nil {
		return nil, fmt.Errorf("websocket config: %w", err)
	}

	for k, vals := range headers {
		for _, v := range vals {
			config.Header.Set(k, v)
		}
	}

	conn, err := websocket.DialConfig(config)
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}

	conn.PayloadType = websocket.BinaryFrame
	return &WSConn{conn: conn}, nil
}

// Accept upgrades an HTTP request to a WebSocket connection.
func Accept(w http.ResponseWriter, r *http.Request) (*WSConn, error) {
	handler := func(conn *websocket.Conn) {
		// This blocks until the connection is done.
		// We'll use a different pattern — see AcceptHandler.
		select {} // should not be called directly
	}
	_ = handler

	// Use the websocket.Handler pattern
	return nil, fmt.Errorf("use AcceptHandler instead")
}

// AcceptHandler returns an http.Handler that upgrades to WebSocket and calls fn.
func AcceptHandler(fn func(ws *WSConn)) http.Handler {
	return websocket.Handler(func(conn *websocket.Conn) {
		conn.PayloadType = websocket.BinaryFrame
		fn(&WSConn{conn: conn})
	})
}

// Write sends binary data.
func (w *WSConn) Write(data []byte) error {
	_, err := w.conn.Write(data)
	return err
}

// Read reads binary data.
func (w *WSConn) Read() ([]byte, error) {
	var msg []byte
	err := websocket.Message.Receive(w.conn, &msg)
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("connection closed")
		}
		return nil, err
	}
	return msg, nil
}

// Close closes the connection.
func (w *WSConn) Close() error {
	return w.conn.Close()
}

// Raw returns the underlying websocket.Conn.
func (w *WSConn) Raw() *websocket.Conn {
	return w.conn
}
