package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testDetector() *Detector {
	d := NewDetector()
	d.resolveAlias = func(alias string) string {
		if alias == "gh-work" {
			return "github.com"
		}
		return ""
	}
	return d
}

func TestNormalizeGit(t *testing.T) {
	d := testDetector()
	for _, in := range []string{
		"git@github.com:acme/rpg.git",
		"https://github.com/acme/rpg",
		"ssh://git@github.com:22/acme/rpg.git",
		"https://user@github.com/acme/rpg/",
		"https://GitHub.com/Acme/RPG.git",
		"gh-work:acme/rpg.git", // ssh alias
		"git+ssh://git@github.com/acme/rpg",
	} {
		if got, ok := d.NormalizeGit(in); !ok || got != "git:github.com/acme/rpg" {
			t.Errorf("NormalizeGit(%q) = %q, %v", in, got, ok)
		}
	}
	if got, _ := d.NormalizeGit("https://git.studio.lan:8443/games/rpg.git"); got != "git:git.studio.lan:8443/games/rpg" {
		t.Errorf("custom port: %q", got)
	}
	for _, in := range []string{"", "/srv/git/rpg.git", `C:\repos\rpg`, "file:///srv/rpg.git", "https://github.com/"} {
		if got, ok := d.NormalizeGit(in); ok {
			t.Errorf("NormalizeGit(%q) = %q, want no key", in, got)
		}
	}
}

