//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/tunnel"
)

// fakeRelay accepts one agent connection, completes AUTH/HELLO and then
// hands the socket to the test, which plays relay + CLI.
type fakeRelay struct {
	t     *testing.T
	srv   *httptest.Server
	conns chan *tunnel.WSConn
	hello chan protocol.AgentHello
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	fr := &fakeRelay{t: t, conns: make(chan *tunnel.WSConn, 1), hello: make(chan protocol.AgentHello, 1)}
	fr.srv = httptest.NewServer(tunnel.AcceptHandler(func(ws *tunnel.WSConn) {
		f := readFrame(t, ws)
		if f.MsgType != protocol.MsgAuthRequest {
			t.Errorf("first frame %s", protocol.MsgTypeName(f.MsgType))
			return
		}
		p, _ := json.Marshal(protocol.AuthResponse{OK: true, OrgID: "org_t", Name: "prod-1", SessionID: "sess_x"})
		_ = ws.Write(protocol.Encode(protocol.NewFrame(protocol.MsgAuthResponse, f.RequestID, protocol.RelayID, "prod-1", 0, p)))
		f = readFrame(t, ws)
		var h protocol.AgentHello
		_ = json.Unmarshal(f.Payload, &h)
		fr.hello <- h
		_ = ws.Write(protocol.Encode(protocol.NewFrame(protocol.MsgRelayAck, f.RequestID, protocol.RelayID, "prod-1", 0, nil)))
		fr.conns <- ws
		<-ws.Done()
	}))
	t.Cleanup(fr.srv.Close)
	return fr
}

func readFrame(t *testing.T, ws *tunnel.WSConn) *protocol.Frame {
	t.Helper()
	_ = ws.SetReadTimeout(10 * time.Second)
	data, err := ws.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	f, err := protocol.Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return f
}

type harness struct {
	t        *testing.T
	ws       *tunnel.WSConn
	agentPub [32]byte
	cli      *crypto.KeyPair
	hello    protocol.AgentHello
	dir      string
	agent    *Agent
}

// fakeMuster is a stand-in muster binary. "muster env" prints the caller
// variables; "muster status" starts a background child (recording its PID),
// prints "started" and waits.
const fakeMuster = `#!/bin/sh
case "$1" in
env)
  echo "via=$MUSTER_VIA_AGENT caller=$MUSTER_CLOUD_CALLER id=$MUSTER_CLOUD_CALLER_ID source=$MUSTER_DEPLOY_SOURCE extra=$EXTRA"
  ;;
status)
  sleep 300 &
  echo $! > "$PIDFILE"
  echo started
  wait
  ;;
esac
`

