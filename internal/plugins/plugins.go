// Package plugins finds the web UI's plugins (spec §8.5): the built-in ones embedded in the binary and those in
// the plugins/ folder of the user's config folder. The server only reads their plugin.toml and serves their
// files to a sandboxed iframe; it never runs plugin code.
package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Permissions a plugin may ask for. read is always granted.
const (
	PermRead    = "read"
	PermStorage = "storage"
	PermUI      = "ui"
)

// View is one view a plugin adds to the UI.
type View struct {
	ID    string `toml:"id" json:"id"`
	Title string `toml:"title" json:"title"`
	Icon  string `toml:"icon" json:"icon,omitempty"`
	Scope string `toml:"scope" json:"scope"` // project or board
}

// Plugin is a plugin that loaded.
type Plugin struct {
	Name        string   `toml:"name" json:"name"`
	Version     string   `toml:"version" json:"version"`
	Title       string   `toml:"title" json:"title"`
	Entry       string   `toml:"entry" json:"entry"`
	Views       []View   `toml:"views" json:"views"`
	Permissions []string `toml:"permissions" json:"permissions"`
	Builtin     bool     `toml:"-" json:"builtin"`
	Langs       []string `toml:"-" json:"langs"` // i18n/<lang>.json files it ships

	FS fs.FS `toml:"-" json:"-"` // the plugin's folder
}

// Has reports whether the plugin was granted perm.
func (p *Plugin) Has(perm string) bool {
	return perm == PermRead || slices.Contains(p.Permissions, perm)
}

// Problem is a plugin that did not load.
type Problem struct {
	Plugin string `json:"plugin"`
	Error  string `json:"error"`
}

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	idRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	langRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// Load reads the built-in plugins (each a folder of builtin) and the plugins in dir ("" or missing: none). A
// folder plugin cannot replace a built-in one. Plugins are sorted by name.
func Load(builtin fs.FS, dir string) ([]*Plugin, []Problem) {
	var out []*Plugin
	var probs []Problem
	seen := map[string]bool{}
	add := func(fsys fs.FS, isBuiltin bool, where string) {
		entries, err := fs.ReadDir(fsys, ".")
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				probs = append(probs, Problem{Plugin: where, Error: err.Error()})
			}
			return
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
				continue
			}
			sub, _ := fs.Sub(fsys, e.Name())
			p, err := read(sub, e.Name())
			if err == nil && seen[p.Name] {
				err = fmt.Errorf("a built-in plugin is called %s already", p.Name)
			}
			if err != nil {
				probs = append(probs, Problem{Plugin: e.Name(), Error: err.Error()})
				continue
			}
			p.Builtin = isBuiltin
			seen[p.Name] = true
			out = append(out, p)
		}
	}
	if builtin != nil {
		add(builtin, true, "built-in plugins")
	}
	if dir != "" {
		if _, err := os.Stat(dir); err == nil {
			add(os.DirFS(dir), false, dir)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, probs
}

// read loads and checks one plugin folder named folder.
func read(fsys fs.FS, folder string) (*Plugin, error) {
	data, err := fs.ReadFile(fsys, "plugin.toml")
	if err != nil {
		return nil, fmt.Errorf("no plugin.toml: %w", err)
	}
	p := &Plugin{FS: fsys}
	if err := toml.Unmarshal(data, p); err != nil {
		return nil, fmt.Errorf("plugin.toml: %w", err)
	}
	switch {
	case !nameRe.MatchString(p.Name):
		return nil, fmt.Errorf("plugin.toml: name %q: use lowercase letters, digits and -", p.Name)
	case p.Name != folder:
		return nil, fmt.Errorf("plugin.toml: name %q must match its folder %q", p.Name, folder)
	case p.Title == "":
		return nil, errors.New("plugin.toml: title is missing")
	case len(p.Views) == 0:
		return nil, errors.New("plugin.toml: no views")
	}
	if p.Entry == "" {
		p.Entry = "main.js"
	}
	if !fs.ValidPath(p.Entry) || strings.HasPrefix(p.Entry, "_qm/") {
		return nil, fmt.Errorf("plugin.toml: entry %q is not a file in the plugin's folder", p.Entry)
	}
	if _, err := fs.Stat(fsys, p.Entry); err != nil {
		return nil, fmt.Errorf("entry %s: %w", p.Entry, err)
	}
	ids := map[string]bool{}
	for i, v := range p.Views {
		if !idRe.MatchString(v.ID) || ids[v.ID] {
			return nil, fmt.Errorf("plugin.toml: view id %q: use a unique id of lowercase letters, digits and -", v.ID)
		}
		ids[v.ID] = true
		if v.Title == "" {
			p.Views[i].Title = v.ID
		}
		if v.Scope == "" {
			p.Views[i].Scope = "project"
		}
		if s := p.Views[i].Scope; s != "project" && s != "board" {
			return nil, fmt.Errorf("plugin.toml: view %s: scope %q: use project or board", v.ID, s)
		}
	}
	for _, perm := range p.Permissions {
		if perm != PermRead && perm != PermStorage && perm != PermUI {
			return nil, fmt.Errorf("plugin.toml: unknown permission %q (read, storage, ui)", perm)
		}
	}
	if p.Permissions == nil {
		p.Permissions = []string{}
	}
	p.Langs = []string{}
	if names, err := fs.Glob(fsys, "i18n/*.json"); err == nil {
		for _, n := range names {
			if code := strings.TrimSuffix(path.Base(n), ".json"); langRe.MatchString(code) {
				p.Langs = append(p.Langs, code)
			}
		}
	}
	return p, nil
}

// keySeg is one segment of a storage key: no leading dot, so keys never name temp files or "..".
var keySeg = regexp.MustCompile(`^[a-z0-9_-][a-z0-9._-]*$`)

// ValidKey reports whether key is a storage key: a relative path of [a-z0-9._-] segments.
func ValidKey(key string) bool {
	if key == "" || len(key) > 200 {
		return false
	}
	for _, s := range strings.Split(key, "/") {
		base, _, _ := strings.Cut(s, ".")
		// Boards sync between machines, so keys must work on Windows too: no device names (con, nul.json …) and
		// no trailing dot, which Windows drops ("a." would be the same file as "a").
		if !keySeg.MatchString(s) || strings.HasSuffix(s, ".") || winDevice.MatchString(base) {
			return false
		}
	}
	return true
}

// winDevice are names Windows keeps for devices, whatever the extension.
var winDevice = regexp.MustCompile(`^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)
