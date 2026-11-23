package vcs

import (
	"os"
	"path/filepath"
)

// GitRoot returns the top folder of the git working copy that holds dir: the nearest folder, dir itself or
// above, that has a .git entry (a folder, or a file for worktrees and submodules). "" if there is none. It
// runs no git process, so it is cheap enough for every reply.
func GitRoot(dir string) string {
	if dir == "" {
		return ""
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		up := filepath.Dir(d)
		if up == d {
			return ""
		}
		d = up
	}
}
