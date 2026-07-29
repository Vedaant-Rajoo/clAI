package capability

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const deadlineMeasurementTolerance = 150 * time.Millisecond

type runnerCall struct {
	path        string
	args        []string
	env         []string
	deadline    time.Time
	hasDeadline bool
}

type fakeRunner struct {
	mu        sync.Mutex
	calls     []runnerCall
	responses map[string]fakeResponse
	run       func(context.Context, string, []string, []string) ([]byte, error)
}

type fakeResponse struct {
	output []byte
	err    error
}

func (runner *fakeRunner) Run(ctx context.Context, path string, args, env []string) ([]byte, error) {
	deadline, hasDeadline := ctx.Deadline()
	runner.mu.Lock()
	runner.calls = append(runner.calls, runnerCall{
		path:        path,
		args:        append([]string(nil), args...),
		env:         append([]string(nil), env...),
		deadline:    deadline,
		hasDeadline: hasDeadline,
	})
	run := runner.run
	response := runner.responses[path]
	runner.mu.Unlock()

	if run != nil {
		return run(ctx, path, args, env)
	}
	return append([]byte(nil), response.output...), response.err
}

func (runner *fakeRunner) Calls() []runnerCall {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]runnerCall(nil), runner.calls...)
}

func TestInventoryCanonicalNormalization(t *testing.T) {
	t.Parallel()

	validOS := []string{"darwin", "linux", "windows", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris", "aix", "plan9", "android", "ios", "js", "wasip1"}
	for _, value := range validOS {
		t.Run("os_"+value, func(t *testing.T) {
			if got := normalizeOS(strings.ToUpper(value)); got != value {
				t.Fatalf("normalizeOS(%q) = %q, want %q", strings.ToUpper(value), got, value)
			}
		})
	}
	for _, value := range []string{"", "unix", "linux2", " darwin"} {
		t.Run("unknown_os_"+value, func(t *testing.T) {
			if got := normalizeOS(value); got != "unknown" {
				t.Fatalf("normalizeOS(%q) = %q, want unknown", value, got)
			}
		})
	}

	shellTests := []struct {
		name   string
		widget string
		shell  string
		want   ShellIdentity
	}{
		{name: "widget_fish_wins", widget: "fish", shell: "/hostile/bash", want: ShellIdentity{Family: ShellFish, Provenance: ShellFromWidget}},
		{name: "widget_bash_wins", widget: "bash", shell: "/hostile/zsh", want: ShellIdentity{Family: ShellBash, Provenance: ShellFromWidget}},
		{name: "widget_zsh_wins", widget: "zsh", shell: "/hostile/fish", want: ShellIdentity{Family: ShellZsh, Provenance: ShellFromWidget}},
		{name: "absolute_fish", shell: "/usr/local/bin/FISH", want: ShellIdentity{Family: ShellFish, Path: "/usr/local/bin/FISH", Provenance: ShellFromEnv}},
		{name: "absolute_bash", shell: "/bin/bash", want: ShellIdentity{Family: ShellBash, Path: "/bin/bash", Provenance: ShellFromEnv}},
		{name: "absolute_zsh", shell: "/bin/zsh", want: ShellIdentity{Family: ShellZsh, Path: "/bin/zsh", Provenance: ShellFromEnv}},
		{name: "empty", want: ShellIdentity{Family: ShellUnknown, Provenance: ShellFromEnv}},
		{name: "relative", shell: "bin/bash", want: ShellIdentity{Family: ShellUnknown, Provenance: ShellFromEnv}},
		{name: "alias", shell: "/bin/sh", want: ShellIdentity{Family: ShellUnknown, Path: "/bin/sh", Provenance: ShellFromEnv}},
		{name: "suffix", shell: "/bin/bash.exe", want: ShellIdentity{Family: ShellUnknown, Path: "/bin/bash.exe", Provenance: ShellFromEnv}},
		{name: "unknown_widget_falls_back", widget: "sh", shell: "/bin/zsh", want: ShellIdentity{Family: ShellZsh, Path: "/bin/zsh", Provenance: ShellFromEnv}},
	}
	for _, test := range shellTests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeShell(test.widget, test.shell); got != test.want {
				t.Fatalf("normalizeShell(%q, %q) = %#v, want %#v", test.widget, test.shell, got, test.want)
			}
		})
	}
}

