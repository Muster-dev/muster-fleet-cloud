package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CommandRequest represents a decrypted command from the CLI.
type CommandRequest struct {
	Action   string            `json:"action"`   // "deploy", "status", "rollback", "logs", "exec"
	Command  string            `json:"command"`   // raw command (for "exec" action)
	Services []string          `json:"services,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Stream   bool              `json:"stream"`
}

// OutputLine represents a single line of command output.
type OutputLine struct {
	Text     string `json:"text,omitempty"`
	Stream   string `json:"stream,omitempty"` // "stdout" or "stderr"
	ExitCode int    `json:"exit_code,omitempty"`
	Done     bool   `json:"done,omitempty"`
}

// Executor runs commands on behalf of the agent.
type Executor struct {
	musterPath      string
	allowedCommands []string
	defaultCwd      string
}

// NewExecutor creates a command executor.
func NewExecutor(musterPath string, allowedCommands []string, defaultCwd string) *Executor {
	return &Executor{
		musterPath:      musterPath,
		allowedCommands: allowedCommands,
		defaultCwd:      defaultCwd,
	}
}

// ValidateCommand checks if a command is allowed.
func (e *Executor) ValidateCommand(cmd string) error {
	for _, allowed := range e.allowedCommands {
		if strings.HasPrefix(cmd, allowed) {
			return nil
		}
	}
	return fmt.Errorf("command rejected: %q not in allowlist", cmd)
}

// Execute runs a muster command and streams output line by line.
func (e *Executor) Execute(ctx context.Context, req *CommandRequest) (<-chan OutputLine, error) {
	var cmdStr string
	switch req.Action {
	case "deploy":
		cmdStr = e.musterPath + " deploy --json"
		if len(req.Services) > 0 {
			cmdStr += " " + strings.Join(req.Services, " ")
		}
	case "status":
		cmdStr = e.musterPath + " status --json"
	case "rollback":
		cmdStr = e.musterPath + " rollback --json"
		if len(req.Services) > 0 {
			cmdStr += " " + strings.Join(req.Services, " ")
		}
	case "logs":
		if len(req.Services) > 0 {
			cmdStr = e.musterPath + " logs " + req.Services[0]
		} else {
			return nil, fmt.Errorf("logs action requires a service name")
		}
	case "exec":
		cmdStr = req.Command
	default:
		return nil, fmt.Errorf("unknown action: %s", req.Action)
	}

	if err := e.ValidateCommand(cmdStr); err != nil {
		return nil, err
	}

	cwd := e.defaultCwd
	if req.Cwd != "" {
		cwd = req.Cwd
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", cmdStr)
	if cwd != "" {
		cmd.Dir = cwd
	}

	// Build environment
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}

	ch := make(chan OutputLine, 64)
	go func() {
		defer close(ch)

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			ch <- OutputLine{Text: scanner.Text(), Stream: "stdout"}
		}

		exitCode := 0
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}

		ch <- OutputLine{ExitCode: exitCode, Done: true}
	}()

	return ch, nil
}

// ExecuteHook runs a piped hook script (for push mode).
func (e *Executor) ExecuteHook(ctx context.Context, script string, env map[string]string, cwd string) (<-chan OutputLine, error) {
	if cwd == "" {
		cwd = e.defaultCwd
	}

	cmd := exec.CommandContext(ctx, "bash", "-s")
	if cwd != "" {
		cmd.Dir = cwd
	}

	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	cmd.Stdin = strings.NewReader(script)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start hook: %w", err)
	}

	ch := make(chan OutputLine, 64)
	go func() {
		defer close(ch)

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			ch <- OutputLine{Text: scanner.Text(), Stream: "stdout"}
		}

		exitCode := 0
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}

		ch <- OutputLine{ExitCode: exitCode, Done: true}
	}()

	return ch, nil
}

// ParseCommandRequest decodes a JSON command request.
func ParseCommandRequest(data []byte) (*CommandRequest, error) {
	var req CommandRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("parse command: %w", err)
	}
	return &req, nil
}
