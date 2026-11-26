// Package service installs muster-agent as a background service: a systemd
// unit on Linux, a launchd job on macOS. Rendering is pure (Render); Install
// writes the file and drives systemctl/launchctl through a Runner so it can
// be tested without touching the machine.
package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// UnitName is the systemd unit name.
	UnitName = "muster-agent.service"
	// Label is the launchd job label.
	Label = "dev.getmuster.agent"
)

// Options describes the service to install.
type Options struct {
	GOOS string // "linux" or "darwin"
	// User installs a per-user service (systemd --user / LaunchAgent)
	// instead of a system-wide one (needs root).
	User       bool
	Exe        string // absolute path of the muster-agent binary
	ConfigPath string // absolute path of the agent config
	// RunAs is the account the system-wide service runs as (systemd User=,
	// launchd UserName). Ignored with User.
	RunAs string
	// Home is RunAs's (or, with User, the current user's) home directory.
	Home string
	// UID is the current user's uid (launchd gui/<uid> domain).
	UID int
	// Root prefixes every system path (tests); "" in production.
	Root string
}

// Kind returns a human description of what will be installed.
func (o Options) Kind() string {
	switch {
	case o.GOOS == "darwin" && o.User:
		return "launchd LaunchAgent"
	case o.GOOS == "darwin":
		return "launchd LaunchDaemon"
	case o.User:
		return "systemd user unit"
	default:
		return "systemd system unit"
	}
}

// NeedsRoot reports whether installing requires root.
func (o Options) NeedsRoot() bool { return !o.User }

// Path returns where the unit / plist is written.
func (o Options) Path() string {
	switch {
	case o.GOOS == "darwin" && o.User:
		return filepath.Join(o.Home, "Library", "LaunchAgents", Label+".plist")
	case o.GOOS == "darwin":
		return filepath.Join(o.Root, "/Library/LaunchDaemons", Label+".plist")
	case o.User:
		return filepath.Join(o.Home, ".config", "systemd", "user", UnitName)
	default:
		return filepath.Join(o.Root, "/etc/systemd/system", UnitName)
	}
}

// LogPath is where launchd sends the agent's output (systemd uses the
// journal).
func (o Options) LogPath() string {
	return filepath.Join(filepath.Dir(o.ConfigPath), "agent.log")
}

func (o Options) validate() error {
	switch o.GOOS {
	case "linux", "darwin":
	default:
		return fmt.Errorf("install-service supports Linux (systemd) and macOS (launchd), not %s", o.GOOS)
	}
	if !filepath.IsAbs(o.Exe) || !filepath.IsAbs(o.ConfigPath) {
		return errors.New("binary and config paths must be absolute")
	}
	if o.Home == "" {
		return errors.New("home directory is unknown")
	}
	if !o.User && o.RunAs == "" {
		return errors.New("the account to run the service as is unknown (use --run-as)")
	}
	for _, s := range []string{o.Exe, o.ConfigPath, o.RunAs, o.Home} {
		if strings.ContainsAny(s, "\x00\n\r\"\\") {
			return fmt.Errorf("unsupported character in %q", s)
		}
	}
	return nil
}

// searchPath is the PATH the agent (and the muster commands it runs) get:
// launchd and systemd start services with a minimal one.
func (o Options) searchPath() string {
	dirs := []string{filepath.Join(o.Home, ".muster", "bin"), filepath.Join(o.Home, ".local", "bin")}
	if o.GOOS == "darwin" {
		dirs = append(dirs, "/opt/homebrew/bin", "/opt/homebrew/sbin")
	}
	dirs = append(dirs, "/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin")
	return strings.Join(dirs, ":")
}

// Render returns the unit / plist contents.
func Render(o Options) (string, error) {
	if err := o.validate(); err != nil {
		return "", err
	}
	if o.GOOS == "darwin" {
		return renderPlist(o), nil
	}
	return renderUnit(o), nil
}

