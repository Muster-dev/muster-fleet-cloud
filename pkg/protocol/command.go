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
}

// StreamLine is the (encrypted) payload of a STREAM_DATA frame.
type StreamLine struct {
	Line   string `json:"line"`
	Stream string `json:"stream"`
}

// CommandResult is the (encrypted) payload of a COMMAND_RESULT frame.
type CommandResult struct {
	ExitCode int    `json:"exit_code"`
	Status   string `json:"status"`
}
