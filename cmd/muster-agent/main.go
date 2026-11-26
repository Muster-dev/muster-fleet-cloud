package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/Muster-dev/muster-fleet-cloud/internal/agent"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
)

var version = "0.2.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "join":
		cmdJoin(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "trust":
		cmdTrust(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("muster-agent %s\n", version)
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: muster-agent <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  join      Enroll this machine with a relay using a one-time join token")
	fmt.Println("  run       Start the agent daemon (foreground)")
	fmt.Println("  status    Show agent configuration")
	fmt.Println("  trust     List, add or reset pinned CLI keys (trust list|add|reset)")
	fmt.Println("  version   Print version")
	fmt.Println()
	fmt.Println("All commands accept --config <path> (default: $MUSTER_AGENT_CONFIG or ~/.muster-agent/config.json).")
	fmt.Println("Run 'muster-agent join --help' for enrollment options.")
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", config.AgentConfigPath(), "agent config file")
	_ = fs.Parse(args)

	cfg, err := config.LoadAgentConfigFrom(*configPath)
	if err != nil {
		log.Fatalf("load config: %v (run 'muster-agent join' first)", err)
	}
	if cfg.Relay.URL == "" || cfg.Relay.Token == "" {
		log.Fatalf("config %s has no relay url/token; run 'muster-agent join'", *configPath)
	}

	keys, err := crypto.LoadKeyPair(config.KeyDirFor(*configPath))
	if err != nil {
		log.Fatalf("load keys: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := agent.New(cfg, keys, agent.Options{ConfigPath: *configPath, Version: version})
	if err != nil {
		log.Fatalf("agent: %v", err)
	}
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("agent stopped: %v", err)
	}
	log.Println("shut down")
}

func cmdJoin(args []string) {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	configPath := fs.String("config", config.AgentConfigPath(), "agent config file to write")
	relayURL := fs.String("relay", config.DefaultRelayURL, "relay URL (wss://...)")
	token := fs.String("token", os.Getenv("MUSTER_AGENT_JOIN_TOKEN"), "one-time join token (mst_agent_...; env MUSTER_AGENT_JOIN_TOKEN)")
	orgID := fs.String("org", "", "organization ID (optional; verified against the token)")
	name := fs.String("name", "", "agent name (default: the token's machine name, else this host's name)")
	projectDir := fs.String("project", "", "project directory on this machine")
	mode := fs.String("mode", "muster", "deploy mode: muster or push")
	musterPath := fs.String("muster-path", "", "path to the muster binary (default: muster)")
	keyPolicy := fs.String("cli-key-policy", "", "CLI key pinning: tofu (default), strict or off")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: muster-agent join --token mst_agent_... [--relay wss://relay.getmuster.dev] [--name prod-1] [--project /opt/app]")
		fmt.Fprintln(os.Stderr)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *token == "" {
		fmt.Fprintln(os.Stderr, "error: --token is required")
		fs.Usage()
		os.Exit(2)
	}
	if !agent.IsJoinToken(*token) {
		fmt.Fprintln(os.Stderr, "error: --token must be a join token (mst_agent_...)")
		os.Exit(2)
	}
	if !config.ValidCLIKeyPolicy(*keyPolicy) {
		fmt.Fprintln(os.Stderr, "error: --cli-key-policy must be tofu, strict or off")
		os.Exit(2)
	}
	if *mode != "muster" && *mode != "push" {
		fmt.Fprintln(os.Stderr, "error: --mode must be muster or push")
		os.Exit(2)
	}
	reqName := *name
	if reqName == "" {
		host, _ := os.Hostname()
		reqName = protocol.SanitizeName(host)
	} else if !protocol.ValidName(reqName) {
		fmt.Fprintln(os.Stderr, "error: --name must be 1-32 chars of [A-Za-z0-9._-]")
		os.Exit(2)
	}

	// Reuse an existing keypair so re-joining keeps the agent's identity key
	// (CLIs pin it); generate one otherwise.
	keyDir := config.KeyDirFor(*configPath)
	keys, err := crypto.LoadKeyPair(keyDir)
	if err != nil {
		keys, err = crypto.GenerateKeyPair()
		if err != nil {
			log.Fatalf("generate keys: %v", err)
		}
		if err := crypto.SaveKeyPair(keyDir, keys); err != nil {
			log.Fatalf("save keys: %v", err)
		}
	}

	cfg := config.DefaultAgentConfig()
	if existing, err := config.LoadAgentConfigFrom(*configPath); err == nil {
		cfg = existing // keep allowlist and other local settings
	}
	cfg.Relay.URL = *relayURL
	cfg.Relay.Token = *token
	cfg.Identity.OrgID = *orgID
	cfg.Identity.Name = reqName
	if *projectDir != "" {
		cfg.Project.Dir = *projectDir
	}
	cfg.Project.Mode = *mode
	if *musterPath != "" {
		cfg.MusterPath = *musterPath
	}
	if *keyPolicy != "" {
		cfg.CLIKeyPolicy = *keyPolicy
	}

	a, err := agent.New(cfg, keys, agent.Options{ConfigPath: *configPath, Version: version})
	if err != nil {
		log.Fatalf("join: %v", err)
	}
	resp, err := a.Enroll(context.Background())
	if err != nil {
		log.Fatalf("join failed: %v", err)
	}
	if resp.NodeToken == "" {
		log.Fatalf("join failed: relay did not issue a node credential")
	}

	fmt.Printf("Agent enrolled: %s (org %s)\n", resp.Name, resp.OrgID)
	fmt.Printf("Config saved to: %s (node credential, mode 0600)\n", *configPath)
	fmt.Printf("Public key fingerprint: %s\n", crypto.Fingerprint(keys.PublicKey))
	fmt.Println()
	fmt.Println("Start the agent:")
	fmt.Printf("  muster-agent run --config %s\n", *configPath)
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := fs.String("config", config.AgentConfigPath(), "agent config file")
	_ = fs.Parse(args)

	cfg, err := config.LoadAgentConfigFrom(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No agent config found at %s. Run 'muster-agent join' first.\n", *configPath)
		os.Exit(1)
	}
	cred := "none"
	switch {
	case agent.IsJoinToken(cfg.Relay.Token):
		cred = "join token (not yet enrolled)"
	case cfg.Relay.Token != "":
		cred = "node credential"
	}
	fmt.Printf("Agent:      %s (org %s)\n", cfg.Identity.Name, cfg.Identity.OrgID)
	fmt.Printf("Relay:      %s\n", cfg.Relay.URL)
	fmt.Printf("Credential: %s\n", cred)
	fmt.Printf("Project:    %s (%s mode)\n", cfg.Project.Dir, cfg.Project.Mode)
	if keys, err := crypto.LoadKeyPair(config.KeyDirFor(*configPath)); err == nil {
		fmt.Printf("Key:        %s\n", crypto.Fingerprint(keys.PublicKey))
	}
	policy := cfg.CLIKeyPolicy
	if policy == "" {
		policy = config.CLIKeyPolicyTOFU
	}
	fmt.Printf("CLI keys:   %s (pins in %s)\n", policy, config.TrustedCLIsPath(*configPath))
	if cfg.AllowShell {
		fmt.Printf("Commands:   ANY (allow_shell is on)\n")
	} else {
		fmt.Printf("Commands:   %s\n", strings.Join(cfg.AllowedCommands, ", "))
	}
	fmt.Printf("Config:     %s\n", *configPath)
}

func cmdTrust(args []string) {
	usage := func() {
		fmt.Fprintln(os.Stderr, "Usage: muster-agent trust list [--config PATH]")
		fmt.Fprintln(os.Stderr, "       muster-agent trust add <token-id> <cli-public-key-base64> [--name NAME] [--config PATH]")
		fmt.Fprintln(os.Stderr, "       muster-agent trust reset <token-id>|--all [--config PATH]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "The agent pins the key each CLI token seals commands with. 'muster-tunnel whoami'")
		fmt.Fprintln(os.Stderr, "prints the token ID and public key on the CLI machine.")
	}
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("trust "+sub, flag.ExitOnError)
	configPath := fs.String("config", config.AgentConfigPath(), "agent config file")
	name := fs.String("name", "", "display name for the token (add)")
	all := fs.Bool("all", false, "reset every pin (reset)")
	// Allow flags before or after positional arguments.
	var pos []string
	for len(rest) > 0 {
		_ = fs.Parse(rest)
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	ts := agent.NewTrustStore(config.TrustedCLIsPath(*configPath))

	switch sub {
	case "list":
		list, err := ts.List()
		if err != nil {
			log.Fatal(err)
		}
		if len(list) == 0 {
			fmt.Println("No pinned CLI keys.")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TOKEN ID\tNAME\tKEY\tPINNED\tSOURCE")
		for _, e := range list {
			fp := "?"
			if k, err := crypto.DecodePublicKey(e.Key); err == nil {
				fp = crypto.Fingerprint(k)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.TokenID, e.TokenName, fp, e.PinnedAt.Format("2006-01-02 15:04"), e.Source)
		}
		_ = tw.Flush()
	case "add":
		if len(pos) != 2 {
			usage()
			os.Exit(2)
		}
		key, err := crypto.DecodePublicKey(pos[1])
		if err != nil {
			log.Fatalf("public key: %v", err)
		}
		if err := ts.Add(pos[0], *name, key); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Pinned %s for token %s\n", crypto.Fingerprint(key), pos[0])
	case "reset":
		id := ""
		switch {
		case *all && len(pos) == 0:
		case !*all && len(pos) == 1:
			id = pos[0]
		default:
			usage()
			os.Exit(2)
		}
		n, err := ts.Reset(id)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Removed %d pin(s). The next command from that CLI will be pinned again (tofu).\n", n)
	default:
		usage()
		os.Exit(2)
	}
}
