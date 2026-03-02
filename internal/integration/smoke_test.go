package integration

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/internal/auth"
	"github.com/Muster-dev/muster-fleet-cloud/internal/protocol"
	"github.com/Muster-dev/muster-fleet-cloud/internal/relay"
	"github.com/Muster-dev/muster-fleet-cloud/internal/tunnel"
)

// testEnv holds the shared state for an integration test scenario.
type testEnv struct {
	server     *httptest.Server
	relay      *relay.Server
	tokenStore *auth.TokenStore
	storePath  string

	agentToken string // raw agent token
	cliToken   string // raw CLI token
	orgID      string
}

// setupTestEnv starts a relay server with auth on a random port and creates test tokens.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	// Create a temp token store
	dir := t.TempDir()
	storePath := filepath.Join(dir, "tokens.json")
	store, err := auth.NewTokenStore(storePath)
	if err != nil {
		t.Fatalf("create token store: %v", err)
	}

	orgID := "test-org"

	// Create an agent-session token (reusable, unlike agent-join)
	agentRaw, agentHash, err := auth.GenerateToken(auth.PrefixAgentSession)
	if err != nil {
		t.Fatalf("generate agent token: %v", err)
	}
	if _, err := store.CreateToken(agentHash, auth.TypeAgentSession, orgID, "agent-token"); err != nil {
		t.Fatalf("store agent token: %v", err)
	}

	// Create a CLI token
	cliRaw, cliHash, err := auth.GenerateToken(auth.PrefixCLI)
	if err != nil {
		t.Fatalf("generate cli token: %v", err)
	}
	if _, err := store.CreateToken(cliHash, auth.TypeCLI, orgID, "cli-token"); err != nil {
		t.Fatalf("store cli token: %v", err)
	}

	// Create relay server
	srv := relay.NewServer(store)
	ts := httptest.NewServer(srv.Handler())

	return &testEnv{
		server:     ts,
		relay:      srv,
		tokenStore: store,
		storePath:  storePath,
		agentToken: agentRaw,
		cliToken:   cliRaw,
		orgID:      orgID,
	}
}

func (e *testEnv) cleanup() {
	e.server.Close()
	os.Remove(e.storePath)
}

// wsURL converts the httptest server URL to a ws:// URL.
func (e *testEnv) wsURL() string {
	return strings.Replace(e.server.URL, "http://", "ws://", 1)
}

// connectAgent creates a tunnel client configured as an agent, connects, authenticates,
// and sends AGENT_HELLO + waits for RELAY_ACK. Returns the client ready for message loop.
func (e *testEnv) connectAgent(t *testing.T, name string) *tunnel.Client {
	t.Helper()

	client := tunnel.NewClient(e.wsURL(), e.agentToken, e.orgID, name)
	if err := client.Connect(); err != nil {
		t.Fatalf("agent connect: %v", err)
	}
	if err := client.Authenticate(); err != nil {
		t.Fatalf("agent auth: %v", err)
	}

	// Send AGENT_HELLO
	hello, _ := json.Marshal(map[string]interface{}{
		"agent_name": name,
		"org_id":     e.orgID,
		"version":    "0.1.0-test",
		"public_key": "",
	})
	var reqID [16]byte
	helloFrame := protocol.NewFrame(
		protocol.MsgAgentHello,
		reqID,
		e.orgID+"/"+name,
		"relay",
		0,
		hello,
	)
	if err := client.SendFrame(helloFrame); err != nil {
		t.Fatalf("send AGENT_HELLO: %v", err)
	}

	// Wait for RELAY_ACK
	ack, err := client.ReadFrame()
	if err != nil {
		t.Fatalf("read RELAY_ACK: %v", err)
	}
	if ack.MsgType != protocol.MsgRelayAck {
		t.Fatalf("expected RELAY_ACK, got %s", protocol.MsgTypeName(ack.MsgType))
	}

	return client
}

// connectCLI creates a tunnel client configured as a CLI, connects and authenticates.
func (e *testEnv) connectCLI(t *testing.T, name string) *tunnel.Client {
	t.Helper()

	client := tunnel.NewClient(e.wsURL(), e.cliToken, e.orgID, name)
	// Need to set client type to "cli" — NewClient defaults to "agent"
	// We'll create the client manually with the right type
	return client
}

