package compiler

import (
	"strings"
	"testing"

	machinecontext "codeberg.org/newedia/clai/internal/context"
)

var gitRepo = machinecontext.Context{GitRepository: true}
var noRepo = machinecontext.Context{}

func TestCompile(t *testing.T) {
	tests := []struct {
		name    string
		request Request
		command string
	}{
		// Git rules, in-repo.
		{"status in git repository", Request{Intent: "show status", Context: gitRepo}, "git status"},
		{"repo status", Request{Intent: "working tree changes", Context: gitRepo}, "git status"},
		{"status outside git repository", Request{Intent: "show status", Context: noRepo}, "ls -la"},
		{"git diff", Request{Intent: "what changed", Context: gitRepo}, "git diff"},
		{"git diff outside repo", Request{Intent: "show diff", Context: noRepo}, `echo "No Git repository detected"`},
		{"staged diff", Request{Intent: "show staged changes", Context: gitRepo}, "git diff --cached"},
		{"cached diff outside repo", Request{Intent: "cached diff", Context: noRepo}, `echo "No Git repository detected"`},
		{"git log", Request{Intent: "recent commits", Context: gitRepo}, "git log --oneline -10"},
		{"git log outside repo", Request{Intent: "commit history", Context: noRepo}, `echo "No Git repository detected"`},
		{"current branch", Request{Intent: "current branch", Context: gitRepo}, "git branch --show-current"},
		{"branch outside repo", Request{Intent: "branch name", Context: noRepo}, `echo "No Git repository detected"`},
		{"remotes", Request{Intent: "git remote", Context: gitRepo}, "git remote -v"},
		{"remotes outside repo", Request{Intent: "remote url", Context: noRepo}, `echo "No Git repository detected"`},
		{"tags", Request{Intent: "list git tags", Context: gitRepo}, "git tag --list"},
		{"tags outside repo", Request{Intent: "tags", Context: noRepo}, `echo "No Git repository detected"`},

		// Non-git rules.
		{"pwd", Request{Intent: "where am i"}, "pwd"},
		{"list files", Request{Intent: "show files"}, "ls -la"},
		{"hidden files", Request{Intent: "hidden files"}, "ls -la"},
		{"directories", Request{Intent: "list folders"}, "find . -type d"},
		{"count files", Request{Intent: "count files"}, "find . -type f | wc -l"},
		{"recent files", Request{Intent: "modified today"}, "find . -type f -mtime -1"},
		{"large files", Request{Intent: "largest files"}, "du -ah . | sort -hr | head -20"},
		{"directory size", Request{Intent: "folder size"}, "du -sh ."},
		{"disk space", Request{Intent: "free space"}, "df -h"},
		{"todo", Request{Intent: "show todo"}, "rg TODO"},
		{"fixme", Request{Intent: "show fixme"}, "rg TODO"},
		{"search", Request{Intent: "find text"}, "rg <pattern>"},
		{"grep", Request{Intent: "grep"}, "rg <pattern>"},
		{"go test", Request{Intent: "run tests"}, "go test ./..."},
		{"go vet", Request{Intent: "go vet"}, "go vet ./..."},
		{"go packages", Request{Intent: "list packages"}, "go list ./..."},
		{"processes", Request{Intent: "running processes"}, "ps aux"},
		{"ports", Request{Intent: "open ports"}, "lsof -iTCP -sTCP:LISTEN -n -P"},
		{"environment", Request{Intent: "env vars"}, "printenv"},
		{"path variable", Request{Intent: "show path"}, "printf '%s\n' \"$PATH\""},
		{"which shell", Request{Intent: "which shell"}, "printf '%s\n' \"$SHELL\""},
		{"hostname", Request{Intent: "machine name"}, "hostname"},
		{"network", Request{Intent: "ip address"}, "ifconfig"},
		{"date", Request{Intent: "what time is it"}, "date"},
		{"docker containers", Request{Intent: "docker containers"}, "docker ps"},
		{"docker images", Request{Intent: "docker images"}, "docker images"},

		// Fallback.
		{"fallback", Request{Intent: "make me coffee"}, `echo "No suggestion available yet"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Compile(tt.request)
			if result.Command != tt.command {
				t.Fatalf("Compile(%+v).Command = %q, want %q", tt.request, result.Command, tt.command)
			}

			if result.Explanation == "" {
				t.Fatalf("Compile(%+v).Explanation is empty", tt.request)
			}
		})
	}
}

func TestCompileIsCaseInsensitive(t *testing.T) {
	result := Compile(Request{Intent: "GIT STATUS", Context: gitRepo})
	if result.Command != "git status" {
		t.Fatalf("Compile(uppercase).Command = %q, want %q", result.Command, "git status")
	}
}

func TestNoGitRepositoryResult(t *testing.T) {
	result := noGitRepositoryResult()
	if !strings.Contains(result.Command, "No Git repository detected") {
		t.Fatalf("noGitRepositoryResult().Command = %q", result.Command)
	}
}
