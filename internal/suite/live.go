package suite

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// Live keeps a board's suites loaded and reloads them when a file in suites/ changes, so editing a suite file
// takes effect without restarting the server.
type Live struct {
	root string

	mu          sync.Mutex
	reg         *Registry
	err         error
	stamp       string
	builtinOnly bool // reg is the built-in fallback: no load of the suites/ folder has worked yet
}

// NewLive returns suites for a board folder, loaded on first use.
func NewLive(root string) *Live { return &Live{root: root} }

// stamp describes the suites folder (names, sizes, times) cheaply.
func (l *Live) currentStamp() string {
	entries, err := os.ReadDir(filepath.Join(l.root, "suites"))
	if err != nil {
		return "-"
	}
	var b strings.Builder
	for _, e := range entries {
		if fi, err := e.Info(); err == nil {
			fmt.Fprintf(&b, "%s|%d|%d;", e.Name(), fi.Size(), fi.ModTime().UnixNano())
		}
	}
	return b.String()
}

// Registry returns the current suites, reloading them if suites/ changed. A broken suite file keeps the last
// good registry and returns the error alongside it (nil registry only if no load ever worked).
func (l *Live) Registry() (*Registry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.currentStamp()
	if l.reg != nil && st == l.stamp {
		return l.reg, l.err
	}
	reg, err := Load(l.root)
	l.stamp = st
	if err != nil {
		l.err = err
		if l.reg == nil {
			// Fall back to the built-in suites so the board keeps working.
			if b, berr := Load(""); berr == nil {
				l.reg, l.builtinOnly = b, true
			}
		}
		return l.reg, err
	}
	l.reg, l.err, l.builtinOnly = reg, nil, false
	return reg, nil
}

// NameFor returns the suite name a project uses: its own, else the board default, else Default.
func NameFor(b *board.Board, p *board.Project) string {
	if n := p.Config().Suite; n != "" {
		return n
	}
	if n := b.Config().DefaultSuite; n != "" {
		return n
	}
	return Default
}

// For returns a project's suite. If the project names an unknown suite it falls back to the built-in
// default; if a suite file is broken it keeps the suites that last loaded (the built-in ones if none did).
// Either way it returns the problem as err, saying what is in use, so callers can keep working and warn.
func (l *Live) For(b *board.Board, p *board.Project) (*Suite, error) {
	reg, rerr := l.Registry()
	if reg == nil {
		return nil, rerr
	}
	l.mu.Lock()
	builtinOnly := l.builtinOnly
	l.mu.Unlock()
	using := "using the suite files as they last loaded"
	if builtinOnly {
		using = "using the built-in suites only"
	}
	s, err := reg.Get(NameFor(b, p))
	if err != nil {
		d, _ := reg.Get(Default)
		if rerr != nil {
			return d, fmt.Errorf("%w; %s, and the default suite %s for this project", rerr, using, Default)
		}
		return d, fmt.Errorf("%w; using the default suite %s", err, Default)
	}
	if rerr != nil {
		return s, fmt.Errorf("%w; %s", rerr, using)
	}
	return s, nil
}
