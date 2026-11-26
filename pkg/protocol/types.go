package protocol

// Protocol version
const Version uint8 = 0x01

// Message types
const (
	// Auth + registration
	MsgAuthRequest  uint8 = 0x01
	MsgAuthResponse uint8 = 0x02
	MsgAgentHello   uint8 = 0x03
	MsgRelayAck     uint8 = 0x04

	// Keepalive
	MsgHeartbeat    uint8 = 0x05
	MsgHeartbeatAck uint8 = 0x06

	// Commands (CLI -> Agent via relay)
	MsgCommand       uint8 = 0x10
	MsgCommandAck    uint8 = 0x11
	MsgCommandResult uint8 = 0x12
	MsgCommandError  uint8 = 0x13
	// MsgCommandCancel asks the agent to stop the command started by the
	// COMMAND with the same RequestID (CLI -> Agent, routed, empty payload).
	// Added in v1.2; older agents ignore it.
	MsgCommandCancel uint8 = 0x14

	// Streaming (Agent -> CLI via relay)
	MsgStreamData uint8 = 0x20
	MsgStreamEnd  uint8 = 0x21

	// Key exchange
	MsgKeyExchange    uint8 = 0x30
	MsgKeyExchangeAck uint8 = 0x31

	// Relay -> Agent notifications about CLI sessions (v1.2; older agents
	// ignore them). The relay is the only possible sender: SourceID is
	// always "relay" and CLIs cannot address these types to an agent.
	MsgSessionInfo   uint8 = 0x40 // JSON SessionInfo, before a CLI's first routed frame
	MsgSessionClosed uint8 = 0x41 // JSON SessionClosed, when that CLI disconnects

	// Relay-level
	MsgError            uint8 = 0xF0
	MsgAgentList        uint8 = 0xF1
	MsgAgentListRequest uint8 = 0xF2
)

// Flags
const (
	FlagEncrypted       uint8 = 0x01
	FlagCompressed      uint8 = 0x02
	FlagStreamContinued uint8 = 0x04
	FlagStreamEnd       uint8 = 0x08
)

// Error codes (in MsgError payloads)
const (
	ErrAuth            = "E_AUTH"
	ErrNotConnected    = "E_NOT_CONNECTED"
	ErrCommandRejected = "E_COMMAND_REJECTED"
	ErrTimeout         = "E_TIMEOUT"
	ErrDecrypt         = "E_DECRYPT"
	ErrAgentBusy       = "E_AGENT_BUSY"
	ErrBadFrame        = "E_BAD_FRAME"
)

// MsgTypeName returns a human-readable name for a message type.
func MsgTypeName(t uint8) string {
	switch t {
	case MsgAuthRequest:
		return "AUTH_REQUEST"
	case MsgAuthResponse:
		return "AUTH_RESPONSE"
	case MsgAgentHello:
		return "AGENT_HELLO"
	case MsgRelayAck:
		return "RELAY_ACK"
	case MsgHeartbeat:
		return "HEARTBEAT"
	case MsgHeartbeatAck:
		return "HEARTBEAT_ACK"
	case MsgCommand:
		return "COMMAND"
	case MsgCommandAck:
		return "COMMAND_ACK"
	case MsgCommandResult:
		return "COMMAND_RESULT"
	case MsgCommandError:
		return "COMMAND_ERROR"
	case MsgCommandCancel:
		return "COMMAND_CANCEL"
	case MsgStreamData:
		return "STREAM_DATA"
	case MsgStreamEnd:
		return "STREAM_END"
	case MsgKeyExchange:
		return "KEY_EXCHANGE"
	case MsgKeyExchangeAck:
		return "KEY_EXCHANGE_ACK"
	case MsgSessionInfo:
		return "SESSION_INFO"
	case MsgSessionClosed:
		return "SESSION_CLOSED"
	case MsgError:
		return "ERROR"
	case MsgAgentList:
		return "AGENT_LIST"
	case MsgAgentListRequest:
		return "AGENT_LIST_REQUEST"
	default:
		return "UNKNOWN"
	}
}
