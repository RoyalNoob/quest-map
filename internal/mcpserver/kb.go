package mcpserver

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/kb"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// Knowledge base tools (spec §4.5): kb_find before exploring, kb_show to read a page, kb_write after an answer
// that took real exploring.

// PageOut is a page in a reply.
type PageOut struct {
	Ref       string         `json:"ref"` // project/slug, what kb_show and kb_write take
	Title     string         `json:"title"`
	Question  string         `json:"question,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	Sources   []string       `json:"sources,omitempty"`
	Checked   *board.Checked `json:"checked,omitempty"`
	Retired   bool           `json:"retired,omitempty"`
	Freshness kb.Freshness   `json:"freshness"`
	Body      string         `json:"body,omitempty"`
}

func sourceStrings(src []board.Source) []string {
	var out []string
	for _, s := range src {
		if s.Task != "" {
			out = append(out, "task:"+s.Task)
		} else {
			out = append(out, s.Path)
		}
	}
	return out
}

func pageOut(r index.PageRow, f kb.Freshness) PageOut {
	return PageOut{Ref: r.Project + "/" + r.Slug, Title: r.Title, Question: r.Question, Tags: r.Tags,
		Sources: sourceStrings(r.Sources), Checked: r.Checked, Retired: r.Retired, Freshness: f}
}

// freshText renders freshness for a reply line.
func freshText(f kb.Freshness, checked *board.Checked, now time.Time) string {
	switch f.Status {
	case kb.Stale:
		return "STALE – changed since the check: " + strings.Join(f.Changed, ", ")
	case kb.Fresh:
		return "fresh"
	}
	s := "unknown (" + f.Reason + ")"
	if checked != nil && !checked.At.IsZero() {
		s += fmt.Sprintf(", checked %s ago", board.FormatDuration(now.Sub(checked.At).Truncate(time.Hour)))
	}
	return s
}

// ---- kb_find ----

// KBFindIn is the input of kb_find.
type KBFindIn struct {
	Query   string `json:"query" jsonschema:"what you want to know, in your own words; terms tag:x, source:path, stale:yes|no, retired:yes narrow it"`
	Scope   string `json:"scope,omitempty" jsonschema:"project (default) or board: every project"`
	Project string `json:"project,omitempty" jsonschema:"project folder or key; default: the project linked to your working folder"`
}

// KBFindOut is the reply of kb_find.
type KBFindOut struct {
	Pages []PageOut `json:"pages"`
}

// KBFind searches the knowledge base and says how fresh each page is for the caller's working copy.
func (s *Service) KBFind(c Caller, in KBFindIn) (string, KBFindOut, error) {
	q, err := kb.ParseQuery(in.Query)
	if err != nil {
		return "", KBFindOut{}, err
	}
	project := ""
	if in.Scope != "board" {
		p, err := s.project(c, in.Project)
		if err != nil {
			return "", KBFindOut{}, err
		}
		project = p.Slug()
		q.Project = project
	}
	res, err := kb.Find(s.ix, q, func(slug string) string { return s.linkedCwd(c, slug) })
	if err != nil {
		return "", KBFindOut{}, err
	}
	out := KBFindOut{Pages: []PageOut{}}
	stale := 0
	var b strings.Builder
	now := s.b.Now()
	for _, r := range res {
		out.Pages = append(out.Pages, pageOut(r.PageRow, r.Freshness))
		if r.Freshness.Status == kb.Stale {
			stale++
		}
		fmt.Fprintf(&b, "%s/%s: %s [%s]\n", r.Project, r.Slug, r.Title, freshText(r.Freshness, r.Checked, now))
		if r.Question != "" {
			fmt.Fprintf(&b, "  answers: %s\n", r.Question)
		}
	}
	s.ix.CountLookup(project, len(res), stale)
	if len(res) == 0 {
		return "No page answers that yet. If you find the answer by exploring, save it with kb_write.", out, nil
	}
	b.WriteString("Read one with kb_show. A stale page is a good start: check what changed, then kb_write it (recheck: true if it still holds).")
	return b.String(), out, nil
}

// ---- kb_show ----

// KBShowIn is the input of kb_show.
type KBShowIn struct {
	Page    string `json:"page" jsonschema:"page as kb_find lists it (project/slug), or a slug in your project"`
	Project string `json:"project,omitempty" jsonschema:"project folder or key, when page is a bare slug"`
}

// pageRef resolves "project/slug" or a bare slug in the caller's (or the given) project.
func (s *Service) pageRef(c Caller, ref, project string) (*board.Project, string, error) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "/"); i > 0 {
		p, err := s.project(c, ref[:i])
		return p, ref[i+1:], err
	}
	p, err := s.project(c, project)
	return p, ref, err
}

// pageRow finds a page's index row (for its freshness).
func (s *Service) pageRow(p *board.Project, slug string) (index.PageRow, bool) {
	rows, err := s.ix.FindPages(index.PageQuery{Project: p.Slug(), Slug: slug, Retired: true, Limit: 1})
	if err != nil || len(rows) == 0 {
		return index.PageRow{}, false
	}
	return rows[0], true
}

// KBShow returns a page with its freshness and what changed since it was checked.
func (s *Service) KBShow(c Caller, in KBShowIn) (string, PageOut, error) {
	p, slug, err := s.pageRef(c, in.Page, in.Project)
	if err != nil {
		return "", PageOut{}, err
	}
	pg, err := p.Page(slug)
	if errors.Is(err, board.ErrNotFound) {
		return "", PageOut{}, errorf("no page %s/%s; kb_find lists pages", p.Slug(), slug)
	}
	if err != nil {
		return "", PageOut{}, err
	}
	row, ok := s.pageRow(p, slug)
	if !ok {
		row = index.PageRow{Project: p.Slug(), Slug: slug, Path: p.PagePath(slug), Title: pg.Title, Sources: pg.Sources, Checked: pg.Checked, Hash: pg.Version()}
	}
	f := kb.Check(s.ix, row, s.linkedCwd(c, p.Slug()))
	out := pageOut(row, f)
	out.Body = pg.Body
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\nfreshness: %s\n", out.Ref, pg.Title, freshText(f, pg.Checked, s.b.Now()))
	if pg.Question != "" {
		fmt.Fprintf(&b, "answers: %s\n", pg.Question)
	}
	if len(pg.Tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(pg.Tags, ", "))
	}
	if len(out.Sources) > 0 {
		fmt.Fprintf(&b, "sources: %s\n", strings.Join(out.Sources, ", "))
	}
	if c := pg.Checked; c != nil {
		fmt.Fprintf(&b, "checked: %s at %s by %s\n", c.Rev, c.At.In(s.b.Location()).Format("2006-01-02 15:04"), c.By)
	}
	if pg.Retired {
		b.WriteString("retired: this page is hidden from searches\n")
	}
	b.WriteString("\n" + pg.Body)
	return strings.TrimRight(b.String(), "\n"), out, nil
}

// ---- kb_write ----

// KBWriteIn is the input of kb_write.
type KBWriteIn struct {
	Page     string   `json:"page,omitempty" jsonschema:"page to update (project/slug or slug); leave empty to create a page named after its title"`
	Project  string   `json:"project,omitempty" jsonschema:"project folder or key; default: the project linked to your working folder"`
	Title    string   `json:"title,omitempty" jsonschema:"the page title; needed for a new page"`
	Question string   `json:"question,omitempty" jsonschema:"the question this page answers, in the words someone would ask it"`
	Body     string   `json:"body,omitempty" jsonschema:"the answer, in Markdown (at most 64 KB); empty keeps the body"`
	Tags     []string `json:"tags,omitempty" jsonschema:"tags such as physics or build; replaces the page's tags"`
	Sources  []string `json:"sources,omitempty" jsonschema:"what the answer was checked against: repo file paths (relative to the repository root) and task ids as task:RPG-12; replaces the page's sources"`
	Recheck  bool     `json:"recheck,omitempty" jsonschema:"true: the page still holds; record the check against your working copy now"`
	Retired  *bool    `json:"retired,omitempty" jsonschema:"true hides the page from searches (the file stays); false brings it back"`
}

// KBWrite creates or updates a page. Every write records the check from the caller's working copy (HEAD) and
// session, so the page is fresh for what the caller looked at.
func (s *Service) KBWrite(c Caller, in KBWriteIn) (string, PageOut, error) {
	var p *board.Project
	var slug string
	var err error
	create := false
	if strings.TrimSpace(in.Page) == "" {
		if strings.TrimSpace(in.Title) == "" {
			return "", PageOut{}, errorf("pass title for a new page, or page to update one")
		}
		if p, err = s.project(c, in.Project); err != nil {
			return "", PageOut{}, err
		}
		if slug = board.Slugify(in.Title); slug == "" {
			return "", PageOut{}, errorf("the title %q makes no page name; use letters or digits", in.Title)
		}
		create = true
	} else {
		if p, slug, err = s.pageRef(c, in.Page, in.Project); err != nil {
			return "", PageOut{}, err
		}
		if _, err := p.Page(slug); errors.Is(err, board.ErrNotFound) {
			if strings.TrimSpace(in.Title) == "" {
				return "", PageOut{}, errorf("no page %s/%s; pass title to create it", p.Slug(), slug)
			}
			create = true
		}
	}
	e := board.PageEdit{}
	if in.Title != "" {
		e.Title = &in.Title
	}
	if in.Question != "" {
		e.Question = &in.Question
	}
	if in.Body != "" {
		e.Body = &in.Body
	}
	if in.Tags != nil {
		e.Tags = &in.Tags
	}
	if in.Sources != nil {
		src := s.parseSources(c, in.Sources)
		e.Sources = &src
	}
	e.Retired = in.Retired
	cwd := s.linkedCwd(c, p.Slug())
	contentChange := e.Title != nil || e.Question != nil || e.Body != nil || e.Tags != nil || e.Sources != nil
	if create || contentChange || in.Recheck {
		e.Checked = &board.Checked{Rev: kb.Revision(cwd), By: c.ID}
	}
	if !create && !contentChange && !in.Recheck && in.Retired == nil {
		return "", PageOut{}, errorf("nothing to write: pass body, title, question, tags, sources, recheck or retired")
	}
	pg, changed, err := p.WritePage(slug, create, e, s.actor(c))
	if errors.Is(err, board.ErrPageExists) {
		return "", PageOut{}, errorf("page %s/%s exists: update it with page %q instead of writing a second one", p.Slug(), slug, p.Slug()+"/"+slug)
	}
	if err != nil {
		return "", PageOut{}, err
	}
	s.ix.RefreshPage(p.PagePath(slug)) // searchable at once, not after the watcher's delay
	row, _ := s.pageRow(p, slug)
	if row.Slug == "" {
		row = index.PageRow{Project: p.Slug(), Slug: slug, Path: p.PagePath(slug), Title: pg.Title, Sources: pg.Sources, Checked: pg.Checked, Hash: pg.Version()}
	}
	f := kb.Check(s.ix, row, cwd)
	out := pageOut(row, f)
	verb := "Updated"
	if create {
		verb = "Created"
	}
	text := fmt.Sprintf("%s %s (%s) – %s.", verb, out.Ref, strings.Join(changed, ", "), freshText(f, pg.Checked, s.b.Now()))
	if pg.Checked != nil && pg.Checked.Rev == "" && hasPath(pg.Sources) {
		if c.Cwd != "" && cwd == "" {
			text += fmt.Sprintf(" Note: your working folder is not a repository linked to %s (repo_link links one), so the page's freshness stays unknown.", p.Key())
		} else {
			text += " Note: your working folder is not a git repository, so the page's freshness stays unknown."
		}
	}
	var notFound []string
	for _, ch := range f.Changed {
		if strings.HasSuffix(ch, kb.NotFound) {
			notFound = append(notFound, strings.TrimSuffix(ch, kb.NotFound))
		}
	}
	if len(notFound) > 0 {
		text += " Note: not in your working copy: " + strings.Join(notFound, ", ") +
			" – fix these sources (paths relative to the repository root) so the page can be fresh."
	}
	return text, out, nil
}

func hasPath(src []board.Source) bool {
	for _, s := range src {
		if s.Path != "" {
			return true
		}
	}
	return false
}

// parseSources reads "task:RPG-12" and file paths. An absolute path inside the caller's repository becomes
// relative to the repository root, so every clone reads it the same.
func (s *Service) parseSources(c Caller, in []string) []board.Source {
	root := vcs.GitRoot(c.Cwd)
	var out []board.Source
	for _, v := range in {
		v = strings.TrimSpace(v)
		if id, ok := strings.CutPrefix(strings.ToLower(v), "task:"); ok {
			out = append(out, board.Source{Task: strings.ToUpper(strings.TrimSpace(id))})
			continue
		}
		if root != "" && filepath.IsAbs(v) {
			if rel, err := filepath.Rel(root, v); err == nil && !strings.HasPrefix(rel, "..") {
				v = rel
			}
		}
		out = append(out, board.Source{Path: filepath.ToSlash(v)})
	}
	return out
}
