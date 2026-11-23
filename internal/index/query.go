package index

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RoyalNoob/quest-map/internal/filter"
)

// Row is one task as the index knows it.
type Row struct {
	ID           string
	Project      string
	Path         string
	Archived     bool
	Title        string
	Status       string
	Doer         string
	Assignees    []string
	Custom       map[string]string `json:",omitempty"` // custom front-matter fields
	Priority     string
	Discipline   string
	Milestone    string
	Asset        string
	Labels       []string
	BlockedBy    []string
	Blocked      []string // the blockers still open: they exist, are not done and not archived (filter blocked:yes)
	FoundIn      string
	FixedIn      string
	PipelineName string
	PipelineAt   string
	ClaimBy      string
	ClaimAt      time.Time
	Created      time.Time
	Updated      time.Time
	SpentTokens  int64
	SpentTime    time.Duration
	SpentRecords int
	SpentSelf    int
	ParseError   string
	Version      string // the task file's version (board.Task.Version), for edits that must not overwrite newer ones
}

// FindOptions scope a search.
type FindOptions struct {
	Project         string // project slug; "" searches all projects
	IncludeArchived bool
	Limit           int      // 0: no limit
	Fields          []string // custom field keys the filter string may use (the project's suite, spec §4.1)
}

// Find returns the tasks matching a filter, in board order: project, then id number.
func (ix *Index) Find(q filter.Query, opts FindOptions) ([]Row, error) {
	where, args, err := ix.compile(q)
	if err != nil {
		return nil, err
	}
	if opts.Project != "" {
		where = append(where, "t.project = ?")
		args = append(args, opts.Project)
	}
	if !opts.IncludeArchived {
		where = append(where, "t.archived = 0")
	}
	query := `SELECT t.id, t.project, t.path, t.archived, IFNULL(t.title,''), IFNULL(t.status,''), IFNULL(t.doer,''),
		IFNULL(t.priority,''), IFNULL(t.discipline,''), IFNULL(t.milestone,''), IFNULL(t.asset,''), IFNULL(t.found_in,''),
		IFNULL(t.claim_by,''), IFNULL(t.claim_at,''), IFNULL(t.created,''), IFNULL(t.updated,''),
		t.spent_tokens, t.spent_seconds, t.spent_records, t.spent_self, IFNULL(t.parse_error,''),
		IFNULL(t.fixed_in,''), IFNULL(t.pipeline_name,''), IFNULL(t.pipeline_at,''),
		(SELECT group_concat(label, char(31)) FROM labels WHERE task = t.id),
		(SELECT group_concat(blocker, char(31)) FROM blocks WHERE task = t.id),
		(SELECT group_concat(bl.blocker, char(31) ORDER BY bl.rowid) FROM blocks bl JOIN tasks o ON o.id = bl.blocker
			WHERE bl.task = t.id AND IFNULL(o.status,'') != 'done' AND o.archived = 0),
		(SELECT group_concat(name, char(31)) FROM assignees WHERE task = t.id),
		(SELECT group_concat(key || char(30) || value, char(31)) FROM custom WHERE task = t.id),
		substr(IFNULL(t.hash,''), 1, 16)
		FROM tasks t`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += ` ORDER BY t.project, CAST(substr(t.id, instr(t.id, '-') + 1) AS INTEGER), t.id`
	if opts.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", opts.Limit)
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	rows, err := ix.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var claimAt, created, updated string
		var spentSec int64
		var labels, blocks, open, assignees, custom sql.NullString
		if err := rows.Scan(&r.ID, &r.Project, &r.Path, &r.Archived, &r.Title, &r.Status, &r.Doer, &r.Priority,
			&r.Discipline, &r.Milestone, &r.Asset, &r.FoundIn, &r.ClaimBy, &claimAt, &created, &updated,
			&r.SpentTokens, &spentSec, &r.SpentRecords, &r.SpentSelf, &r.ParseError, &r.FixedIn, &r.PipelineName, &r.PipelineAt,
			&labels, &blocks, &open, &assignees, &custom, &r.Version); err != nil {
			return nil, err
		}
		r.ClaimAt, r.Created, r.Updated = parseTS(claimAt), parseTS(created), parseTS(updated)
		r.SpentTime = time.Duration(spentSec) * time.Second
		r.Labels, r.BlockedBy, r.Blocked, r.Assignees = splitList(labels), splitList(blocks), splitList(open), splitList(assignees)
		for _, kv := range splitList(custom) {
			if k, v, ok := strings.Cut(kv, "\x1e"); ok {
				if r.Custom == nil {
					r.Custom = map[string]string{}
				}
				r.Custom[k] = v
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Link is a task another one points at through blocked_by, as the drawer lists it.
type Link struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`   // "" when no such task is indexed
	Archived bool   `json:"archived"` // archived counts as done
}

// Links returns the tasks id's blocked_by names (missing ones with an empty status) and the open tasks waiting
// on id.
func (ix *Index) Links(id string) (blockedBy, blocks []Link, err error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	scan := func(q string) ([]Link, error) {
		rows, err := ix.db.Query(q, id)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Link{}
		for rows.Next() {
			var l Link
			if err := rows.Scan(&l.ID, &l.Title, &l.Status, &l.Archived); err != nil {
				return nil, err
			}
			out = append(out, l)
		}
		return out, rows.Err()
	}
	if blockedBy, err = scan(`SELECT bl.blocker, IFNULL(o.title,''), IFNULL(o.status,''), IFNULL(o.archived,0)
		FROM blocks bl LEFT JOIN tasks o ON o.id = bl.blocker WHERE bl.task = ? ORDER BY bl.rowid`); err != nil {
		return nil, nil, err
	}
	blocks, err = scan(`SELECT t.id, IFNULL(t.title,''), IFNULL(t.status,''), t.archived FROM blocks bl JOIN tasks t ON t.id = bl.task
		WHERE bl.blocker = ? AND IFNULL(t.status,'') != 'done' AND t.archived = 0
		ORDER BY t.project, CAST(substr(t.id, instr(t.id, '-') + 1) AS INTEGER)`)
	return blockedBy, blocks, err
}

// FindString parses a filter string and runs Find.
func (ix *Index) FindString(s string, opts FindOptions) ([]Row, error) {
	q, err := filter.ParseFields(s, opts.Fields)
	if err != nil {
		return nil, err
	}
	return ix.Find(q, opts)
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func splitList(s sql.NullString) []string {
	if !s.Valid || s.String == "" {
		return nil
	}
	return strings.Split(s.String, "\x1f")
}

// compile turns filter terms into SQL conditions on tasks t.
func (ix *Index) compile(q filter.Query) ([]string, []any, error) {
	var where []string
	var args []any
	for _, term := range q.Terms {
		cond, a, err := ix.termSQL(term)
		if err != nil {
			return nil, nil, err
		}
		if term.Neg {
			cond = "NOT (" + cond + ")"
		}
		where = append(where, cond)
		args = append(args, a...)
	}
	return where, args, nil
}

func (ix *Index) termSQL(t filter.Term) (string, []any, error) {
	v := t.Value
	switch t.Key {
	case "":
		// The trigram index matches substrings in any language (Japanese has no spaces between words) but needs
		// at least three characters; shorter words are matched with LIKE.
		if utf8.RuneCountInString(strings.TrimSpace(v)) >= 3 {
			return `t.rowid IN (SELECT rowid FROM tasks_fts WHERE tasks_fts MATCH ?)`, []any{ftsQuery(v)}, nil
		}
		pat := "%" + likeEscape(strings.TrimSpace(v)) + "%"
		return `t.rowid IN (SELECT rowid FROM tasks_fts WHERE title LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\' OR log LIKE ? ESCAPE '\')`,
			[]any{pat, pat, pat}, nil
	case "status", "doer", "priority":
		return "IFNULL(t." + t.Key + ",'') = ?", []any{v}, nil
	case "discipline", "milestone":
		return "IFNULL(t." + t.Key + ",'') = ? COLLATE NOCASE", []any{v}, nil
	case "assignee":
		return `EXISTS (SELECT 1 FROM assignees a WHERE a.task = t.id AND a.name = ? COLLATE NOCASE)`, []any{v}, nil
	case "label":
		return `EXISTS (SELECT 1 FROM labels l WHERE l.task = t.id AND l.label = ? COLLATE NOCASE)`, []any{v}, nil
	case "asset":
		pat := globToLike(v)
		return `(IFNULL(t.asset,'') LIKE ? ESCAPE '\' OR IFNULL(t.asset_name,'') LIKE ? ESCAPE '\')`, []any{pat, pat}, nil
	case "model":
		pat := globToLike(v)
		return `(EXISTS (SELECT 1 FROM usage u WHERE u.owner = t.id AND u.model LIKE ? ESCAPE '\')
			OR EXISTS (SELECT 1 FROM transitions s WHERE s.owner = t.id AND s.model LIKE ? ESCAPE '\'))`, []any{pat, pat}, nil
	case "found":
		if v == "latest" {
			return `IFNULL(t.found_in,'') = IFNULL((SELECT b.id FROM releases b WHERE b.project = t.project ORDER BY b.date DESC, b.id DESC LIMIT 1), char(0))`, nil, nil
		}
		if plat, ok := strings.CutPrefix(v, "latest/"); ok {
			return `IFNULL(t.found_in,'') = IFNULL((SELECT b.id FROM releases b WHERE b.project = t.project AND b.target = ? COLLATE NOCASE
				ORDER BY b.date DESC, b.id DESC LIMIT 1), char(0))`, []any{plat}, nil
		}
		return `IFNULL(t.found_in,'') = ?`, []any{v}, nil
	case "blocked":
		cond := `EXISTS (SELECT 1 FROM blocks bl JOIN tasks o ON o.id = bl.blocker
			WHERE bl.task = t.id AND IFNULL(o.status,'') != 'done' AND o.archived = 0)`
		return yesNo(cond, v), nil, nil
	case "stale":
		cutoff := ix.opts.Now().Add(-ix.opts.ClaimTTL).UTC().Format(time.RFC3339)
		return yesNo(`(IFNULL(t.status,'') = 'doing' AND IFNULL(t.updated,'') < ?)`, v), []any{cutoff}, nil
	case "spent":
		return yesNo(`t.spent_any > 0`, v), nil, nil // any usage record (§7), counted or not
	}
	if !slices.Contains(filter.Keys(), t.Key) { // a suite's custom field (filter.ParseFields let it through)
		return `EXISTS (SELECT 1 FROM custom c WHERE c.task = t.id AND c.key = ? AND c.value = ? COLLATE NOCASE)`, []any{t.Key, v}, nil
	}
	return "", nil, fmt.Errorf("filter %s: not supported by the index", t.Key)
}

func yesNo(cond, v string) string {
	if v == "no" {
		return "NOT " + cond
	}
	return cond
}

// ftsQuery makes an FTS5 query from a free word or phrase: one quoted phrase, which the trigram index
// matches as a substring.
func ftsQuery(s string) string {
	return `"` + strings.ReplaceAll(strings.TrimSpace(s), `"`, `""`) + `"`
}

// likeEscape escapes LIKE's wildcards so s matches literally.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// globToLike converts * and ? wildcards to a LIKE pattern, escaping LIKE's own wildcards.
func globToLike(g string) string {
	var b strings.Builder
	for _, r := range g {
		switch r {
		case '\\', '%', '_':
			b.WriteRune('\\')
			b.WriteRune(r)
		case '*':
			b.WriteRune('%')
		case '?':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
