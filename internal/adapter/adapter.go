// Package adapter holds built-in code that suites switch on by name (spec §4.2): asset resolvers, which map a
// task's asset value to files in a repository, and lock providers, which read file lock state. Nothing is
// loaded at run time; a suite only names adapters that are compiled in.
package adapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AssetResolver maps a task's asset value to files in a working copy.
type AssetResolver interface {
	Name() string
	// Files returns the repo-relative files (slash-separated) an asset value refers to, for a working copy at
	// repoRoot. Files that do not exist are still returned when the mapping is certain; callers decide.
	Files(repoRoot, asset string) []string
	// ShortName is the asset's name without folders or extension, used for display and the asset: filter.
	ShortName(asset string) string
}

// Lock is one file lock as a lock provider reports it.
type Lock struct {
	Path  string // repo-relative, slash-separated
	Owner string
	Since string // RFC 3339, if known
}

// LockProvider reads lock state (read-only; quest-map never takes or breaks locks).
type LockProvider interface {
	Name() string
	// Root returns the top of the working copy this provider reads locks for that holds dir, or "".
	Root(dir string) string
	// Locks returns the locks on files (relative to root) in the repository at root; a provider may return
	// more. ErrNoLocks means the repository does not use these locks; any other error means the lock state is
	// unknown.
	Locks(ctx context.Context, root string, files []string) ([]Lock, error)
}

var (
	resolvers     = map[string]AssetResolver{"path": pathResolver{}, "unreal": unrealResolver{}}
	lockProviders = map[string]LockProvider{"git-lfs": gitLFS{}, "lore": lore{}}
	// vcsDetectors known by name besides git, which is always on.
	vcsDetectors = map[string]bool{"lore": true}
)

// Resolver returns the asset resolver with this name.
func Resolver(name string) (AssetResolver, error) {
	if r, ok := resolvers[name]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("unknown asset resolver %q (known: %s)", name, strings.Join(keys(resolvers), ", "))
}

