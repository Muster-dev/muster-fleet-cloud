package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/tunnel"
)

const (
	// HeartbeatInterval is how often the agent pings the relay.
	HeartbeatInterval = 30 * time.Second
	// ReadTimeout: if nothing (not even a heartbeat ack) arrives for this
	// long, the relay is considered gone and the agent reconnects.
	ReadTimeout = 95 * time.Second
	// MaxClockSkew bounds how old/new a command's timestamp may be.
	MaxClockSkew = 5 * time.Minute
	// MaxConcurrentCommands bounds parallel command execution.
	MaxConcurrentCommands = 4
)

// Agent is the muster-agent daemon.
type Agent struct {
	cfg        *config.AgentConfig
	configPath string
	keys       *crypto.KeyPair
	version    string
	client     *tunnel.Client
	executor   *Executor
	logf       func(string, ...any)

	sem    chan struct{}
	replay *replayCache
	trust  *TrustStore

	mu sync.Mutex
	// sessions maps a CLI connection name to what the relay told us about
	// it (SESSION_INFO). Cleared on every relay (re)connect: the relay
	// re-sends SESSION_INFO to a new connection.
	sessions map[string]protocol.SessionInfo
	// running tracks in-flight commands so COMMAND_CANCEL / SESSION_CLOSED
	// can stop them.
	running map[runKey]*runningCmd
	// connectedSince is when the current relay connection was registered
	// (for the state file); nil while disconnected.
	connectedSince *time.Time
}

type runKey struct {
	cli   string
	reqID [16]byte
}

type runningCmd struct {
	cancel context.CancelFunc
}

// Options configures an Agent.
type Options struct {
	// ConfigPath is where the config is persisted when the relay issues a
	// node token. Required for enrollment.
	ConfigPath string
	Version    string
	Logf       func(string, ...any)
}

// New creates a new Agent from config.
func New(cfg *config.AgentConfig, keys *crypto.KeyPair, opts Options) (*Agent, error) {
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if !config.ValidCLIKeyPolicy(cfg.CLIKeyPolicy) {
		return nil, fmt.Errorf("invalid cli_key_policy %q (tofu, strict or off)", cfg.CLIKeyPolicy)
	}
	client, err := tunnel.NewClient(cfg.Relay.URL, cfg.Relay.Token, tunnel.Options{
		ClientType: protocol.ClientAgent,
		Name:       cfg.Identity.Name,
		OrgID:      cfg.Identity.OrgID,
		Version:    opts.Version,
		Logf:       opts.Logf,
	})
	if err != nil {
		return nil, err
	}
	return &Agent{
		cfg:        cfg,
		configPath: opts.ConfigPath,
		keys:       keys,
		version:    opts.Version,
		client:     client,
		logf:       opts.Logf,
		executor: NewExecutor(ExecutorOptions{
			MusterPath:      cfg.MusterPath,
			AllowedCommands: cfg.AllowedCommands,
			DefaultCwd:      cfg.Project.Dir,
			AllowShell:      cfg.AllowShell,
			AllowPush:       cfg.Project.Mode == "push",
		}),
		sem:      make(chan struct{}, MaxConcurrentCommands),
		replay:   newReplayCache(2 * MaxClockSkew),
		trust:    NewTrustStore(trustPath(opts.ConfigPath)),
		sessions: make(map[string]protocol.SessionInfo),
		running:  make(map[runKey]*runningCmd),
	}, nil
}

func trustPath(configPath string) string {
	if configPath == "" {
		return ""
	}
	return config.TrustedCLIsPath(configPath)
}

// Trust returns the agent's CLI key trust store.
func (a *Agent) Trust() *TrustStore { return a.trust }

