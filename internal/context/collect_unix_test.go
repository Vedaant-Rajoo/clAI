//go:build darwin || linux

package machinecontext

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestGitInfoFIFOGitEntryDoesNotBlock(t *testing.T) {
	repo := t.TempDir()
	makeFIFO(t, filepath.Join(repo, ".git"))

	root, branch, ok := gitInfoWithTimeout(t, repo)
	if ok {
		t.Fatalf("gitInfo = (%q, %q, true), want unsafe entry ignored", root, branch)
	}
}

func TestGitInfoFIFOHeadDoesNotBlock(t *testing.T) {
	repo := t.TempDir()
	mkdirAll(t, filepath.Join(repo, ".git"))
	makeFIFO(t, filepath.Join(repo, ".git", "HEAD"))

	root, branch, ok := gitInfoWithTimeout(t, repo)
	if !ok || root != repo || branch != "" {
		t.Fatalf("gitInfo = (%q, %q, %t), want (%q, empty, true)", root, branch, ok, repo)
	}
}

func TestGitInfoSymlinkGitEntryIsNotAuthority(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(t.TempDir(), "actual.git")
	writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/untrusted\n")
	if err := os.Symlink(gitDir, filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}

	root, branch, ok := gitInfoWithTimeout(t, repo)
	if ok {
		t.Fatalf("gitInfo = (%q, %q, true), want symlink ignored", root, branch)
	}
}

func TestGitInfoSymlinkHeadRetainsRoot(t *testing.T) {
	repo := t.TempDir()
	target := filepath.Join(t.TempDir(), "HEAD")
	writeFile(t, target, "ref: refs/heads/untrusted\n")
	mkdirAll(t, filepath.Join(repo, ".git"))
	if err := os.Symlink(target, filepath.Join(repo, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}

	root, branch, ok := gitInfoWithTimeout(t, repo)
	if !ok || root != repo || branch != "" {
		t.Fatalf("gitInfo = (%q, %q, %t), want (%q, empty, true)", root, branch, ok, repo)
	}
}

func makeFIFO(t *testing.T, path string) {
	t.Helper()
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitInfoWithTimeout(t *testing.T, dir string) (root, branch string, ok bool) {
	t.Helper()
	type result struct {
		root   string
		branch string
		ok     bool
	}

	results := make(chan result, 1)
	go func() {
		root, branch, ok := gitInfo(context.Background(), dir)
		results <- result{root: root, branch: branch, ok: ok}
	}()

	select {
	case result := <-results:
		return result.root, result.branch, result.ok
	case <-time.After(2 * time.Second):
		t.Fatal("gitInfo blocked on hostile metadata")
		return "", "", false
	}
}