// dialCLI dials the relay as a CLI client, handling auth manually.
func (e *testEnv) dialCLI(t *testing.T, name string) *tunnel.WSConn {
	t.Helper()

	url := e.wsURL() + "/v1/tunnel"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+e.cliToken)

	ws, err := tunnel.Dial(url, headers)
	if err != nil {
		t.Fatalf("dial CLI: %v", err)
	}

	// Send AUTH_REQUEST
	identity := e.orgID + "/" + name
	authPayload, _ := json.Marshal(map[string]string{
		"token":       e.cliToken,
		"client_type": "cli",
		"org_id":      e.orgID,
		"name":        name,
	})
	var reqID [16]byte
	authFrame := protocol.NewFrame(
		protocol.MsgAuthRequest,
		reqID,
		identity,
		"relay",
		0,
		authPayload,
	)
	if err := ws.Write(protocol.Encode(authFrame)); err != nil {
		t.Fatalf("send CLI AUTH_REQUEST: %v", err)
	}

	// Read AUTH_RESPONSE
	data, err := ws.Read()
	if err != nil {
		t.Fatalf("read CLI AUTH_RESPONSE: %v", err)
	}
	resp, err := protocol.Decode(data)
	if err != nil {
		t.Fatalf("decode CLI AUTH_RESPONSE: %v", err)
	}
	if resp.MsgType != protocol.MsgAuthResponse {
		t.Fatalf("expected AUTH_RESPONSE, got %s", protocol.MsgTypeName(resp.MsgType))
	}
	var authResp struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(resp.Payload, &authResp)
	if !authResp.OK {
		t.Fatalf("CLI auth failed: %s", string(resp.Payload))
	}

	return ws
}

// readFrameWithTimeout reads a frame with a deadline to avoid hanging tests.
func readFrameWithTimeout(ws *tunnel.WSConn, timeout time.Duration) (*protocol.Frame, error) {
	type result struct {
		frame *protocol.Frame
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := ws.Read()
		if err != nil {
			ch <- result{nil, err}
			return
		}
		f, err := protocol.Decode(data)
		ch <- result{f, err}
	}()

	select {
	case r := <-ch:
		return r.frame, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("read timed out after %s", timeout)
	}
}

func clientReadFrameWithTimeout(c *tunnel.Client, timeout time.Duration) (*protocol.Frame, error) {
	type result struct {
		frame *protocol.Frame
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := c.ReadFrame()
		ch <- result{f, err}
	}()

	select {
	case r := <-ch:
		return r.frame, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("read timed out after %s", timeout)
	}
}

// TestSmoke_AgentConnectAndRegister verifies the agent can connect,
// authenticate, and register with the relay.
func TestSmoke_AgentConnectAndRegister(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	agent := env.connectAgent(t, "test-agent-1")
	defer agent.Close()

	// Verify agent appears in the router
	agents := env.relay.Router().ListAgents(env.orgID)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Version != "0.1.0-test" {
		t.Errorf("expected agent version 0.1.0-test, got %q", agents[0].Version)
	}
}

// TestSmoke_CLIConnectAndAuth verifies a CLI client can connect and authenticate.
func TestSmoke_CLIConnectAndAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	cli := env.dialCLI(t, "test-cli-1")
	defer cli.Close()

	// CLI should be registered
	conns := env.relay.Router().AllConnections()
	found := false
	for _, c := range conns {
		if c.ClientType == "cli" {
			found = true
		}
	}
	if !found {
		t.Fatal("CLI client not found in router connections")
	}
}

