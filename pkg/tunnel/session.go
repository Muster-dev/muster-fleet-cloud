package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
)

// RemoteError is an ERROR or COMMAND_ERROR returned by the relay or agent.
type RemoteError struct {
	Code    string
	Message string
}

func (e *RemoteError) Error() string { return e.Code + ": " + e.Message }

// Session is an authenticated CLI connection to the relay. Each session uses
// a fresh ephemeral X25519 keypair; command payloads are end-to-end
// encrypted to the agent's public key so the relay cannot read them.
type Session struct {
	c          *Client
	keys       *crypto.KeyPair
	persistent bool
	auth       *protocol.AuthResponse

	// CancelGrace is how long Exec keeps reading after it sent
	// COMMAND_CANCEL, waiting for the agent's final COMMAND_RESULT
	// (default DefaultCancelGrace).
	CancelGrace time.Duration
}

// DefaultCancelGrace covers the agent's SIGTERM→SIGKILL window (3 s) plus
// a round trip.
const DefaultCancelGrace = 5 * time.Second

// SessionOptions configures DialSession.
type SessionOptions struct {
	OrgID   string // optional org check
	Version string
	Logf    func(string, ...any)
	// Keys is the CLI's long-lived identity keypair (config.CLIKeyDir).
	// Agents pin it per CLI token. nil uses a fresh per-session keypair
	// (v0.3 behaviour; agents cannot pin it).
	Keys *crypto.KeyPair
}

// DialSession connects to the relay and authenticates with a CLI token.
func DialSession(ctx context.Context, relayURL, token string, opts SessionOptions) (*Session, error) {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	c, err := NewClient(relayURL, token, Options{
		ClientType: protocol.ClientCLI,
		OrgID:      opts.OrgID,
		Version:    opts.Version,
		Logf:       opts.Logf,
	})
	if err != nil {
		return nil, err
	}
	keys, persistent := opts.Keys, opts.Keys != nil
	if keys == nil {
		if keys, err = crypto.GenerateKeyPair(); err != nil {
			return nil, err
		}
	}
	dctx, cancel := context.WithTimeout(ctx, DefaultDialTimeout)
	defer cancel()
	if err := c.ConnectContext(dctx); err != nil {
		return nil, err
	}
	auth, err := c.Authenticate()
	if err != nil {
		c.Close()
		return nil, err
	}
	return &Session{c: c, keys: keys, persistent: persistent, auth: auth, CancelGrace: DefaultCancelGrace}, nil
}

// Close closes the session.
func (s *Session) Close() error { return s.c.Close() }

// OrgID returns the authenticated org.
func (s *Session) OrgID() string { return s.auth.OrgID }

// PublicKey returns the key commands are sealed with.
func (s *Session) PublicKey() [32]byte { return s.keys.PublicKey }

// Name returns the relay-assigned connection name.
func (s *Session) Name() string { return s.auth.Name }

// Client exposes the underlying client (for raw frame access in tests).
func (s *Session) Client() *Client { return s.c }

// roundTrip sends f and returns frames with the same request ID to handle
// until it returns done=true. Relay-level heartbeat acks are skipped.
func (s *Session) roundTrip(ctx context.Context, f *protocol.Frame, timeout time.Duration, handle func(*protocol.Frame) (bool, error)) error {
	if err := s.c.SendFrame(f); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { s.c.SetReadTimeout(time.Millisecond) })
	defer stop()
	for {
		s.c.SetReadTimeout(timeout)
		resp, err := s.c.ReadFrame()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if resp.RequestID != f.RequestID {
			continue
		}
		if resp.MsgType == protocol.MsgError {
			var ep protocol.ErrorPayload
			_ = json.Unmarshal(resp.Payload, &ep)
			return &RemoteError{Code: ep.Code, Message: ep.Message}
		}
		done, err := handle(resp)
		if err != nil || done {
			return err
		}
	}
}

// AgentKey performs KEY_EXCHANGE with an agent and returns its public key.
// The caller should pin it (trust on first use) — the key travels through
// the relay, so pinning is what protects against a malicious relay swapping
// keys on later sessions.
func (s *Session) AgentKey(ctx context.Context, agent string) ([32]byte, error) {
	var pub [32]byte
	payload, _ := json.Marshal(protocol.KeyExchange{PublicKey: s.keys.PublicKeyBase64()})
	f := protocol.NewFrame(protocol.MsgKeyExchange, NewRequestID(), s.Name(), agent, 0, payload)
	err := s.roundTrip(ctx, f, 20*time.Second, func(r *protocol.Frame) (bool, error) {
		if r.MsgType != protocol.MsgKeyExchangeAck {
			return false, nil
		}
		var kx protocol.KeyExchange
		if err := json.Unmarshal(r.Payload, &kx); err != nil {
			return true, fmt.Errorf("parse KEY_EXCHANGE_ACK: %w", err)
		}
		k, err := crypto.DecodePublicKey(kx.PublicKey)
		if err != nil {
			return true, fmt.Errorf("agent public key: %w", err)
		}
		pub = k
		return true, nil
	})
	return pub, err
}

// Ping sends a HEARTBEAT to an agent through the relay and waits for its ack.
func (s *Session) Ping(ctx context.Context, agent string) error {
	f := protocol.NewFrame(protocol.MsgHeartbeat, NewRequestID(), s.Name(), agent, 0, nil)
	return s.roundTrip(ctx, f, 15*time.Second, func(r *protocol.Frame) (bool, error) {
		return r.MsgType == protocol.MsgHeartbeatAck, nil
	})
}

