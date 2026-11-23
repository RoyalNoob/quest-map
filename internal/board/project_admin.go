package board

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// TrashDir is the folder in the board root that deleted projects are moved to. It is never synced
// (.gitignore), so a deletion reaches other copies of the board as removed files, while this copy can still
// restore the project by moving its folder back to projects/.
const TrashDir = ".trash"

// maxProjectName is the longest project name, in characters.
const maxProjectName = 80

// SetName changes a project's display name. Its key and folder stay the same.
func (p *Project) SetName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "\r\n") || utf8.RuneCountInString(name) > maxProjectName {
		return fmt.Errorf("project name: use one line of 1 to %d characters", maxProjectName)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Name = name
	return p.saveConfig()
}

// TrashProject deletes a project by moving its folder to .trash/<slug>-<UTC time>/ in the board root, and
// returns the new folder. Writes to the project wait for the move; later ones fail because the folder is gone.
func (b *Board) TrashProject(slug string) (string, error) {
	p, err := b.Project(slug)
	if err != nil {
		return "", err
	}
	trash := filepath.Join(b.root, TrashDir)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return "", err
	}
	stamp := b.now().Format("20060102T150405Z")
	dst := filepath.Join(trash, slug+"-"+stamp)
	for i := 2; dirExists(dst); i++ {
		dst = filepath.Join(trash, fmt.Sprintf("%s-%s-%d", slug, stamp, i))
	}
	p.mu.Lock()
	err = renameWithRetry(p.dir, dst)
	p.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("delete project %s: %w", slug, err)
	}
	b.Forget(slug)
	return dst, nil
}
