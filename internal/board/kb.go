package board

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// Knowledge base pages (spec §4.5): projects/<slug>/kb/<page>.md, Markdown with front matter, written like
// task files (atomic, through the server, unknown keys kept, a page that does not parse is never overwritten).

// KindKB is the activity record kind of a page write.
const KindKB = "kb"

// MaxPageBody is the largest page body the server writes.
const MaxPageBody = 64 << 10

// Source is what a page was checked against: a file path in the working copy, or a task.
type Source struct {
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	Task string `yaml:"task,omitempty" json:"task,omitempty"`
}

// Checked records the working copy revision a page was last checked against, when and by whom.
type Checked struct {
	Rev string    `yaml:"rev,omitempty" json:"rev,omitempty"`
	At  time.Time `yaml:"at" json:"at"`
	By  string    `yaml:"by,omitempty" json:"by,omitempty"`
}

// Page is one knowledge base page.
type Page struct {
	Slug     string
	Title    string
	Tags     []string
	Question string
	Sources  []Source
	Checked  *Checked
	Retired  bool
	Created  time.Time
	Body     string

	doc     *yaml.Node // parsed front matter, kept so unknown keys and comments survive a write
	crlf    bool
	version string
}

// ContentHash is the version hash of file content (as Page.Version and Task.Version compute it).
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// Version is a hash of the page file as read.
func (pg *Page) Version() string { return pg.version }

// ParsePage reads a page file.
func ParsePage(data []byte) (*Page, error) {
	sum := sha256.Sum256(data)
	pg := &Page{version: hex.EncodeToString(sum[:8])}
	s := strings.TrimPrefix(string(data), utf8BOM)
	if strings.Contains(s, "\r\n") {
		pg.crlf = true
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	if !strings.HasPrefix(s, "---\n") {
		return nil, &ParseError{Err: errors.New("missing front matter (file must start with ---)")}
	}
	end := strings.Index(s[3:], "\n---\n")
	var fm, rest string
	switch {
	case end >= 0:
		fm, rest = s[4:3+end+1], s[3+end+5:]
	case strings.HasSuffix(s, "\n---"):
		fm = s[4 : len(s)-3]
	default:
		return nil, &ParseError{Err: errors.New("front matter is not closed with ---")}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fm), &doc); err != nil {
		return nil, &ParseError{Err: fmt.Errorf("front matter: %w", err)}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, &ParseError{Err: errors.New("front matter is not a YAML mapping")}
	}
	pg.doc = &doc
	var f struct {
		Title    string   `yaml:"title"`
		Tags     []string `yaml:"tags"`
		Question string   `yaml:"question"`
		Sources  []Source `yaml:"sources"`
		Checked  *Checked `yaml:"checked"`
		Retired  bool     `yaml:"retired"`
		Created  time.Time
	}
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i].Value, m.Content[i+1]
		var err error
		switch k {
		case "title":
			err = v.Decode(&f.Title)
		case "tags":
			err = v.Decode(&f.Tags)
		case "question":
			err = v.Decode(&f.Question)
		case "sources":
			err = v.Decode(&f.Sources)
		case "checked":
			err = v.Decode(&f.Checked)
		case "retired":
			err = v.Decode(&f.Retired)
		case "created":
			err = v.Decode(&f.Created)
		}
		if err != nil {
			return nil, &ParseError{Err: fmt.Errorf("%s: %w", k, err)}
		}
	}
	if strings.TrimSpace(f.Title) == "" {
		return nil, &ParseError{Err: errors.New("title is missing")}
	}
	pg.Title, pg.Tags, pg.Question, pg.Sources, pg.Checked, pg.Retired, pg.Created =
		f.Title, f.Tags, f.Question, f.Sources, f.Checked, f.Retired, f.Created
	pg.Body = strings.Trim(rest, "\n")
	return pg, nil
}

