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
		sem:    make(chan struct{}, MaxConcurrentCommands),
		replay: newReplayCache(2 * MaxClockSkew),
	}, nil
}

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

func (a *Agent) register() error {
	host, _ := os.Hostname()
	hello, err := json.Marshal(protocol.AgentHello{
		AgentName: a.client.Identity(),
		Version:   a.version,
		PublicKey: a.keys.PublicKeyBase64(),
		Hostname:  host,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	})
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
	if err := a.register(); err != nil {
		return err
	}
	a.logf("agent %s registered with relay %s", a.client.Identity(), a.client.URLs().WS)

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

		case protocol.MsgHeartbeatAck, protocol.MsgRelayAck:
			// keepalive response

		case protocol.MsgCommand:
			go a.handleCommand(runCtx, frame)

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

func (a *Agent) handleCommand(ctx context.Context, frame *protocol.Frame) {
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

	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	default:
		r.fail(protocol.ErrAgentBusy, "agent is busy")
		return
	}

	outputCh, err := a.executor.Execute(ctx, req)
	if err != nil {
		a.logf("command from %s rejected: %v", r.dest, err)
		r.fail(protocol.ErrCommandRejected, err.Error())
		return
	}
	a.logf("running %s command for %s", req.Action, r.dest)
	r.send(protocol.MsgCommandAck, 0, nil)

	for line := range outputCh {
		if line.Done {
			r.send(protocol.MsgCommandResult, 0, protocol.CommandResult{ExitCode: line.ExitCode, Status: "done"})
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