func TestInventoryFixedAllowlistAndLookPathOnly(t *testing.T) {
	t.Parallel()

	wantNames := []string{"git", "rg", "fd", "jq", "curl", "wget", "tar", "sed", "awk", "grep", "lsof", "ifconfig", "ip", "bash"}
	lookups := make([]string, 0, len(wantNames))
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"/resolved/git":  {output: []byte("git version 2.45.1\n")},
		"/resolved/rg":   {output: []byte("ripgrep 14.1.0\n")},
		"/resolved/bash": {output: []byte("GNU bash, version 5.2.26(1)-release\n")},
	}}
	collector := collector{
		runner: runner,
		lookPath: func(name string) (string, error) {
			lookups = append(lookups, name)
			if name == "jq" {
				return "", errors.New("absent")
			}
			return "/resolved/" + name, nil
		},
		goos:   "DARWIN",
		goarch: "ARM64",
		getenv: func(name string) string {
			switch name {
			case "PATH":
				return "/safe/bin"
			case "SHELL":
				return "/hostile/rg"
			default:
				return ""
			}
		},
	}

	inventory := collector.collect(context.Background(), "")
	if inventory.Version() != InventoryVersion || inventory.OSFamily() != "darwin" || inventory.Arch() != "arm64" {
		t.Fatalf("inventory identity = %q, %q, %q", inventory.Version(), inventory.OSFamily(), inventory.Arch())
	}
	if got := inventory.Shell(); got != (ShellIdentity{Family: ShellUnknown, Path: "/hostile/rg", Provenance: ShellFromEnv}) {
		t.Fatalf("Shell() = %#v", got)
	}
	if !reflect.DeepEqual(lookups, wantNames) {
		t.Fatalf("LookPath order = %#v, want %#v", lookups, wantNames)
	}

	tools := inventory.Tools()
	gotNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		gotNames = append(gotNames, tool.Name)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("tool order = %#v, want %#v", gotNames, wantNames)
	}
	jq, _ := inventory.LookupTool("jq")
	if jq.Present || jq.Path != "" || jq.Version.Known() {
		t.Fatalf("absent jq fact = %#v", jq)
	}

	calls := runner.Calls()
	if len(calls) != 3 {
		t.Fatalf("runner calls = %d, want 3: %#v", len(calls), calls)
	}
	for index, name := range []string{"git", "rg", "bash"} {
		call := calls[index]
		if call.path != "/resolved/"+name {
			t.Errorf("call %d path = %q, want LookPath result", index, call.path)
		}
		if !reflect.DeepEqual(call.args, []string{"--version"}) {
			t.Errorf("call %d args = %#v, want [--version]", index, call.args)
		}
		if !reflect.DeepEqual(call.env, []string{"PATH=/safe/bin", "LC_ALL=C"}) {
			t.Errorf("call %d env = %#v", index, call.env)
		}
		if !call.hasDeadline {
			t.Errorf("call %d has no deadline", index)
		} else if remaining := time.Until(call.deadline); remaining > probeExecutionTimeout || remaining < probeExecutionTimeout-250*time.Millisecond {
			t.Errorf("call %d deadline remaining = %s, want approximately %s", index, remaining, probeExecutionTimeout)
		}
		if call.path == "/hostile/rg" {
			t.Errorf("hostile SHELL path was executed")
		}
	}
}

