package board

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Location returns the board's log-line time zone (board.toml timezone).
func (b *Board) Location() *time.Location { return b.loc }

// ReloadConfig re-reads project.toml after it was edited by hand.
func (p *Project) ReloadConfig() error {
	data, err := os.ReadFile(filepath.Join(p.dir, "project.toml"))
	if err != nil {
		return err
	}
	var cfg ProjectConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("project %q: project.toml: %w", p.slug, err)
	}
	if !keyRe.MatchString(cfg.Key) {
		return fmt.Errorf("project %q: project.toml: invalid key %q", p.slug, cfg.Key)
	}
	p.mu.Lock()
	p.setCfg(cfg)
	p.mu.Unlock()
	return nil
}

// TaskPath returns where a task's file belongs.
func (p *Project) TaskPath(id string, archived bool) string { return p.taskPath(id, archived) }

// Adopt renames a task file stored under another name (a copy made while resolving a merge, or a renumbered
// file whose rename did not finish) to its id's file name, if that name is free. It returns the path the file
// ends up at.
func (p *Project) Adopt(path, id string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	want := p.taskPath(id, filepath.Base(filepath.Dir(path)) == "archive")
	if filepath.Clean(path) == filepath.Clean(want) {
		return path, nil
	}
	if _, err := os.Stat(want); err == nil {
		return path, nil
	}
	if err := renameWithRetry(path, want); err != nil {
		return path, err
	}
	return want, nil
}
