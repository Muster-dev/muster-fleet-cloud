package protocol

// CommandRequest is the (encrypted) payload of a COMMAND frame.
type CommandRequest struct {
	// Action is "exec", "push_hook", "deploy", "status", "rollback" or "logs".
	Action string `json:"action"`
	// Command is the command line for "exec", or the script body for
	// "push_hook".
	Command  string            `json:"command,omitempty"`
	Services []string          `json:"services,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Stream   bool              `json:"stream"`
	// Timestamp is the sender's Unix time in seconds. Agents reject commands
	// whose timestamp is too far from their clock (replay protection
	// together with nonce tracking).
	Timestamp int64 `json:"ts"`
	// PersistentKey is set by v1.2 CLIs that seal commands with a long-lived
	// identity key (rather than a per-session one). Agents pin such keys per
	// CLI token; see docs/protocol.md "CLI key pinning".
	PersistentKey bool `json:"persistent_key,omitempty"`
}

// StreamLine is the (encrypted) payload of a STREAM_DATA frame.
type StreamLine struct {
	Line   string `json:"line"`
	Stream string `json:"stream"`
}

// CommandResult is the (encrypted) payload of a COMMAND_RESULT frame.
type CommandResult struct {
	ExitCode int `json:"exit_code"`
	// Status is StatusDone, or StatusCancelled when the command was stopped
	// by COMMAND_CANCEL / SESSION_CLOSED (ExitCode is then ExitCancelled).
	Status string `json:"status"`
}

// CommandResult statuses.
const (
	StatusDone      = "done"
	StatusCancelled = "cancelled"
)

// ExitCancelled is the exit code reported for a cancelled command (128 +
// SIGINT, like a shell).
const ExitCancelled = 130

// Environment variables the agent sets on every command. They are owned by
// the agent: a CommandRequest may not set them (or anything else starting
// with "MUSTER_CLOUD_").
const (
	EnvViaAgent     = "MUSTER_VIA_AGENT"       // always "1"
	EnvCloudCaller  = "MUSTER_CLOUD_CALLER"    // CLI token name, as reported by the relay
	EnvCallerID     = "MUSTER_CLOUD_CALLER_ID" // CLI token ID, as reported by the relay
	EnvDeploySource = "MUSTER_DEPLOY_SOURCE"   // "cloud:<token name>" ("cloud" if unknown)
)
