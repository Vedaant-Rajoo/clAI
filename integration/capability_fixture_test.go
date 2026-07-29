package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
	"github.com/Vedaant-Rajoo/clai/internal/provider"
	"github.com/Vedaant-Rajoo/clai/internal/provider/rules"
)

const capabilityFixtureSchema = "capability-fixtures/v1"

type capabilityFixtures struct {
	Schema   string              `json:"schema"`
	Intent   string              `json:"intent"`
	Fixtures []capabilityFixture `json:"fixtures"`
}

type capabilityFixture struct {
	Name        string             `json:"name"`
	RGPresent   bool               `json:"rg_present"`
	Command     string             `json:"command"`
	Explanation string             `json:"explanation"`
	Requirement fixtureRequirement `json:"requirement"`
}

type fixtureRequirement struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func TestMain(m *testing.M) {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	if name == "git" || name == "rg" || name == "bash" {
		runCapabilityProbeShim(name)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runCapabilityProbeShim(name string) {
	if len(os.Args) != 2 || os.Args[1] != "--version" {
		fmt.Fprintln(os.Stderr, "capability fixture shim accepts only --version")
		os.Exit(2)
	}
	mode, err := os.ReadFile(os.Args[0] + ".mode")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	switch strings.TrimSpace(string(mode)) {
	case "version":
		switch name {
		case "git":
			fmt.Println("git version 2.45.1")
		case "rg":
			fmt.Println("ripgrep 14.1.0")
		case "bash":
			fmt.Println("GNU bash, version 5.2.26(1)-release")
		}
	case "overflow":
		fmt.Print(strings.Repeat("x", 4097))
	case "timeout":
		time.Sleep(10 * time.Second)
	default:
		fmt.Fprintln(os.Stderr, "unknown capability fixture mode")
		os.Exit(2)
	}
}

func TestControlledCapabilityFixtureRealCollector(t *testing.T) {
	fixtures := loadCapabilityFixtures(t)
	if fixtures.Schema != capabilityFixtureSchema {
		t.Fatalf("fixture schema = %q, want %q", fixtures.Schema, capabilityFixtureSchema)
	}
	if fixtures.Intent != "search for TODO" || len(fixtures.Fixtures) != 2 {
		t.Fatalf("fixture identity = intent %q, count %d", fixtures.Intent, len(fixtures.Fixtures))
	}

	for _, fixture := range fixtures.Fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			directory := t.TempDir()
			installProbeShim(t, directory, "git", "version")
			installProbeShim(t, directory, "bash", "overflow")
			installPresenceShim(t, directory, "grep")
			if fixture.RGPresent {
				installProbeShim(t, directory, "rg", "timeout")
			}
			t.Setenv("PATH", directory)
			t.Setenv("SHELL", filepath.Join(directory, "hostile-shell-must-not-run"))

			started := time.Now()
			inventory := capability.Collect(t.Context(), "bash")
			elapsed := time.Since(started)

			assertCapabilityFixtureInventory(t, inventory, directory, fixture.RGPresent, elapsed)
			candidates, err := (rules.Provider{}).Compile(t.Context(), provider.Request{
				Intent:       fixtures.Intent,
				Capabilities: inventory,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := provider.Candidate{
				Command:     fixture.Command,
				Explanation: fixture.Explanation,
				Requirements: []capability.Requirement{{
					Kind: capability.RequirementKind(fixture.Requirement.Kind),
					Name: fixture.Requirement.Name,
				}},
			}
			if len(candidates) != 1 || !reflect.DeepEqual(candidates[0], want) {
				t.Fatalf("candidates = %#v, want %#v", candidates, want)
			}
		})
	}
}

func assertCapabilityFixtureInventory(t *testing.T, inventory capability.Inventory, directory string, rgPresent bool, elapsed time.Duration) {
	t.Helper()
	git, _ := inventory.LookupTool("git")
	if !git.Present || git.Path != shimPath(directory, "git") || git.Version.String() != "2.45.1" {
		t.Fatalf("git fact = %#v, want fixed shim path and parsed version", git)
	}
	bash, _ := inventory.LookupTool("bash")
	if !bash.Present || bash.Path != shimPath(directory, "bash") || bash.Version.Known() {
		t.Fatalf("bash fact = %#v, want present at fixed shim path with overflow-hidden version", bash)
	}
	rg, _ := inventory.LookupTool("rg")
	if rgPresent {
		if !rg.Present || rg.Path != shimPath(directory, "rg") || rg.Version.Known() {
			t.Fatalf("rg fact = %#v, want timed-out fixed shim with unknown version", rg)
		}
		if elapsed < time.Second || elapsed > 3*time.Second {
			t.Fatalf("real collector timeout duration = %s, want bounded timeout within 1-3s", elapsed)
		}
	} else if rg.Present || rg.Path != "" || rg.Version.Known() {
		t.Fatalf("absent rg fact = %#v", rg)
	}
	grep, _ := inventory.LookupTool("grep")
	if !grep.Present || grep.Path != shimPath(directory, "grep") || grep.Version.Known() {
		t.Fatalf("grep fact = %#v, want presence-only fixed shim", grep)
	}
	for _, fact := range inventory.Tools() {
		wantPresent := fact.Name == "git" || fact.Name == "bash" || fact.Name == "grep" || (fact.Name == "rg" && rgPresent)
		if fact.Present != wantPresent {
			t.Fatalf("ambient capability leaked into fixture: %#v", fact)
		}
	}
}

func loadCapabilityFixtures(t *testing.T) capabilityFixtures {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve capability fixture source path")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "testdata", "capability-fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures capabilityFixtures
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func installProbeShim(t *testing.T, directory, name, mode string) {
	t.Helper()
	path := installExecutableProbeShim(t, directory, name)
	if err := os.WriteFile(path+".mode", []byte(mode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func installPresenceShim(t *testing.T, directory, name string) string {
	t.Helper()
	path := shimPath(directory, name)
	if err := os.WriteFile(path, []byte("presence-only fixture; fixed collector must not execute this file\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func installExecutableProbeShim(t *testing.T, directory, name string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := shimPath(directory, name)
	if err := os.Link(executable, path); err == nil {
		return path
	}

	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func shimPath(directory, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(directory, name)
}