// Render writes the page file: known keys from the page, every other key as it was read.
func (pg *Page) Render() ([]byte, error) {
	if strings.TrimSpace(pg.Title) == "" {
		return nil, errors.New("a page needs a title")
	}
	if len(pg.Body) > MaxPageBody {
		return nil, fmt.Errorf("the page body is %d KB; at most %d KB", len(pg.Body)>>10, MaxPageBody>>10)
	}
	if pg.doc == nil {
		pg.doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	m := pg.doc.Content[0]
	set := func(key string, n *yaml.Node) {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				m.Content[i+1] = n
				return
			}
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, n)
	}
	set("title", strNode(pg.Title))
	set("tags", listNode(pg.Tags))
	if pg.Question != "" {
		set("question", strNode(pg.Question))
	} else {
		set("question", nullNode())
	}
	src := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, s := range pg.Sources {
		if s.Task != "" {
			src.Content = append(src.Content, flowMap("task", strNode(s.Task)))
		} else {
			src.Content = append(src.Content, flowMap("path", strNode(s.Path)))
		}
	}
	set("sources", src)
	if c := pg.Checked; c != nil {
		set("checked", flowMap("rev", strNode(c.Rev), "at", timeNode(c.At), "by", strNode(c.By)))
	} else {
		set("checked", nullNode())
	}
	set("retired", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(pg.Retired)})
	set("created", timeNode(pg.Created))
	var fm bytes.Buffer
	enc := yaml.NewEncoder(&fm)
	enc.SetIndent(2)
	if err := enc.Encode(pg.doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	out := "---\n" + fm.String() + "---\n\n" + strings.Trim(pg.Body, "\n") + "\n"
	if pg.crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return []byte(out), nil
}

var slugDash = regexp.MustCompile(`-+`)

// Slugify makes a page file name from a title: lowercase letters and digits joined by dashes. Letters of any
// script are kept (a Japanese title makes a Japanese file name); "" when nothing usable is left.
func Slugify(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	s := strings.Trim(slugDash.ReplaceAllString(b.String(), "-"), "-")
	if r := []rune(s); len(r) > 80 {
		s = strings.Trim(string(r[:80]), "-")
	}
	if winReserved.MatchString(s) {
		s += "-page" // con.md, nul.md … cannot be written on Windows, and boards are shared across machines
	}
	return s
}

// winReserved are file names Windows keeps for devices, whatever the extension.
var winReserved = regexp.MustCompile(`^(con|prn|aux|nul|com[0-9¹²³]|lpt[0-9¹²³])$`)

// ValidSlug reports whether s can name a page file.
func ValidSlug(s string) bool { return s != "" && Slugify(s) == s }

// KBDir is the project's knowledge base folder.
func (p *Project) KBDir() string { return filepath.Join(p.dir, "kb") }

// PagePath is the file of page slug.
func (p *Project) PagePath(slug string) string { return filepath.Join(p.KBDir(), slug+".md") }

// Page reads one page. A missing page is ErrNotFound.
func (p *Project) Page(slug string) (*Page, error) {
	if !ValidSlug(slug) {
		return nil, fmt.Errorf("page %q: %w", slug, ErrNotFound)
	}
	data, err := os.ReadFile(p.PagePath(slug))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("page %s/%s: %w", p.slug, slug, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	pg, err := ParsePage(data)
	if err != nil {
		var pe *ParseError
		if errors.As(err, &pe) {
			pe.Path = p.PagePath(slug)
		}
		return nil, err
	}
	pg.Slug = slug
	return pg, nil
}

// Pages reads every page of the project, sorted by slug; pages that do not parse are returned as errors.
func (p *Project) Pages() ([]*Page, []error) {
	entries, err := os.ReadDir(p.KBDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}
	var pages []*Page
	var errs []error
	for _, e := range entries {
		slug, ok := strings.CutSuffix(e.Name(), ".md")
		if e.IsDir() || !ok || !ValidSlug(slug) {
			continue
		}
		pg, err := p.Page(slug)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		pages = append(pages, pg)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Slug < pages[j].Slug })
	return pages, errs
}