// LockProviderNamed returns the lock provider with this name.
func LockProviderNamed(name string) (LockProvider, error) {
	if p, ok := lockProviders[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("unknown lock provider %q (known: %s)", name, strings.Join(keys(lockProviders), ", "))
}

// CheckVCS reports whether a VCS detector name is known ("git" is always on and need not be named).
func CheckVCS(name string) error {
	if name == "git" || vcsDetectors[name] {
		return nil
	}
	return fmt.Errorf("unknown VCS detector %q (known: git, %s)", name, strings.Join(keys(vcsDetectors), ", "))
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- path: the asset value is a repo-relative path ----

type pathResolver struct{}

func (pathResolver) Name() string { return "path" }

// Files accepts only a relative path inside the repository: no drive letter or UNC prefix and no ".." segment.
// Both slash kinds separate folders on every OS.
func (pathResolver) Files(_, asset string) []string {
	a := strings.ReplaceAll(strings.TrimSpace(asset), `\`, "/")
	if strings.HasPrefix(a, "//") || filepath.VolumeName(filepath.FromSlash(a)) != "" || hasDrive(a) {
		return nil
	}
	a = strings.Trim(a, "/")
	if a == "" || !cleanSegments(strings.Split(a, "/")) {
		return nil
	}
	return []string{a}
}

// hasDrive reports a Windows drive prefix ("C:"), which filepath.VolumeName does not see on Linux.
func hasDrive(a string) bool {
	return len(a) >= 2 && a[1] == ':' && (a[0]|0x20 >= 'a' && a[0]|0x20 <= 'z')
}

// cleanSegments reports whether no path segment is empty, "." or "..".
func cleanSegments(segs []string) bool {
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return true
}

func (pathResolver) ShortName(asset string) string {
	a := strings.ReplaceAll(asset, `\`, "/")
	a = a[strings.LastIndex(a, "/")+1:]
	if i := strings.LastIndex(a, "."); i > 0 {
		a = a[:i]
	}
	return a
}

// ---- unreal: /Game/… object paths and asset names ----

type unrealResolver struct{}

func (unrealResolver) Name() string { return "unreal" }

func (unrealResolver) ShortName(asset string) string {
	a := strings.TrimSpace(asset)
	if i := strings.LastIndex(a, "."); i > strings.LastIndex(a, "/") {
		a = a[:i] // "/Game/Props/SM_X.SM_X" object path form
	}
	return a[strings.LastIndex(a, "/")+1:]
}

// engineMounts are mount points that are not project content (engine, C++ classes, transient packages).
var engineMounts = map[string]bool{"engine": true, "script": true, "temp": true, "memory": true, "config": true}

// Files maps an object path (/Game/Props/SM_X, /Game/Props/SM_X.SM_X, /PluginMount/…) to Content files of the
// .uproject found in repoRoot (or one folder below it). A plugin mount is found by its .uplugin file anywhere
// under Plugins/. Unreal paths ignore case, so folders and files are matched without case. Paths that cannot
// be mapped with certainty give no files: a bare asset name (needs the asset registry), engine and script
// paths, a plugin that is not in the project, or a folder with several .uproject files.
func (u unrealResolver) Files(repoRoot, asset string) []string {
	a := strings.TrimSpace(asset)
	if repoRoot == "" || !strings.HasPrefix(a, "/") || strings.HasPrefix(a, "//") {
		return nil
	}
	if i := strings.LastIndex(a, "."); i > strings.LastIndex(a, "/") {
		a = a[:i]
	}
	segs := strings.Split(strings.TrimPrefix(a, "/"), "/")
	if len(segs) < 2 || !cleanSegments(segs) || strings.ContainsAny(a, `\:`) {
		return nil
	}
	mount := segs[0]
	if engineMounts[strings.ToLower(mount)] {
		return nil
	}
	proj, ok := uprojectDir(repoRoot)
	if !ok {
		return nil
	}
	var content string
	if strings.EqualFold(mount, "Game") {
		content = filepath.Join(proj, "Content")
	} else if dir := findPlugin(filepath.Join(proj, "Plugins"), mount, 4); dir != "" {
		content = filepath.Join(dir, "Content")
	} else {
		return nil
	}
	rel := func(p string) string {
		r, err := filepath.Rel(repoRoot, p)
		if err != nil {
			return filepath.ToSlash(p)
		}
		return filepath.ToSlash(r)
	}
	// Walk the folders, taking each one's spelling on disk; a folder that is missing keeps the asked spelling.
	dir := content
	for _, s := range segs[1 : len(segs)-1] {
		dir = filepath.Join(dir, entryName(dir, s, true))
	}
	name := segs[len(segs)-1]
	var out []string
	for _, ext := range []string{".uasset", ".umap"} {
		if n := entryName(dir, name+ext, false); fileExists(filepath.Join(dir, n)) {
			out = append(out, rel(filepath.Join(dir, n)))
		}
	}
	if len(out) == 0 {
		out = []string{rel(filepath.Join(dir, name+".uasset"))}
	}
	return out
}

// entryName returns the name of dir's entry that matches name without case (exact match first), or name.
// The spelling on disk is returned on every OS (Windows would also open the asked spelling, but report it).
func entryName(dir, name string, wantDir bool) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return name
	}
	found := ""
	for _, e := range entries {
		if e.IsDir() != wantDir {
			continue
		}
		if e.Name() == name {
			return name
		}
		if found == "" && strings.EqualFold(e.Name(), name) {
			found = e.Name()
		}
	}
	if found != "" {
		return found
	}
	return name
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// findPlugin returns the folder of <mount>.uplugin under dir (searched depth levels deep), or "".
func findPlugin(dir, mount string, depth int) string {
	entries, err := os.ReadDir(dir)
	if err != nil || depth < 0 {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), mount+".uplugin") {
			return dir
		}
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.EqualFold(e.Name(), "Content") &&
			!strings.EqualFold(e.Name(), "Binaries") && !strings.EqualFold(e.Name(), "Intermediate") {
			if d := findPlugin(filepath.Join(dir, e.Name()), mount, depth-1); d != "" {
				return d
			}
		}
	}
	return ""
}

// uprojectDir finds the folder holding the .uproject: repoRoot itself, else the one subfolder that has one.
// Several .uproject files at the same level are ambiguous and give ok == false.
func uprojectDir(repoRoot string) (string, bool) {
	if m, _ := filepath.Glob(filepath.Join(repoRoot, "*.uproject")); len(m) > 0 {
		return repoRoot, len(m) == 1
	}
	if m, _ := filepath.Glob(filepath.Join(repoRoot, "*", "*.uproject")); len(m) > 0 {
		return filepath.Dir(m[0]), len(m) == 1
	}
	return repoRoot, true
}
