package board

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPagesFromTwoCopiesMerge is part of the M9 done-when: two board copies that wrote different pages merge
// with no conflict (the pages are separate files; activity.jsonl merges as a union, §3.5).
func TestPagesFromTwoCopiesMerge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	a := t.TempDir()
	ba, err := Open(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ba.CreateProject("rpg", "RPG", "RPG"); err != nil {
		t.Fatal(err)
	}
	git(a, "init", "-q", "-b", "main")
	git(a, "add", "-A")
	git(a, "commit", "-q", "-m", "board")
	b := filepath.Join(t.TempDir(), "copy")
	git(a, "clone", "-q", a, b)

	write := func(root, title, body string) {
		t.Helper()
		bd, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		p, err := bd.Project("rpg")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.WritePage(Slugify(title), true, PageEdit{Title: &title, Body: &body}, Actor{Kind: "agent", Session: "s-" + filepath.Base(root)}); err != nil {
			t.Fatal(err)
		}
		git(root, "add", "-A")
		git(root, "commit", "-q", "-m", title)
	}
	write(a, "Bridge collision", "From BridgeGen.cpp.")
	write(b, "Shader cache", "In DerivedDataCache.")
	git(b, "pull", "-q", "--no-rebase", "origin", "main")

	if out := git(b, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("merge left changes or conflicts:\n%s", out)
	}
	bd, _ := Open(b)
	p, _ := bd.Project("rpg")
	pages, errs := p.Pages()
	if len(errs) > 0 || len(pages) != 2 {
		t.Fatalf("pages after merge = %d (%v)", len(pages), errs)
	}
	recs, _, _ := p.Activity()
	n := 0
	for _, r := range recs {
		if r.Kind == KindKB {
			n++
		}
	}
	if n != 2 {
		t.Errorf("kb records after merge = %d, want 2", n)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"How bridge collision is generated": "how-bridge-collision-is-generated",
		"  Build: steps (v2)! ":             "build-steps-v2",
		"シェーダーキャッシュの場所":                     "シェーダーキャッシュの場所",
		"!!!": "",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugAvoidsWindowsDeviceNames(t *testing.T) {
	for title, want := range map[string]string{"Con": "con-page", "NUL": "nul-page", "com1": "com1-page", "Console": "console"} {
		if got := Slugify(title); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", title, got, want)
		}
	}
	if ValidSlug("con") {
		t.Error("con is a valid slug")
	}
}
