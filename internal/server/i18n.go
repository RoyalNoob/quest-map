package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/RoyalNoob/quest-map/web"
)

// UI text catalogs (spec §4.4): built in as web/i18n/<lang>.json (en.json holds every key), extended by the
// board's i18n/<lang>.json, which can override keys or add a language. A board file that does not load never
// stops the UI: its language falls back to the built-in text and GET /api/warnings says what is wrong.

func (s *Server) i18nRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/i18n", s.api(s.apiLanguages))
	mux.HandleFunc("GET /api/i18n/{lang}", s.api(s.apiCatalog))
}

// langCode accepts BCP 47-like codes (ja, zh-TW, en-XA), so a code can never name a path.
var langCode = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// pluralForm is the Intl.PluralRules suffix of a plural key (spec §4.4).
var pluralForm = regexp.MustCompile(`\.(zero|one|two|few|many|other)$`)

type langJSON struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// i18nProblem is a board language file that did not load, or loaded with keys English does not have.
type i18nProblem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

func (s *Server) boardI18nDir() string { return filepath.Join(s.Board.Root(), "i18n") }

// builtinCatalog reads web/i18n/<code>.json; ok is false when there is none.
func builtinCatalog(code string) (map[string]string, bool) {
	data, err := fs.ReadFile(web.Files, path.Join("i18n", code+".json"))
	if err != nil {
		return nil, false
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil {
		return nil, false // a test keeps the built-in files valid
	}
	return m, true
}

// boardCatalog reads the board's i18n/<code>.json. ok is false when the file does not exist.
func (s *Server) boardCatalog(code string) (m map[string]string, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(s.boardI18nDir(), code+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, true, fmt.Errorf("not a JSON object of text values: %w", err)
	}
	return m, true, nil
}

// languageCodes lists the built-in and board languages.
func (s *Server) languageCodes() []string {
	seen := map[string]bool{}
	add := func(names []string) {
		for _, n := range names {
			if code := strings.TrimSuffix(path.Base(filepath.ToSlash(n)), ".json"); strings.HasSuffix(n, ".json") && langCode.MatchString(code) {
				seen[code] = true
			}
		}
	}
	builtin, _ := fs.Glob(web.Files, "i18n/*.json")
	add(builtin)
	board, _ := filepath.Glob(filepath.Join(s.boardI18nDir(), "*.json"))
	add(board)
	codes := make([]string, 0, len(seen))
	for c := range seen {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes
}

// apiLanguages lists the languages with a catalog, each named in its own language, and the board's default
// ([ui] language in board.toml).
func (s *Server) apiLanguages(r *http.Request) (any, error) {
	out := struct {
		Languages []langJSON `json:"languages"`
		Default   string     `json:"default"`
	}{Languages: []langJSON{}}
	for _, code := range s.languageCodes() {
		cat, err := s.catalog(code)
		if err != nil {
			continue
		}
		name := cat["lang.name"]
		if name == "" {
			name = code
		}
		out.Languages = append(out.Languages, langJSON{Code: code, Name: name})
	}
	var cfg struct {
		UI struct {
			Language string `toml:"language"`
		} `toml:"ui"`
	}
	if data, err := os.ReadFile(filepath.Join(s.Board.Root(), "board.toml")); err == nil {
		_ = toml.Unmarshal(data, &cfg) // a broken board.toml is reported where it is loaded
	}
	out.Default = cfg.UI.Language
	return out, nil
}

// catalog merges a language's built-in catalog with the board's file for it (the board's keys win).
func (s *Server) catalog(code string) (map[string]string, error) {
	if !langCode.MatchString(code) {
		return nil, badRequest("%q is not a language code like ja or zh-TW", code)
	}
	out, builtin := builtinCatalog(code)
	if out == nil {
		out = map[string]string{}
	}
	board, ok, err := s.boardCatalog(code)
	if err == nil {
		for k, v := range board {
			out[k] = v
		}
	}
	if !builtin && (!ok || err != nil) {
		return nil, apiError{http.StatusNotFound, "no catalog for language " + code}
	}
	return out, nil
}

func (s *Server) apiCatalog(r *http.Request) (any, error) { return s.catalog(r.PathValue("lang")) }

// i18nProblems checks the board's language files: files that do not load, and keys English does not have
// (a typo, or a key a newer quest-map renamed).
func (s *Server) i18nProblems() []i18nProblem {
	en, _ := builtinCatalog("en")
	files, _ := filepath.Glob(filepath.Join(s.boardI18nDir(), "*.json"))
	probs := []i18nProblem{}
	for _, f := range files {
		name := "i18n/" + filepath.Base(f)
		code := strings.TrimSuffix(filepath.Base(f), ".json")
		if !langCode.MatchString(code) {
			probs = append(probs, i18nProblem{name, "the file name is not a language code like ja or zh-TW"})
			continue
		}
		m, _, err := s.boardCatalog(code)
		if err != nil {
			probs = append(probs, i18nProblem{name, err.Error()})
			continue
		}
		var unknown []string
		for k := range m {
			// A plural form English lacks (x.few, x.two) is fine where English has x.other.
			_, plural := en[pluralForm.ReplaceAllString(k, "")+".other"]
			if _, ok := en[k]; !ok && !(plural && pluralForm.MatchString(k)) {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			if len(unknown) > 5 {
				unknown = append(unknown[:5], "…")
			}
			probs = append(probs, i18nProblem{name, "keys quest-map does not have: " + strings.Join(unknown, ", ")})
		}
	}
	return probs
}