// Run starts the agent with automatic reconnection. It returns when ctx is
// cancelled or the relay permanently rejects the credential (revoked).
func (a *Agent) Run(ctx context.Context) error {
	rcfg := tunnel.DefaultReconnectConfig()
	if d, err := time.ParseDuration(a.cfg.ReconnectBaseDelay); err == nil && d > 0 {
		rcfg.BaseDelay = d
	}
	if d, err := time.ParseDuration(a.cfg.ReconnectMaxDelay); err == nil && d > 0 {
		rcfg.MaxDelay = d
	}
	err := a.client.ConnectLoop(ctx, rcfg, a.onAuth, a.session)
	if tunnel.IsAuthError(err) {
		return fmt.Errorf("%w (the node credential may have been revoked; re-join with a new join token)", err)
	}
	return err
}

// Enroll connects once, authenticates (exchanging a join token for a node
// token if needed), registers, persists the config and disconnects.
func (a *Agent) Enroll(ctx context.Context) (*protocol.AuthResponse, error) {
	dctx, cancel := context.WithTimeout(ctx, tunnel.DefaultDialTimeout)
	defer cancel()
	if err := a.client.ConnectContext(dctx); err != nil {
		return nil, err
	}
	defer a.client.Close()
	resp, err := a.client.Authenticate()
	if err != nil {
		return nil, err
	}
	if err := a.onAuth(resp); err != nil {
		return nil, err
	}
	if err := a.register(); err != nil {
		return nil, err
	}
	return resp, nil
}

// onAuth persists the identity and node credential the relay returned.
// It runs before any reconnect could happen, so the (now used) join token is
// never presented again.
func (a *Agent) onAuth(resp *protocol.AuthResponse) error {
	changed := false
	if resp.NodeToken != "" {
		a.cfg.Relay.Token = resp.NodeToken
		a.client.SetToken(resp.NodeToken)
		changed = true
		a.logf("enrolled with relay as %s (node credential saved)", resp.Name)
	}
	if resp.OrgID != "" && resp.OrgID != a.cfg.Identity.OrgID {
		a.cfg.Identity.OrgID = resp.OrgID
		changed = true
	}
	if resp.Name != "" && resp.Name != a.cfg.Identity.Name {
		a.cfg.Identity.Name = resp.Name
		changed = true
	}
	if !changed {
		return nil
	}
	if a.configPath == "" {
		if resp.NodeToken != "" {
			return errors.New("relay issued a node token but no config path is set to persist it")
		}
		return nil
	}
	if err := config.SaveAgentConfigTo(a.configPath, a.cfg); err != nil {
		return fmt.Errorf("persist node credential: %w", err)
	}
	return nil
}

// Hello builds the AGENT_HELLO payload.
func (a *Agent) Hello() protocol.AgentHello {
	host, _ := os.Hostname()
	mode := a.cfg.Project.Mode
	if mode == "" {
		mode = "muster"
	}
	projectDir := false
	if a.cfg.Project.Dir != "" {
		if st, err := os.Stat(a.cfg.Project.Dir); err == nil && st.IsDir() {
			projectDir = true
		}
	}
	features := []string{protocol.FeatureCancel, protocol.FeatureCallerEnv}
	if a.cfg.CLIKeyPolicy != config.CLIKeyPolicyOff {
		features = append(features, protocol.FeatureCLIKeyPinning)
	}
	return protocol.AgentHello{
		AgentName:       a.client.Identity(),
		Version:         a.version,
		PublicKey:       a.keys.PublicKeyBase64(),
		Hostname:        host,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Mode:            mode,
		ProjectDir:      projectDir,
		AllowedCommands: append([]string(nil), a.cfg.AllowedCommands...),
		AllowShell:      a.cfg.AllowShell,
		Features:        features,
	}
}

func (a *Agent) register() error {
	hello, err := json.Marshal(a.Hello())
	if err != nil {
		return err
	}
	f := protocol.NewFrame(protocol.MsgAgentHello, tunnel.NewRequestID(), a.client.Identity(), protocol.RelayID, 0, hello)
	if err := a.client.SendFrame(f); err != nil {
		return fmt.Errorf("send AGENT_HELLO: %w", err)
	}
	a.client.SetReadTimeout(15 * time.Second)
	ack, err := a.client.ReadFrame()
	a.client.SetReadTimeout(0)
	if err != nil {
		return fmt.Errorf("read RELAY_ACK: %w", err)
	}
	if ack.MsgType != protocol.MsgRelayAck {
		return fmt.Errorf("expected RELAY_ACK, got %s", protocol.MsgTypeName(ack.MsgType))
	}
	return nil
}

