package vcs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitRoot(t *testing.T) {
	top := t.TempDir()
	os.Mkdir(filepath.Join(top, ".git"), 0o755)
	deep := filepath.Join(top, "Source", "Game")
	os.MkdirAll(deep, 0o755)
	if got := GitRoot(deep); got != top {
		t.Errorf("GitRoot(deep) = %q, want %q", got, top)
	}
	wt := filepath.Join(top, "wt")
	os.Mkdir(wt, 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../.git/worktrees/wt\n"), 0o644)
	if got := GitRoot(wt); got != wt {
		t.Errorf("worktree root = %q", got)
	}
	if got := GitRoot(""); got != "" {
		t.Errorf("empty dir = %q", got)
	}
}
