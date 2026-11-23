package vcs

import (
	"os"
	"path/filepath"
	"testing"
)

// writeLoreWorkspace writes .lore/config.toml as Lore 0.14 does (only remote_url matters here).
func writeLoreWorkspace(t *testing.T, dir, remote string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, ".lore"), 0o755)
	cfg := "remote_url = \"" + remote + "\"\n\n[store]\nmax_capacity = 2000000\n\n[vfs]\nvfs_type = \"None\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".lore", "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeLore(t *testing.T) {
	for in, want := range map[string]string{
		"lore://lore.studio.lan:41337/RPG":     "lore:lore.studio.lan/RPG",
		"lore://Lore.Studio.lan/RPG/":          "lore:lore.studio.lan/RPG",
		"lore://user@lore.studio.lan:9000/RPG": "lore:lore.studio.lan:9000/RPG",
		"lores://lore.example.com/Team/RPG":    "lore:lore.example.com/Team/RPG",
	} {
		if got, ok := NormalizeLore(in); !ok || got != want {
			t.Errorf("NormalizeLore(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "lore://host", "https://github.com/a/b", "LoreTestRepo"} {
		if got, ok := NormalizeLore(in); ok {
			t.Errorf("NormalizeLore(%q) = %q, want no key", in, got)
		}
	}
	// A pasted Lore URL and a key normalize to the same key.
	d := testDetector()
	a, _ := d.NormalizeKey("lore://LORE.studio.lan:41337/RPG")
	b, _ := d.NormalizeKey("lore:lore.studio.lan/RPG")
	if a != b || a != "lore:lore.studio.lan/RPG" {
		t.Errorf("NormalizeKey: %q vs %q", a, b)
	}
}

func TestLoreDetector(t *testing.T) {
	ws := t.TempDir()
	writeLoreWorkspace(t, ws, "lore://lore.studio.lan:41337/RPG")
	sub := filepath.Join(ws, "Content", "Props")
	os.MkdirAll(sub, 0o755)
	if LoreRoot(sub) != ws {
		t.Fatalf("LoreRoot(%s) = %q", sub, LoreRoot(sub))
	}
	d := NewDetectorWith(func(string) [][2]string { return nil })
	if keys := d.Keys(sub); len(keys) != 1 || keys[0] != "lore:lore.studio.lan/RPG" {
		t.Errorf("Keys = %v", keys)
	}
	// An offline repository (empty remote_url) has no key, and a folder outside any workspace has no root.
	off := t.TempDir()
	writeLoreWorkspace(t, off, "")
	if keys := d.Keys(off); len(keys) != 0 {
		t.Errorf("offline Keys = %v", keys)
	}
	if LoreRoot(t.TempDir()) != "" {
		t.Error("LoreRoot outside a workspace")
	}
	// What Lore 0.14 writes for a server repository: remote_url names only the server, .lore/id the repository.
	srv := t.TempDir()
	writeLoreWorkspace(t, srv, "lore://172.24.46.13:41337")
	os.WriteFile(filepath.Join(srv, ".lore", "id"), []byte{0x01, 0xa1, 0xf6, 0xb2, 0xb0, 0x40, 0x72, 0xa5, 0xb6, 0x63, 0xad, 0xc5, 0x27, 0x7a, 0xd4, 0xa2}, 0o644)
	if keys := d.Keys(srv); len(keys) != 1 || keys[0] != "lore:172.24.46.13/01a1f6b2b04072a5b663adc5277ad4a2" {
		t.Errorf("server repository Keys = %v", keys)
	}
}