func (a *Agent) session(ctx context.Context) error {
	a.mu.Lock()
	a.sessions = make(map[string]protocol.SessionInfo)
	a.mu.Unlock()
	if err := a.register(); err != nil {
		return err
	}
	a.logf("agent %s registered with relay %s", a.client.Identity(), a.client.URLs().WS)
	a.writeState(true)
	defer a.writeState(false)

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.client.Heartbeat(sctx, HeartbeatInterval)
	go func() {
		<-sctx.Done()
		if ctx.Err() != nil {
			a.client.Close() // shutdown: unblock ReadFrame (no reconnect follows)
		}
	}()
	return a.messageLoop(sctx, ctx)
}

// messageLoop reads frames until the connection fails. Commands run under
// runCtx (the agent's lifetime), not the connection's: a relay blip must not
// kill a deploy halfway through.
func (a *Agent) messageLoop(ctx, runCtx context.Context) error {
	for {
		a.client.SetReadTimeout(ReadTimeout)
		frame, err := a.client.ReadFrame()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		src := protocol.ParseID(frame.SourceID)

		switch frame.MsgType {
		case protocol.MsgHeartbeat:
			ack := protocol.NewFrame(protocol.MsgHeartbeatAck, frame.RequestID, a.client.Identity(), src, 0, nil)
			if err := a.client.SendFrame(ack); err != nil {
				return err
			}

		case protocol.MsgHeartbeatAck:
			a.writeState(true) // keepalive response; refresh the state file

		case protocol.MsgRelayAck:
			// keepalive response

		case protocol.MsgCommand:
			a.startCommand(runCtx, src, frame)

		case protocol.MsgCommandCancel:
			if a.cancelCommand(runKey{src, frame.RequestID}) {
				a.logf("command from %s cancelled by the CLI", src)
			}

		case protocol.MsgSessionInfo:
			if src != protocol.RelayID {
				continue // only the relay may describe sessions
			}
			var si protocol.SessionInfo
			if err := json.Unmarshal(frame.Payload, &si); err != nil || si.CLI == "" {
				a.logf("malformed SESSION_INFO ignored")
				continue
			}
			si.TokenName = cleanLabel(si.TokenName)
			a.mu.Lock()
			a.sessions[si.CLI] = si
			a.mu.Unlock()

		case protocol.MsgSessionClosed:
			if src != protocol.RelayID {
				continue
			}
			var sc protocol.SessionClosed
			if err := json.Unmarshal(frame.Payload, &sc); err != nil || sc.CLI == "" {
				continue
			}
			if n := a.closeSession(sc.CLI); n > 0 {
				a.logf("CLI %s disconnected: cancelled %d running command(s)", sc.CLI, n)
			}

		case protocol.MsgKeyExchange:
			a.handleKeyExchange(frame)

		case protocol.MsgError:
			var ep protocol.ErrorPayload
			_ = json.Unmarshal(frame.Payload, &ep)
			a.logf("relay error: %s %s", ep.Code, ep.Message)

		default:
			a.logf("unhandled message type: %s", protocol.MsgTypeName(frame.MsgType))
		}
	}
}

// responder sends (encrypted, when the request was) replies to one CLI.
type responder struct {
	a     *Agent
	reqID [16]byte
	dest  string
	peer  *[32]byte // CLI public key; nil → plaintext replies
}

func (r *responder) send(msgType uint8, flags uint8, v any) {
	var payload []byte
	if v != nil {
		raw, err := json.Marshal(v)
		if err != nil {
			return
		}
		payload = raw
		if r.peer != nil {
			sealed, err := crypto.Seal(raw, r.peer, r.a.keys)
			if err != nil {
				r.a.logf("encrypt reply: %v", err)
				return
			}
			payload = sealed
			flags |= protocol.FlagEncrypted
		}
	}
	f := protocol.NewFrame(msgType, r.reqID, r.a.client.Identity(), r.dest, flags, payload)
	if err := r.a.client.SendFrame(f); err != nil {
		r.a.logf("send %s to %s: %v", protocol.MsgTypeName(msgType), r.dest, err)
	}
}

