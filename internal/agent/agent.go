package agent

import (
	"context"
	"encoding/json"
	"log"

	"github.com/ImJustRicky/muster-fleet-cloud/internal/config"
	"github.com/ImJustRicky/muster-fleet-cloud/internal/crypto"
	"github.com/ImJustRicky/muster-fleet-cloud/internal/protocol"
	"github.com/ImJustRicky/muster-fleet-cloud/internal/tunnel"
)

// Agent is the muster-agent daemon.
type Agent struct {
	cfg      *config.AgentConfig
	client   *tunnel.Client
	executor *Executor
	keys     *crypto.KeyPair
}

// New creates a new Agent from config.
func New(cfg *config.AgentConfig, keys *crypto.KeyPair) *Agent {
	return &Agent{
		cfg:  cfg,
		keys: keys,
		client: tunnel.NewClient(
			cfg.Relay.URL,
			cfg.Relay.Token,
			cfg.Identity.OrgID,
			cfg.Identity.Name,
		),
		executor: NewExecutor(
			cfg.MusterPath,
			cfg.AllowedCommands,
			cfg.Project.Dir,
		),
	}
}

// Run starts the agent with automatic reconnection.
func (a *Agent) Run(ctx context.Context) error {
	rcfg := tunnel.DefaultReconnectConfig()

	return a.client.ConnectLoop(ctx, rcfg, func(ctx context.Context) error {
		// Send AGENT_HELLO
		if err := a.sendHello(); err != nil {
			return err
		}

		// Wait for RELAY_ACK
		frame, err := a.client.ReadFrame()
		if err != nil {
			return err
		}
		if frame.MsgType != protocol.MsgRelayAck {
			log.Printf("expected RELAY_ACK, got %s", protocol.MsgTypeName(frame.MsgType))
		}

		log.Printf("agent registered with relay")

		// Message loop
		return a.messageLoop(ctx)
	})
}

func (a *Agent) sendHello() error {
	hello := map[string]interface{}{
		"agent_name": a.cfg.Identity.Name,
		"org_id":     a.cfg.Identity.OrgID,
		"version":    "0.1.0",
		"public_key": a.keys.PublicKeyBase64(),
	}

	payload, err := json.Marshal(hello)
	if err != nil {
		return err
	}

	var reqID [16]byte
	frame := protocol.NewFrame(
		protocol.MsgAgentHello,
		reqID,
		a.client.Identity(),
		"relay",
		0,
		payload,
	)

	return a.client.SendFrame(frame)
}

func (a *Agent) messageLoop(ctx context.Context) error {
	for {
		frame, err := a.client.ReadFrame()
		if err != nil {
			return err
		}

		switch frame.MsgType {
		case protocol.MsgHeartbeat:
			ack := protocol.NewFrame(
				protocol.MsgHeartbeatAck,
				frame.RequestID,
				a.client.Identity(),
				protocol.ParseID(frame.SourceID),
				0,
				nil,
			)
			if err := a.client.SendFrame(ack); err != nil {
				return err
			}

		case protocol.MsgCommand:
			go a.handleCommand(ctx, frame)

		case protocol.MsgKeyExchange:
			a.handleKeyExchange(frame)

		default:
			log.Printf("unhandled message type: %s", protocol.MsgTypeName(frame.MsgType))
		}
	}
}

func (a *Agent) handleCommand(ctx context.Context, frame *protocol.Frame) {
	sourceID := protocol.ParseID(frame.SourceID)

	// Decrypt payload
	var plaintext []byte
	var err error
	if frame.IsEncrypted() {
		senderPub, decErr := crypto.DecodePublicKey("") // TODO: look up sender's public key
		if decErr != nil {
			log.Printf("unknown sender key for %s", sourceID)
			return
		}
		plaintext, err = crypto.Decrypt(frame.Payload, &senderPub, &a.keys.PrivateKey)
		if err != nil {
			log.Printf("decrypt failed from %s: %v", sourceID, err)
			a.sendError(frame.RequestID, sourceID, protocol.ErrDecrypt, "decryption failed")
			return
		}
	} else {
		plaintext = frame.Payload
	}

	// Parse command
	req, err := ParseCommandRequest(plaintext)
	if err != nil {
		log.Printf("parse command failed: %v", err)
		a.sendError(frame.RequestID, sourceID, protocol.ErrCommandRejected, err.Error())
		return
	}

	// Send ACK
	ack := protocol.NewFrame(
		protocol.MsgCommandAck,
		frame.RequestID,
		a.client.Identity(),
		sourceID,
		0,
		nil,
	)
	a.client.SendFrame(ack)

	// Execute
	var outputCh <-chan OutputLine
	if req.Action == "push_hook" {
		// Push mode: script content in Command field
		outputCh, err = a.executor.ExecuteHook(ctx, req.Command, req.Env, req.Cwd)
	} else {
		outputCh, err = a.executor.Execute(ctx, req)
	}

	if err != nil {
		a.sendError(frame.RequestID, sourceID, protocol.ErrCommandRejected, err.Error())
		return
	}

	// Stream output
	for line := range outputCh {
		if line.Done {
			// Send result
			result, _ := json.Marshal(map[string]interface{}{
				"exit_code": line.ExitCode,
				"status":    "done",
			})
			resultFrame := protocol.NewFrame(
				protocol.MsgCommandResult,
				frame.RequestID,
				a.client.Identity(),
				sourceID,
				0,
				result,
			)
			a.client.SendFrame(resultFrame)
		} else {
			// Stream data
			data, _ := json.Marshal(map[string]string{
				"line":   line.Text,
				"stream": line.Stream,
			})
			streamFrame := protocol.NewFrame(
				protocol.MsgStreamData,
				frame.RequestID,
				a.client.Identity(),
				sourceID,
				protocol.FlagStreamContinued,
				data,
			)
			a.client.SendFrame(streamFrame)
		}
	}
}

func (a *Agent) handleKeyExchange(frame *protocol.Frame) {
	sourceID := protocol.ParseID(frame.SourceID)
	log.Printf("key exchange from %s", sourceID)

	// TODO: store CLI's public key from payload

	// Send our public key back
	payload, _ := json.Marshal(map[string]string{
		"public_key": a.keys.PublicKeyBase64(),
	})

	ack := protocol.NewFrame(
		protocol.MsgKeyExchangeAck,
		frame.RequestID,
		a.client.Identity(),
		sourceID,
		0,
		payload,
	)
	a.client.SendFrame(ack)
}

func (a *Agent) sendError(reqID [16]byte, destID, code, message string) {
	payload, _ := json.Marshal(map[string]string{
		"code":    code,
		"message": message,
	})

	frame := protocol.NewFrame(
		protocol.MsgCommandError,
		reqID,
		a.client.Identity(),
		destID,
		0,
		payload,
	)
	a.client.SendFrame(frame)
}