// ListAgents asks the relay for the org's connected agents (raw JSON).
func (s *Session) ListAgents(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	f := protocol.NewFrame(protocol.MsgAgentListRequest, NewRequestID(), s.Name(), protocol.RelayID, 0, nil)
	err := s.roundTrip(ctx, f, 15*time.Second, func(r *protocol.Frame) (bool, error) {
		if r.MsgType != protocol.MsgAgentList {
			return false, nil
		}
		out = append(json.RawMessage(nil), r.Payload...)
		return true, nil
	})
	return out, err
}

// Exec sends an encrypted COMMAND to agent and streams output lines to out
// until COMMAND_RESULT. Replies must be encrypted by agentPub; anything else
// is rejected.
//
// If ctx is cancelled while the command runs, Exec sends COMMAND_CANCEL
// (same request ID), keeps streaming for up to CancelGrace while the agent
// stops the command, and returns protocol.ExitCancelled with ctx.Err(). The
// session must not be reused after a cancelled Exec.
func (s *Session) Exec(ctx context.Context, agent string, agentPub [32]byte, req protocol.CommandRequest, out func(protocol.StreamLine)) (int, error) {
	req.Timestamp = time.Now().Unix()
	req.PersistentKey = s.persistent
	plain, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	sealed, err := crypto.Seal(plain, &agentPub, s.keys)
	if err != nil {
		return 0, err
	}
	reqID := NewRequestID()
	f := protocol.NewFrame(protocol.MsgCommand, reqID, s.Name(), agent, protocol.FlagEncrypted, sealed)

	hctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.c.Heartbeat(hctx, 30*time.Second)

	open := func(r *protocol.Frame) ([]byte, error) {
		if !r.IsEncrypted() {
			return nil, fmt.Errorf("agent sent an unencrypted %s", protocol.MsgTypeName(r.MsgType))
		}
		sender, pt, err := crypto.Open(r.Payload, s.keys)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", protocol.MsgTypeName(r.MsgType), err)
		}
		if !crypto.KeysEqual(sender, agentPub) {
			return nil, fmt.Errorf("%s was not sent by the expected agent key", protocol.MsgTypeName(r.MsgType))
		}
		return pt, nil
	}

	if err := ctx.Err(); err != nil {
		return protocol.ExitCancelled, err
	}
	if err := s.c.SendFrame(f); err != nil {
		return 0, err
	}

	type readResult struct {
		f   *protocol.Frame
		err error
	}
	// One read at a time, so no read is left in flight after a normal
	// return (the session stays usable).
	var pending chan readResult
	readNext := func() chan readResult {
		ch := make(chan readResult, 1)
		go func() {
			s.c.SetReadTimeout(95 * time.Second)
			fr, err := s.c.ReadFrame()
			ch <- readResult{fr, err}
		}()
		return ch
	}

	cancelled := false
	var grace <-chan time.Time
	done := ctx.Done()
	for {
		if pending == nil {
			pending = readNext()
		}
		var rr readResult
		select {
		case <-done:
			done = nil
			cancelled = true
			cf := protocol.NewFrame(protocol.MsgCommandCancel, reqID, s.Name(), agent, 0, nil)
			if err := s.c.SendFrame(cf); err != nil {
				return protocol.ExitCancelled, ctx.Err()
			}
			g := s.CancelGrace
			if g <= 0 {
				g = DefaultCancelGrace
			}
			t := time.NewTimer(g)
			defer t.Stop()
			grace = t.C
			continue
		case <-grace:
			return protocol.ExitCancelled, ctx.Err()
		case rr = <-pending:
			pending = nil
		}
		if rr.err != nil {
			if cancelled {
				return protocol.ExitCancelled, ctx.Err()
			}
			return 0, rr.err
		}
		r := rr.f
		if r.RequestID != reqID {
			continue
		}
		switch r.MsgType {
		case protocol.MsgError:
			if cancelled {
				// e.g. an older relay refusing COMMAND_CANCEL (E_BAD_FRAME).
				return protocol.ExitCancelled, ctx.Err()
			}
			var ep protocol.ErrorPayload
			_ = json.Unmarshal(r.Payload, &ep)
			return 0, &RemoteError{Code: ep.Code, Message: ep.Message}
		case protocol.MsgStreamData:
			pt, err := open(r)
			if err != nil {
				return 0, err
			}
			var line protocol.StreamLine
			if err := json.Unmarshal(pt, &line); err == nil && out != nil {
				out(line)
			}
		case protocol.MsgCommandResult:
			pt, err := open(r)
			if err != nil {
				return 0, err
			}
			var res protocol.CommandResult
			if err := json.Unmarshal(pt, &res); err != nil {
				return 0, fmt.Errorf("parse COMMAND_RESULT: %w", err)
			}
			if cancelled {
				return protocol.ExitCancelled, ctx.Err()
			}
			return res.ExitCode, nil
		case protocol.MsgCommandError:
			var ep protocol.ErrorPayload
			pt := r.Payload
			if r.IsEncrypted() {
				var err error
				if pt, err = open(r); err != nil {
					return 0, err
				}
			}
			_ = json.Unmarshal(pt, &ep)
			if cancelled {
				return protocol.ExitCancelled, ctx.Err()
			}
			return 0, &RemoteError{Code: ep.Code, Message: ep.Message}
		}
	}
}
