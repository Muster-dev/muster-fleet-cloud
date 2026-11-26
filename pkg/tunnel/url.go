package tunnel

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// TunnelPath is the relay's WebSocket endpoint.
const TunnelPath = "/v1/tunnel"

// RelayURLs holds the normalized forms of a relay address.
type RelayURLs struct {
	// WS is the WebSocket base URL (ws:// or wss://, no path), e.g.
	// "wss://relay.getmuster.dev". The tunnel endpoint is WS + TunnelPath.
	WS string
	// HTTP is the matching HTTP base URL for the REST API, e.g.
	// "https://relay.getmuster.dev".
	HTTP string
}

// Tunnel returns the full WebSocket tunnel endpoint URL.
func (r RelayURLs) Tunnel() string { return r.WS + TunnelPath }

// NormalizeRelayURL accepts any of "wss://host", "https://host",
// "wss://host/v1/tunnel", "host" (assumes wss) and returns both forms.
//
// Plain ws:// / http:// is refused for non-loopback hosts because tokens
// would cross the network in clear text. Set MUSTER_CLOUD_INSECURE=1 to
// override (testing only).
func NormalizeRelayURL(raw string) (RelayURLs, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RelayURLs{}, fmt.Errorf("relay URL is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "wss://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return RelayURLs{}, fmt.Errorf("invalid relay URL: %w", err)
	}
	if u.Host == "" {
		return RelayURLs{}, fmt.Errorf("invalid relay URL %q: missing host", raw)
	}

	var secure bool
	switch u.Scheme {
	case "wss", "https":
		secure = true
	case "ws", "http":
		secure = false
	default:
		return RelayURLs{}, fmt.Errorf("invalid relay URL scheme %q (want wss:// or https://)", u.Scheme)
	}
	if !secure && !isLoopbackHost(u.Hostname()) && os.Getenv("MUSTER_CLOUD_INSECURE") != "1" {
		return RelayURLs{}, fmt.Errorf("refusing insecure relay URL %q: use wss:// (set MUSTER_CLOUD_INSECURE=1 to override for testing)", raw)
	}

	path := strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, TunnelPath)
	path = strings.TrimRight(path, "/")

	host := u.Host
	if secure {
		return RelayURLs{WS: "wss://" + host + path, HTTP: "https://" + host + path}, nil
	}
	return RelayURLs{WS: "ws://" + host + path, HTTP: "http://" + host + path}, nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
