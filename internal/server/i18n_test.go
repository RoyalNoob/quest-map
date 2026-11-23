package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBoardLanguageFile is part of the Localization done-when: a board i18n/ file adds a language without code
// changes; one that does not load is reported and never stops the UI.
func TestBoardLanguageFile(t *testing.T) {
	s, _ := startWithProject(t)
	br := newBrowser(t, s)
	dir := filepath.Join(s.Board.Root(), "i18n")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{"lang.name": "Deutsch", "nav.board": "Tafel", "nope.key": "x"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "ja.json"), []byte(`{"nav.board": "かんばん"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "fr.json"), []byte(`{"nav.board": 3`), 0o644)
	// Polish plural forms English does not have are fine; a plural of a key English lacks is not.
	os.WriteFile(filepath.Join(dir, "pl.json"), []byte(`{"bulk.done.few": "x", "bulk.done.many": "x"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "cs.json"), []byte(`{"nope.few": "x"}`), 0o644)
	os.WriteFile(filepath.Join(s.Board.Root(), "board.toml"), []byte("[ui]\nlanguage = \"de\"\n"), 0o644)

	var list struct {
		Languages []langJSON `json:"languages"`
		Default   string     `json:"default"`
	}
	if code := br.do("GET", "/api/i18n", nil, &list); code != 200 {
		t.Fatalf("languages = %d", code)
	}
	names := map[string]string{}
	for _, l := range list.Languages {
		names[l.Code] = l.Name
	}
	for code, want := range map[string]string{"en": "English", "ja": "日本語", "zh-TW": "繁體中文", "de": "Deutsch"} {
		if names[code] != want {
			t.Errorf("language %s = %q, want %q (all: %v)", code, names[code], want, names)
		}
	}
	if _, ok := names["fr"]; ok {
		t.Error("a board file that does not load (fr) is offered")
	}
	if list.Default != "de" {
		t.Errorf("default = %q, want de from board.toml [ui] language", list.Default)
	}

	var de, ja map[string]string
	br.do("GET", "/api/i18n/de", nil, &de)
	br.do("GET", "/api/i18n/ja", nil, &ja)
	if de["nav.board"] != "Tafel" || ja["nav.board"] != "かんばん" || ja["drawer.save"] == "" {
		t.Errorf("de nav.board = %q; ja nav.board = %q, drawer.save = %q", de["nav.board"], ja["nav.board"], ja["drawer.save"])
	}
	if code := br.do("GET", "/api/i18n/xx", nil, nil); code != 404 {
		t.Errorf("unknown language = %d, want 404", code)
	}
	if code := br.do("GET", "/api/i18n/..%2Fboard", nil, nil); code != 400 && code != 404 {
		t.Errorf("path-like language = %d, want 400", code)
	}

	var w struct {
		I18n []i18nProblem `json:"i18n"`
	}
	br.do("GET", "/api/warnings", nil, &w)
	got := map[string]string{}
	for _, p := range w.I18n {
		got[p.File] = p.Error
	}
	if got["i18n/fr.json"] == "" || got["i18n/de.json"] == "" || got["i18n/cs.json"] == "" || got["i18n/ja.json"] != "" || got["i18n/pl.json"] != "" {
		t.Errorf("i18n warnings = %v, want fr (broken), de and cs (unknown keys)", got)
	}
}
