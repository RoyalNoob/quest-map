// Package vcs finds which version-control remotes a working folder belongs to, as normalized remote keys
// such as git:github.com/acme/rpg. Detection never fails loudly: a folder that is not a repository, a missing
// tool or a timeout gives no keys.
package vcs

import (
	"bufio"
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Timeout bounds every detector command.
var Timeout = 3 * time.Second

// How long detector results are cached. A folder with remotes is asked again after FoundTTL, so a changed
// remote is seen; a folder with none (not a repository yet, git missing, a timeout) after EmptyTTL.
var (
	FoundTTL = 5 * time.Minute
	EmptyTTL = 30 * time.Second
)

// Detector caches the keys found for each working folder (and each resolved ssh alias) for a while.
type Detector struct {
	mu      sync.Mutex
	byDir   map[string]cached
	aliases map[string]cached

	// now is the clock for cache expiry; tests replace it.
	now func() time.Time
	// resolveAlias maps an ssh host alias to its real host name; tests replace it.
	resolveAlias func(alias string) string
	// gitRemotes lists "name url" pairs for a folder; tests replace it.
	gitRemotes func(dir string) [][2]string
	// loreRemote returns the remote_url of the Lore workspace holding a folder ("" if none); tests replace it.
	loreRemote func(dir string) string
}

type cached struct {
	keys    []string // byDir
	host    string   // aliases
	expires time.Time
}

// NewDetector returns a detector that runs git and ssh.
func NewDetector() *Detector {
	return &Detector{byDir: map[string]cached{}, aliases: map[string]cached{}, now: time.Now,
		resolveAlias: sshHostname, gitRemotes: gitRemoteList, loreRemote: loreRemote}
}

// NewDetectorWith returns a detector that gets a folder's git remotes from remotes instead of running git
// (for tests). ssh aliases are not resolved.
func NewDetectorWith(remotes func(dir string) [][2]string) *Detector {
	d := NewDetector()
	d.gitRemotes = remotes
	d.resolveAlias = func(string) string { return "" }
	return d
}

// dirKey normalizes a folder for the cache, so equivalent spellings share an entry (case-insensitive on
// Windows).
func dirKey(dir string) string {
	k := filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		k = strings.ToLower(k)
	}
	return k
}

// Keys returns the remote keys for a working folder: git remotes (origin first), then the Lore workspace's
// remote. Results are cached per folder: FoundTTL
// when keys were found, EmptyTTL when none were.
func (d *Detector) Keys(dir string) []string {
	k := dirKey(dir)
	d.mu.Lock()
	if c, ok := d.byDir[k]; ok && d.now().Before(c.expires) {
		d.mu.Unlock()
		return c.keys
	}
	d.mu.Unlock()
	var keys []string
	seen := map[string]bool{}
	for _, r := range d.gitRemotes(dir) {
		key, ok := d.NormalizeGit(r[1])
		if ok && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	if d.loreRemote != nil {
		if key, ok := NormalizeLore(d.loreRemote(dir)); ok && !seen[key] {
			keys = append(keys, key)
		}
	}
	ttl := FoundTTL
	if len(keys) == 0 {
		ttl = EmptyTTL
	}
	d.mu.Lock()
	d.byDir[k] = cached{keys: keys, expires: d.now().Add(ttl)}
	d.mu.Unlock()
	return keys
}

// Forget drops the cached keys of a folder (after repo_link, for example), so the next Keys asks git again.
func (d *Detector) Forget(dir string) {
	d.mu.Lock()
	delete(d.byDir, dirKey(dir))
	d.mu.Unlock()
}

// NormalizeKey accepts a remote key (git:…, lore:…, p4:…) or a git remote URL and returns the normalized
// key. git keys get the same treatment as URLs: host and path lowercased, a trailing .git and slashes dropped,
// default ports (22, 443) dropped and an ssh alias host resolved. Lore keys get a lowercase host and lose
// the default port 41337; their path keeps its case.
func (d *Detector) NormalizeKey(s string) (string, bool) {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if scheme, _, ok := strings.Cut(lower, "://"); ok {
		if isLoreScheme(scheme) {
			return NormalizeLore(s)
		}
		return d.NormalizeGit(s)
	}
	switch {
	case strings.HasPrefix(lower, "git:"):
		return d.normalizeGitKey(s[len("git:"):])
	case strings.HasPrefix(lower, "lore:"):
		hostPort, path, _ := strings.Cut(strings.Trim(s[len("lore:"):], "/"), "/")
		host, port := splitPort(hostPort)
		if host == "" || path == "" {
			return "", false
		}
		key := "lore:" + strings.ToLower(host)
		if port != "" && port != "41337" {
			key += ":" + port
		}
		return key + "/" + strings.Trim(path, "/"), true
	case strings.HasPrefix(lower, "p4:"):
		rest := strings.TrimRight(s[len("p4:"):], "/")
		if rest == "" {
			return "", false
		}
		return "p4:" + rest, true
	}
	return d.NormalizeGit(s)
}

// normalizeGitKey normalizes the part of a git key after "git:": <host>[:<port>]/<path>. The scheme is not
// known here, so both git default ports (22 for ssh, 443 for https) are dropped.
func (d *Detector) normalizeGitKey(rest string) (string, bool) {
	hostPort, path, _ := strings.Cut(strings.Trim(rest, "/"), "/")
	host, port := splitPort(hostPort)
	if port == "22" || port == "443" {
		port = ""
	}
	return d.gitKey(host, port, path, true)
}

// splitPort splits host:port; a part after the colon that is not all digits stays with the host.
func splitPort(hp string) (host, port string) {
	i := strings.LastIndex(hp, ":")
	if i < 0 {
		return hp, ""
	}
	p := hp[i+1:]
	if p == "" {
		return hp[:i], ""
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return hp, ""
		}
	}
	return hp[:i], p
}

