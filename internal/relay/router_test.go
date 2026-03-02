package relay

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/internal/tunnel"
	"golang.org/x/net/websocket"
)

// newTestWSConn creates a WSConn backed by a real websocket connection
// using an in-process HTTP test server.
func newTestWSConn(t *testing.T) *tunnel.WSConn {
	t.Helper()

	srv := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		// Block until the connection is closed
		buf := make([]byte, 1)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + srv.URL[len("http"):]
	ws, err := tunnel.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	t.Cleanup(func() { ws.Close() })

	return ws
}

func makeConn(t *testing.T, identity, orgID string) *Conn {
	t.Helper()
	return &Conn{
		WS:          newTestWSConn(t),
		Identity:    identity,
		OrgID:       orgID,
		ClientType:  "agent",
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}
}

func TestRegisterAgentAndRoute(t *testing.T) {
	r := NewRouter()
	conn := makeConn(t, "org1/agent-a", "org1")

	r.RegisterAgent("org1/agent-a", conn)

	found, ok := r.Route("org1/agent-a")
	if !ok {
		t.Fatal("Route did not find registered agent")
	}
	if found != conn {
		t.Error("Route returned wrong connection")
	}
}

func TestRouteReturnsFalseForUnknown(t *testing.T) {
	r := NewRouter()
	_, ok := r.Route("org1/unknown")
	if ok {
		t.Error("Route returned true for unknown identity")
	}
}

func TestUnregisterRemovesConnection(t *testing.T) {
	r := NewRouter()
	conn := makeConn(t, "org1/agent-a", "org1")

	r.RegisterAgent("org1/agent-a", conn)
	r.Unregister("org1/agent-a")

	_, ok := r.Route("org1/agent-a")
	if ok {
		t.Error("Route found agent after Unregister")
	}
}

func TestRegisterAgentReplacesExisting(t *testing.T) {
	r := NewRouter()
	old := makeConn(t, "org1/agent-a", "org1")
	newConn := makeConn(t, "org1/agent-a", "org1")

	r.RegisterAgent("org1/agent-a", old)
	r.RegisterAgent("org1/agent-a", newConn)

	found, ok := r.Route("org1/agent-a")
	if !ok {
		t.Fatal("Route did not find agent after replacement")
	}
	if found != newConn {
		t.Error("Route returned old connection instead of new one")
	}
}

func TestListAgentsFiltersByOrg(t *testing.T) {
	r := NewRouter()
	conn1 := makeConn(t, "org1/agent-a", "org1")
	conn2 := makeConn(t, "org1/agent-b", "org1")
	conn3 := makeConn(t, "org2/agent-c", "org2")

	r.RegisterAgent("org1/agent-a", conn1)
	r.RegisterAgent("org1/agent-b", conn2)
	r.RegisterAgent("org2/agent-c", conn3)

	org1Agents := r.ListAgents("org1")
	if len(org1Agents) != 2 {
		t.Errorf("expected 2 agents for org1, got %d", len(org1Agents))
	}

	org2Agents := r.ListAgents("org2")
	if len(org2Agents) != 1 {
		t.Errorf("expected 1 agent for org2, got %d", len(org2Agents))
	}

	noAgents := r.ListAgents("org-unknown")
	if len(noAgents) != 0 {
		t.Errorf("expected 0 agents for unknown org, got %d", len(noAgents))
	}
}

func TestAgentCount(t *testing.T) {
	r := NewRouter()

	if r.AgentCount() != 0 {
		t.Errorf("expected 0 agents initially, got %d", r.AgentCount())
	}

	conn1 := makeConn(t, "org1/agent-a", "org1")
	conn2 := makeConn(t, "org1/agent-b", "org1")

	r.RegisterAgent("org1/agent-a", conn1)
	if r.AgentCount() != 1 {
		t.Errorf("expected 1 agent, got %d", r.AgentCount())
	}

	r.RegisterAgent("org1/agent-b", conn2)
	if r.AgentCount() != 2 {
		t.Errorf("expected 2 agents, got %d", r.AgentCount())
	}

	r.Unregister("org1/agent-a")
	if r.AgentCount() != 1 {
		t.Errorf("expected 1 agent after unregister, got %d", r.AgentCount())
	}
}

func TestRegisterClientAndRoute(t *testing.T) {
	r := NewRouter()
	conn := &Conn{
		WS:          newTestWSConn(t),
		Identity:    "org1/cli-user",
		OrgID:       "org1",
		ClientType:  "cli",
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}

	r.RegisterClient("org1/cli-user", conn)

	found, ok := r.Route("org1/cli-user")
	if !ok {
		t.Fatal("Route did not find registered client")
	}
	if found != conn {
		t.Error("Route returned wrong connection for client")
	}
}
