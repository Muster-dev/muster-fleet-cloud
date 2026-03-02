package relay

import (
	"sync"
	"time"

	"github.com/ImJustRicky/muster-fleet-cloud/internal/tunnel"
)

// Conn wraps a WebSocket connection with metadata.
type Conn struct {
	WS          *tunnel.WSConn
	Identity    string // "org_id/name"
	ClientType  string // "agent" or "cli"
	OrgID       string
	ConnectedAt time.Time
	LastSeen    time.Time
	PublicKey   string // base64 X25519 public key (agents only)
	Version     string // agent version
	mu          sync.Mutex
}

// AgentInfo is the public view of a connected agent.
type AgentInfo struct {
	Name        string    `json:"name"`
	OrgID       string    `json:"org_id"`
	ConnectedAt time.Time `json:"connected_at"`
	LastSeen    time.Time `json:"last_seen"`
	Version     string    `json:"version,omitempty"`
	PublicKey   string    `json:"public_key,omitempty"`
}

// Router manages connected agents and CLI clients.
type Router struct {
	mu      sync.RWMutex
	agents  map[string]*Conn // key: "org_id/agent_name"
	clients map[string]*Conn // key: "org_id/cli_name"
}

// NewRouter creates a new connection router.
func NewRouter() *Router {
	return &Router{
		agents:  make(map[string]*Conn),
		clients: make(map[string]*Conn),
	}
}

// RegisterAgent adds an agent connection to the routing table.
func (r *Router) RegisterAgent(identity string, conn *Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Close existing connection if any
	if old, ok := r.agents[identity]; ok {
		old.WS.Close()
	}

	r.agents[identity] = conn
}

// RegisterClient adds a CLI client connection.
func (r *Router) RegisterClient(identity string, conn *Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old, ok := r.clients[identity]; ok {
		old.WS.Close()
	}

	r.clients[identity] = conn
}

// Unregister removes a connection from the routing table.
func (r *Router) Unregister(identity string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.agents, identity)
	delete(r.clients, identity)
}

// Route finds the connection for a destination identity.
func (r *Router) Route(destID string) (*Conn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if conn, ok := r.agents[destID]; ok {
		return conn, true
	}
	if conn, ok := r.clients[destID]; ok {
		return conn, true
	}
	return nil, false
}

// ListAgents returns all connected agents for an org.
func (r *Router) ListAgents(orgID string) []AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var agents []AgentInfo
	for _, conn := range r.agents {
		if conn.OrgID == orgID {
			agents = append(agents, AgentInfo{
				Name:        conn.Identity,
				OrgID:       conn.OrgID,
				ConnectedAt: conn.ConnectedAt,
				LastSeen:    conn.LastSeen,
				Version:     conn.Version,
				PublicKey:   conn.PublicKey,
			})
		}
	}
	return agents
}

// AgentCount returns the number of connected agents.
func (r *Router) AgentCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}

// AllConnections returns all connections (for heartbeat checking).
func (r *Router) AllConnections() []*Conn {
	r.mu.RLock()
	defer r.mu.RUnlock()

	conns := make([]*Conn, 0, len(r.agents)+len(r.clients))
	for _, c := range r.agents {
		conns = append(conns, c)
	}
	for _, c := range r.clients {
		conns = append(conns, c)
	}
	return conns
}
