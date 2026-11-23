package board

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetName(t *testing.T) {
	b, p := testBoard(t)
	if err := p.SetName("  Role-playing game  "); err != nil {
		t.Fatal(err)
	}
	if got := p.Config().Name; got != "Role-playing game" {
		t.Fatalf("name = %q", got)
	}
	if !strings.Contains(readFile(t, filepath.Join(b.Root(), "projects", "rpg", "project.toml")), "Role-playing game") {
		t.Error("project.toml does not have the new name")
	}
	for _, bad := range []string{"", "   ", "two\nlines", strings.Repeat("x", 81)} {
		if err := p.SetName(bad); err == nil {
			t.Errorf("SetName(%q) worked", bad)
		}
	}
	if got := p.Config().Name; got != "Role-playing game" {
		t.Errorf("a refused name changed it to %q", got)
	}
}

func TestTrashProject(t *testing.T) {
	b, p := testBoard(t)
	if _, err := p.Create(&Task{Title: "Keep me"}, roy, nil); err != nil {
		t.Fatal(err)
	}
	dst, err := b.TrashProject("rpg")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dst) != filepath.Join(b.Root(), TrashDir) || !strings.HasPrefix(filepath.Base(dst), "rpg-") {
		t.Fatalf("moved to %s", dst)
	}
	if _, err := os.Stat(filepath.Join(dst, "tasks", "RPG-1.md")); err != nil {
		t.Errorf("task file not in the trash: %v", err)
	}
	if dirExists(filepath.Join(b.Root(), "projects", "rpg")) {
		t.Error("project folder still there")
	}
	if _, err := b.Project("rpg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Project after delete: %v", err)
	}
	if ps, _ := b.Projects(); len(ps) != 0 {
		t.Errorf("Projects = %d", len(ps))
	}
	if _, err := b.TrashProject("rpg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}

	// A new project with the same folder and key can be made, and deleting it again does not overwrite the
	// first one in the trash.
	if _, err := b.CreateProject("rpg", "RPG", "RPG again"); err != nil {
		t.Fatal(err)
	}
	dst2, err := b.TrashProject("rpg")
	if err != nil {
		t.Fatal(err)
	}
	if dst2 == dst || !dirExists(dst) || !dirExists(dst2) {
		t.Errorf("trash folders %s and %s", dst, dst2)
	}
}
