package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Muster-dev/muster-fleet-cloud/internal/agent"
	"github.com/Muster-dev/muster-fleet-cloud/internal/pair"
	"github.com/Muster-dev/muster-fleet-cloud/internal/termui"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/crypto"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/tunnel"
)

// cmdPair connects this machine to Fleet Cloud with a short code: the user
// approves it in the dashboard (scan the QR code or open the URL), the relay
// hands back a one-time join token, and the agent enrolls with it.
func cmdPair(args []string) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	configPath := fs.String("config", config.AgentConfigPath(), "agent config file to write")
	relayURL := fs.String("relay", config.DefaultRelayURL, "relay URL (wss://...)")
	name := fs.String("name", "", "agent name (default: this host's name; can be changed when approving)")
	projectDir := fs.String("project", "", "project directory on this machine")
	mode := fs.String("mode", "muster", "deploy mode: muster or push")
	musterPath := fs.String("muster-path", "", "path to the muster binary (default: muster)")
	noQR := fs.Bool("no-qr", false, "don't print a QR code (it is also hidden when stdout is not a terminal)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: muster-agent pair [--name prod-1] [--project /opt/app] [--mode muster|push] [--relay wss://relay.getmuster.dev] [--no-qr] [--config PATH]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Shows a code and a QR code. Approve it at the printed URL (signed in to Fleet Cloud),")
		fmt.Fprintln(os.Stderr, "and this machine joins your org. No tokens to copy.")
		fmt.Fprintln(os.Stderr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *mode != "muster" && *mode != "push" {
		fmt.Fprintln(os.Stderr, "error: --mode must be muster or push")
		return 2
	}
	if *name != "" && !protocol.ValidName(*name) {
		fmt.Fprintln(os.Stderr, "error: --name must be 1-32 chars of [A-Za-z0-9._-]")
		return 2
	}
	urls, err := tunnel.NormalizeRelayURL(*relayURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	if *projectDir != "" {
		if abs, err := filepath.Abs(*projectDir); err == nil {
			*projectDir = abs
		}
	}

	host, _ := os.Hostname()
	projectSet := *projectDir != ""
	if existing, err := config.LoadAgentConfigFrom(*configPath); err == nil {
		projectSet = projectSet || existing.Project.Dir != ""
		if existing.Relay.Token != "" && !agent.IsJoinToken(existing.Relay.Token) {
			fmt.Printf("Note: %s already holds an enrolled agent (%s, org %s).\n", *configPath, existing.Identity.Name, existing.Identity.OrgID)
			fmt.Println("      Pairing replaces it on this machine; revoke the old agent in the dashboard.")
			fmt.Println()
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := &pair.Client{BaseURL: urls.HTTP, UserAgent: "muster-agent/" + version}
	start, err := client.Start(ctx, pair.StartRequest{
		Name: *name, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH,
		ProjectDirSet: projectSet, AgentVersion: version,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if errors.Is(err, pair.ErrUnsupported) {
			fmt.Fprintln(os.Stderr, "Create a join token in the dashboard and run 'muster-agent join --token mst_agent_...' instead.")
		}
		return 1
	}

	tty := termui.IsTerminal(os.Stdout)
	printPairPrompt(start, tty && !*noQR, tty)
	fmt.Println()
	fmt.Printf("Waiting for approval (code expires in %s; Ctrl+C to cancel)...\n", fmtDuration(time.Duration(start.ExpiresIn)*time.Second))

	warned := false
	client.OnPoll = func(status string) {
		if status == "network_error" && !warned {
			warned = true
			fmt.Fprintln(os.Stderr, "(relay unreachable, still trying...)")
		}
	}
	res, err := client.Wait(ctx, start)
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, "\nCancelled.")
		return 130
	case errors.Is(err, pair.ErrDenied):
		fmt.Fprintln(os.Stderr, "Pairing was denied in the dashboard. Nothing was changed on this machine.")
		return 1
	case errors.Is(err, pair.ErrExpired):
		fmt.Fprintln(os.Stderr, "The pairing code expired before it was approved. Run 'muster-agent pair' again.")
		return 1
	case err != nil:
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("Approved as %s (org %s). Enrolling...\n", res.Name, res.OrgID)
	resp, keys, err := enroll(enrollParams{
		configPath: *configPath, relayURL: urls.WS, token: res.JoinToken, orgID: res.OrgID,
		name: res.Name, projectDir: *projectDir, mode: *mode, musterPath: *musterPath, quiet: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: enrollment failed: %v\n", err)
		return 1
	}

	fmt.Println()
	fmt.Printf("Paired: %s (org %s)\n", resp.Name, resp.OrgID)
	fmt.Printf("  Config: %s (node credential, mode 0600)\n", *configPath)
	fmt.Printf("  Key:    %s\n", crypto.Fingerprint(keys.PublicKey))
	fmt.Println()
	printNextSteps(*configPath)
	return 0
}

func printPairPrompt(start *pair.StartResponse, showQR, tty bool) {
	bold, reset := "", ""
	if tty {
		bold, reset = "\x1b[1m", "\x1b[0m"
	}
	fmt.Printf("%sConnect this machine to Muster Fleet Cloud%s\n\n", bold, reset)
	if showQR {
		if err := termui.RenderQR(os.Stdout, start.VerificationURIComplete); err == nil {
			fmt.Println()
			fmt.Println("Scan the QR code with your phone, or open:")
		} else {
			fmt.Println("Open:")
		}
	} else {
		fmt.Println("Open:")
	}
	fmt.Printf("  %s%s%s\n\n", bold, start.VerificationURIComplete, reset)
	fmt.Println("and check that it shows this code:")
	fmt.Println()
	if tty {
		fmt.Print(termui.BigText(start.PairCode, "  "))
		fmt.Println()
	}
	fmt.Printf("  Code: %s%s%s\n", bold, start.PairCode, reset)
}

func printNextSteps(configPath string) {
	cfgFlag := ""
	if configPath != config.AgentConfigPath() {
		cfgFlag = " --config " + configPath
	}
	manager := "systemd"
	if runtime.GOOS == "darwin" {
		manager = "launchd"
	}
	fmt.Println("Next, keep the agent running:")
	fmt.Printf("  Start at boot (%s, needs sudo):\n", manager)
	fmt.Printf("    sudo muster-agent install-service%s\n", cfgFlag)
	fmt.Println("  Or as your user, without sudo:")
	fmt.Printf("    muster-agent install-service --user%s\n", cfgFlag)
	fmt.Println("  Or in the foreground now:")
	fmt.Printf("    muster-agent run%s\n", cfgFlag)
}

func fmtDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return d.String()
}
