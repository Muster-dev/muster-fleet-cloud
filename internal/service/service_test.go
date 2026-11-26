package service

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func opts(goos string, user bool) Options {
	return Options{
		GOOS: goos, User: user, Exe: "/usr/local/bin/muster-agent",
		ConfigPath: "/home/deploy/.muster-agent/config.json", RunAs: "deploy",
		Home: "/home/deploy", UID: 501,
	}
}

func TestRenderGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Options
	}{
		{"systemd-system.golden", opts("linux", false)},
		{"systemd-user.golden", opts("linux", true)},
		{"launchd-daemon.golden", opts("darwin", false)},
		{"launchd-agent.golden", opts("darwin", true)},
	} {
		got, err := Render(tc.o)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		path := filepath.Join("testdata", tc.name)
		if *update {
			_ = os.WriteFile(path, []byte(got), 0644)
		}
		want, err := os.ReadFile(path)
		if err != nil || got != string(want) {
			t.Errorf("%s differs (go test -update):\n%s", tc.name, got)
		}
	}
}

func TestPathsAndKinds(t *testing.T) {
	cases := map[string][2]string{
		"linux/system":  {"/etc/systemd/system/muster-agent.service", "systemd system unit"},
		"linux/user":    {"/home/deploy/.config/systemd/user/muster-agent.service", "systemd user unit"},
		"darwin/system": {"/Library/LaunchDaemons/dev.getmuster.agent.plist", "launchd LaunchDaemon"},
		"darwin/user":   {"/home/deploy/Library/LaunchAgents/dev.getmuster.agent.plist", "launchd LaunchAgent"},
	}
	for k, want := range cases {
		goos, mode, _ := strings.Cut(k, "/")
		o := opts(goos, mode == "user")
		if o.Path() != want[0] || o.Kind() != want[1] || o.NeedsRoot() != (mode == "system") {
			t.Errorf("%s: %s %s", k, o.Path(), o.Kind())
		}
	}
}

func TestRenderRejectsBadInput(t *testing.T) {
	bad := []func(*Options){
		func(o *Options) { o.GOOS = "windows" },
		func(o *Options) { o.Exe = "muster-agent" },
		func(o *Options) { o.ConfigPath = "/tmp/a\"b" },
		func(o *Options) { o.RunAs = "" },
		func(o *Options) { o.Home = "" },
	}
	for i, f := range bad {
		o := opts("linux", false)
		f(&o)
		if _, err := Render(o); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	o := opts("linux", true)
	o.RunAs = "" // not needed for user units
	if _, err := Render(o); err != nil {
		t.Errorf("user unit without RunAs: %v", err)
	}
}

type recorder struct {
	calls []string
	fail  map[string]bool
}

func (r *recorder) run(name string, args ...string) error {
	c := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, c)
	if r.fail[c] {
		return errors.New("exit 1")
	}
	return nil
}

func TestInstallSystemdIdempotent(t *testing.T) {
	o := opts("linux", false)
	o.Root = t.TempDir()
	var out strings.Builder
	r := &recorder{}
	if err := Install(o, r.run, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl daemon-reload", "systemctl enable muster-agent.service", "systemctl restart muster-agent.service"}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("first install calls %v", r.calls)
	}
	st, err := os.Stat(o.Path())
	if err != nil || st.Mode().Perm() != 0644 {
		t.Fatalf("unit file: %v %v", st, err)
	}
	if !strings.Contains(out.String(), "Wrote systemd system unit") {
		t.Fatalf("output: %s", out.String())
	}

	// Second run: nothing changed → just make sure it's enabled and running.
	r = &recorder{}
	out.Reset()
	if err := Install(o, r.run, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.calls, "|") != "systemctl enable --now muster-agent.service" || !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("second install: %v %s", r.calls, out.String())
	}

	// Config moved → unit updated and restarted.
	o.ConfigPath = "/srv/agent/config.json"
	r = &recorder{}
	out.Reset()
	if err := Install(o, r.run, &out); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 || !strings.Contains(out.String(), "Updated") {
		t.Fatalf("update: %v %s", r.calls, out.String())
	}

	// A failing systemctl is reported.
	o.ConfigPath = "/srv/agent2/config.json"
	r = &recorder{fail: map[string]bool{"systemctl daemon-reload": true}}
	if err := Install(o, r.run, &out); err == nil {
		t.Fatal("systemctl failure not reported")
	}
}

func TestInstallSystemdUser(t *testing.T) {
	o := opts("linux", true)
	o.Home = t.TempDir()
	r := &recorder{}
	var out strings.Builder
	if err := Install(o, r.run, &out); err != nil {
		t.Fatal(err)
	}
	if r.calls[0] != "systemctl --user daemon-reload" || r.calls[2] != "systemctl --user restart muster-agent.service" {
		t.Fatalf("calls %v", r.calls)
	}
}

func TestInstallLaunchdIdempotent(t *testing.T) {
	o := opts("darwin", true)
	o.Home = t.TempDir()
	target := "gui/501/dev.getmuster.agent"
	// Not loaded yet.
	r := &recorder{fail: map[string]bool{"launchctl print " + target: true}}
	var out strings.Builder
	if err := Install(o, r.run, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"launchctl print " + target, "launchctl enable " + target, "launchctl bootstrap gui/501 " + o.Path()}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("first install calls %v", r.calls)
	}
	// Unchanged and loaded → nothing to do.
	r = &recorder{}
	out.Reset()
	if err := Install(o, r.run, &out); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || !strings.Contains(out.String(), "already loaded") {
		t.Fatalf("second install: %v %s", r.calls, out.String())
	}
	// Changed and loaded → bootout, then bootstrap again.
	o.Exe = "/opt/muster/bin/muster-agent"
	r = &recorder{}
	if err := Install(o, r.run, &out); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 4 || r.calls[1] != "launchctl bootout "+target || r.calls[3] != "launchctl bootstrap gui/501 "+o.Path() {
		t.Fatalf("update calls %v", r.calls)
	}

	// LaunchDaemon uses the system domain.
	d := opts("darwin", false)
	d.Root = t.TempDir()
	r = &recorder{fail: map[string]bool{"launchctl print system/dev.getmuster.agent": true}}
	if err := Install(d, r.run, &out); err != nil {
		t.Fatal(err)
	}
	if r.calls[2] != "launchctl bootstrap system "+d.Path() {
		t.Fatalf("daemon calls %v", r.calls)
	}
}
