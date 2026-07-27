package capability

var fixedToolNames = [...]string{
	"git",
	"rg",
	"fd",
	"jq",
	"curl",
	"wget",
	"tar",
	"sed",
	"awk",
	"grep",
	"lsof",
	"ifconfig",
	"ip",
	"bash",
}

// ToolNames returns the capability-inventory/v1 presence allowlist in canonical
// order. The returned slice is independent and may be modified by the caller.
func ToolNames() []string {
	return append([]string(nil), fixedToolNames[:]...)
}

type probeDefinition struct {
	name   string
	args   []string
	parser func(string) (Version, bool)
}

var fixedProbes = [...]probeDefinition{
	{name: "git", args: []string{"--version"}, parser: parseGitVersion},
	{name: "rg", args: []string{"--version"}, parser: parseRipgrepVersion},
	{name: "bash", args: []string{"--version"}, parser: parseBashVersion},
}