func TestInventoryProbeBoundsAndExactParsing(t *testing.T) {
	t.Run("parsers", func(t *testing.T) {
		tests := []struct {
			name   string
			parse  func(string) (Version, bool)
			output string
			want   Version
			ok     bool
		}{
			{name: "git", parse: parseGitVersion, output: "git version 2.45.1\nignored 9.9", want: "2.45.1", ok: true},
			{name: "git_four_components", parse: parseGitVersion, output: "git version 1.2.3.4", want: "1.2.3.4", ok: true},
			{name: "git_wrong_prefix", parse: parseGitVersion, output: "x git version 2.45", ok: false},
			{name: "git_second_line", parse: parseGitVersion, output: "noise\ngit version 2.45", ok: false},
			{name: "rg", parse: parseRipgrepVersion, output: "ripgrep 14.1.0\nfeatures", want: "14.1.0", ok: true},
			{name: "rg_wrong_prefix", parse: parseRipgrepVersion, output: "rg 14.1.0", ok: false},
			{name: "bash_contains", parse: parseBashVersion, output: "GNU bash, version 5.2.26(1)-release", want: "5.2.26", ok: true},
			{name: "bash_second_line", parse: parseBashVersion, output: "GNU bash\nversion 5.2.26", ok: false},
			{name: "empty", parse: parseGitVersion, output: "", ok: false},
			{name: "leading_v", parse: parseGitVersion, output: "git version v2.45", ok: false},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				got, ok := test.parse(test.output)
				if got != test.want || ok != test.ok {
					t.Fatalf("parse(%q) = %q, %v; want %q, %v", test.output, got, ok, test.want, test.ok)
				}
			})
		}
	})

	t.Run("unknown_version_failures", func(t *testing.T) {
		runner := &fakeRunner{responses: map[string]fakeResponse{
			"/git":  {output: []byte(strings.Repeat("x", probeOutputLimit+1))},
			"/rg":   {err: errors.New("nonzero")},
			"/bash": {output: []byte("malformed\n")},
		}}
		inventory := fixtureCollector(runner).collect(context.Background(), "")
		for _, name := range []string{"git", "rg", "bash"} {
			fact, _ := inventory.LookupTool(name)
			if !fact.Present || fact.Version.Known() {
				t.Errorf("%s fact = %#v, want present with unknown version", name, fact)
			}
		}
	})

	t.Run("absent_tool_skips_its_probe", func(t *testing.T) {
		runner := &fakeRunner{responses: map[string]fakeResponse{
			"/git":  {output: []byte("git version 2.45.1\n")},
			"/bash": {output: []byte("GNU bash, version 5.2.26(1)-release\n")},
		}}
		collector := fixtureCollector(runner)
		collector.lookPath = func(name string) (string, error) {
			switch name {
			case "git", "bash":
				return "/" + name, nil
			default:
				return "", errors.New("absent")
			}
		}
		inventory := collector.collect(context.Background(), "")
		for _, call := range runner.Calls() {
			if call.path == "/rg" || strings.Contains(call.path, "rg") {
				t.Fatalf("absent rg was probed: %#v", call)
			}
		}
		if len(runner.Calls()) != 2 {
			t.Fatalf("runner calls = %d, want probes only for present git and bash", len(runner.Calls()))
		}
		rg, _ := inventory.LookupTool("rg")
		if rg.Present || rg.Version.Known() {
			t.Fatalf("absent rg fact = %#v", rg)
		}
	})

	t.Run("output_limit_exact_boundary", func(t *testing.T) {
		atLimit := "git version 2.45.1\n" + strings.Repeat("x", probeOutputLimit-len("git version 2.45.1\n"))
		if len(atLimit) != probeOutputLimit {
			t.Fatalf("fixture length = %d, want exactly %d", len(atLimit), probeOutputLimit)
		}
		runner := &fakeRunner{responses: map[string]fakeResponse{
			"/git":  {output: []byte(atLimit)},
			"/rg":   {output: []byte("ripgrep 14.1.0\n" + strings.Repeat("x", probeOutputLimit-len("ripgrep 14.1.0\n")+1))},
			"/bash": {output: []byte("GNU bash, version 5.2.26(1)-release\n")},
		}}
		inventory := fixtureCollector(runner).collect(context.Background(), "")
		git, _ := inventory.LookupTool("git")
		if !git.Present || git.Version != "2.45.1" {
			t.Fatalf("git at exactly %d bytes = %#v, want parsed version", probeOutputLimit, git)
		}
		rg, _ := inventory.LookupTool("rg")
		if !rg.Present || rg.Version.Known() {
			t.Fatalf("rg at %d bytes = %#v, want unknown version", probeOutputLimit+1, rg)
		}
	})

	t.Run("per_probe_and_aggregate_deadlines", func(t *testing.T) {
		started := time.Now()
		runner := &fakeRunner{}
		runner.run = func(ctx context.Context, _ string, _ []string, _ []string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		inventory := fixtureCollector(runner).collect(context.Background(), "")
		elapsed := time.Since(started)
		if elapsed < aggregateExecutionLimit-100*time.Millisecond || elapsed > aggregateTimeout+deadlineMeasurementTolerance {
			t.Fatalf("aggregate probe duration = %s, want within normative %s bound", elapsed, aggregateTimeout)
		}
		if len(runner.Calls()) != 3 {
			t.Fatalf("runner calls = %d, want 3 bounded calls within aggregate execution budget", len(runner.Calls()))
		}
		for _, name := range []string{"git", "rg", "bash"} {
			fact, _ := inventory.LookupTool(name)
			if !fact.Present || fact.Version.Known() {
				t.Errorf("%s fact after timeout = %#v", name, fact)
			}
		}
	})

	t.Run("caller_cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &fakeRunner{}
		inventory := fixtureCollector(runner).collect(ctx, "")
		if len(runner.Calls()) != 0 {
			t.Fatalf("cancelled collection made %d runner calls", len(runner.Calls()))
		}
		git, _ := inventory.LookupTool("git")
		if !git.Present || git.Version.Known() {
			t.Fatalf("cancelled git fact = %#v", git)
		}
	})
}