func (r *responder) fail(code, msg string) {
	r.send(protocol.MsgCommandError, 0, protocol.ErrorPayload{Code: code, Message: msg})
}

// startCommand registers a COMMAND under (CLI, request ID) before handing it
// to a goroutine, so a COMMAND_CANCEL that follows immediately finds it.
func (a *Agent) startCommand(runCtx context.Context, src string, frame *protocol.Frame) {
	k := runKey{src, frame.RequestID}
	ctx, cancel := context.WithCancel(runCtx)
	a.mu.Lock()
	if _, dup := a.running[k]; dup {
		a.mu.Unlock()
		cancel()
		r := &responder{a: a, reqID: frame.RequestID, dest: src}
		r.fail(protocol.ErrCommandRejected, "duplicate request ID")
		return
	}
	rc := &runningCmd{cancel: cancel}
	a.running[k] = rc
	info, known := a.sessions[src]
	a.mu.Unlock()
	if !known {
		info = protocol.SessionInfo{CLI: src}
	}
	go func() {
		defer func() {
			cancel()
			a.mu.Lock()
			if a.running[k] == rc {
				delete(a.running, k)
			}
			a.mu.Unlock()
		}()
		a.handleCommand(ctx, frame, info)
	}()
}

// cancelCommand cancels one running command. It reports whether one was found.
func (a *Agent) cancelCommand(k runKey) bool {
	a.mu.Lock()
	rc, ok := a.running[k]
	a.mu.Unlock()
	if ok {
		rc.cancel()
	}
	return ok
}

// closeSession forgets a CLI session and cancels all its running commands.
func (a *Agent) closeSession(cli string) int {
	a.mu.Lock()
	delete(a.sessions, cli)
	var victims []*runningCmd
	for k, rc := range a.running {
		if k.cli == cli {
			victims = append(victims, rc)
		}
	}
	a.mu.Unlock()
	for _, rc := range victims {
		rc.cancel()
	}
	return len(victims)
}

// RunningCount returns the number of in-flight commands.
func (a *Agent) RunningCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.running)
}

// CallerEnv returns the agent-controlled environment for a command from the
// CLI session described by info. Values come from the relay's SESSION_INFO,
// never from the command itself.
func CallerEnv(info protocol.SessionInfo) map[string]string {
	env := map[string]string{
		protocol.EnvViaAgent:     "1",
		protocol.EnvCloudCaller:  info.TokenName,
		protocol.EnvCallerID:     info.TokenID,
		protocol.EnvDeploySource: "cloud",
	}
	if info.TokenName != "" {
		env[protocol.EnvDeploySource] = "cloud:" + info.TokenName
	} else if info.TokenID != "" {
		env[protocol.EnvCloudCaller] = info.TokenID
		env[protocol.EnvDeploySource] = "cloud:" + info.TokenID
	}
	return env
}

// cleanLabel keeps a relay-supplied display name printable and short.
func cleanLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