// NormalizeGit turns a git remote URL into a key git:<host>[:<port>]/<path>: scheme, user, the scheme's
// default port (ssh 22, https 443, http 80, git 9418), a trailing .git and slashes are dropped; host and path
// are lowercased. ssh host aliases are resolved with `ssh -G`; if that fails the alias stays the host.
func (d *Detector) NormalizeGit(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	var host, port, path string
	isSSH := false
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", false
		}
		scheme := strings.ToLower(u.Scheme)
		if scheme == "file" {
			return "", false
		}
		isSSH = scheme == "ssh" || scheme == "git+ssh" || scheme == "ssh+git"
		host, port, path = u.Hostname(), u.Port(), u.Path
		if port == defaultPort(scheme) {
			port = ""
		}
	case strings.Contains(raw, ":") && !strings.HasPrefix(raw, "/") && !isWindowsPath(raw):
		// scp-like: [user@]host:path (always ssh, no port)
		hp, p, _ := strings.Cut(raw, ":")
		if i := strings.LastIndex(hp, "@"); i >= 0 {
			hp = hp[i+1:]
		}
		host, path, isSSH = hp, p, true
	default:
		return "", false // a local path
	}
	return d.gitKey(host, port, path, isSSH)
}

// defaultPort returns the port a URL scheme uses when none is given.
func defaultPort(scheme string) string {
	switch scheme {
	case "ssh", "git+ssh", "ssh+git":
		return "22"
	case "https":
		return "443"
	case "http":
		return "80"
	case "git":
		return "9418"
	}
	return ""
}

// gitKey builds git:<host>[:<port>]/<path> from parts, resolving an ssh alias host if resolve is set.
func (d *Detector) gitKey(host, port, path string, resolve bool) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", false
	}
	if resolve {
		host = d.alias(host)
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if path == "" {
		return "", false
	}
	key := "git:" + host
	if port != "" {
		key += ":" + port
	}
	return key + "/" + strings.ToLower(path), true
}

func isWindowsPath(s string) bool {
	return len(s) >= 2 && s[1] == ':' && ((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z'))
}

// alias resolves an ssh host alias, caching the answer (FoundTTL when ssh answered, EmptyTTL when it failed).
func (d *Detector) alias(host string) string {
	d.mu.Lock()
	if c, ok := d.aliases[host]; ok && d.now().Before(c.expires) {
		d.mu.Unlock()
		return c.host
	}
	d.mu.Unlock()
	h, ttl := host, EmptyTTL
	if validSSHHost(host) {
		if real := strings.ToLower(strings.TrimSpace(d.resolveAlias(host))); real != "" && validSSHHost(real) {
			h, ttl = real, FoundTTL
		}
	}
	d.mu.Lock()
	d.aliases[host] = cached{host: h, expires: d.now().Add(ttl)}
	d.mu.Unlock()
	return h
}

// validSSHHost reports whether a host name is safe to hand to ssh: not empty, not an option (leading "-"),
// no whitespace or control characters.
func validSSHHost(h string) bool {
	if h == "" || strings.HasPrefix(h, "-") {
		return false
	}
	for _, c := range h {
		if unicode.IsSpace(c) || unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// sshHostname asks `ssh -G` for the real host name of an alias.
func sshHostname(alias string) string {
	if !validSSHHost(alias) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-G", "--", alias)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "hostname "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// GitEnv is gitEnv over the server's environment, for other packages that run git (lock providers).
func GitEnv() []string { return gitEnv(os.Environ()) }

// GitRemoteEnv is GitEnv that keeps the system config, for git commands that talk to a remote as the user
// (board sync, §3.5; LFS locks): Git for Windows sets its credential helper there, so without it a fetch or
// push finds no saved credentials and fails on the disabled prompt. The helper uses saved credentials only:
// the server runs in the background (and auto-sync on a timer), so no sign-in window or ssh password dialog
// may open – missing credentials are an error that says what to do.
func GitRemoteEnv() []string {
	return append(slices.DeleteFunc(GitEnv(), func(kv string) bool { return kv == "GIT_CONFIG_NOSYSTEM=1" }),
		"GCM_INTERACTIVE=never", "GCM_GUI_PROMPT=false", "SSH_ASKPASS_REQUIRE=never")
}

// HideWindow keeps a console window from flashing up when the server runs a tool (no-op off Windows).
func HideWindow(cmd *exec.Cmd) { hideWindow(cmd) }

// gitEnv returns the environment for running git: the server's, without GIT_* variables (a GIT_DIR or
// GIT_WORK_TREE inherited from a hook or a shell would point git at another repository), and with prompts
// and the system config turned off.
func gitEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		if strings.HasPrefix(strings.ToUpper(kv), "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
}

// gitRemoteList runs `git -C dir remote -v` and returns (name, fetch url) pairs, origin first.
func gitRemoteList(dir string) [][2]string {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=", "-C", dir, "remote", "-v")
	cmd.Env = gitEnv(cmd.Environ())
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var origin, others [][2]string
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || (len(f) >= 3 && f[2] != "(fetch)") || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		if f[0] == "origin" {
			origin = append(origin, [2]string{f[0], f[1]})
		} else {
			others = append(others, [2]string{f[0], f[1]})
		}
	}
	return append(origin, others...)
}