func startAgent(t *testing.T, mut func(*config.AgentConfig)) *harness {
	t.Helper()
	fr := newFakeRelay(t)
	dir := t.TempDir()
	muster := filepath.Join(dir, "muster")
	if err := os.WriteFile(muster, []byte(fakeMuster), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIDFILE", filepath.Join(dir, "child.pid"))
	cfg := config.DefaultAgentConfig()
	cfg.Relay.URL = "ws" + strings.TrimPrefix(fr.srv.URL, "http")
	cfg.Relay.Token = "mst_node_x"
	cfg.Identity.Name = "prod-1"
	cfg.Project.Dir = dir
	cfg.Project.Mode = "muster"
	cfg.MusterPath = muster
	cfg.AllowedCommands = []string{"muster env", "muster status"}
	if mut != nil {
		mut(cfg)
	}
	keys, _ := crypto.GenerateKeyPair()
	a, err := New(cfg, keys, Options{ConfigPath: filepath.Join(dir, "config.json"), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	var ws *tunnel.WSConn
	select {
	case ws = <-fr.conns:
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not connect")
	}
	cli, _ := crypto.GenerateKeyPair()
	return &harness{t: t, ws: ws, agentPub: keys.PublicKey, cli: cli, hello: <-fr.hello, dir: dir, agent: a}
}

func (h *harness) sendJSON(msgType uint8, reqID [16]byte, src string, v any) {
	p, _ := json.Marshal(v)
	if err := h.ws.Write(protocol.Encode(protocol.NewFrame(msgType, reqID, src, "prod-1", 0, p))); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) command(src string, req protocol.CommandRequest) [16]byte {
	req.Timestamp = time.Now().Unix()
	plain, _ := json.Marshal(req)
	sealed, err := crypto.Seal(plain, &h.agentPub, h.cli)
	if err != nil {
		h.t.Fatal(err)
	}
	id := tunnel.NewRequestID()
	if err := h.ws.Write(protocol.Encode(protocol.NewFrame(protocol.MsgCommand, id, src, "prod-1", protocol.FlagEncrypted, sealed))); err != nil {
		h.t.Fatal(err)
	}
	return id
}

// next returns the next reply for reqID (skipping heartbeats), decrypted.
func (h *harness) next(reqID [16]byte) (uint8, []byte) {
	for {
		f := readFrame(h.t, h.ws)
		if f.RequestID != reqID {
			continue
		}
		if !f.IsEncrypted() {
			return f.MsgType, f.Payload
		}
		_, pt, err := crypto.Open(f.Payload, h.cli)
		if err != nil {
			h.t.Fatalf("open %s: %v", protocol.MsgTypeName(f.MsgType), err)
		}
		return f.MsgType, pt
	}
}

// collect reads until COMMAND_RESULT or COMMAND_ERROR.
func (h *harness) collect(reqID [16]byte) (lines []string, res protocol.CommandResult, errp *protocol.ErrorPayload) {
	for {
		typ, pt := h.next(reqID)
		switch typ {
		case protocol.MsgStreamData:
			var l protocol.StreamLine
			_ = json.Unmarshal(pt, &l)
			lines = append(lines, l.Line)
		case protocol.MsgCommandResult:
			_ = json.Unmarshal(pt, &res)
			return
		case protocol.MsgCommandError:
			errp = &protocol.ErrorPayload{}
			_ = json.Unmarshal(pt, errp)
			return
		}
	}
}

// waitStarted reads until the "started" line of the fake status command and
// returns the background child's PID.
func (h *harness) waitStarted(reqID [16]byte) int {
	for {
		typ, pt := h.next(reqID)
		switch typ {
		case protocol.MsgStreamData:
			var l protocol.StreamLine
			_ = json.Unmarshal(pt, &l)
			if l.Line == "started" {
				raw, err := os.ReadFile(filepath.Join(h.dir, "child.pid"))
				if err != nil {
					h.t.Fatal(err)
				}
				pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
				return pid
			}
		case protocol.MsgCommandResult, protocol.MsgCommandError:
			h.t.Fatalf("command ended early: %s %s", protocol.MsgTypeName(typ), pt)
		}
	}
}

func processGone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestAgentHelloAdvertisesModeAndAllowlist(t *testing.T) {
	h := startAgent(t, nil)
	if h.hello.Mode != "muster" || !h.hello.ProjectDir {
		t.Fatalf("mode/project_dir: %+v", h.hello)
	}
	if strings.Join(h.hello.AllowedCommands, ",") != "muster env,muster status" {
		t.Fatalf("allowlist %v", h.hello.AllowedCommands)
	}
	feats := strings.Join(h.hello.Features, ",")
	for _, f := range []string{protocol.FeatureCancel, protocol.FeatureCallerEnv, protocol.FeatureCLIKeyPinning} {
		if !strings.Contains(feats, f) {
			t.Errorf("feature %s missing: %v", f, h.hello.Features)
		}
	}
	raw, _ := json.Marshal(h.hello)
	if strings.Contains(string(raw), h.dir) {
		t.Fatal("hello leaks the project path")
	}

	h2 := startAgent(t, func(c *config.AgentConfig) { c.Project.Dir = "/does/not/exist"; c.Project.Mode = "push" })
	if h2.hello.Mode != "push" || h2.hello.ProjectDir {
		t.Fatalf("push hello: %+v", h2.hello)
	}
}

func TestAgentCallerEnvFromRelayNotCLI(t *testing.T) {
	h := startAgent(t, nil)
	h.sendJSON(protocol.MsgSessionInfo, tunnel.NewRequestID(), protocol.RelayID,
		protocol.SessionInfo{CLI: "cli-aaaa1111", TokenID: "tok_real", TokenName: "ricky laptop"})
	// A CLI trying to describe itself is ignored (only the relay may).
	h.sendJSON(protocol.MsgSessionInfo, tunnel.NewRequestID(), "cli-aaaa1111",
		protocol.SessionInfo{CLI: "cli-aaaa1111", TokenID: "tok_evil", TokenName: "evil"})

	id := h.command("cli-aaaa1111", protocol.CommandRequest{Action: "exec", Command: "muster env", Env: map[string]string{
		"MUSTER_CLOUD_CALLER": "spoofed", "MUSTER_DEPLOY_SOURCE": "spoofed", "MUSTER_VIA_AGENT": "0", "MUSTER_CLOUD_CALLER_ID": "spoofed", "EXTRA": "ok",
	}})
	lines, res, errp := h.collect(id)
	if errp != nil {
		t.Fatalf("error: %+v", errp)
	}
	want := "via=1 caller=ricky laptop id=tok_real source=cloud:ricky laptop extra=ok"
	if res.ExitCode != 0 || len(lines) != 1 || lines[0] != want {
		t.Fatalf("got %q exit %d, want %q", lines, res.ExitCode, want)
	}

	// Unknown session (relay without SESSION_INFO support): no caller.
	id = h.command("cli-bbbb2222", protocol.CommandRequest{Action: "exec", Command: "muster env", Env: map[string]string{"MUSTER_CLOUD_CALLER": "spoofed"}})
	lines, _, _ = h.collect(id)
	if len(lines) != 1 || lines[0] != "via=1 caller= id= source=cloud extra=" {
		t.Fatalf("unknown session: %q", lines)
	}
}

func TestAgentCommandCancelKillsProcessGroup(t *testing.T) {
	old := KillGrace
	KillGrace = time.Second
	t.Cleanup(func() { KillGrace = old })
	h := startAgent(t, nil)
	id := h.command("cli-cccc3333", protocol.CommandRequest{Action: "exec", Command: "muster status"})
	child := h.waitStarted(id)

	// A cancel from a different CLI for the same request ID is ignored.
	h.sendJSON(protocol.MsgCommandCancel, id, "cli-other000", nil)
	time.Sleep(200 * time.Millisecond)
	if h.agent.RunningCount() != 1 {
		t.Fatal("foreign cancel stopped the command")
	}

	if err := h.ws.Write(protocol.Encode(protocol.NewFrame(protocol.MsgCommandCancel, id, "cli-cccc3333", "prod-1", 0, nil))); err != nil {
		t.Fatal(err)
	}
	_, res, errp := h.collect(id)
	if errp != nil {
		t.Fatalf("error: %+v", errp)
	}
	if res.ExitCode != 130 || res.Status != protocol.StatusCancelled {
		t.Fatalf("result %+v", res)
	}
	if !processGone(child) {
		t.Fatalf("background child %d survived the cancel", child)
	}
	if n := h.agent.RunningCount(); n != 0 {
		t.Fatalf("%d commands still tracked", n)
	}
}

func TestAgentSessionClosedCancels(t *testing.T) {
	h := startAgent(t, nil)
	h.sendJSON(protocol.MsgSessionInfo, tunnel.NewRequestID(), protocol.RelayID, protocol.SessionInfo{CLI: "cli-dddd4444", TokenID: "tok_d"})
	id := h.command("cli-dddd4444", protocol.CommandRequest{Action: "exec", Command: "muster status"})
	child := h.waitStarted(id)

	// Forged SESSION_CLOSED from a CLI is ignored.
	h.sendJSON(protocol.MsgSessionClosed, tunnel.NewRequestID(), "cli-dddd4444", protocol.SessionClosed{CLI: "cli-dddd4444"})
	time.Sleep(200 * time.Millisecond)
	if h.agent.RunningCount() != 1 {
		t.Fatal("SESSION_CLOSED from a non-relay source was honoured")
	}

	h.sendJSON(protocol.MsgSessionClosed, tunnel.NewRequestID(), protocol.RelayID, protocol.SessionClosed{CLI: "cli-dddd4444"})
	_, res, _ := h.collect(id)
	if res.ExitCode != 130 || res.Status != protocol.StatusCancelled {
		t.Fatalf("result %+v", res)
	}
	if !processGone(child) {
		t.Fatalf("background child %d survived SESSION_CLOSED", child)
	}
}

func TestAgentPinsCLIKeyPerToken(t *testing.T) {
	h := startAgent(t, nil)
	h.sendJSON(protocol.MsgSessionInfo, tunnel.NewRequestID(), protocol.RelayID, protocol.SessionInfo{CLI: "cli-eeee5555", TokenID: "tok_e", TokenName: "e"})
	id := h.command("cli-eeee5555", protocol.CommandRequest{Action: "exec", Command: "muster env", PersistentKey: true})
	if _, res, errp := h.collect(id); errp != nil || res.ExitCode != 0 {
		t.Fatalf("first command: %+v %+v", res, errp)
	}

	// Same token, different key (e.g. a relay impersonating the CLI): denied.
	h.cli, _ = crypto.GenerateKeyPair()
	h.sendJSON(protocol.MsgSessionInfo, tunnel.NewRequestID(), protocol.RelayID, protocol.SessionInfo{CLI: "cli-ffff6666", TokenID: "tok_e", TokenName: "e"})
	for _, persistent := range []bool{true, false} {
		id = h.command("cli-ffff6666", protocol.CommandRequest{Action: "exec", Command: "muster env", PersistentKey: persistent})
		_, _, errp := h.collect(id)
		if errp == nil || errp.Code != protocol.ErrCommandRejected || !strings.Contains(errp.Message, "changed") {
			t.Fatalf("key change (persistent=%v) not rejected: %+v", persistent, errp)
		}
	}

	// trust reset (another process) → the new key is pinned.
	if n, err := NewTrustStore(config.TrustedCLIsPath(filepath.Join(h.dir, "config.json"))).Reset("tok_e"); err != nil || n != 1 {
		t.Fatalf("reset: %d %v", n, err)
	}
	id = h.command("cli-ffff6666", protocol.CommandRequest{Action: "exec", Command: "muster env", PersistentKey: true})
	if _, _, errp := h.collect(id); errp != nil {
		t.Fatalf("after reset: %+v", errp)
	}
}

func TestAgentStrictPolicy(t *testing.T) {
	h := startAgent(t, func(c *config.AgentConfig) { c.CLIKeyPolicy = config.CLIKeyPolicyStrict })
	h.sendJSON(protocol.MsgSessionInfo, tunnel.NewRequestID(), protocol.RelayID, protocol.SessionInfo{CLI: "cli-gggg7777", TokenID: "tok_g"})
	id := h.command("cli-gggg7777", protocol.CommandRequest{Action: "exec", Command: "muster env", PersistentKey: true})
	if _, _, errp := h.collect(id); errp == nil || !strings.Contains(errp.Message, "not trusted") {
		t.Fatalf("unknown key accepted under strict: %+v", errp)
	}
	if err := h.agent.Trust().Add("tok_g", "g", h.cli.PublicKey); err != nil {
		t.Fatal(err)
	}
	id = h.command("cli-gggg7777", protocol.CommandRequest{Action: "exec", Command: "muster env", PersistentKey: true})
	if _, _, errp := h.collect(id); errp != nil {
		t.Fatalf("pinned key rejected: %+v", errp)
	}
	// No SESSION_INFO → no token ID → rejected under strict.
	id = h.command("cli-hhhh8888", protocol.CommandRequest{Action: "exec", Command: "muster env", PersistentKey: true})
	if _, _, errp := h.collect(id); errp == nil {
		t.Fatal("unidentified caller accepted under strict")
	}
}

func TestExecutorCancelEscalatesToSIGKILL(t *testing.T) {
	old := KillGrace
	KillGrace = 300 * time.Millisecond
	t.Cleanup(func() { KillGrace = old })
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "pid")
	e := newTestExecutor(t, ExecutorOptions{AllowPush: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Leader and child both ignore SIGTERM, so only SIGKILL stops them.
	script := "trap '' TERM\n(trap '' TERM; while :; do sleep 1; done) &\necho $! > " + pidfile + "\necho started\nwhile :; do sleep 1; done\n"
	ch, err := e.Execute(ctx, &protocol.CommandRequest{Action: "push_hook", Command: script}, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Time{}
	for l := range ch {
		if l.Text == "started" {
			start = time.Now()
			cancel()
		}
		if l.Done {
			if !l.Cancelled || l.ExitCode != 130 {
				t.Fatalf("done line %+v", l)
			}
			if el := time.Since(start); el < KillGrace || el > 5*time.Second {
				t.Errorf("stopped after %v (want ≥ KillGrace %v)", el, KillGrace)
			}
		}
	}
	raw, _ := os.ReadFile(pidfile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid == 0 || !processGone(pid) {
		t.Fatalf("TERM-ignoring child %d survived", pid)
	}
}