func TestInventoryProcessInvocationLifetime(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	calls := 0
	cached := newCached(func(context.Context) Inventory {
		mu.Lock()
		calls++
		mu.Unlock()
		return Inventory{
			version: InventoryVersion,
			tools:   []ToolFact{{Name: "git", Present: true}},
		}
	})

	const callers = 16
	results := make(chan Inventory, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- cached.Inventory(context.Background())
		}()
	}
	wait.Wait()
	close(results)

	for inventory := range results {
		tools := inventory.Tools()
		tools[0].Name = "mutated"
	}
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("collector calls = %d, want 1", gotCalls)
	}
	if fact, _ := cached.Inventory(context.Background()).LookupTool("git"); fact.Name != "git" {
		t.Fatalf("cached inventory was mutated: %#v", fact)
	}
}

func TestDirectRunnerRejectsOverflow(t *testing.T) {
	t.Parallel()

	t.Run("overflow", func(t *testing.T) {
		output, err := runProbeHelper("overflow")
		if !errors.Is(err, errProbeOutputTooLarge) {
			t.Fatalf("directRunner overflow error = %v, want %v; output bytes=%d", err, errProbeOutputTooLarge, len(output))
		}
	})

	t.Run("stderr_discarded", func(t *testing.T) {
		output, err := runProbeHelper("stderr")
		if err != nil {
			t.Fatalf("directRunner stderr-only error = %v", err)
		}
		if len(output) != 0 {
			t.Fatalf("directRunner stderr-only output = %q, want empty", output)
		}
	})
}

func TestDirectRunnerDeadlineWithInheritedPipes(t *testing.T) {
	syncPath := filepath.Join(t.TempDir(), "descendant-started")
	ctx, cancel := context.WithTimeout(context.Background(), probeExecutionTimeout)
	defer cancel()

	started := time.Now()
	_, err := runSynchronizedProbeHelper(ctx, "spawn-descendant-wait", syncPath)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("directRunner returned nil error while cancelled child and descendant retained output pipes")
	}
	if elapsed < probeExecutionTimeout-100*time.Millisecond {
		t.Fatalf("directRunner returned after %s before the probe execution deadline", elapsed)
	}
	if elapsed > probeTimeout+deadlineMeasurementTolerance {
		t.Fatalf("directRunner returned after %s, exceeding normative %s probe bound", elapsed, probeTimeout)
	}
	if _, statErr := os.Stat(syncPath); statErr != nil {
		t.Fatalf("descendant synchronization marker: %v", statErr)
	}
}

type synchronizedHelperRunner struct {
	directory string
	mu        sync.Mutex
	calls     int
}

