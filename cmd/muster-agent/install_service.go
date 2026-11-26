package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/Muster-dev/muster-fleet-cloud/internal/service"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
)

// cmdInstallService installs the agent as a systemd (Linux) or launchd
// (macOS) service. System-wide (default) needs root and runs the agent as
// the invoking user (SUDO_USER under sudo); --user installs a per-user
// service without root.
func cmdInstallService(args []string) int {
	fs := flag.NewFlagSet("install-service", flag.ContinueOnError)
	userMode := fs.Bool("user", false, "install for the current user (systemctl --user / LaunchAgent), no root needed")
	printOnly := fs.Bool("print", false, "only print the unit / plist and where it would go")
	configFlag := fs.String("config", "", "agent config file (default: the invoking user's ~/.muster-agent/config.json)")
	runAs := fs.String("run-as", "", "account the system-wide service runs as (default: $SUDO_USER, else the current user)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: sudo muster-agent install-service [--run-as USER] [--config PATH] [--print]")
		fmt.Fprintln(os.Stderr, "       muster-agent install-service --user [--config PATH] [--print]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Linux: a systemd unit (system-wide, or --user). macOS: a LaunchDaemon (system-wide)")
		fmt.Fprintln(os.Stderr, "or a LaunchAgent (--user). Safe to re-run: an unchanged, running service is left alone.")
		fmt.Fprintln(os.Stderr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
		return 1
	}

	cur, err := user.Current()
	if err != nil {
		return fail("current user: %v", err)
	}
	isRoot := os.Geteuid() == 0
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "root" {
		sudoUser = ""
	}
	if *userMode && isRoot && sudoUser != "" {
		return fail("--user installs a service for the current user; run it without sudo")
	}

	o := service.Options{GOOS: runtime.GOOS, User: *userMode, UID: os.Getuid()}
	account := cur
	if !*userMode {
		o.RunAs = *runAs
		if o.RunAs == "" && isRoot && sudoUser != "" {
			o.RunAs = sudoUser
		}
		if o.RunAs == "" {
			o.RunAs = cur.Username
		}
		if o.RunAs != cur.Username {
			if account, err = user.Lookup(o.RunAs); err != nil {
				return fail("user %s: %v", o.RunAs, err)
			}
		}
	}
	o.Home = account.HomeDir

	switch {
	case *configFlag != "":
		o.ConfigPath = *configFlag
	case account.Username != cur.Username && os.Getenv("MUSTER_AGENT_CONFIG") == "":
		// Under sudo, use the invoking user's config, not root's.
		o.ConfigPath = filepath.Join(account.HomeDir, ".muster-agent", "config.json")
	default:
		o.ConfigPath = config.AgentConfigPath()
	}
	if o.ConfigPath, err = filepath.Abs(o.ConfigPath); err != nil {
		return fail("%v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail("locate muster-agent binary: %v", err)
	}
	if o.Exe, err = filepath.Abs(exe); err != nil {
		return fail("%v", err)
	}

	if *printOnly {
		content, err := service.Render(o)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Printf("# %s → %s\n", o.Kind(), o.Path())
		fmt.Print(content)
		return 0
	}

	cfg, err := config.LoadAgentConfigFrom(o.ConfigPath)
	if err != nil || cfg.Relay.Token == "" {
		return fail("no enrolled agent config at %s; run 'muster-agent pair' first (or pass --config)", o.ConfigPath)
	}
	if o.NeedsRoot() && !isRoot {
		return fail("installing a system-wide service needs root:\n  sudo muster-agent install-service\nor install it for your user only:\n  muster-agent install-service --user")
	}

	run := func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if name == "launchctl" && len(args) > 0 && args[0] == "print" {
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard // existence probe
		}
		return cmd.Run()
	}
	if err := service.Install(o, run, os.Stdout); err != nil {
		return fail("%v", err)
	}

	fmt.Println()
	fmt.Printf("muster-agent runs as %s with %s.\n", account.Username, o.ConfigPath)
	switch {
	case o.GOOS == "darwin":
		fmt.Printf("Logs:   tail -f %s\n", o.LogPath())
	case o.User:
		fmt.Println("Logs:   journalctl --user -u muster-agent -f")
		if _, err := os.Stat(filepath.Join("/var/lib/systemd/linger", account.Username)); err != nil {
			fmt.Println("Note:   user services stop when you log out. To keep the agent running:")
			fmt.Printf("          sudo loginctl enable-linger %s\n", account.Username)
		}
	default:
		fmt.Println("Logs:   journalctl -u muster-agent -f")
	}
	check := "muster-agent status --connected"
	if *configFlag != "" || account.Username != cur.Username {
		check += " --config " + o.ConfigPath
	}
	fmt.Printf("Check:  %s\n", check)
	return 0
}