// PageEdit is a page write. Nil fields stay as they are; Body etc. replace. Create refuses an existing slug.
type PageEdit struct {
	Title    *string
	Tags     *[]string
	Question *string
	Sources  *[]Source
	Body     *string
	Retired  *bool
	// Checked, when set, replaces the page's check (a write or recheck from an agent's working copy).
	Checked *Checked
	// Version, when set, refuses the write if the page file changed since it was read (ErrConflict).
	Version string
}

// ErrPageExists is returned when a new page's slug is taken.
var ErrPageExists = errors.New("a page with this name exists")

// WritePage creates page slug (create) or updates it, and records a kb activity record with the changed
// fields. It returns the page as written and the changed field names.
func (p *Project) WritePage(slug string, create bool, e PageEdit, by Actor) (*Page, []string, error) {
	if !ValidSlug(slug) {
		return nil, nil, fmt.Errorf("page name %q: use lowercase letters, digits and dashes", slug)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	path := p.PagePath(slug)
	data, err := os.ReadFile(path)
	var pg *Page
	switch {
	case errors.Is(err, os.ErrNotExist):
		if !create {
			return nil, nil, fmt.Errorf("page %s/%s: %w", p.slug, slug, ErrNotFound)
		}
		pg = &Page{Slug: slug, Created: p.board.now()}
	case err != nil:
		return nil, nil, err
	default:
		if create {
			return nil, nil, fmt.Errorf("%w: %s/%s", ErrPageExists, p.slug, slug)
		}
		if pg, err = ParsePage(data); err != nil {
			return nil, nil, fmt.Errorf("page %s cannot be read, so it is not overwritten: %w", slug, err)
		}
		pg.Slug = slug
		if e.Version != "" && e.Version != pg.version {
			return nil, nil, ErrConflict
		}
	}
	var changed []string
	str := func(name string, dst *string, v *string) {
		if v != nil && strings.TrimSpace(*v) != *dst {
			*dst = strings.TrimSpace(*v)
			changed = append(changed, name)
		}
	}
	str("title", &pg.Title, e.Title)
	str("question", &pg.Question, e.Question)
	if e.Body != nil && strings.Trim(*e.Body, "\n") != pg.Body {
		pg.Body = strings.Trim(*e.Body, "\n")
		changed = append(changed, "body")
	}
	if e.Tags != nil {
		if tags := CleanNames(*e.Tags); !slices.Equal(tags, pg.Tags) {
			pg.Tags = tags
			changed = append(changed, "tags")
		}
	}
	if e.Sources != nil {
		src := cleanSources(*e.Sources)
		if !slices.Equal(src, pg.Sources) {
			pg.Sources = src
			changed = append(changed, "sources")
		}
	}
	if e.Retired != nil && *e.Retired != pg.Retired {
		pg.Retired = *e.Retired
		changed = append(changed, "retired")
	}
	if e.Checked != nil {
		c := *e.Checked
		if c.At.IsZero() {
			c.At = p.board.now()
		}
		pg.Checked = &c
		changed = append(changed, "checked")
	}
	if len(changed) == 0 && !create {
		return pg, nil, nil
	}
	out, err := pg.Render()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(p.KBDir(), 0o755); err != nil {
		return nil, nil, err
	}
	if err := writeFileAtomic(path, out, 0o644); err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(out)
	pg.version = hex.EncodeToString(sum[:8])
	rec := by.record(p.board.now(), KindKB, "", func(r *Record) { r.Page, r.Fields = slug, changed })
	if create {
		rec.Text = "created"
	}
	return pg, changed, p.board.appendActivity(p.activityPath(), rec)
}

// cleanSources trims sources, normalizes paths to forward slashes and task ids to upper case, and drops
// empty and duplicate entries.
func cleanSources(in []Source) []Source {
	var out []Source
	for _, s := range in {
		s.Path = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(s.Path)), "./")
		s.Task = strings.ToUpper(strings.TrimSpace(s.Task))
		if s.Task != "" {
			s.Path = ""
		}
		if (s.Path == "" && s.Task == "") || slices.Contains(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}
