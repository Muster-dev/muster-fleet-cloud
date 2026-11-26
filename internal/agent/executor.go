package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
)

// CommandRequest is the decrypted command from the CLI.
type CommandRequest = protocol.CommandRequest

// OutputLine represents a single line of command output.
type OutputLine struct {
	Text     string `json:"text,omitempty"`
	Stream   string `json:"stream,omitempty"` // "stdout" or "stderr"
	ExitCode int    `json:"exit_code,omitempty"`
	Done     bool   `json:"done,omitempty"`
	// Cancelled is set on the Done line when the command was stopped
	// because its context was cancelled.
	Cancelled bool `json:"cancelled,omitempty"`
}

// ExecutorOptions configures an Executor.
type ExecutorOptions struct {
	MusterPath      string
	AllowedCommands []string
	DefaultCwd      string
	// AllowShell runs "exec" commands via `bash -c` without the allowlist.
	AllowShell bool
	// AllowPush permits "push_hook" (arbitrary scripts). Enabled for agents
	// in push mode.
	AllowPush bool
}

// Executor runs commands on behalf of the agent.
//
// Security model: in the default mode an "exec" command line is split into
// argv WITHOUT a shell, must not contain shell metacharacters, and its
// leading words must match an allowlist entry ("muster deploy" allows
// `muster deploy api` but not `muster deploy; rm -rf /`). The binary is
// executed directly, so there is no shell to inject into.
type Executor struct {
	opts ExecutorOptions
}

// NewExecutor creates a command executor.
func NewExecutor(opts ExecutorOptions) *Executor {
	if opts.MusterPath == "" {
		opts.MusterPath = "muster"
	}
	return &Executor{opts: opts}
}

var (
	serviceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// Environment variables a remote caller may never set: they change how the
// dynamic loader or the shell behaves and turn "set an env var" into "run
// arbitrary code".
var blockedEnv = map[string]bool{
	"PATH": true, "IFS": true, "BASH_ENV": true, "ENV": true, "SHELLOPTS": true,
	"BASHOPTS": true, "PROMPT_COMMAND": true, "PS4": true, "HOME": true,
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "LD_AUDIT": true,
	"GCONV_PATH": true, "NODE_OPTIONS": true, "PYTHONPATH": true,
	"PYTHONSTARTUP": true, "PERL5OPT": true, "RUBYOPT": true,
}

func validateEnv(env map[string]string) error {
	for k, v := range env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("invalid env var name %q", k)
		}
		if blockedEnv[k] || strings.HasPrefix(k, "DYLD_") || strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "BASH_FUNC_") {
			return fmt.Errorf("env var %q may not be set remotely", k)
		}
		if strings.ContainsRune(v, 0) {
			return fmt.Errorf("env var %q contains a NUL byte", k)
		}
	}
	return nil
}

func (e *Executor) resolveCwd(reqCwd string) (string, error) {
	if reqCwd == "" {
		return e.opts.DefaultCwd, nil
	}
	if !filepath.IsAbs(reqCwd) {
		return "", fmt.Errorf("cwd must be an absolute path")
	}
	return filepath.Clean(reqCwd), nil
}