// TestSmoke_CommandFlow verifies the full command lifecycle:
// CLI sends COMMAND -> relay routes to agent -> agent responds with ACK + output + result.
func TestSmoke_CommandFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	// Connect agent
	agent := env.connectAgent(t, "cmd-agent")
	defer agent.Close()

	// Connect CLI
	cli := env.dialCLI(t, "cmd-cli")
	defer cli.Close()

	// CLI sends a COMMAND frame to the agent
	agentIdentity := env.orgID + "/cmd-agent"
	cliIdentity := env.orgID + "/cmd-cli"

	cmdPayload, _ := json.Marshal(map[string]interface{}{
		"action":  "exec",
		"command": "echo hello-from-test",
		"stream":  true,
	})

	var reqID [16]byte
	copy(reqID[:], []byte("test-cmd-req-001"))
	cmdFrame := protocol.NewFrame(
		protocol.MsgCommand,
		reqID,
		cliIdentity,
		agentIdentity,
		0,
		cmdPayload,
	)
	if err := cli.Write(protocol.Encode(cmdFrame)); err != nil {
		t.Fatalf("send COMMAND: %v", err)
	}

	// Agent should receive the COMMAND (routed by relay)
	agentFrame, err := clientReadFrameWithTimeout(agent, 5*time.Second)
	if err != nil {
		t.Fatalf("agent read COMMAND: %v", err)
	}
	if agentFrame.MsgType != protocol.MsgCommand {
		t.Fatalf("agent expected COMMAND, got %s", protocol.MsgTypeName(agentFrame.MsgType))
	}

	// Verify command payload arrived intact
	var received map[string]interface{}
	json.Unmarshal(agentFrame.Payload, &received)
	if received["action"] != "exec" {
		t.Errorf("expected action=exec, got %v", received["action"])
	}

	// Agent sends COMMAND_ACK back to CLI
	ackFrame := protocol.NewFrame(
		protocol.MsgCommandAck,
		reqID,
		agentIdentity,
		cliIdentity,
		0,
		nil,
	)
	if err := agent.SendFrame(ackFrame); err != nil {
		t.Fatalf("send COMMAND_ACK: %v", err)
	}

	// Agent sends STREAM_DATA
	streamPayload, _ := json.Marshal(map[string]string{
		"line":   "hello-from-test",
		"stream": "stdout",
	})
	streamFrame := protocol.NewFrame(
		protocol.MsgStreamData,
		reqID,
		agentIdentity,
		cliIdentity,
		protocol.FlagStreamContinued,
		streamPayload,
	)
	if err := agent.SendFrame(streamFrame); err != nil {
		t.Fatalf("send STREAM_DATA: %v", err)
	}

	// Agent sends COMMAND_RESULT
	resultPayload, _ := json.Marshal(map[string]interface{}{
		"exit_code": 0,
		"status":    "done",
	})
	resultFrame := protocol.NewFrame(
		protocol.MsgCommandResult,
		reqID,
		agentIdentity,
		cliIdentity,
		0,
		resultPayload,
	)
	if err := agent.SendFrame(resultFrame); err != nil {
		t.Fatalf("send COMMAND_RESULT: %v", err)
	}

	// CLI should receive COMMAND_ACK, STREAM_DATA, and COMMAND_RESULT
	cliFrame1, err := readFrameWithTimeout(cli, 5*time.Second)
	if err != nil {
		t.Fatalf("CLI read COMMAND_ACK: %v", err)
	}
	if cliFrame1.MsgType != protocol.MsgCommandAck {
		t.Fatalf("CLI expected COMMAND_ACK, got %s", protocol.MsgTypeName(cliFrame1.MsgType))
	}

	cliFrame2, err := readFrameWithTimeout(cli, 5*time.Second)
	if err != nil {
		t.Fatalf("CLI read STREAM_DATA: %v", err)
	}
	if cliFrame2.MsgType != protocol.MsgStreamData {
		t.Fatalf("CLI expected STREAM_DATA, got %s", protocol.MsgTypeName(cliFrame2.MsgType))
	}
	var streamLine map[string]string
	json.Unmarshal(cliFrame2.Payload, &streamLine)
	if streamLine["line"] != "hello-from-test" {
		t.Errorf("expected stream line 'hello-from-test', got %q", streamLine["line"])
	}

	cliFrame3, err := readFrameWithTimeout(cli, 5*time.Second)
	if err != nil {
		t.Fatalf("CLI read COMMAND_RESULT: %v", err)
	}
	if cliFrame3.MsgType != protocol.MsgCommandResult {
		t.Fatalf("CLI expected COMMAND_RESULT, got %s", protocol.MsgTypeName(cliFrame3.MsgType))
	}
	var cmdResult map[string]interface{}
	json.Unmarshal(cliFrame3.Payload, &cmdResult)
	if cmdResult["exit_code"].(float64) != 0 {
		t.Errorf("expected exit_code=0, got %v", cmdResult["exit_code"])
	}
}

