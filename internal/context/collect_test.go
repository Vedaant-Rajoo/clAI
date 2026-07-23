package machinecontext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectFallsBackToConfiguredShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/fish")

	context := Collect()
	if context.Shell != "/bin/fish" {
		t.Fatalf("Shell = %q, want /bin/fish", context.Shell)
	}
	if context.ShellProvenance != "SHELL" {
		t.Fatalf("ShellProvenance = %q, want SHELL", context.ShellProvenance)
	}
}

func TestCollectWithShellOverridesConfiguredShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")

	for _, shell := range []string{"fish", "bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			context := CollectWithShell(shell)
			if context.Shell != shell {
				t.Fatalf("Shell = %q, want %q", context.Shell, shell)
			}
			if context.ShellProvenance != "widget-declared-shell" {
				t.Fatalf("ShellProvenance = %q, want widget-declared-shell", context.ShellProvenance)
			}
		})
	}
}

func TestCollectWithShellRejectsUnsupportedOverride(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")

	context := CollectWithShell("sh")
	if context.Shell != "/bin/zsh" {
		t.Fatalf("Shell = %q, want fallback /bin/zsh", context.Shell)
	}
	if context.ShellProvenance != "SHELL" {
		t.Fatalf("ShellProvenance = %q, want SHELL", context.ShellProvenance)
	}
}

func TestGitInfoNonGitDirectory(t *testing.T) {
	root, branch, ok := gitInfo(t.TempDir())
	if ok {
		t.Fatalf("gitInfo returned ok=true, root=%q, branch=%q", root, branch)
	}
}

func TestGitInfoFindsParentRepository(t *testing.T) {
	repo := t.TempDir()
	child := filepath.Join(repo, "a", "b")
	mkdirAll(t, child)
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")

	root, branch, ok := gitInfo(child)
	if !ok {
		t.Fatal("gitInfo returned ok=false")
	}

	if root != repo {
		t.Fatalf("root = %q, want %q", root, repo)
	}

	if branch != "main" {
		t.Fatalf("branch = %q, want main", branch)
	}
}

func TestReadGitBranchDetachedHead(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	writeFile(t, filepath.Join(gitDir, "HEAD"), "3f4b8f29b9d8c7e6a5b4c3d2e1f0a9b8c7d6e5f4\n")

	branch := readGitBranch(gitDir)
	if branch != "" {
		t.Fatalf("branch = %q, want empty", branch)
	}
}

func TestGitInfoRelativeGitDirFile(t *testing.T) {
	repo := t.TempDir()
	worktree := filepath.Join(repo, "worktree")
	gitDir := filepath.Join(repo, "actual.git")
	mkdirAll(t, worktree)
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: ../actual.git\n")
	writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/dev\n")

	root, branch, ok := gitInfo(worktree)
	if !ok {
		t.Fatal("gitInfo returned ok=false")
	}

	if root != worktree {
		t.Fatalf("root = %q, want %q", root, worktree)
	}

	if branch != "dev" {
		t.Fatalf("branch = %q, want dev", branch)
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