func (runner *synchronizedHelperRunner) Run(ctx context.Context, _ string, _ []string, env []string) ([]byte, error) {
	runner.mu.Lock()
	runner.calls++
	call := runner.calls
	runner.mu.Unlock()

	syncPath := filepath.Join(runner.directory, fmt.Sprintf("descendant-started-%d", call))
	return (directRunner{}).Run(
		ctx,
		os.Args[0],
		[]string{
			"-test.run=^TestCapabilityProbeHelperProcess$",
			"--",
			"--capability-helper=spawn-descendant-wait",
			"--capability-sync=" + syncPath,
		},
		env,
	)
}

func (runner *synchronizedHelperRunner) Calls() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.calls
}

func TestCollectorAggregateDeadlineWithInheritedPipes(t *testing.T) {
	directory := t.TempDir()
	runner := &synchronizedHelperRunner{directory: directory}
	started := time.Now()
	inventory := fixtureCollector(runner).collect(context.Background(), "")
	elapsed := time.Since(started)

	if elapsed < aggregateExecutionLimit-200*time.Millisecond {
		t.Fatalf("collector returned after %s before exercising the aggregate execution budget", elapsed)
	}
	if elapsed > aggregateTimeout+deadlineMeasurementTolerance {
		t.Fatalf("collector returned after %s, exceeding normative %s aggregate bound", elapsed, aggregateTimeout)
	}
	if runner.Calls() < 1 {
		t.Fatal("collector did not execute a synchronized helper probe")
	}
	for call := 1; call <= runner.Calls(); call++ {
		syncPath := filepath.Join(directory, fmt.Sprintf("descendant-started-%d", call))
		if _, err := os.Stat(syncPath); err != nil {
			t.Fatalf("probe %d descendant synchronization marker: %v", call, err)
		}
	}
	for _, name := range []string{"git", "rg", "bash"} {
		fact, _ := inventory.LookupTool(name)
		if !fact.Present || fact.Version.Known() {
			t.Errorf("%s fact = %#v, want present with unknown version", name, fact)
		}
	}
}

func runProbeHelper(mode string) ([]byte, error) {
	return (directRunner{}).Run(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=^TestCapabilityProbeHelperProcess$", "--", "--capability-helper=" + mode},
		[]string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"},
	)
}

func runSynchronizedProbeHelper(ctx context.Context, mode, syncPath string) ([]byte, error) {
	return (directRunner{}).Run(
		ctx,
		os.Args[0],
		[]string{
			"-test.run=^TestCapabilityProbeHelperProcess$",
			"--",
			"--capability-helper=" + mode,
			"--capability-sync=" + syncPath,
		},
		[]string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"},
	)
}

func TestCapabilityProbeHelperProcess(t *testing.T) {
	var mode, syncPath string
	for _, argument := range os.Args {
		if value, ok := strings.CutPrefix(argument, "--capability-helper="); ok {
			mode = value
		}
		if value, ok := strings.CutPrefix(argument, "--capability-sync="); ok {
			syncPath = value
		}
	}

	switch mode {
	case "overflow":
		fmt.Print(strings.Repeat("x", probeOutputLimit+1))
	case "stderr":
		fmt.Fprint(os.Stderr, "stderr-only version 9.9.9")
		os.Exit(0)
	case "spawn-descendant-exit", "spawn-descendant-wait":
		startPipeHoldingDescendant(t, syncPath)
		if mode == "spawn-descendant-wait" {
			time.Sleep(10 * time.Second)
		}
	case "hold-pipes":
		if err := os.WriteFile(syncPath, []byte("started"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
}

func startPipeHoldingDescendant(t *testing.T, syncPath string) {
	t.Helper()
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestCapabilityProbeHelperProcess$",
		"--",
		"--capability-helper=hold-pipes",
		"--capability-sync="+syncPath,
	)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(syncPath); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant did not publish synchronization marker %q", syncPath)
		}
		time.Sleep(time.Millisecond)
	}
}

func fixtureCollector(runner Runner) collector {
	return collector{
		runner: runner,
		lookPath: func(name string) (string, error) {
			switch name {
			case "git", "rg", "bash":
				return "/" + name, nil
			default:
				return "", errors.New("absent")
			}
		},
		goos:   "linux",
		goarch: "amd64",
		getenv: func(name string) string {
			if name == "PATH" {
				return "/bin"
			}
			return ""
		},
	}
}
