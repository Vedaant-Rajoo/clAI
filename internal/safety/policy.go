package safety

import (
	"slices"
	"strings"
)

type Decision string

const (
	Allow Decision = "allow"
	Warn  Decision = "warn"
	Block Decision = "block"
)

type Result struct {
	Decision Decision
	Reasons  []string
}

func Evaluate(command string) Result {
	normalized := strings.ToLower(strings.TrimSpace(command))
	first := firstToken(normalized)

	switch {
	case normalized == "":
		return Result{Decision: Block, Reasons: []string{"Empty command."}}
	case firstIs(first, "rm", "sudo", "dd", "mkfs", "shutdown", "reboot", "kill", "pkill"):
		return Result{Decision: Block, Reasons: []string{"Command starts with a blocked executable."}}
	case containsAny(normalized, "git reset --hard", "git clean", "| sh", "| bash"):
		return Result{Decision: Block, Reasons: []string{"Command contains a blocked destructive pattern."}}
	case strings.Contains(normalized, "curl ") && strings.Contains(normalized, "|"):
		return Result{Decision: Block, Reasons: []string{"Piping curl output is blocked by local policy."}}
	case strings.Contains(normalized, "wget ") && strings.Contains(normalized, "|"):
		return Result{Decision: Block, Reasons: []string{"Piping wget output is blocked by local policy."}}
	case firstIs(first, "mv", "cp", "chmod", "chown", "mkdir", "touch", "curl", "wget"):
		return Result{Decision: Warn, Reasons: []string{"Command may modify files, permissions, or download remote content."}}
	case startsWithAny(normalized, "git add", "git commit", "git checkout", "git switch", "git merge", "git rebase"):
		return Result{Decision: Warn, Reasons: []string{"Git command may modify repository state."}}
	case startsWithAny(normalized, "docker run", "docker build", "docker pull"):
		return Result{Decision: Warn, Reasons: []string{"Docker command may create containers, build images, or download remote content."}}
	case strings.Contains(normalized, ">"):
		return Result{Decision: Warn, Reasons: []string{"Command redirects output and may write to a file."}}
	case firstIs(first, "pwd", "ls", "find", "rg", "du", "df", "date", "hostname", "go", "ps", "lsof", "printenv", "printf", "ifconfig", "echo"):
		return Result{Decision: Allow, Reasons: []string{"Command appears to be read-only."}}
	case startsWithAny(normalized, "git status", "git diff", "git log", "git branch", "git remote", "git tag", "docker ps", "docker images"):
		return Result{Decision: Allow, Reasons: []string{"Command appears to be read-only."}}
	default:
		return Result{Decision: Warn, Reasons: []string{"Command is not recognized by the local safety policy."}}
	}
}

func firstToken(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}

	return fields[0]
}

func firstIs(first string, values ...string) bool {
	return slices.Contains(values, first)
}

func startsWithAny(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}

	return false
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}

	return false
}