func TestNormalizeGitPortsBySchema(t *testing.T) {
	d := testDetector()
	for in, want := range map[string]string{
		"https://host.lan:443/a/b":     "git:host.lan/a/b",
		"http://host.lan:80/a/b":       "git:host.lan/a/b",
		"ssh://git@host.lan:22/a/b":    "git:host.lan/a/b",
		"git://host.lan:9418/a/b":      "git:host.lan/a/b",
		"https://host.lan:80/a/b":      "git:host.lan:80/a/b",
		"http://host.lan:443/a/b":      "git:host.lan:443/a/b",
		"ssh://git@host.lan:443/a/b":   "git:host.lan:443/a/b",
		"ssh://git@host.lan:2222/a/b":  "git:host.lan:2222/a/b",
		"ssh://git@gh-work:22/acme/rp": "git:github.com/acme/rp",
	} {
		if got, ok := d.NormalizeGit(in); !ok || got != want {
			t.Errorf("NormalizeGit(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestNormalizeKey(t *testing.T) {
	d := testDetector()
	for in, want := range map[string]string{
		"git:github.com/acme/rpg":          "git:github.com/acme/rpg",
		"GIT:github.com/acme/rpg/":         "git:github.com/acme/rpg",
		"git:GitHub.com/Acme/RPG.git":      "git:github.com/acme/rpg",
		"git:github.com:22/acme/rpg":       "git:github.com/acme/rpg",
		"git:github.com:443/acme/rpg.git/": "git:github.com/acme/rpg",
		"git:git.studio.lan:8443/Games/R":  "git:git.studio.lan:8443/games/r",
		"git:gh-work/acme/rpg":             "git:github.com/acme/rpg", // ssh alias in key form
		" git:gh-work:22/acme/rpg.git ":    "git:github.com/acme/rpg",
		"lore:lore.studio:9000/Rpg":        "lore:lore.studio:9000/Rpg",
		"lore:Lore.Studio:41337/Rpg/":      "lore:lore.studio/Rpg",
		"git@github.com:acme/rpg.git":      "git:github.com/acme/rpg",
		"https://GitHub.com/acme/rpg":      "git:github.com/acme/rpg",
	} {
		if got, ok := d.NormalizeKey(in); !ok || got != want {
			t.Errorf("NormalizeKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"git:", "git:github.com", "git:github.com/", "lore:host", "p4:"} {
		if got, ok := d.NormalizeKey(in); ok {
			t.Errorf("NormalizeKey(%q) = %q, want no key", in, got)
		}
	}
}

func TestAliasRejectsUnsafeHosts(t *testing.T) {
	d := testDetector()
	var asked []string
	d.resolveAlias = func(a string) string { asked = append(asked, a); return "evil.example" }
	for _, in := range []string{"-oProxyCommand=calc:acme/rpg.git", "git:-oProxyCommand=x/acme/rpg"} {
		got, _ := d.NormalizeKey(in)
		if strings.Contains(got, "evil") {
			t.Errorf("NormalizeKey(%q) = %q: resolved an option-like host", in, got)
		}
	}
	d.alias("bad\thost")
	d.alias("bad host")
	d.alias("bad\x01host")
	if len(asked) != 0 {
		t.Errorf("ssh asked about %q", asked)
	}
	for h, want := range map[string]bool{"github.com": true, "gh-work": true, "": false, "-G": false, "a b": false, "a\nb": false} {
		if validSSHHost(h) != want {
			t.Errorf("validSSHHost(%q) = %v", h, !want)
		}
	}
}

func TestKeysOrderAndCache(t *testing.T) {
	d := testDetector()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	calls := 0
	remotes := [][2]string{{"origin", "git@github.com:acme/rpg.git"}, {"upstream", "https://github.com/acme/rpg"}, {"mirror", "https://gitlab.com/acme/rpg"}}
	d.gitRemotes = func(string) [][2]string {
		calls++
		return remotes
	}
	keys := d.Keys("C:/work/rpg")
	if len(keys) != 2 || keys[0] != "git:github.com/acme/rpg" || keys[1] != "git:gitlab.com/acme/rpg" {
		t.Errorf("keys = %v", keys)
	}
	d.Keys("C:/work/rpg")
	d.Keys("C:/work/./rpg/")
	if runtime.GOOS == "windows" {
		d.Keys(`c:\WORK\rpg`)
	}
	if calls != 1 {
		t.Errorf("git ran %d times, want 1 (cached)", calls)
	}
	// A changed remote is seen once the entry expires.
	remotes = [][2]string{{"origin", "https://gitlab.com/acme/rpg2"}}
	now = now.Add(FoundTTL + time.Second)
	if keys := d.Keys("C:/work/rpg"); len(keys) != 1 || keys[0] != "git:gitlab.com/acme/rpg2" {
		t.Errorf("after expiry: %v", keys)
	}
	// Forget drops the entry at once.
	remotes = [][2]string{{"origin", "https://gitlab.com/acme/rpg3"}}
	d.Forget("C:/work/rpg/")
	if keys := d.Keys("C:/work/rpg"); len(keys) != 1 || keys[0] != "git:gitlab.com/acme/rpg3" {
		t.Errorf("after Forget: %v", keys)
	}
}

func TestEmptyResultsExpireSoon(t *testing.T) {
	d := testDetector()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	var remotes [][2]string
	calls := 0
	d.gitRemotes = func(string) [][2]string { calls++; return remotes }
	if keys := d.Keys("/w/new"); len(keys) != 0 {
		t.Fatalf("keys = %v", keys)
	}
	d.Keys("/w/new")
	if calls != 1 {
		t.Errorf("empty result not cached briefly: %d calls", calls)
	}
	remotes = [][2]string{{"origin", "https://github.com/acme/new"}}
	now = now.Add(EmptyTTL + time.Second)
	if keys := d.Keys("/w/new"); len(keys) != 1 {
		t.Errorf("remote added after a failed detection not seen: %v", keys)
	}
}

func TestGitEnvDropsGitVariables(t *testing.T) {
	env := gitEnv([]string{"PATH=/bin", "GIT_DIR=/elsewhere", "git_work_tree=/x", "GIT_CONFIG_GLOBAL=/y", "HOME=/h"})
	got := strings.Join(env, " ")
	if strings.Contains(got, "/elsewhere") || strings.Contains(got, "/x") || strings.Contains(got, "/y") ||
		!strings.Contains(got, "PATH=/bin") || !strings.Contains(got, "HOME=/h") ||
		!strings.Contains(got, "GIT_TERMINAL_PROMPT=0") || !strings.Contains(got, "GIT_CONFIG_NOSYSTEM=1") {
		t.Errorf("env = %v", env)
	}
}

// Sync talks to a remote as the user, so it keeps the system config, where Git for Windows sets the
// credential helper; prompts stay off.
func TestGitRemoteEnvKeepsSystemConfig(t *testing.T) {
	got := strings.Join(GitRemoteEnv(), " ")
	if strings.Contains(got, "GIT_CONFIG_NOSYSTEM") || !strings.Contains(got, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("env = %v", got)
	}
	// Nothing may ask a person: no credential manager window, no ssh password dialog (auto-sync runs on a timer).
	for _, kv := range []string{"GCM_INTERACTIVE=never", "GCM_GUI_PROMPT=false", "SSH_ASKPASS_REQUIRE=never"} {
		if !strings.Contains(got, kv) {
			t.Errorf("env lacks %s: %v", kv, got)
		}
	}
}

func TestRealGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", "https://github.com/acme/rpg.git")
	sub := filepath.Join(dir, "Source", "Game")
	os.MkdirAll(sub, 0o755)
	d := NewDetector()
	// An inherited GIT_DIR must not point detection at another repository.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "nowhere"))
	if keys := d.Keys(sub); len(keys) != 1 || keys[0] != "git:github.com/acme/rpg" {
		t.Errorf("keys in a subfolder = %v", keys)
	}
	if keys := d.Keys(t.TempDir()); len(keys) != 0 {
		t.Errorf("non-repo keys = %v", keys)
	}
}