func (a *Agent) handleCommand(ctx context.Context, frame *protocol.Frame, info protocol.SessionInfo) {
	r := &responder{a: a, reqID: frame.RequestID, dest: protocol.ParseID(frame.SourceID)}

	var plaintext []byte
	if frame.IsEncrypted() {
		senderPub, pt, err := crypto.Open(frame.Payload, a.keys)
		if err != nil {
			a.logf("decrypt failed from %s: %v", r.dest, err)
			r.fail(protocol.ErrDecrypt, "decryption failed")
			return
		}
		nonce, _ := crypto.SealNonce(frame.Payload)
		if !a.replay.add(nonce) {
			a.logf("replayed command from %s rejected", r.dest)
			r.fail(protocol.ErrCommandRejected, "replayed command")
			return
		}
		r.peer = &senderPub
		plaintext = pt
	} else {
		if !a.cfg.AllowPlaintext {
			r.fail(protocol.ErrDecrypt, "agent requires end-to-end encrypted commands")
			return
		}
		plaintext = frame.Payload
	}

	req, err := ParseCommandRequest(plaintext)
	if err != nil {
		r.fail(protocol.ErrCommandRejected, "malformed command")
		return
	}
	if r.peer != nil {
		skew := time.Since(time.Unix(req.Timestamp, 0))
		if skew > MaxClockSkew || skew < -MaxClockSkew {
			r.fail(protocol.ErrCommandRejected, "command timestamp outside allowed clock skew")
			return
		}
	}

	if r.peer != nil {
		switch d, err := a.trust.Check(a.cfg.CLIKeyPolicy, info.TokenID, info.TokenName, *r.peer, req.PersistentKey); {
		case err != nil:
			a.logf("command from %s rejected: %v", r.dest, err)
			r.fail(protocol.ErrCommandRejected, err.Error())
			return
		case d == TrustPinned:
			a.logf("pinned CLI key %s for token %s (%s)", crypto.Fingerprint(*r.peer), info.TokenID, info.TokenName)
		}
	} else if a.cfg.CLIKeyPolicy == config.CLIKeyPolicyStrict {
		r.fail(protocol.ErrCommandRejected, "plaintext commands are not allowed with cli_key_policy strict")
		return
	}

	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	default:
		r.fail(protocol.ErrAgentBusy, "agent is busy")
		return
	}

	outputCh, err := a.executor.Execute(ctx, req, CallerEnv(info))
	if errors.Is(err, context.Canceled) {
		r.send(protocol.MsgCommandResult, 0, protocol.CommandResult{ExitCode: protocol.ExitCancelled, Status: protocol.StatusCancelled})
		return
	}
	if err != nil {
		a.logf("command from %s rejected: %v", r.dest, err)
		r.fail(protocol.ErrCommandRejected, err.Error())
		return
	}
	caller := info.TokenName
	if caller == "" {
		caller = "unknown caller"
	}
	a.logf("running %s command for %s (%s)", req.Action, r.dest, caller)
	r.send(protocol.MsgCommandAck, 0, nil)

	for line := range outputCh {
		if line.Done {
			res := protocol.CommandResult{ExitCode: line.ExitCode, Status: protocol.StatusDone}
			if line.Cancelled {
				res.Status = protocol.StatusCancelled
				a.logf("command for %s stopped (process group terminated), exit %d", r.dest, line.ExitCode)
			}
			r.send(protocol.MsgCommandResult, 0, res)
			continue
		}
		r.send(protocol.MsgStreamData, protocol.FlagStreamContinued, protocol.StreamLine{Line: line.Text, Stream: line.Stream})
	}
}

func (a *Agent) handleKeyExchange(frame *protocol.Frame) {
	src := protocol.ParseID(frame.SourceID)
	payload, _ := json.Marshal(protocol.KeyExchange{PublicKey: a.keys.PublicKeyBase64()})
	ack := protocol.NewFrame(protocol.MsgKeyExchangeAck, frame.RequestID, a.client.Identity(), src, 0, payload)
	if err := a.client.SendFrame(ack); err != nil {
		a.logf("send KEY_EXCHANGE_ACK: %v", err)
	}
}

// replayCache remembers recently seen encryption nonces.
type replayCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[[crypto.NonceSize]byte]time.Time
}

func newReplayCache(ttl time.Duration) *replayCache {
	return &replayCache{ttl: ttl, seen: make(map[[crypto.NonceSize]byte]time.Time)}
}

// add records nonce and reports whether it was new.
func (c *replayCache) add(n [crypto.NonceSize]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if len(c.seen) > 1024 {
		for k, t := range c.seen {
			if now.Sub(t) > c.ttl {
				delete(c.seen, k)
			}
		}
	}
	if t, ok := c.seen[n]; ok && now.Sub(t) <= c.ttl {
		return false
	}
	c.seen[n] = now
	return true
}

// IsJoinToken reports whether tok is a one-time join token.
func IsJoinToken(tok string) bool { return strings.HasPrefix(tok, "mst_agent_") }
