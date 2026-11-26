package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/tunnel"
)

var version = "0.2.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		// After the first SIGINT/SIGTERM (which cancels ctx: exec/push send
		// COMMAND_CANCEL and wait briefly), restore default handling so a
		// second signal kills the process immediately.
		<-ctx.Done()
		stop()
	}()

	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "login":
		err = cmdLogin(ctx, args)
	case "logout":
		err = cmdLogout(args)
	case "whoami":
		err = cmdWhoami(ctx, args)
	case "exec":
		err = cmdExec(ctx, args)
	case "push":
		err = cmdPush(ctx, args)
	case "ping":
		err = cmdPing(ctx, args)
	case "agents":
		err = cmdAgents(ctx, args)
	case "version", "--version", "-v":
		fmt.Printf("muster-tunnel %s\n", version)
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
	if err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(int(ee))
		}
		if errors.Is(err, context.Canceled) {
			os.Exit(protocol.ExitCancelled)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func printUsage() {
	fmt.Println("Usage: muster-tunnel <command> [options]")
	fmt.Println()
	fmt.Println("Cloud transport for muster fleet and group deployments.")
	fmt.Println("Routes end-to-end encrypted commands through a relay to remote muster-agents.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  login    Log in with your browser (device code flow) and save a CLI token")
	fmt.Println("  logout   Forget the saved CLI token")
	fmt.Println("  whoami   Show which org the token belongs to")
	fmt.Println("  exec     Execute a command on a remote agent")
	fmt.Println("  push     Push a hook script to a remote agent (agent must be in push mode)")
	fmt.Println("  ping     Test connectivity to a remote agent")
	fmt.Println("  agents   List connected agents in your org (JSON)")
	fmt.Println("  version  Print version")
	fmt.Println()
	fmt.Println("Common flags:")
	fmt.Println("  --relay URL       Relay URL (env MUSTER_CLOUD_RELAY, config file, default " + config.DefaultRelayURL + ")")
	fmt.Println("  --token TOKEN     CLI token mst_cli_... (env MUSTER_CLOUD_TOKEN, else config file)")
	fmt.Println("  --config PATH     Credential file (env MUSTER_CLOUD_CONFIG, default ~/.muster/cloud.json)")
	fmt.Println("  --org ORG         Optional: fail unless the token belongs to this org")
	fmt.Println("  --agent NAME      Target agent name")
	fmt.Println("  --accept-new-key  Accept a changed agent public key (after an agent was re-installed)")
	fmt.Println()
	fmt.Println("Exec/Push flags:")
	fmt.Println("  --cmd COMMAND     Command to execute (exec only)")
	fmt.Println("  --hook FILE       Hook script to push (push only)")
	fmt.Println("  --env KEY=VALUE   Env var for the command; repeatable. For push, a value")
	fmt.Println("                    may also be a comma list KEY=val,KEY2=val2 (legacy)")
	fmt.Println("  --cwd DIR         Working directory on the remote agent (absolute)")
	fmt.Println()
	fmt.Println("Ctrl+C / SIGTERM during exec or push asks the agent to stop the command")
	fmt.Println("(its whole process group) and exits 130. A second Ctrl+C exits at once.")
	fmt.Println()
	fmt.Println("Commands are sealed with a long-lived CLI key (~/.muster/cloud_keys, env")
	fmt.Println("MUSTER_CLOUD_KEY_DIR) that agents pin per CLI token; 'whoami' prints it.")
}

type common struct {
	relay        string
	token        string
	configPath   string
	org          string
	agent        string
	acceptNewKey bool
}

func newFlagSet(name string, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&c.relay, "relay", "", "relay URL")
	fs.StringVar(&c.token, "token", "", "CLI token (mst_cli_...)")
	fs.StringVar(&c.configPath, "config", config.CLIConfigPath(), "credential file")
	fs.StringVar(&c.org, "org", "", "expected org ID (optional)")
	fs.StringVar(&c.agent, "agent", "", "target agent name")
	fs.BoolVar(&c.acceptNewKey, "accept-new-key", false, "accept a changed agent public key")
	return fs
}

// resolve fills relay/token from flag > env > config file > default.
func (c *common) resolve(needToken bool) error {
	cfg, err := config.LoadCLIConfig(c.configPath)
	if err != nil {
		return err
	}
	if c.relay == "" {
		c.relay = os.Getenv("MUSTER_CLOUD_RELAY")
	}
	if c.relay == "" {
		c.relay = cfg.RelayURL
	}
	if c.relay == "" {
		c.relay = config.DefaultRelayURL
	}
	if c.token == "" {
		c.token = os.Getenv("MUSTER_CLOUD_TOKEN")
	}
	if c.token == "" {
		c.token = cfg.Token
	}
	if needToken {
		if c.token == "" {
			return errors.New("no CLI token: run 'muster-tunnel login', or pass --token / set MUSTER_CLOUD_TOKEN")
		}
		if !strings.HasPrefix(c.token, "mst_cli_") {
			return errors.New("token must be a CLI token (mst_cli_...)")
		}
	}
	return nil
}

