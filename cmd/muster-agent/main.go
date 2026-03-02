package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ImJustRicky/muster-fleet-cloud/internal/agent"
	"github.com/ImJustRicky/muster-fleet-cloud/internal/config"
	"github.com/ImJustRicky/muster-fleet-cloud/internal/crypto"
)

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun()
	case "join":
		cmdJoin()
	case "status":
		cmdStatus()
	case "version":
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
	fmt.Println("Usage: muster-agent <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run       Start the agent daemon (foreground)")
	fmt.Println("  join      Register with a relay using a join token")
	fmt.Println("  status    Show agent connection status")
	fmt.Println("  version   Print version")
	fmt.Println()
	fmt.Println("Run 'muster-agent join --help' for registration options.")
}

func cmdRun() {
	cfg, err := config.LoadAgentConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	keyDir := filepath.Join(config.AgentConfigDir(), "keys")
	keys, err := crypto.LoadKeyPair(keyDir)
	if err != nil {
		log.Fatalf("load keys: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		cancel()
	}()

	a := agent.New(cfg, keys)
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("agent error: %v", err)
	}
}

func cmdJoin() {
	// Parse flags
	var relayURL, token, orgID, name, projectDir, mode string

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--relay":
			i++
			if i < len(args) {
				relayURL = args[i]
			}
		case "--token":
			i++
			if i < len(args) {
				token = args[i]
			}
		case "--org":
			i++
			if i < len(args) {
				orgID = args[i]
			}
		case "--name":
			i++
			if i < len(args) {
				name = args[i]
			}
		case "--project":
			i++
			if i < len(args) {
				projectDir = args[i]
			}
		case "--mode":
			i++
			if i < len(args) {
				mode = args[i]
			}
		case "--help", "-h":
			fmt.Println("Usage: muster-agent join --relay <url> --token <join-token> --org <org> --name <name> [options]")
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  --relay <url>      Relay WebSocket URL (wss://...)")
			fmt.Println("  --token <token>    One-time join token")
			fmt.Println("  --org <org>        Organization ID")
			fmt.Println("  --name <name>      Agent name (e.g., prod-1)")
			fmt.Println("  --project <dir>    Project directory on this machine")
			fmt.Println("  --mode <mode>      Deploy mode: muster or push (default: muster)")
			return
		}
	}

	if relayURL == "" || token == "" || orgID == "" || name == "" {
		fmt.Fprintln(os.Stderr, "error: --relay, --token, --org, and --name are required")
		fmt.Fprintln(os.Stderr, "Run 'muster-agent join --help' for usage.")
		os.Exit(1)
	}

	if mode == "" {
		mode = "muster"
	}

	// Generate keypair
	keys, err := crypto.GenerateKeyPair()
	if err != nil {
		log.Fatalf("generate keys: %v", err)
	}

	keyDir := filepath.Join(config.AgentConfigDir(), "keys")
	if err := crypto.SaveKeyPair(keyDir, keys); err != nil {
		log.Fatalf("save keys: %v", err)
	}

	// TODO: connect to relay with join token, exchange for session token
	// For now, store the join token as the session token (placeholder)
	sessionToken := token

	cfg := config.DefaultAgentConfig()
	cfg.Relay.URL = relayURL
	cfg.Relay.Token = sessionToken
	cfg.Identity.OrgID = orgID
	cfg.Identity.Name = name
	cfg.Project.Dir = projectDir
	cfg.Project.Mode = mode

	if err := config.SaveAgentConfig(cfg); err != nil {
		log.Fatalf("save config: %v", err)
	}

	fmt.Printf("Agent registered: %s/%s\n", orgID, name)
	fmt.Printf("Config saved to: %s\n", config.AgentConfigPath())
	fmt.Printf("Public key: %s\n", keys.PublicKeyBase64())
	fmt.Println()
	fmt.Println("Start the agent:")
	fmt.Println("  muster-agent run")
}

func cmdStatus() {
	cfg, err := config.LoadAgentConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "No agent config found. Run 'muster-agent join' first.\n")
		os.Exit(1)
	}

	fmt.Printf("Agent: %s/%s\n", cfg.Identity.OrgID, cfg.Identity.Name)
	fmt.Printf("Relay: %s\n", cfg.Relay.URL)
	fmt.Printf("Project: %s (%s mode)\n", cfg.Project.Dir, cfg.Project.Mode)
	fmt.Printf("Config: %s\n", config.AgentConfigPath())
}
