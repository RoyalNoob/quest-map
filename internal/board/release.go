package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Release is a recorded deliverable (the game-dev suite calls it a build, its target a platform).
type Release struct {
	ID       string    `toml:"id" json:"id"`
	Target   string    `toml:"target,omitempty" json:"target"`
	Revision string    `toml:"revision,omitempty" json:"revision"`
	Date     time.Time `toml:"date" json:"date"`
	Notes    string    `toml:"notes,omitempty" json:"notes"`
}

type releasesFile struct {
	Release []Release `toml:"release"`
}

func (p *Project) releasesPath() string { return filepath.Join(p.dir, "releases.toml") }

// ReleasesPath returns the project's releases.toml.
func (p *Project) ReleasesPath() string { return p.releasesPath() }

// Releases reads the project's releases, newest first.
func (p *Project) Releases() ([]Release, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.releasesLocked()
}

func (p *Project) releasesLocked() ([]Release, error) {
	data, err := os.ReadFile(p.releasesPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f releasesFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", p.releasesPath(), err)
	}
	sort.SliceStable(f.Release, func(i, j int) bool { return f.Release[i].Date.After(f.Release[j].Date) })
	return f.Release, nil
}

// RecordRelease adds a release. Ids are unique per project; Date defaults to now.
func (p *Project) RecordRelease(r Release) (Release, error) {
	r.ID = strings.TrimSpace(r.ID)
	r.Target = strings.TrimSpace(r.Target)
	if r.ID == "" || strings.ContainsAny(r.ID, " \t\n") {
		return r, errors.New("id is empty or has spaces")
	}
	if r.Date.IsZero() {
		r.Date = p.board.now()
	}
	r.Date = r.Date.UTC().Truncate(time.Second)
	p.mu.Lock()
	defer p.mu.Unlock()
	all, err := p.releasesLocked()
	if err != nil {
		return r, err
	}
	if slices.ContainsFunc(all, func(x Release) bool { return strings.EqualFold(x.ID, r.ID) }) {
		return r, fmt.Errorf("%s is already recorded in %s", r.ID, p.slug)
	}
	all = append(all, r)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Date.Before(all[j].Date) }) // file order: oldest first
	data, err := toml.Marshal(releasesFile{Release: all})
	if err != nil {
		return r, err
	}
	return r, writeFileAtomic(p.releasesPath(), data, 0o644)
}

// SetMilestones replaces the project's milestones (names unique, case-insensitively).
func (p *Project) SetMilestones(ms []Milestone) error {
	seen := map[string]bool{}
	for i := range ms {
		ms[i].Name = strings.TrimSpace(ms[i].Name)
		k := strings.ToLower(ms[i].Name)
		if k == "" {
			return errors.New("a milestone needs a name")
		}
		if seen[k] {
			return fmt.Errorf("milestone %q is listed twice", ms[i].Name)
		}
		seen[k] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Milestones = slices.Clone(ms)
	return p.saveConfig()
}

// SetArchiveDays sets archive_done_after_days (0 turns auto-archiving off).
func (p *Project) SetArchiveDays(days int) error {
	if days < 0 || days > 3650 {
		return fmt.Errorf("archive after %d days: use 0 (never) to 3650", days)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.ArchiveDoneAfterDays = days
	return p.saveConfig()
}

// SetSuite sets the project's suite ("" uses the board's default suite).
func (p *Project) SetSuite(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Suite = strings.TrimSpace(name)
	return p.saveConfig()
}