func (c *common) dial(ctx context.Context) (*tunnel.Session, error) {
	if err := c.resolve(true); err != nil {
		return nil, err
	}
	keys, err := crypto.LoadOrCreateKeyPair(config.CLIKeyDir())
	if err != nil {
		return nil, fmt.Errorf("CLI identity key: %w", err)
	}
	s, err := tunnel.DialSession(ctx, c.relay, c.token, tunnel.SessionOptions{OrgID: c.org, Version: version, Keys: keys})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// agentKey gets the agent's public key and checks it against the pin file
// (trust on first use).
func (c *common) agentKey(ctx context.Context, s *tunnel.Session) ([32]byte, error) {
	pub, err := s.AgentKey(ctx, c.agent)
	if err != nil {
		return pub, err
	}
	urls, _ := tunnel.NormalizeRelayURL(c.relay)
	path := config.KnownAgentsPath()
	known, err := config.LoadKnownAgents(path)
	if err != nil {
		return pub, err
	}
	key := config.KnownAgentKey(urls.WS, s.OrgID(), c.agent)
	b64 := base64.StdEncoding.EncodeToString(pub[:])
	switch pinned, ok := known[key]; {
	case !ok:
		fmt.Fprintf(os.Stderr, "muster-tunnel: pinning key for agent %s (%s)\n", c.agent, crypto.Fingerprint(pub))
	case pinned == b64:
		return pub, nil
	case !c.acceptNewKey:
		old, _ := crypto.DecodePublicKey(pinned)
		return pub, fmt.Errorf("agent %s public key CHANGED (pinned %s, now %s). If the agent was re-installed, re-run with --accept-new-key; otherwise the relay may be tampering with traffic", c.agent, crypto.Fingerprint(old), crypto.Fingerprint(pub))
	default:
		fmt.Fprintf(os.Stderr, "muster-tunnel: replacing pinned key for agent %s (%s)\n", c.agent, crypto.Fingerprint(pub))
	}
	known[key] = b64
	if err := config.SaveKnownAgents(path, known); err != nil {
		return pub, fmt.Errorf("save pinned key: %w", err)
	}
	return pub, nil
}

func printLine(l protocol.StreamLine) {
	if l.Stream == "stderr" {
		fmt.Fprintln(os.Stderr, l.Line)
		return
	}
	fmt.Println(l.Line)
}

// envFlag collects repeatable --env KEY=VALUE flags. With splitCommas (push,
// for compatibility with v0.3) a value may hold several pairs separated by
// commas.
type envFlag struct {
	vars        map[string]string
	splitCommas bool
}

func (e *envFlag) String() string { return "" }

func (e *envFlag) Set(v string) error {
	if e.vars == nil {
		e.vars = map[string]string{}
	}
	pairs := []string{v}
	if e.splitCommas {
		pairs = strings.Split(v, ",")
	}
	for _, pair := range pairs {
		if pair == "" && e.splitCommas {
			continue
		}
		k, val, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return fmt.Errorf("invalid --env %q (want KEY=VALUE)", pair)
		}
		e.vars[k] = val
	}
	return nil
}

func cmdExec(ctx context.Context, args []string) error {
	var c common
	var cmd, cwd string
	var env envFlag
	fs := newFlagSet("exec", &c)
	fs.StringVar(&cmd, "cmd", "", "command to execute")
	fs.StringVar(&cwd, "cwd", "", "working directory on the agent")
	fs.Var(&env, "env", "env var KEY=VALUE for the command (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitError(2)
	}
	if c.agent == "" || cmd == "" {
		return errors.New("--agent and --cmd are required")
	}
	s, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	pub, err := c.agentKey(ctx, s)
	if err != nil {
		return err
	}
	code, err := s.Exec(ctx, c.agent, pub, protocol.CommandRequest{Action: "exec", Command: cmd, Env: env.vars, Cwd: cwd, Stream: true}, printLine)
	return execOutcome(code, err)
}

func cmdPush(ctx context.Context, args []string) error {
	var c common
	var hook, cwd string
	env := envFlag{splitCommas: true}
	fs := newFlagSet("push", &c)
	fs.StringVar(&hook, "hook", "", "hook script to push")
	fs.Var(&env, "env", "env vars KEY=val (repeatable, or KEY=val,KEY2=val2)")
	fs.StringVar(&cwd, "cwd", "", "working directory on the agent")
	if err := fs.Parse(args); err != nil {
		return exitError(2)
	}
	if c.agent == "" || hook == "" {
		return errors.New("--agent and --hook are required")
	}
	script, err := os.ReadFile(hook)
	if err != nil {
		return fmt.Errorf("read hook file: %w", err)
	}
	s, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	pub, err := c.agentKey(ctx, s)
	if err != nil {
		return err
	}
	code, err := s.Exec(ctx, c.agent, pub, protocol.CommandRequest{Action: "push_hook", Command: string(script), Env: env.vars, Cwd: cwd, Stream: true}, printLine)
	return execOutcome(code, err)
}