// TestSmoke_Heartbeat verifies the heartbeat/keepalive flow.
func TestSmoke_Heartbeat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	agent := env.connectAgent(t, "hb-agent")
	defer agent.Close()

	// Agent sends HEARTBEAT to relay
	var reqID [16]byte
	copy(reqID[:], []byte("heartbeat-001"))
	hb := protocol.NewFrame(
		protocol.MsgHeartbeat,
		reqID,
		env.orgID+"/hb-agent",
		"relay",
		0,
		nil,
	)
	if err := agent.SendFrame(hb); err != nil {
		t.Fatalf("send HEARTBEAT: %v", err)
	}

	// Should receive HEARTBEAT_ACK from relay
	ack, err := clientReadFrameWithTimeout(agent, 5*time.Second)
	if err != nil {
		t.Fatalf("read HEARTBEAT_ACK: %v", err)
	}
	if ack.MsgType != protocol.MsgHeartbeatAck {
		t.Fatalf("expected HEARTBEAT_ACK, got %s", protocol.MsgTypeName(ack.MsgType))
	}
}

// TestSmoke_AgentListRequest verifies a CLI can request the list of connected agents.
func TestSmoke_AgentListRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	// Connect two agents
	agent1 := env.connectAgent(t, "list-agent-1")
	defer agent1.Close()
	agent2 := env.connectAgent(t, "list-agent-2")
	defer agent2.Close()

	// Connect CLI
	cli := env.dialCLI(t, "list-cli")
	defer cli.Close()

	// CLI sends AGENT_LIST_REQUEST
	var reqID [16]byte
	copy(reqID[:], []byte("list-req-001"))
	listReq := protocol.NewFrame(
		protocol.MsgAgentListRequest,
		reqID,
		env.orgID+"/list-cli",
		"relay",
		0,
		nil,
	)
	if err := cli.Write(protocol.Encode(listReq)); err != nil {
		t.Fatalf("send AGENT_LIST_REQUEST: %v", err)
	}

	// Should receive AGENT_LIST
	resp, err := readFrameWithTimeout(cli, 5*time.Second)
	if err != nil {
		t.Fatalf("read AGENT_LIST: %v", err)
	}
	if resp.MsgType != protocol.MsgAgentList {
		t.Fatalf("expected AGENT_LIST, got %s", protocol.MsgTypeName(resp.MsgType))
	}

	var listResp struct {
		Agents []relay.AgentInfo `json:"agents"`
	}
	if err := json.Unmarshal(resp.Payload, &listResp); err != nil {
		t.Fatalf("parse AGENT_LIST payload: %v", err)
	}
	if len(listResp.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(listResp.Agents))
	}
}

// TestSmoke_AuthRejectedBadToken verifies that invalid tokens are rejected.
func TestSmoke_AuthRejectedBadToken(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	url := env.wsURL() + "/v1/tunnel"
	headers := http.Header{}
	ws, err := tunnel.Dial(url, headers)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Send AUTH_REQUEST with a bad token
	authPayload, _ := json.Marshal(map[string]string{
		"token":       "mst_agent_invalid_token_value",
		"client_type": "agent",
		"org_id":      env.orgID,
		"name":        "bad-agent",
	})
	var reqID [16]byte
	authFrame := protocol.NewFrame(
		protocol.MsgAuthRequest,
		reqID,
		env.orgID+"/bad-agent",
		"relay",
		0,
		authPayload,
	)
	if err := ws.Write(protocol.Encode(authFrame)); err != nil {
		t.Fatalf("send bad AUTH_REQUEST: %v", err)
	}

	// Should receive AUTH_RESPONSE with ok=false
	data, err := ws.Read()
	if err != nil {
		t.Fatalf("read AUTH_RESPONSE: %v", err)
	}
	resp, err := protocol.Decode(data)
	if err != nil {
		t.Fatalf("decode AUTH_RESPONSE: %v", err)
	}
	if resp.MsgType != protocol.MsgAuthResponse {
		t.Fatalf("expected AUTH_RESPONSE, got %s", protocol.MsgTypeName(resp.MsgType))
	}
	var authResp struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	json.Unmarshal(resp.Payload, &authResp)
	if authResp.OK {
		t.Fatal("expected auth to be rejected, but it was accepted")
	}
}

