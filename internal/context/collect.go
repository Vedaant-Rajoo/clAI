package machinecontext

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Context struct {
	WorkingDirectory string
	Shell            string
	OS               string
	GitRepository    bool
	GitRoot          string
	GitBranch        string
}

func Collect() Context {
	c := Context{
		Shell: os.Getenv("SHELL"),
		OS:    runtime.GOOS,
	}

	if wd, err := os.Getwd(); err == nil {
		c.WorkingDirectory = wd
		if root, branch, ok := gitInfo(wd); ok {
			c.GitRepository = true
			c.GitRoot = root
			c.GitBranch = branch
		}
	}

	return c
}

func gitInfo(dir string) (root, branch string, ok bool) {
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			gitDir := gitPath
			if !info.IsDir() {
				gitDir, err = readGitDir(gitPath)
				if err != nil {
					return dir, "", true
				}
			}
			return dir, readGitBranch(gitDir), true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

func readGitDir(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if gitDir, ok := strings.CutPrefix(line, "gitdir: "); ok {
			gitDir = strings.TrimSpace(gitDir)
			if filepath.IsAbs(gitDir) {
				return gitDir, nil
			}

			return filepath.Clean(filepath.Join(filepath.Dir(path), gitDir)), nil
		}
	}

	return "", os.ErrNotExist
}

func readGitBranch(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
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
