package index

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// Knowledge base rows (spec §4.5): one row per page file, its text in kb_fts (trigram, so any language and any
// substring matches), freshness results cached per (page version, working copy revision), lookups counted.

const kbSchema = `
CREATE TABLE kb (id INTEGER PRIMARY KEY, path TEXT UNIQUE NOT NULL, project TEXT NOT NULL, slug TEXT NOT NULL,
  title TEXT, tags TEXT, question TEXT, sources TEXT, rev TEXT, checked_at TEXT, checked_by TEXT, retired INTEGER,
  created TEXT, hash TEXT, parse_error TEXT);
CREATE INDEX kb_project ON kb(project, slug);
CREATE VIRTUAL TABLE kb_fts USING fts5(title, question, tags, body, tokenize='trigram');
CREATE TABLE kb_fresh (path TEXT NOT NULL, hash TEXT NOT NULL, head TEXT NOT NULL, status TEXT NOT NULL, changed TEXT,
  PRIMARY KEY (path, hash, head));
CREATE TABLE kb_lookups (project TEXT, at TEXT, found INTEGER, stale INTEGER);
`

// PageRow is a page as the index knows it.
type PageRow struct {
	Project  string         `json:"project"`
	Slug     string         `json:"slug"`
	Path     string         `json:"-"`
	Title    string         `json:"title"`
	Tags     []string       `json:"tags"`
	Question string         `json:"question,omitempty"`
	Sources  []board.Source `json:"sources"`
	Checked  *board.Checked `json:"checked,omitempty"`
	Retired  bool           `json:"retired,omitempty"`
	Created  string         `json:"created,omitempty"`
	Hash     string         `json:"-"`
	Rank     float64        `json:"-"`
}

func isPageFile(path string) bool {
	return strings.HasSuffix(path, ".md") && filepath.Base(filepath.Dir(path)) == "kb"
}

