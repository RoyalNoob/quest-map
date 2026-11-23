package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/kb"
)

// TestHandEditedPageIsSearchable is part of the M9 done-when: a page written by hand (an editor, a git pull) is
// found by a search within a second, through the watcher.
func TestHandEditedPageIsSearchable(t *testing.T) {
	s, p := startWithProject(t)
	br := newBrowser(t, s)
	os.MkdirAll(p.KBDir(), 0o755)
	start := time.Now()
	os.WriteFile(filepath.Join(p.KBDir(), "shader-cache.md"),
		[]byte("---\ntitle: Where the shader cache lives\ntags: [render]\n---\n\nDerivedDataCache under the project folder.\n"), 0o644)
	var found []kb.Result
	for time.Since(start) < time.Second {
		br.do("GET", "/api/kb?project=rpg&q=shader+cache", nil, &found)
		if len(found) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(found) != 1 || found[0].Slug != "shader-cache" {
		t.Fatalf("not searchable within a second: %+v", found)
	}
	t.Logf("searchable after %v", time.Since(start).Round(time.Millisecond))
}

func TestKBREST(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	var pg pageJSON
	if code := br.do("POST", "/api/kb/rpg", map[string]any{"title": "Build steps", "body": "Run the cook.", "tags": []string{"build"},
		"sources": []string{"task:rpg-1"}}, &pg); code != 200 || pg.Slug != "build-steps" || pg.Sources[0].Task != "RPG-1" {
		t.Fatalf("create = %d %+v", code, pg)
	}
	if code := br.do("POST", "/api/kb/rpg", map[string]any{"title": "Build steps"}, nil); code != 409 {
		t.Errorf("duplicate = %d, want 409", code)
	}
	old := pg.Version
	if code := br.do("PUT", "/api/kb/rpg/build-steps", map[string]any{"body": "Run the cook, then package.", "version": old}, &pg); code != 200 || pg.Body != "Run the cook, then package." {
		t.Fatalf("edit = %d %+v", code, pg)
	}
	if code := br.do("PUT", "/api/kb/rpg/build-steps", map[string]any{"body": "stale edit", "version": old}, nil); code != 409 {
		t.Errorf("stale version = %d, want 409", code)
	}
	var forTask []kb.Result
	if br.do("GET", "/api/tasks/RPG-1/kb", nil, &forTask); len(forTask) != 1 || forTask[0].Freshness.Status == "" {
		t.Errorf("task pages = %+v", forTask)
	}
	if code := br.do("GET", "/api/kb/rpg/nope", nil, nil); code != 404 {
		t.Errorf("missing page = %d", code)
	}
}