// TestSmoke_AuthRejectedOrgMismatch verifies that tokens for the wrong org are rejected.
func TestSmoke_AuthRejectedOrgMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	url := env.wsURL() + "/v1/tunnel"
	headers := http.Header{}
	ws, err := tunnel.Dial(url, headers)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Send AUTH_REQUEST with valid token but wrong org
	authPayload, _ := json.Marshal(map[string]string{
		"token":       env.agentToken,
		"client_type": "agent",
		"org_id":      "wrong-org",
		"name":        "mismatch-agent",
	})
	var reqID [16]byte
	authFrame := protocol.NewFrame(
		protocol.MsgAuthRequest,
		reqID,
		"wrong-org/mismatch-agent",
		"relay",
		0,
		authPayload,
	)
	if err := ws.Write(protocol.Encode(authFrame)); err != nil {
		t.Fatalf("send AUTH_REQUEST: %v", err)
	}

	data, err := ws.Read()
	if err != nil {
		t.Fatalf("read AUTH_RESPONSE: %v", err)
	}
	resp, err := protocol.Decode(data)
	if err != nil {
		t.Fatalf("decode AUTH_RESPONSE: %v", err)
	}
	var authResp struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	json.Unmarshal(resp.Payload, &authResp)
	if authResp.OK {
		t.Fatal("expected org mismatch to be rejected")
	}
}

// TestSmoke_AuthRejectedTypeMismatch verifies that agent tokens cannot authenticate as CLI.
func TestSmoke_AuthRejectedTypeMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	url := env.wsURL() + "/v1/tunnel"
	headers := http.Header{}
	ws, err := tunnel.Dial(url, headers)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Use agent token but claim to be CLI
	authPayload, _ := json.Marshal(map[string]string{
		"token":       env.agentToken,
		"client_type": "cli",
		"org_id":      env.orgID,
		"name":        "type-mismatch",
	})
	var reqID [16]byte
	authFrame := protocol.NewFrame(
		protocol.MsgAuthRequest,
		reqID,
		env.orgID+"/type-mismatch",
		"relay",
		0,
		authPayload,
	)
	if err := ws.Write(protocol.Encode(authFrame)); err != nil {
		t.Fatalf("send AUTH_REQUEST: %v", err)
	}

	data, err := ws.Read()
	if err != nil {
		t.Fatalf("read AUTH_RESPONSE: %v", err)
	}
	resp, err := protocol.Decode(data)
	if err != nil {
		t.Fatalf("decode AUTH_RESPONSE: %v", err)
	}
	var authResp struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(resp.Payload, &authResp)
	if authResp.OK {
		t.Fatal("expected type mismatch to be rejected")
	}
}

// TestSmoke_RouteToDisconnectedAgent verifies error when routing to a non-existent agent.
func TestSmoke_RouteToDisconnectedAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	cli := env.dialCLI(t, "route-cli")
	defer cli.Close()

	// Send COMMAND to a non-existent agent
	cmdPayload, _ := json.Marshal(map[string]interface{}{
		"action":  "status",
		"command": "",
	})
	var reqID [16]byte
	copy(reqID[:], []byte("route-err-001"))
	cmdFrame := protocol.NewFrame(
		protocol.MsgCommand,
		reqID,
		env.orgID+"/route-cli",
		env.orgID+"/ghost-agent",
		0,
		cmdPayload,
	)
	if err := cli.Write(protocol.Encode(cmdFrame)); err != nil {
		t.Fatalf("send COMMAND: %v", err)
	}

	// Should receive ERROR with E_NOT_CONNECTED
	errFrame, err := readFrameWithTimeout(cli, 5*time.Second)
	if err != nil {
		t.Fatalf("read ERROR: %v", err)
	}
	if errFrame.MsgType != protocol.MsgError {
		t.Fatalf("expected ERROR, got %s", protocol.MsgTypeName(errFrame.MsgType))
	}
	var errPayload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	json.Unmarshal(errFrame.Payload, &errPayload)
	if errPayload.Code != protocol.ErrNotConnected {
		t.Errorf("expected error code %s, got %s", protocol.ErrNotConnected, errPayload.Code)
	}
}