// RefreshPage reads one page file into the index (or drops its row when the file is gone). It reports whether
// the index changed.
func (ix *Index) RefreshPage(path string) (bool, error) {
	p, ok := ix.projectOfPath(path)
	if !ok {
		return false, nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	tx, err := ix.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	changed, err := refreshPageTx(tx, p.Slug(), path)
	if err != nil || !changed {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	ix.emit(Change{Kind: "kb", Project: p.Slug(), Path: path})
	return true, nil
}

func refreshPageTx(tx *sql.Tx, project, path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		res, err := tx.Exec(`DELETE FROM kb_fts WHERE rowid = (SELECT id FROM kb WHERE path = ?)`, path)
		if err != nil {
			return false, err
		}
		n1, _ := res.RowsAffected()
		res, err = tx.Exec(`DELETE FROM kb WHERE path = ?`, path)
		if err != nil {
			return false, err
		}
		n2, _ := res.RowsAffected()
		return n1+n2 > 0, nil
	}
	if err != nil {
		return false, err
	}
	pg, perr := board.ParsePage(data)
	hash := ""
	if pg != nil {
		hash = pg.Version()
	} else {
		hash = board.ContentHash(data)
	}
	var old string
	if err := tx.QueryRow(`SELECT hash FROM kb WHERE path = ?`, path).Scan(&old); err == nil && old == hash {
		return false, nil
	}
	if _, err := tx.Exec(`DELETE FROM kb_fts WHERE rowid = (SELECT id FROM kb WHERE path = ?)`, path); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM kb WHERE path = ?`, path); err != nil {
		return false, err
	}
	slug := strings.TrimSuffix(filepath.Base(path), ".md")
	if perr != nil {
		_, err := tx.Exec(`INSERT INTO kb (path, project, slug, hash, parse_error) VALUES (?,?,?,?,?)`, path, project, slug, hash, perr.Error())
		return err == nil, err
	}
	tags, _ := json.Marshal(pg.Tags)
	sources, _ := json.Marshal(pg.Sources)
	var rev, at, by string
	if c := pg.Checked; c != nil {
		rev, by = c.Rev, c.By
		if !c.At.IsZero() {
			at = c.At.UTC().Format(time.RFC3339)
		}
	}
	created := ""
	if !pg.Created.IsZero() {
		created = pg.Created.UTC().Format(time.RFC3339)
	}
	res, err := tx.Exec(`INSERT INTO kb (path, project, slug, title, tags, question, sources, rev, checked_at, checked_by, retired, created, hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, path, project, slug, pg.Title, string(tags), pg.Question, string(sources), rev, at, by, pg.Retired, created, hash)
	if err != nil {
		return false, err
	}
	id, _ := res.LastInsertId()
	_, err = tx.Exec(`INSERT INTO kb_fts (rowid, title, question, tags, body) VALUES (?,?,?,?,?)`,
		id, pg.Title, pg.Question, strings.Join(pg.Tags, " "), pg.Body)
	return err == nil, err
}

// loadPagesTx indexes every page of the projects (Rebuild).
func loadPagesTx(tx *sql.Tx, projects []*board.Project) error {
	for _, stmt := range []string{"DELETE FROM kb", "DELETE FROM kb_fts"} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	for _, p := range projects {
		entries, err := os.ReadDir(p.KBDir())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if path := filepath.Join(p.KBDir(), e.Name()); !e.IsDir() && isPageFile(path) {
				if _, err := refreshPageTx(tx, p.Slug(), path); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// PageQuery selects pages: free words (any of them, best match first), tags (all), a source path or task
// substring, and whether retired pages are included. Project "" means every project.
type PageQuery struct {
	Project string
	Slug    string // only this page
	Words   []string
	Tags    []string
	Source  string
	Retired bool
	Limit   int
}

// stopWords are left out of a search: they match nearly every page.
var stopWords = map[string]bool{"the": true, "and": true, "how": true, "why": true, "what": true, "does": true, "for": true,
	"with": true, "from": true, "this": true, "that": true, "are": true, "was": true, "where": true, "when": true, "which": true,
	"can": true, "not": true, "into": true, "its": true, "has": true, "have": true, "our": true, "you": true}

// searchTerms turns free words into trigram FTS terms. A question asked in other words should still find the
// page, so every word is optional (OR), and common English endings are also tried without the ending ("falls"
// finds "fall", "generated" finds "generation" through "generat").
func searchTerms(words []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if utf8.RuneCountInString(s) >= 3 && !seen[s] && !stopWords[s] {
			seen[s] = true
			out = append(out, `"`+strings.ReplaceAll(s, `"`, `""`)+`"`)
		}
	}
	for _, w := range words {
		w = strings.ToLower(strings.Trim(w, ".,;:!?()[]{}'\"`"))
		add(w)
		for _, suf := range []string{"ing", "ed", "es", "s", "ion", "ions", "ly"} {
			if stem, ok := strings.CutSuffix(w, suf); ok && utf8.RuneCountInString(stem) >= 4 {
				add(stem)
			}
		}
	}
	return out
}

// FindPages lists pages matching q, best first (pages without a match rank by title when no words are given).
func (ix *Index) FindPages(q PageQuery) ([]PageRow, error) {
	if q.Limit <= 0 {
		q.Limit = 20
	}
	where := []string{"k.parse_error IS NULL"}
	var args []any
	if q.Project != "" {
		where = append(where, "k.project = ?")
		args = append(args, q.Project)
	}
	if q.Slug != "" {
		where = append(where, "k.slug = ?")
		args = append(args, q.Slug)
	}
	if !q.Retired {
		where = append(where, "NOT k.retired")
	}
	for _, tag := range q.Tags {
		where = append(where, "EXISTS (SELECT 1 FROM json_each(k.tags) WHERE lower(value) = lower(?))")
		args = append(args, tag)
	}
	if q.Source != "" {
		where = append(where, "k.sources LIKE ? ESCAPE '\\'")
		args = append(args, "%"+likeEscape(filepath.ToSlash(q.Source))+"%")
	}
	from, order := "kb k", "k.title COLLATE NOCASE"
	rank := "0"
	if terms := searchTerms(q.Words); len(terms) > 0 {
		from = "kb k JOIN kb_fts f ON f.rowid = k.id"
		where = append(where, "kb_fts MATCH ?")
		args = append(args, strings.Join(terms, " OR "))
		rank = "bm25(kb_fts, 5.0, 4.0, 3.0, 1.0)"
		order = "r"
	} else if len(q.Words) > 0 {
		return nil, nil // only words too short or too common to search for
	}
	rows, err := ix.db.Query(`SELECT k.project, k.slug, k.path, k.title, k.tags, IFNULL(k.question,''), k.sources, IFNULL(k.rev,''),
		IFNULL(k.checked_at,''), IFNULL(k.checked_by,''), k.retired, IFNULL(k.created,''), k.hash, `+rank+` AS r
		FROM `+from+` WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order+` LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageRow
	for rows.Next() {
		var r PageRow
		var tags, sources, rev, at, by string
		if err := rows.Scan(&r.Project, &r.Slug, &r.Path, &r.Title, &tags, &r.Question, &sources, &rev, &at, &by, &r.Retired, &r.Created, &r.Hash, &r.Rank); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(tags), &r.Tags)
		json.Unmarshal([]byte(sources), &r.Sources)
		if rev != "" || at != "" {
			c := &board.Checked{Rev: rev, By: by}
			c.At, _ = time.Parse(time.RFC3339, at)
			r.Checked = c
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PagesForTask lists the pages that name a task as a source.
func (ix *Index) PagesForTask(id string) ([]PageRow, error) {
	b, _ := json.Marshal(board.Source{Task: strings.ToUpper(id)})
	return ix.FindPages(PageQuery{Source: strings.Trim(string(b), "{}"), Retired: false, Limit: 50})
}

// TaskChangedSince reports whether a task changed status after t (a page's task source is then stale).
func (ix *Index) TaskChangedSince(id string, t time.Time) (bool, error) {
	var n int
	err := ix.db.QueryRow(`SELECT COUNT(*) FROM transitions WHERE (owner = ? OR task = ?) AND at > ?`,
		id, id, t.UTC().Format(time.RFC3339)).Scan(&n)
	return n > 0, err
}

// CachedFreshness returns a freshness result cached for a page version at a working copy revision.
func (ix *Index) CachedFreshness(path, hash, head string) (status string, changed []string, ok bool) {
	var c string
	if ix.db.QueryRow(`SELECT status, IFNULL(changed,'') FROM kb_fresh WHERE path = ? AND hash = ? AND head = ?`, path, hash, head).Scan(&status, &c) != nil {
		return "", nil, false
	}
	json.Unmarshal([]byte(c), &changed)
	return status, changed, true
}

// CacheFreshness stores a freshness result for a page version at a working copy revision.
func (ix *Index) CacheFreshness(path, hash, head, status string, changed []string) error {
	c, _ := json.Marshal(changed)
	_, err := ix.db.Exec(`INSERT OR REPLACE INTO kb_fresh (path, hash, head, status, changed) VALUES (?,?,?,?,?)`, path, hash, head, status, string(c))
	return err
}

// CountLookup records a kb_find: how many pages it returned and how many of them were stale. Lookups live in
// the index only; they are not synced (§4.5).
func (ix *Index) CountLookup(project string, found, stale int) {
	ix.db.Exec(`INSERT INTO kb_lookups (project, at, found, stale) VALUES (?,?,?,?)`, project, ix.opts.Now().UTC().Format(time.RFC3339), found, stale)
}

// PageProblems lists page files that do not parse.
func (ix *Index) PageProblems() ([]ParseProblem, error) {
	rows, err := ix.db.Query(`SELECT path, parse_error FROM kb WHERE parse_error IS NOT NULL ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ParseProblem
	for rows.Next() {
		var p ParseProblem
		if err := rows.Scan(&p.Path, &p.Err); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
