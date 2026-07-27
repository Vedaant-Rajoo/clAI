package capability

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	probeOutputLimit        = 4 * 1024
	probeTimeout            = 1500 * time.Millisecond
	aggregateTimeout        = 3 * time.Second
	probeDrainBudget        = 100 * time.Millisecond
	probeExecutionTimeout   = probeTimeout - probeDrainBudget
	aggregateExecutionLimit = aggregateTimeout - probeDrainBudget
)

var (
	errProbeOutputTooLarge = errors.New("capability probe output exceeds 4 KiB")
	gitVersionPattern      = regexp.MustCompile(`^git version ([0-9]+(?:\.[0-9]+){0,3})`)
	rgVersionPattern       = regexp.MustCompile(`^ripgrep ([0-9]+(?:\.[0-9]+){0,3})`)
	bashVersionPattern     = regexp.MustCompile(`version ([0-9]+(?:\.[0-9]+){0,3})`)
)

type Runner interface {
	Run(ctx context.Context, path string, args, env []string) ([]byte, error)
}

type directRunner struct{}

func (directRunner) Run(ctx context.Context, path string, args, env []string) ([]byte, error) {
	command := exec.CommandContext(ctx, path, args...)
	output := &boundedOutput{limit: probeOutputLimit}
	command.Stdout = output
	command.Stderr = io.Discard
	command.Env = append([]string(nil), env...)
	// Bound pipe draining after cancellation or child exit. Without WaitDelay, a
	// descendant that inherited stdout or stderr could keep Run blocked after the
	// probe process and its context deadline have ended.
	command.WaitDelay = probeDrainBudget

	err := command.Run()
	if output.overflow {
		return nil, errProbeOutputTooLarge
	}
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), output.data...), nil
}

type boundedOutput struct {
	data     []byte
	limit    int
	overflow bool
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	if output.overflow {
		return 0, errProbeOutputTooLarge
	}
	remaining := output.limit - len(output.data)
	if len(data) > remaining {
		if remaining > 0 {
			output.data = append(output.data, data[:remaining]...)
		}
		output.overflow = true
		return remaining, errProbeOutputTooLarge
	}
	output.data = append(output.data, data...)
	return len(data), nil
}

type collector struct {
	runner   Runner
	lookPath func(string) (string, error)
	goos     string
	goarch   string
	getenv   func(string) string
}

func newCollector() collector {
	return collector{
		runner:   directRunner{},
		lookPath: exec.LookPath,
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
		getenv:   os.Getenv,
	}
}

// Collect creates a new inventory. Process-level callers should use Cached so
// every consumer shares one invocation-scoped snapshot.
func Collect(ctx context.Context, widgetShell string) Inventory {
	return newCollector().collect(ctx, widgetShell)
}

func (c collector) collect(ctx context.Context, widgetShell string) Inventory {
	tools := make([]ToolFact, 0, len(fixedToolNames))
	toolIndexes := make(map[string]int, len(fixedToolNames))
	for _, name := range fixedToolNames {
		fact := ToolFact{Name: name}
		if path, err := c.lookPath(name); err == nil {
			fact.Present = true
			fact.Path = path
		}
		toolIndexes[name] = len(tools)
		tools = append(tools, fact)
	}

	// Reserve the pipe-drain budget inside the normative aggregate deadline so a
	// final cancelled probe cannot extend collection beyond aggregateTimeout.
	aggregateContext, cancelAggregate := context.WithTimeout(ctx, aggregateExecutionLimit)
	defer cancelAggregate()

	environment := []string{"PATH=" + c.getenv("PATH"), "LC_ALL=C"}
	for _, probe := range fixedProbes {
		index := toolIndexes[probe.name]
		if !tools[index].Present || aggregateContext.Err() != nil {
			continue
		}

		// Reserve the same budget inside each normative per-probe deadline for
		// cancellation and inherited-pipe draining in directRunner.
		probeContext, cancelProbe := context.WithTimeout(aggregateContext, probeExecutionTimeout)
		output, err := c.runner.Run(
			probeContext,
			tools[index].Path,
			append([]string(nil), probe.args...),
			append([]string(nil), environment...),
		)
		cancelProbe()
		if err != nil || len(output) > probeOutputLimit {
			continue
		}
		if version, ok := probe.parser(string(output)); ok {
			tools[index].Version = version
		}
	}

	return Inventory{
		version:  InventoryVersion,
		osFamily: normalizeOS(c.goos),
		arch:     normalizeArch(c.goarch),
		shell:    normalizeShell(widgetShell, c.getenv("SHELL")),
		tools:    tools,
	}
}

func normalizeOS(value string) string {
	normalized := strings.ToLower(value)
	switch normalized {
	case "darwin", "linux", "windows", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris", "aix", "plan9", "android", "ios", "js", "wasip1":
		return normalized
	default:
		return "unknown"
	}
}

func normalizeArch(value string) string {
	if value == "" {
		return "unknown"
	}
	return strings.ToLower(value)
}

func normalizeShell(widgetShell, shellPath string) ShellIdentity {
	if family := knownShellFamily(widgetShell); family != ShellUnknown {
		return ShellIdentity{Family: family, Provenance: ShellFromWidget}
	}

	identity := ShellIdentity{Family: ShellUnknown, Provenance: ShellFromEnv}
	if !filepath.IsAbs(shellPath) {
		return identity
	}
	identity.Path = shellPath
	identity.Family = knownShellFamily(strings.ToLower(filepath.Base(shellPath)))
	return identity
}

func knownShellFamily(value string) ShellFamily {
	switch value {
	case "fish":
		return ShellFish
	case "bash":
		return ShellBash
	case "zsh":
		return ShellZsh
	default:
		return ShellUnknown
	}
}

func firstLine(output string) string {
	if line, _, found := strings.Cut(output, "\n"); found {
		return strings.TrimSuffix(line, "\r")
	}
	return strings.TrimSuffix(output, "\r")
}

func parseGitVersion(output string) (Version, bool) {
	return parseVersionMatch(gitVersionPattern.FindStringSubmatch(firstLine(output)))
}

func parseRipgrepVersion(output string) (Version, bool) {
	return parseVersionMatch(rgVersionPattern.FindStringSubmatch(firstLine(output)))
}

func parseBashVersion(output string) (Version, bool) {
	return parseVersionMatch(bashVersionPattern.FindStringSubmatch(firstLine(output)))
}

func parseVersionMatch(matches []string) (Version, bool) {
	if len(matches) != 2 {
		return "", false
	}
	return ParseVersion(matches[1])
}

// Cached collects at most one inventory and returns immutable copies of that
// same invocation-scoped snapshot to all callers.
type Cached struct {
	once      sync.Once
	collect   func(context.Context) Inventory
	inventory Inventory
}

func NewCached(widgetShell string) *Cached {
	c := newCollector()
	return newCached(func(ctx context.Context) Inventory {
		return c.collect(ctx, widgetShell)
	})
}

func newCached(collect func(context.Context) Inventory) *Cached {
	return &Cached{collect: collect}
}

func (cached *Cached) Inventory(ctx context.Context) Inventory {
	cached.once.Do(func() {
		cached.inventory = cloneInventory(cached.collect(ctx))
	})
	return cloneInventory(cached.inventory)
}

func cloneInventory(inventory Inventory) Inventory {
	inventory.tools = append([]ToolFact(nil), inventory.tools...)
	return inventory
}