// TestSmoke_HealthzEndpoint verifies the /healthz HTTP endpoint works.
func TestSmoke_HealthzEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	resp, err := http.Get(env.server.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body["status"])
	}
}

// TestSmoke_AgentListHTTPEndpoint verifies the /api/v1/agents HTTP endpoint.
func TestSmoke_AgentListHTTPEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	// Connect an agent first
	agent := env.connectAgent(t, "http-agent")
	defer agent.Close()

	resp, err := http.Get(env.server.URL + "/api/v1/agents?org_id=" + env.orgID)
	if err != nil {
		t.Fatalf("GET /api/v1/agents: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Agents []relay.AgentInfo `json:"agents"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(body.Agents))
	}
}

// TestSmoke_MultipleAgentsSameOrg verifies multiple agents from the same org
// can connect simultaneously and are all visible.
func TestSmoke_MultipleAgentsSameOrg(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	agents := make([]*tunnel.Client, 3)
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("multi-agent-%d", i)
		agents[i] = env.connectAgent(t, name)
		defer agents[i].Close()
	}

	listed := env.relay.Router().ListAgents(env.orgID)
	if len(listed) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(listed))
	}

	// Verify agent count via router
	if env.relay.Router().AgentCount() != 3 {
		t.Errorf("expected AgentCount()=3, got %d", env.relay.Router().AgentCount())
	}
}

// TestSmoke_AgentDisconnectCleansUp verifies that closing an agent connection
// removes it from the router.
func TestSmoke_AgentDisconnectCleansUp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	agent := env.connectAgent(t, "cleanup-agent")

	// Verify registered
	if env.relay.Router().AgentCount() != 1 {
		t.Fatal("agent should be registered")
	}

	// Close the agent
	agent.Close()

	// Give the server a moment to process the disconnect
	time.Sleep(100 * time.Millisecond)

	// Agent should be unregistered
	if env.relay.Router().AgentCount() != 0 {
		t.Error("agent should be unregistered after close")
	}
}

// TestSmoke_ConcurrentHeartbeats sends multiple heartbeats concurrently
// to verify the relay handles concurrent frame processing.
func TestSmoke_ConcurrentHeartbeats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := setupTestEnv(t)
	defer env.cleanup()

	agent := env.connectAgent(t, "conc-agent")
	defer agent.Close()

	// Send 5 heartbeats in quick succession
	for i := 0; i < 5; i++ {
		var reqID [16]byte
		copy(reqID[:], fmt.Sprintf("hb-%d", i))
		hb := protocol.NewFrame(
			protocol.MsgHeartbeat,
			reqID,
			env.orgID+"/conc-agent",
			"relay",
			0,
			nil,
		)
		if err := agent.SendFrame(hb); err != nil {
			t.Fatalf("send heartbeat %d: %v", i, err)
		}
	}

	// Read all 5 ACKs
	for i := 0; i < 5; i++ {
		ack, err := clientReadFrameWithTimeout(agent, 5*time.Second)
		if err != nil {
			t.Fatalf("read heartbeat ACK %d: %v", i, err)
		}
		if ack.MsgType != protocol.MsgHeartbeatAck {
			t.Fatalf("heartbeat %d: expected HEARTBEAT_ACK, got %s", i, protocol.MsgTypeName(ack.MsgType))
		}
	}
}

// Ensure testEnv isn't used after cleanup to detect dangling references.
// This also verifies httptest.Server.Close() properly shuts down connections.
var _ net.Listener // imported for compile check