func renderUnit(o Options) string {
	var b strings.Builder
	b.WriteString("# Installed by `muster-agent install-service`. Re-run it after moving the\n")
	b.WriteString("# binary or the config; manual edits are overwritten.\n")
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Muster Fleet Cloud agent\n")
	b.WriteString("Documentation=https://github.com/Muster-dev/muster-fleet-cloud\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	// A revoked credential makes the agent exit 1; give up after a few
	// tries instead of hammering the relay.
	b.WriteString("StartLimitIntervalSec=300\n")
	b.WriteString("StartLimitBurst=5\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	if !o.User {
		fmt.Fprintf(&b, "User=%s\n", o.RunAs)
	}
	fmt.Fprintf(&b, "ExecStart=\"%s\" run --config \"%s\"\n", o.Exe, o.ConfigPath)
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=10\n")
	fmt.Fprintf(&b, "Environment=\"PATH=%s\"\n", o.searchPath())
	if !o.User {
		b.WriteString("EnvironmentFile=-/etc/muster/agent.env\n")
	}
	b.WriteString("SyslogIdentifier=muster-agent\n\n")
	b.WriteString("[Install]\n")
	if o.User {
		b.WriteString("WantedBy=default.target\n")
	} else {
		b.WriteString("WantedBy=multi-user.target\n")
	}
	return b.String()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func renderPlist(o Options) string {
	str := func(s string) string { return "<string>" + xmlEscape(s) + "</string>" }
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<!-- Installed by `muster-agent install-service`. Re-run it after moving the binary or the config. -->\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t" + str(Label) + "\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range []string{o.Exe, "run", "--config", o.ConfigPath} {
		b.WriteString("\t\t" + str(a) + "\n")
	}
	b.WriteString("\t</array>\n")
	if !o.User {
		b.WriteString("\t<key>UserName</key>\n\t" + str(o.RunAs) + "\n")
	}
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	// Restart after a non-zero exit (crash, relay rejection), at most once
	// per ThrottleInterval; a clean stop stays stopped.
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>10</integer>\n")
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	b.WriteString("\t\t<key>PATH</key>\n\t\t" + str(o.searchPath()) + "\n")
	b.WriteString("\t\t<key>HOME</key>\n\t\t" + str(o.Home) + "\n")
	b.WriteString("\t</dict>\n")
	b.WriteString("\t<key>StandardOutPath</key>\n\t" + str(o.LogPath()) + "\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t" + str(o.LogPath()) + "\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// Runner runs a command, streaming its output; it returns an error for a
// non-zero exit.
type Runner func(name string, args ...string) error

// Install writes the unit/plist (only if it changed) and enables and
// starts the service. Re-running it is safe: an unchanged, running service
// is left alone. It reports each step on out.
func Install(o Options, run Runner, out io.Writer) error {
	content, err := Render(o)
	if err != nil {
		return err
	}
	path := o.Path()
	old, err := os.ReadFile(path)
	changed := err != nil || string(old) != content
	if changed {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := writeFile(path, []byte(content)); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		verb := "Wrote"
		if old != nil {
			verb = "Updated"
		}
		fmt.Fprintf(out, "%s %s: %s\n", verb, o.Kind(), path)
	} else {
		fmt.Fprintf(out, "%s already up to date: %s\n", o.Kind(), path)
	}

	step := func(ignoreErr bool, name string, args ...string) error {
		fmt.Fprintf(out, "  $ %s %s\n", name, strings.Join(args, " "))
		if err := run(name, args...); err != nil && !ignoreErr {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return nil
	}

	if o.GOOS == "darwin" {
		domain := "system"
		if o.User {
			domain = "gui/" + strconv.Itoa(o.UID)
		}
		target := domain + "/" + Label
		loaded := run("launchctl", "print", target) == nil
		if !changed && loaded {
			fmt.Fprintf(out, "Service %s is already loaded.\n", target)
			return nil
		}
		if loaded {
			if err := step(true, "launchctl", "bootout", target); err != nil {
				return err
			}
		}
		if err := step(true, "launchctl", "enable", target); err != nil {
			return err
		}
		return step(false, "launchctl", "bootstrap", domain, path)
	}

	ctl := []string{}
	if o.User {
		ctl = append(ctl, "--user")
	}
	sc := func(args ...string) error {
		return step(false, "systemctl", append(append([]string{}, ctl...), args...)...)
	}
	if !changed {
		return sc("enable", "--now", UnitName)
	}
	if err := sc("daemon-reload"); err != nil {
		return err
	}
	if err := sc("enable", UnitName); err != nil {
		return err
	}
	return sc("restart", UnitName)
}

// writeFile writes atomically with mode 0644 (unit files and plists must
// be readable by the service manager; they hold no secrets).
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