// SplitArgs splits a command line into words like a POSIX shell would for
// quoting purposes ('single', "double", backslash escapes) but performs NO
// expansion, and rejects unquoted shell metacharacters (; & | < > ` $ ( ) and
// newlines) so that a command which "looks like" an allowlisted one can never
// smuggle a second command.
func SplitArgs(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inWord  bool
		inS     bool
		inD     bool
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
			inWord = true
		case inS:
			if r == '\'' {
				inS = false
			} else {
				cur.WriteRune(r)
			}
		case inD:
			switch r {
			case '"':
				inD = false
			case '\\':
				escaped = true
			case '`', '$':
				return nil, fmt.Errorf("shell expansion %q is not allowed", r)
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped = true
		case r == '\'':
			inS, inWord = true, true
		case r == '"':
			inD, inWord = true, true
		case r == ' ' || r == '\t':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		case strings.ContainsRune(";&|<>`$()\n\r*?[]{}~#!", r):
			return nil, fmt.Errorf("shell metacharacter %q is not allowed (agent runs commands without a shell)", r)
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inS || inD || escaped {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}

// allowed reports whether argv starts with the words of an allowlist entry.
func (e *Executor) allowed(argv []string) bool {
	for _, entry := range e.opts.AllowedCommands {
		words := strings.Fields(entry)
		if len(words) == 0 || len(words) > len(argv) {
			continue
		}
		match := true
		for i, w := range words {
			if argv[i] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// BuildCommand turns a request into an *exec.Cmd after validation. The
// command is not bound to ctx: Execute handles cancellation (it must kill the
// whole process group, not just the leader).
func (e *Executor) BuildCommand(_ context.Context, req *CommandRequest) (*exec.Cmd, error) {
	if err := validateEnv(req.Env); err != nil {
		return nil, err
	}
	cwd, err := e.resolveCwd(req.Cwd)
	if err != nil {
		return nil, err
	}
	for _, s := range req.Services {
		if !serviceRe.MatchString(s) {
			return nil, fmt.Errorf("invalid service name %q", s)
		}
	}

	var cmd *exec.Cmd
	switch req.Action {
	case "push_hook":
		if !e.opts.AllowPush {
			return nil, fmt.Errorf("push_hook is disabled (agent is not in push mode)")
		}
		cmd = exec.Command("bash", "-s")
		cmd.Stdin = strings.NewReader(req.Command)

	case "exec":
		if e.opts.AllowShell {
			cmd = exec.Command("bash", "-c", req.Command)
			break
		}
		argv, err := SplitArgs(req.Command)
		if err != nil {
			return nil, fmt.Errorf("command rejected: %w", err)
		}
		if len(argv) == 0 {
			return nil, fmt.Errorf("command rejected: empty command")
		}
		if !e.allowed(argv) {
			return nil, fmt.Errorf("command rejected: %q not in allowlist", req.Command)
		}
		bin := argv[0]
		if bin == "muster" {
			bin = e.opts.MusterPath
		}
		cmd = exec.Command(bin, argv[1:]...)

	case "deploy", "rollback":
		argv := append([]string{"muster", req.Action, "--json"}, req.Services...)
		if !e.allowed(argv) {
			return nil, fmt.Errorf("command rejected: muster %s not in allowlist", req.Action)
		}
		cmd = exec.Command(e.opts.MusterPath, argv[1:]...)

	case "status":
		argv := []string{"muster", "status", "--json"}
		if !e.allowed(argv) {
			return nil, fmt.Errorf("command rejected: muster status not in allowlist")
		}
		cmd = exec.Command(e.opts.MusterPath, argv[1:]...)

	case "logs":
		if len(req.Services) == 0 {
			return nil, fmt.Errorf("logs action requires a service name")
		}
		argv := []string{"muster", "logs", req.Services[0]}
		if !e.allowed(argv) {
			return nil, fmt.Errorf("command rejected: muster logs not in allowlist")
		}
		cmd = exec.Command(e.opts.MusterPath, argv[1:]...)

	default:
		return nil, fmt.Errorf("unknown action: %q", req.Action)
	}

	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd, nil
}

// KillGrace is how long a cancelled command's process group gets between
// SIGTERM and SIGKILL.
var KillGrace = 3 * time.Second

// Execute validates and runs a request, streaming merged stdout/stderr line
// by line. The final value on the channel has Done=true and the exit code.
//
// sysEnv holds variables the agent controls (caller identity); they are
// applied last and override both the agent's environment and req.Env.
//
// The command runs in its own process group. When ctx is cancelled the
// whole group gets SIGTERM, then SIGKILL after KillGrace, and the result is
// reported as Cancelled with exit code 130.
func (e *Executor) Execute(ctx context.Context, req *CommandRequest, sysEnv map[string]string) (<-chan OutputLine, error) {
	cmd, err := e.BuildCommand(ctx, req)
	if err != nil {
		return nil, err
	}
	cmd.Env = mergeEnv(cmd.Env, sysEnv)
	setProcessGroup(cmd)

	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := ctx.Err(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("start command: %w", err)
	}
	pw.Close() // child holds its own copy

	exited := make(chan struct{})
	var cancelled atomic.Bool
	killerDone := make(chan struct{})
	go func() {
		defer close(killerDone)
		select {
		case <-exited:
			return
		case <-ctx.Done():
		}
		cancelled.Store(true)
		pgid := cmd.Process.Pid
		_ = signalGroup(pgid, syscall.SIGTERM)
		deadline := time.NewTimer(KillGrace)
		defer deadline.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-deadline.C:
				_ = signalGroup(pgid, syscall.SIGKILL)
				return
			case <-tick.C:
				// Stop as soon as every process of the group is gone (the
				// leader may already be reaped while children linger).
				if !groupAlive(pgid) {
					return
				}
			}
		}
	}()

	ch := make(chan OutputLine, 64)
	go func() {
		defer close(ch)
		defer pr.Close()

		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), 256*1024)
		for scanner.Scan() {
			ch <- OutputLine{Text: scanner.Text(), Stream: "stdout"}
		}
		if scanner.Err() != nil {
			// e.g. a line longer than the buffer: keep draining so the child
			// never blocks on a full pipe.
			ch <- OutputLine{Text: "[muster-agent: output line too long, truncated]", Stream: "stderr"}
			_, _ = io.Copy(io.Discard, pr)
		}

		waitErr := cmd.Wait()
		close(exited)
		<-killerDone // a cancel in progress finishes (group reaped/killed) first

		exitCode := 0
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}
		if cancelled.Load() {
			ch <- OutputLine{ExitCode: protocol.ExitCancelled, Done: true, Cancelled: true}
			return
		}
		ch <- OutputLine{ExitCode: exitCode, Done: true}
	}()
	return ch, nil
}

// mergeEnv returns env with every key in override replaced (existing
// entries for those keys are dropped, then the overrides are appended).
func mergeEnv(env []string, override map[string]string) []string {
	if len(override) == 0 {
		return env
	}
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := override[k]; ok {
			continue
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(override))
	for k := range override {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+override[k])
	}
	return out
}

// ParseCommandRequest decodes a JSON command request.
func ParseCommandRequest(data []byte) (*CommandRequest, error) {
	var req CommandRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("parse command: %w", err)
	}
	return &req, nil
}
