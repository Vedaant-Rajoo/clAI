package machinecontext

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestCollectContextAlreadyCanceledOmitsFilesystemContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	collected := CollectContext(ctx, "")
	if collected.WorkingDirectory != "" {
		t.Fatalf("WorkingDirectory = %q, want empty", collected.WorkingDirectory)
	}
	if collected.GitRepository || collected.GitRoot != "" || collected.GitBranch != "" {
		t.Fatalf("Git context = %#v, want omitted", collected)
	}
}

func TestGitInfoNonGitDirectory(t *testing.T) {
	root, branch, ok := gitInfo(t.Context(), t.TempDir())
	if ok {
		t.Fatalf("gitInfo returned ok=true, root=%q, branch=%q", root, branch)
	}
}

func TestGitInfoFindsParentRepository(t *testing.T) {
	repo := t.TempDir()
	child := filepath.Join(repo, "a", "b")
	mkdirAll(t, child)
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")

	root, branch, ok := gitInfo(t.Context(), child)
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

	branch := readGitBranch(t.Context(), gitDir)
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

	root, branch, ok := gitInfo(t.Context(), worktree)
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

func TestGitInfoAbsoluteGitDirFile(t *testing.T) {
	worktree := t.TempDir()
	gitDir := filepath.Join(t.TempDir(), "actual.git")
	writeFile(t, filepath.Join(worktree, ".git"), fmt.Sprintf("gitdir: %s\n", gitDir))
	writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/absolute\n")

	root, branch, ok := gitInfo(t.Context(), worktree)
	if !ok {
		t.Fatal("gitInfo returned ok=false")
	}
	if root != worktree {
		t.Fatalf("root = %q, want %q", root, worktree)
	}
	if branch != "absolute" {
		t.Fatalf("branch = %q, want absolute", branch)
	}
}

func TestGitInfoOversizeGitDirRetainsRoot(t *testing.T) {
	worktree := t.TempDir()
	gitDir := filepath.Join(t.TempDir(), "actual.git")
	writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/untrusted\n")
	gitFile := fmt.Sprintf("gitdir: %s\n", gitDir)
	gitFile += strings.Repeat("x", gitMetadataByteLimit+1-len(gitFile))
	writeFile(t, filepath.Join(worktree, ".git"), gitFile)

	root, branch, ok := gitInfo(t.Context(), worktree)
	if !ok || root != worktree {
		t.Fatalf("gitInfo = (%q, %q, %t), want root %q with no branch", root, branch, ok, worktree)
	}
	if branch != "" {
		t.Fatalf("branch = %q, want empty", branch)
	}
}

func TestGitInfoMalformedGitDirRetainsRoot(t *testing.T) {
	worktree := t.TempDir()
	writeFile(t, filepath.Join(worktree, ".git"), "not a gitdir\n")

	root, branch, ok := gitInfo(t.Context(), worktree)
	if !ok || root != worktree || branch != "" {
		t.Fatalf("gitInfo = (%q, %q, %t), want (%q, empty, true)", root, branch, ok, worktree)
	}
}

func TestGitInfoOversizeHeadRetainsRoot(t *testing.T) {
	repo := t.TempDir()
	head := "ref: refs/heads/" + strings.Repeat("x", gitMetadataByteLimit)
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), head)

	root, branch, ok := gitInfo(t.Context(), repo)
	if !ok || root != repo {
		t.Fatalf("gitInfo = (%q, %q, %t), want root %q with no branch", root, branch, ok, repo)
	}
	if branch != "" {
		t.Fatalf("branch length = %d, want empty", len(branch))
	}
}

func TestGitInfoHeadDirectoryRetainsRoot(t *testing.T) {
	repo := t.TempDir()
	mkdirAll(t, filepath.Join(repo, ".git", "HEAD"))

	root, branch, ok := gitInfo(t.Context(), repo)
	if !ok || root != repo || branch != "" {
		t.Fatalf("gitInfo = (%q, %q, %t), want (%q, empty, true)", root, branch, ok, repo)
	}
}

func TestReadGitMetadataFileCancellationBetweenInspectAndOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "HEAD")
	writeFile(t, path, "ref: refs/heads/main\n")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	openCalled := false
	_, err = readGitMetadataFileWith(
		ctx,
		path,
		func(string) (os.FileInfo, error) {
			cancel()
			return info, nil
		},
		func(path string) (*os.File, error) {
			openCalled = true
			return os.Open(path)
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if openCalled {
		t.Fatal("metadata file opened after cancellation")
	}
}

func TestReadGitMetadataFileRejectsChangedIdentity(t *testing.T) {
	dir := t.TempDir()
	expectedPath := filepath.Join(dir, "expected")
	openedPath := filepath.Join(dir, "opened")
	writeFile(t, expectedPath, "ref: refs/heads/expected\n")
	writeFile(t, openedPath, "ref: refs/heads/opened\n")

	_, err := readGitMetadataFileWith(t.Context(), expectedPath, os.Lstat, func(string) (*os.File, error) {
		return os.Open(openedPath)
	})
	if !errors.Is(err, errGitMetadataChanged) {
		t.Fatalf("error = %v, want errGitMetadataChanged", err)
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
