package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnrealResolver(t *testing.T) {
	repo := t.TempDir()
	game := filepath.Join(repo, "RPG")
	os.MkdirAll(filepath.Join(game, "Content", "Maps"), 0o755)
	os.WriteFile(filepath.Join(game, "RPG.uproject"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(game, "Content", "Maps", "L_Bridge.umap"), nil, 0o644)
	// A project plugin in a subfolder; its mount name is the .uplugin name, not the folder name.
	plug := filepath.Join(game, "Plugins", "Runtime", "BridgeKit")
	os.MkdirAll(filepath.Join(plug, "Content", "Meshes"), 0o755)
	os.WriteFile(filepath.Join(plug, "Bridges.uplugin"), []byte("{}"), 0o644)
	r, _ := Resolver("unreal")
	for asset, want := range map[string]string{
		"/Game/Props/SM_Bridge_Mid":               "RPG/Content/Props/SM_Bridge_Mid.uasset",
		"/Game/Props/SM_Bridge_Mid.SM_Bridge_Mid": "RPG/Content/Props/SM_Bridge_Mid.uasset",
		"/Game/Maps/L_Bridge":                     "RPG/Content/Maps/L_Bridge.umap",
		"/game/maps/l_bridge":                     "RPG/Content/Maps/L_Bridge.umap", // Unreal paths ignore case
		"/Bridges/Meshes/SM_Rope":                 "RPG/Plugins/Runtime/BridgeKit/Content/Meshes/SM_Rope.uasset",
		"/Water/Materials/M_Water":                "", // engine plugin, not in the project
		"/Engine/BasicShapes/Cube":                "",
		"/Script/Engine.StaticMesh":               "",
		"//server/share/x":                        "",
		"SM_Bridge_Mid":                           "",
		"/Game/../secrets":                        "",
	} {
		if got := strings.Join(r.Files(repo, asset), ","); got != want {
			t.Errorf("Files(%q) = %q, want %q", asset, got, want)
		}
	}
	if n := r.ShortName("/Game/Props/SM_Bridge_Mid.SM_Bridge_Mid"); n != "SM_Bridge_Mid" {
		t.Errorf("ShortName = %q", n)
	}
}

func TestPathResolverAndRegistry(t *testing.T) {
	r, _ := Resolver("path")
	if got := r.Files("", "/web/src/App.tsx"); len(got) != 1 || got[0] != "web/src/App.tsx" {
		t.Errorf("Files = %v", got)
	}
	if r.ShortName("web/src/App.tsx") != "App" || len(r.Files("", "../x")) != 0 {
		t.Error("path resolver")
	}
	for _, bad := range []string{`C:\x\y.txt`, "C:/x", `\\server\share\x`, "a/../../b", "a/./b"} {
		if got := r.Files("", bad); len(got) != 0 {
			t.Errorf("Files(%q) = %v, want nothing", bad, got)
		}
	}
	if got := r.Files("", `docs\v1..2.txt`); len(got) != 1 || got[0] != "docs/v1..2.txt" {
		t.Errorf("Files(v1..2) = %v", got)
	}
	// Several .uproject files next to each other are ambiguous.
	two := t.TempDir()
	os.WriteFile(filepath.Join(two, "A.uproject"), nil, 0o644)
	os.WriteFile(filepath.Join(two, "B.uproject"), nil, 0o644)
	if u, _ := Resolver("unreal"); len(u.Files(two, "/Game/X")) != 0 {
		t.Error("ambiguous .uproject resolved")
	}
	if _, err := Resolver("unity"); err == nil {
		t.Error("unknown resolver accepted")
	}
	for _, name := range []string{"git-lfs", "lore"} {
		p, err := LockProviderNamed(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Locks(context.TODO(), "", nil); err != ErrNoLocks {
			t.Errorf("%s without a repository: %v", name, err)
		}
	}
	if _, err := LockProviderNamed("p4"); err == nil {
		t.Error("unknown lock provider accepted")
	}
	if CheckVCS("git") != nil || CheckVCS("lore") != nil || CheckVCS("svn") == nil {
		t.Error("CheckVCS")
	}
}