// execOutcome maps an Exec result to the process outcome.
func execOutcome(code int, err error) error {
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "muster-tunnel: cancelled (asked the agent to stop the command)")
		return exitError(protocol.ExitCancelled)
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError(code)
	}
	return nil
}

func cmdPing(ctx context.Context, args []string) error {
	var c common
	fs := newFlagSet("ping", &c)
	if err := fs.Parse(args); err != nil {
		return exitError(2)
	}
	if c.agent == "" {
		return errors.New("--agent is required")
	}
	s, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Ping(ctx, c.agent); err != nil {
		return err
	}
	fmt.Println("ok")
	return nil
}

func cmdAgents(ctx context.Context, args []string) error {
	var c common
	fs := newFlagSet("agents", &c)
	if err := fs.Parse(args); err != nil {
		return exitError(2)
	}
	s, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	out, err := s.ListAgents(ctx)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func cmdWhoami(ctx context.Context, args []string) error {
	var c common
	fs := newFlagSet("whoami", &c)
	if err := fs.Parse(args); err != nil {
		return exitError(2)
	}
	s, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	fmt.Printf("org: %s\nrelay: %s\n", s.OrgID(), c.relay)
	if cfg, err := config.LoadCLIConfig(c.configPath); err == nil && cfg.TokenID != "" && cfg.Token == c.token {
		fmt.Printf("token: %s (%s)\n", cfg.TokenID, cfg.Name)
	}
	pub := s.PublicKey()
	fmt.Printf("cli key: %s\n", crypto.Fingerprint(pub))
	fmt.Printf("cli public key: %s\n", base64.StdEncoding.EncodeToString(pub[:]))
	return nil
}

func cmdLogout(args []string) error {
	var c common
	fs := newFlagSet("logout", &c)
	if err := fs.Parse(args); err != nil {
		return exitError(2)
	}
	if err := os.Remove(c.configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("Logged out (local credential removed). Revoke the token in the dashboard to invalidate it everywhere.")
	return nil
}

type deviceCodeResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type deviceTokenResp struct {
	Token    string `json:"token"`
	TokenID  string `json:"token_id"`
	OrgID    string `json:"org_id"`
	Name     string `json:"name"`
	RelayURL string `json:"relay_url"`
	Error    string `json:"error"`
}

func postJSON(ctx context.Context, url string, body any, out any) (int, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "muster-tunnel/"+version)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return resp.StatusCode, fmt.Errorf("unexpected response (HTTP %d)", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func cmdLogin(ctx context.Context, args []string) error {
	var c common
	var name string
	fs := newFlagSet("login", &c)
	fs.StringVar(&name, "name", "", "token name shown in the dashboard (default: muster-cli on <host>)")
	if err := fs.Parse(args); err != nil {
		return exitError(2)
	}
	if err := c.resolve(false); err != nil {
		return err
	}
	urls, err := tunnel.NormalizeRelayURL(c.relay)
	if err != nil {
		return err
	}
	if name == "" {
		host, _ := os.Hostname()
		name = "muster-cli on " + host
	}

	var dc deviceCodeResp
	status, err := postJSON(ctx, urls.HTTP+"/api/v1/device/code", map[string]string{"client_name": name}, &dc)
	if err != nil {
		return fmt.Errorf("start login: %w", err)
	}
	if status != http.StatusOK || dc.DeviceCode == "" {
		return fmt.Errorf("start login: relay returned HTTP %d", status)
	}

	fmt.Println("To log in, open this URL in your browser and confirm the code:")
	fmt.Println()
	fmt.Printf("  %s\n", dc.VerificationURIComplete)
	fmt.Println()
	fmt.Printf("Your code: %s\n", dc.UserCode)
	fmt.Println()
	fmt.Println("Waiting for approval...")

	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		var tr deviceTokenResp
		status, err := postJSON(ctx, urls.HTTP+"/api/v1/device/token", map[string]string{"device_code": dc.DeviceCode}, &tr)
		if err != nil {
			continue // transient network error: keep polling
		}
		if status == http.StatusOK && tr.Token != "" {
			cfg := &config.CLIConfig{RelayURL: urls.WS, Token: tr.Token, TokenID: tr.TokenID, OrgID: tr.OrgID, Name: tr.Name}
			if err := config.SaveCLIConfig(c.configPath, cfg); err != nil {
				return fmt.Errorf("save credential: %w", err)
			}
			fmt.Printf("Logged in to org %s. Token saved to %s\n", tr.OrgID, c.configPath)
			return nil
		}
		switch tr.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return errors.New("login was denied")
		case "expired_token":
			return errors.New("login code expired; run 'muster-tunnel login' again")
		default:
			if status == http.StatusTooManyRequests {
				interval += 5 * time.Second
				continue
			}
			return fmt.Errorf("login failed (HTTP %d %s)", status, tr.Error)
		}
	}
	return errors.New("login code expired; run 'muster-tunnel login' again")
}
