package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
)

func TestRenameProject(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	if code := br.do("PUT", "/api/projects/rpg/name", map[string]string{"name": "Role-playing game"}, nil); code != 200 {
		t.Fatalf("rename = %d", code)
	}
	var projects []projectJSON
	br.do("GET", "/api/projects", nil, &projects)
	if projects[1].Slug != "rpg" || projects[1].Name != "Role-playing game" || projects[1].Key != "RPG" {
		t.Errorf("projects = %+v", projects)
	}
	if code := br.do("PUT", "/api/projects/rpg/name", map[string]string{"name": " "}, nil); code != 400 {
		t.Errorf("empty name = %d", code)
	}
}

func TestDeleteProject(t *testing.T) {
	s, p := startWithProject(t)
	br := newBrowser(t, s)
	if code := br.do("DELETE", "/api/projects/rpg", map[string]string{"confirm": "ART"}, nil); code != 400 {
		t.Fatalf("wrong key = %d", code)
	}
	if code := br.do("DELETE", "/api/projects/rpg", nil, nil); code != 400 {
		t.Fatalf("no key = %d", code)
	}

	// A live claim blocks it.
	if _, err := p.Claim("RPG-1", board.Actor{Kind: "agent", Session: "claude-code:abc"}); err != nil {
		t.Fatal(err)
	}
	s.refreshTask(p, "RPG-1")
	var e struct{ Error string }
	if code := br.do("DELETE", "/api/projects/rpg", map[string]string{"confirm": "rpg"}, &e); code != 409 || !strings.Contains(e.Error, "RPG-1") {
		t.Fatalf("claimed = %d %q", code, e.Error)
	}
	if _, err := p.Release("RPG-1", board.Actor{Kind: "agent", Session: "claude-code:abc"}, "stop", nil); err != nil {
		t.Fatal(err)
	}
	s.refreshTask(p, "RPG-1")

	var out struct{ Trashed string }
	if code := br.do("DELETE", "/api/projects/rpg", map[string]string{"confirm": "rpg"}, &out); code != 200 {
		t.Fatalf("delete = %d", code)
	}
	if !strings.HasPrefix(out.Trashed, ".trash/rpg-") {
		t.Errorf("trashed = %q", out.Trashed)
	}
	if _, err := os.Stat(filepath.Join(s.Board.Root(), filepath.FromSlash(out.Trashed), "tasks", "RPG-1.md")); err != nil {
		t.Errorf("task not kept in the trash: %v", err)
	}
	var projects []projectJSON
	br.do("GET", "/api/projects", nil, &projects)
	if len(projects) != 1 || projects[0].Slug != "art" {
		t.Errorf("projects = %+v", projects)
	}
	if rows, _ := s.Index.FindString("bridge", index.FindOptions{}); len(rows) != 0 {
		t.Errorf("deleted tasks still found: %+v", rows)
	}
	if code := br.do("DELETE", "/api/projects/rpg", map[string]string{"confirm": "RPG"}, nil); code != 404 {
		t.Errorf("second delete = %d", code)
	}
}

func TestCreateProject(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	var out map[string]string
	if code := br.do("POST", "/api/projects", map[string]string{"key": "qm", "name": "quest-map", "suite": "gamedev-unreal"}, &out); code != 200 {
		t.Fatalf("create = %d", code)
	}
	if out["slug"] != "qm" || out["key"] != "QM" || out["name"] != "quest-map" {
		t.Errorf("out = %v", out)
	}
	p, err := s.Board.Project("qm")
	if err != nil || p.Config().Suite != "gamedev-unreal" {
		t.Fatalf("project = %v, %v", p, err)
	}
	var projects []projectJSON
	br.do("GET", "/api/projects", nil, &projects)
	if len(projects) != 3 {
		t.Errorf("projects = %+v", projects)
	}
	var e struct{ Error string }
	for _, bad := range []map[string]string{
		{"key": "RPG"},                       // key taken
		{"key": "NEW", "slug": "rpg"},        // folder taken
		{"key": "9X"},                        // bad key
		{"key": ""},                          // no key
		{"key": "NEW", "suite": "no-such"},   // unknown suite
		{"key": "NEW", "slug": "Bad Folder"}, // bad folder
	} {
		if code := br.do("POST", "/api/projects", bad, &e); code != 400 || e.Error == "" {
			t.Errorf("%v = %d %q", bad, code, e.Error)
		}
	}
	if _, err := s.Board.Project("new"); err == nil {
		t.Error("a refused create left a project behind")
	}
}
