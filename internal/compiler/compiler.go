package compiler

import (
	"strings"

	"github.com/Vedaant-Rajoo/clai/internal/capability"
	machinecontext "github.com/Vedaant-Rajoo/clai/internal/context"
)

type Request struct {
	Intent       string
	Context      machinecontext.Context
	Capabilities capability.Inventory
}

type Result struct {
	Command      string
	Explanation  string
	Requirements []capability.Requirement
}

func Compile(request Request) Result {
	normalized := strings.ToLower(request.Intent)

	switch {
	case normalized == "search for todo":
		if toolPresent(request.Capabilities, "rg") {
			return resultWithTool("rg TODO", "Selected rg because it is installed.", "rg")
		}
		if toolPresent(request.Capabilities, "grep") {
			return resultWithTool("grep -r TODO .", "Selected grep fallback because rg is absent.", "grep")
		}
		return resultWithTool("rg TODO", "Searches for TODO using rg, which may not be installed.", "rg")
	case containsAny(normalized, "git status", "repo status", "repository status", "working tree", "changed files", "the changes", "status"):
		if !request.Context.GitRepository {
			return Result{}
		}
		return resultWithTool("git status", "Uses Git because the current directory is inside a Git repository.", "git")
	case containsAny(normalized, "git diff", "what changed", "show diff", "unstaged changes"):
		if !request.Context.GitRepository {
			return Result{}
		}
		return resultWithTool("git diff", "Shows unstaged changes because the current directory is inside a Git repository.", "git")
	case containsAny(normalized, "staged", "cached diff", "diff cached"):
		if !request.Context.GitRepository {
			return Result{}
		}
		return resultWithTool("git diff --cached", "Shows staged changes that would be included in the next commit.", "git")
	case containsAny(normalized, "git log", "commit history", "recent commits", "history"):
		if !request.Context.GitRepository {
			return Result{}
		}
		return resultWithTool("git log --oneline -10", "Shows the ten most recent commits in a compact format.", "git")
	case containsAny(normalized, "current branch", "git branch", "branch name"):
		if !request.Context.GitRepository {
			return Result{}
		}
		return resultWithTool("git branch --show-current", "Prints the current Git branch because the current directory is inside a Git repository.", "git")
	case containsAny(normalized, "git remote", "remotes", "remote url"):
		if !request.Context.GitRepository {
			return Result{}
		}
		return resultWithTool("git remote -v", "Lists configured Git remotes and their URLs.", "git")
	case containsAny(normalized, "tags", "git tags"):
		if !request.Context.GitRepository {
			return Result{}
		}
		return resultWithTool("git tag --list", "Lists Git tags in the current repository.", "git")
	case containsAny(normalized, "pwd", "current directory", "where am i"):
		return Result{Command: "pwd", Explanation: "Prints the current working directory."}
	case containsAny(normalized, "hidden files", "all files", "list files", "show files", "the files"):
		return Result{Command: "ls -la", Explanation: "Lists files in the current directory, including hidden files."}
	case containsAny(normalized, "directories", "folders", "list dirs", "list folders"):
		return Result{Command: "find . -type d", Explanation: "Lists directories under the current directory."}
	case containsAny(normalized, "count files", "number of files"):
		return Result{Command: "find . -type f | wc -l", Explanation: "Counts regular files under the current directory."}
	case containsAny(normalized, "recent files", "modified today", "new files"):
		return Result{Command: "find . -type f -mtime -1", Explanation: "Lists files modified within the last day."}
	case containsAny(normalized, "large files", "big files", "largest files"):
		return Result{Command: "du -ah . | sort -hr | head -20", Explanation: "Shows the largest files and directories under the current directory."}
	case containsAny(normalized, "directory size", "folder size", "size of this"):
		return Result{Command: "du -sh .", Explanation: "Shows the total size of the current directory."}
	case containsAny(normalized, "disk space", "free space", "storage"):
		return Result{Command: "df -h", Explanation: "Shows disk usage for mounted filesystems in human-readable units."}
	case containsAny(normalized, "todo", "fixme"):
		return resultWithTool("rg 'TODO|FIXME'", "Searches the current directory for TODO and FIXME comments.", "rg")
	case containsAny(normalized, "search", "find text", "grep"):
		return resultWithTool("rg <pattern>", "Searches files recursively for a text pattern. Replace <pattern> before accepting.", "rg")
	case containsAny(normalized, "go test", "run tests", "tests"):
		return Result{Command: "go test ./...", Explanation: "Runs all Go tests in the current module."}
	case containsAny(normalized, "go vet", "vet"):
		return Result{Command: "go vet ./...", Explanation: "Runs Go's static analysis checks across the module."}
	case containsAny(normalized, "go modules", "list packages", "go packages"):
		return Result{Command: "go list ./...", Explanation: "Lists all Go packages in the current module."}
	case containsAny(normalized, "processes", "running processes"):
		return Result{Command: "ps aux", Explanation: "Lists running processes for all users."}
	case containsAny(normalized, "ports", "listening ports", "open ports"):
		return resultWithTool("lsof -iTCP -sTCP:LISTEN -n -P", "Lists processes listening on TCP ports.", "lsof")
	case containsAny(normalized, "environment", "env vars", "environment variables"):
		return Result{Command: "printenv", Explanation: "Prints environment variables available to the shell."}
	case containsAny(normalized, "path variable", "show path"):
		return Result{Command: "printenv PATH", Explanation: "Prints the shell PATH value."}
	case containsAny(normalized, "which shell", "current shell", "shell"):
		return Result{Command: "printenv SHELL", Explanation: "Prints the configured login shell path."}
	case containsAny(normalized, "hostname", "machine name"):
		return Result{Command: "hostname", Explanation: "Prints the machine hostname."}
	case containsAny(normalized, "ip address", "network interfaces", "network"):
		return resultWithTool("ifconfig", "Shows network interface configuration.", "ifconfig")
	case containsAny(normalized, "date", "time"):
		return Result{Command: "date", Explanation: "Prints the current date and time."}
	case containsAny(normalized, "docker containers", "containers"):
		return Result{Command: "docker ps", Explanation: "Lists running Docker containers."}
	case containsAny(normalized, "docker images", "images"):
		return Result{Command: "docker images", Explanation: "Lists local Docker images."}
	default:
		return Result{}
	}
}

func resultWithTool(command, explanation, tool string) Result {
	return Result{
		Command:     command,
		Explanation: explanation,
		Requirements: []capability.Requirement{{
			Kind: capability.RequirementTool,
			Name: tool,
		}},
	}
}

func toolPresent(inventory capability.Inventory, name string) bool {
	tool, found := inventory.LookupTool(name)
	return found && tool.Present
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
