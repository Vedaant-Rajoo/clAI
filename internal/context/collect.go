package machinecontext

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const gitMetadataByteLimit = 8 * 1024

var (
	errGitMetadataNotRegular = errors.New("git metadata is not a regular file")
	errGitMetadataChanged    = errors.New("git metadata identity changed")
	errGitMetadataTooLarge   = errors.New("git metadata exceeds 8 KiB")
)

type Context struct {
	WorkingDirectory string
	Shell            string
	ShellProvenance  string
	OS               string
	GitRepository    bool
	GitRoot          string
	GitBranch        string
}

func Collect() Context {
	return CollectContext(context.Background(), "")
}

func CollectWithShell(shell string) Context {
	return CollectContext(context.Background(), shell)
}

// CollectContext collects optional machine context while honoring cancellation
// before and between filesystem stages. Cancellation cannot portably interrupt
// an operating-system metadata or read syscall that is already in progress.
func CollectContext(ctx context.Context, activeShell string) Context {
	shell := os.Getenv("SHELL")
	shellProvenance := "SHELL"
	if isSupportedShell(activeShell) {
		shell = activeShell
		shellProvenance = "widget-declared-shell"
	}

	c := Context{
		Shell:           shell,
		ShellProvenance: shellProvenance,
		OS:              runtime.GOOS,
	}

	if ctx.Err() != nil {
		return c
	}

	wd, err := os.Getwd()
	if err != nil {
		return c
	}
	c.WorkingDirectory = wd

	if ctx.Err() != nil {
		return c
	}
	if root, branch, ok := gitInfo(ctx, wd); ok {
		c.GitRepository = true
		c.GitRoot = root
		c.GitBranch = branch
	}

	return c
}

func isSupportedShell(shell string) bool {
	switch shell {
	case "fish", "bash", "zsh":
		return true
	default:
		return false
	}
}

func gitInfo(ctx context.Context, dir string) (root, branch string, ok bool) {
	for {
		if ctx.Err() != nil {
			return "", "", false
		}

		gitPath := filepath.Join(dir, ".git")
		info, err := os.Lstat(gitPath)
		if err == nil {
			switch {
			case info.IsDir():
				// The repository root is established even if cancellation or
				// unsafe optional HEAD metadata prevents branch collection.
				if ctx.Err() != nil {
					return dir, "", true
				}
				return dir, readGitBranch(ctx, gitPath), true
			case info.Mode().IsRegular():
				// A regular .git entry establishes the worktree root. Treat a
				// malformed, changed, or oversized gitfile as unavailable detail.
				if ctx.Err() != nil {
					return dir, "", true
				}
				gitDir, readErr := readGitDir(ctx, gitPath)
				if readErr != nil || ctx.Err() != nil {
					return dir, "", true
				}
				return dir, readGitBranch(ctx, gitDir), true
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

func readGitDir(ctx context.Context, path string) (string, error) {
	data, err := readGitMetadataFile(ctx, path)
	if err != nil {
		return "", err
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if gitDir, ok := strings.CutPrefix(line, "gitdir: "); ok {
			gitDir = strings.TrimSpace(gitDir)
			if gitDir == "" {
				return "", os.ErrNotExist
			}
			if filepath.IsAbs(gitDir) {
				return filepath.Clean(gitDir), nil
			}

			return filepath.Clean(filepath.Join(filepath.Dir(path), gitDir)), nil
		}
	}

	return "", os.ErrNotExist
}

func readGitBranch(ctx context.Context, gitDir string) string {
	data, err := readGitMetadataFile(ctx, filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}

	head := strings.TrimSpace(string(data))
	const prefix = "ref: refs/heads/"
	if branch, ok := strings.CutPrefix(head, prefix); ok {
		return branch
	}

	return ""
}

func readGitMetadataFile(ctx context.Context, path string) ([]byte, error) {
	return readGitMetadataFileWith(ctx, path, os.Lstat, openGitMetadataFile)
}

func readGitMetadataFileWith(
	ctx context.Context,
	path string,
	lstat func(string) (os.FileInfo, error),
	open func(string) (*os.File, error),
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	expected, err := lstat(path)
	if err != nil {
		return nil, err
	}
	if !expected.Mode().IsRegular() {
		return nil, errGitMetadataNotRegular
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	file, err := open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// File.Stat is an fstat of the opened object. Checking both its type and
	// identity closes the lstat/open race without trusting pathname metadata.
	actual, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !actual.Mode().IsRegular() {
		return nil, errGitMetadataNotRegular
	}
	if !os.SameFile(expected, actual) {
		return nil, errGitMetadataChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// LimitReader requests one overflow sentinel beyond the accepted ceiling;
	// ReadAll therefore cannot allocate in proportion to a pathological file.
	data, err := io.ReadAll(io.LimitReader(file, gitMetadataByteLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > gitMetadataByteLimit {
		return nil, errGitMetadataTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return data, nil
}
