package compiler

import (
	"strings"
	"testing"

	machinecontext "codeberg.org/newedia/clai/internal/context"
	"codeberg.org/newedia/clai/internal/validate"
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
		{"the changes", Request{Intent: "what are the changes", Context: gitRepo}, "git status"},
		{"status outside git repository", Request{Intent: "show status", Context: noRepo}, ""},
		{"git diff", Request{Intent: "what changed", Context: gitRepo}, "git diff"},
		{"git diff outside repo", Request{Intent: "show diff", Context: noRepo}, ""},
		{"staged diff", Request{Intent: "show staged changes", Context: gitRepo}, "git diff --cached"},
		{"cached diff outside repo", Request{Intent: "cached diff", Context: noRepo}, ""},
		{"git log", Request{Intent: "recent commits", Context: gitRepo}, "git log --oneline -10"},
		{"git log outside repo", Request{Intent: "commit history", Context: noRepo}, ""},
		{"current branch", Request{Intent: "current branch", Context: gitRepo}, "git branch --show-current"},
		{"branch outside repo", Request{Intent: "branch name", Context: noRepo}, ""},
		{"remotes", Request{Intent: "git remote", Context: gitRepo}, "git remote -v"},
		{"remotes outside repo", Request{Intent: "remote url", Context: noRepo}, ""},
		{"tags", Request{Intent: "list git tags", Context: gitRepo}, "git tag --list"},
		{"tags outside repo", Request{Intent: "tags", Context: noRepo}, ""},

		// Non-git rules.
		{"pwd", Request{Intent: "where am i"}, "pwd"},
		{"list files", Request{Intent: "show files"}, "ls -la"},
		{"the files", Request{Intent: "show me the files"}, "ls -la"},
		{"hidden files", Request{Intent: "hidden files"}, "ls -la"},
		{"directories", Request{Intent: "list folders"}, "find . -type d"},
		{"count files", Request{Intent: "count files"}, "find . -type f | wc -l"},
		{"recent files", Request{Intent: "modified today"}, "find . -type f -mtime -1"},
		{"large files", Request{Intent: "largest files"}, "du -ah . | sort -hr | head -20"},
		{"directory size", Request{Intent: "folder size"}, "du -sh ."},
		{"disk space", Request{Intent: "free space"}, "df -h"},
		{"todo", Request{Intent: "show todo"}, "rg 'TODO|FIXME'"},
		{"fixme", Request{Intent: "show fixme"}, "rg 'TODO|FIXME'"},
		{"search", Request{Intent: "find text"}, "rg <pattern>"},
		{"grep", Request{Intent: "grep"}, "rg <pattern>"},
		{"go test", Request{Intent: "run tests"}, "go test ./..."},
		{"go vet", Request{Intent: "go vet"}, "go vet ./..."},
		{"go packages", Request{Intent: "list packages"}, "go list ./..."},
		{"processes", Request{Intent: "running processes"}, "ps aux"},
		{"ports", Request{Intent: "open ports"}, "lsof -iTCP -sTCP:LISTEN -n -P"},
		{"environment", Request{Intent: "env vars"}, "printenv"},
		{"path variable", Request{Intent: "show path"}, "printenv PATH"},
		{"which shell", Request{Intent: "which shell"}, "printenv SHELL"},
		{"hostname", Request{Intent: "machine name"}, "hostname"},
		{"network", Request{Intent: "ip address"}, "ifconfig"},
		{"date", Request{Intent: "what time is it"}, "date"},
		{"docker containers", Request{Intent: "docker containers"}, "docker ps"},
		{"docker images", Request{Intent: "docker images"}, "docker images"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Compile(tt.request)
			if result.Command != tt.command {
				t.Fatalf("Compile(%+v).Command = %q, want %q", tt.request, result.Command, tt.command)
			}

			if tt.command != "" && result.Explanation == "" {
				t.Fatalf("Compile(%+v).Explanation is empty", tt.request)
			}
			if tt.command == "" && result.Explanation != "" {
				t.Fatalf("Compile(%+v).Explanation = %q, want empty no-suggestion result", tt.request, result.Explanation)
			}
		})
	}
}

func TestCompileNoSuggestion(t *testing.T) {
	result := Compile(Request{Intent: "make me coffee"})
	if result.Command != "" || result.Explanation != "" {
		t.Fatalf("Compile(no match) = %+v, want empty result", result)
	}
}

func TestCompileIsCaseInsensitive(t *testing.T) {
	result := Compile(Request{Intent: "GIT STATUS", Context: gitRepo})
	if result.Command != "git status" {
		t.Fatalf("Compile(uppercase).Command = %q, want %q", result.Command, "git status")
	}
}

func TestCompiledCommandsPassValidation(t *testing.T) {
	intents := []string{
		"show path",
		"path variable",
		"which shell",
		"current shell",
		"show todo",
		"show fixme",
	}
	for _, intent := range intents {
		t.Run(intent, func(t *testing.T) {
			result := Compile(Request{Intent: intent})
			if result.Command == "" {
				t.Fatalf("Compile(%q).Command is empty, want a suggestion", intent)
			}

			if got := validate.Command(result.Command); !got.Valid {
				t.Fatalf("validate.Command(%q).Valid = false, want true; reasons: %v", result.Command, got.Reasons)
			}
		})
	}
}

func TestFixmeIntentSearchesForFixme(t *testing.T) {
	result := Compile(Request{Intent: "show fixme"})
	if !strings.Contains(result.Command, "FIXME") {
		t.Fatalf("Compile(fixme).Command = %q, want it to contain %q", result.Command, "FIXME")
	}

	if got := validate.Command(result.Command); !got.Valid {
		t.Fatalf("validate.Command(%q).Valid = false, want true; reasons: %v", result.Command, got.Reasons)
	}
}

func TestGitDependentRulesOutsideRepositoryReturnNoSuggestion(t *testing.T) {
	intents := []string{
		"git status",
		"show diff",
		"cached diff",
		"commit history",
		"branch name",
		"remote url",
		"git tags",
	}
	for _, intent := range intents {
		t.Run(intent, func(t *testing.T) {
			result := Compile(Request{Intent: intent, Context: noRepo})
			if result != (Result{}) {
				t.Fatalf("Compile(%q outside Git) = %+v, want empty no-suggestion result", intent, result)
			}
		})
	}
}
