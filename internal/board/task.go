package board

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// CleanCustom cleans custom field values for a new task: one line each, empty ones dropped.
func CleanCustom(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if v = CleanName(v, 200); v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CleanNames cleans a list of people's names: each through CleanName, empty ones and repeats (any case) dropped,
// order kept.
func CleanNames(in []string) []string {
	out := []string{}
	for _, s := range in {
		n := CleanName(s, 80)
		if n != "" && !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, n) }) {
			out = append(out, n)
		}
	}
	return out
}

// CleanName makes a name safe for one log line: control characters and line breaks become spaces, runs of
// spaces collapse, and it is cut to max runes (max <= 0: no limit). Names can come from outside the machine
// (a playtester's reporter field), so a name must never start a new log line.
func CleanName(s string, max int) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '\u2028' || r == '\u2029' {
			space = true
			continue
		}
		if max > 0 && n >= max {
			break
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
			n++
		}
		space = false
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// LogMarker starts the log section of a task file. Everything above it (after the front matter) is the body.
const LogMarker = "<!-- quest-map:log -->"

const logHeading = "## Log"

const utf8BOM = "\xef\xbb\xbf"

// logTimeLayout is the time format of log lines. Log times are in the board's time zone (board.toml).
const logTimeLayout = "2006-01-02 15:04"

// Status is a task's column.
type Status string

const (
	Backlog Status = "backlog"
	Todo    Status = "todo"
	Doing   Status = "doing"
	Review  Status = "review"
	Done    Status = "done"
)

// Statuses lists every status in board order.
var Statuses = []Status{Backlog, Todo, Doing, Review, Done}

// Valid reports whether s is one of the fixed statuses.
func (s Status) Valid() bool {
	for _, v := range Statuses {
		if s == v {
			return true
		}
	}
	return false
}

// Doer says who is expected to do a task. Agents never pick up person tasks.
type Doer string

const (
	Person Doer = "person"
	Agent  Doer = "agent"
)

// Valid reports whether d is a known doer.
func (d Doer) Valid() bool { return d == Person || d == Agent }

// Priority orders work. The zero value is treated as Normal.
type Priority string

const (
	Low    Priority = "low"
	Normal Priority = "normal"
	High   Priority = "high"
	Urgent Priority = "urgent"
)

// Valid reports whether p is a known priority.
func (p Priority) Valid() bool { return p.Rank() > 0 }

// Rank returns 1 (low) to 4 (urgent), or 0 for an unknown priority.
func (p Priority) Rank() int {
	switch p {
	case Low:
		return 1
	case Normal:
		return 2
	case High:
		return 3
	case Urgent:
		return 4
	}
	return 0
}

// Claim records which agent session is working a doing task, and since when.
type Claim struct {
	By string    `yaml:"by" json:"by"`
	At time.Time `yaml:"at" json:"at"`
}

// Pipeline says which named asset pipeline a task follows and which step it is at.
type Pipeline struct {
	Name string `yaml:"name" json:"name"`
	At   string `yaml:"at" json:"at"`
}

// LogEntry is one parsed line of a task's log.
type LogEntry struct {
	Time  time.Time
	Who   string
	Text  string
	Usage string // the usage reported with the entry, e.g. "+42k tok, +25m"; "" if none
}

// Task is one task file. Exported fields are the known front-matter keys plus the body and parsed log.
type Task struct {
	ID         string
	Title      string
	Status     Status
	Doer       Doer
	Assignees  []string // the people the task is assigned to, free text names
	Claim      *Claim
	Priority   Priority
	Discipline string
	Milestone  string
	Labels     []string
	Asset      string
	Pipeline   *Pipeline
	FoundIn    string
	FixedIn    string
	BlockedBy  []string
	MovedFrom  string
	URL        string
	Created    time.Time
	// Custom holds the other front-matter keys with a plain text value: a suite's custom fields (spec §4.1).
	Custom map[string]string

	// Body is the Markdown between the front matter and the log marker, without surrounding blank lines.
	Body string
	// Log holds the parsed log lines (read-only; append with appendLog).
	Log []LogEntry
	// Archived is true for tasks read from the archive folder.
	Archived bool

	doc     *yaml.Node     // parsed front matter document, kept so unknown keys and comments survive a write
	bodyRaw string         // body exactly as read, used again when Body is unchanged
	logRaw  string         // log section exactly as read (from the marker to the end), "" if there is no marker
	crlf    bool           // file used CRLF line endings
	bom     bool           // file started with a UTF-8 byte order mark
	loc     *time.Location // time zone of log-line times; nil means UTC
	version string         // hash of the file as read (Version)
}

// ErrConflict is returned when a write names a version of the task that is no longer the one on disk.
var ErrConflict = errors.New("the task was changed by someone else since you loaded it")

// Version identifies the file content the task was read from (empty for a task not read from a file). A person's
// edit can pass it back to CheckVersion, so it is refused if the file changed meanwhile.
func (t *Task) Version() string { return t.version }

// CheckVersion returns a check for UpdateIf that accepts the task only while its file is still version v.
// An empty v accepts any version.
func CheckVersion(v string) func(*Task) error {
	return func(t *Task) error {
		if v != "" && t.version != v {
			return fmt.Errorf("%s: %w", t.ID, ErrConflict)
		}
		return nil
	}
}

// ParseError is returned for a task file that cannot be read. Such files are never overwritten.
type ParseError struct {
	Path string
	Err  error
}

func (e *ParseError) Error() string {
	if e.Path == "" {
		return "task file: " + e.Err.Error()
	}
	return e.Path + ": " + e.Err.Error()
}

func (e *ParseError) Unwrap() error { return e.Err }

// field describes one known front-matter key: how to build its YAML value from a Task and load it back.
type field struct {
	key   string
	build func(t *Task) *yaml.Node
	load  func(t *Task, n *yaml.Node) error
}

func strField(key string, p func(t *Task) *string) field {
	return field{key,
		func(t *Task) *yaml.Node { return strNode(*p(t)) },
		func(t *Task, n *yaml.Node) error { return n.Decode(p(t)) }}
}

func listField(key string, p func(t *Task) *[]string) field {
	return field{key,
		func(t *Task) *yaml.Node { return listNode(*p(t)) },
		func(t *Task, n *yaml.Node) error { return n.Decode(p(t)) }}
}

// KnownKeys lists the front-matter keys quest-map itself uses; other keys are kept as they are (custom fields).
func KnownKeys() []string {
	out := []string{legacyAssignee}
	for _, f := range fields {
		out = append(out, f.key)
	}
	return out
}

// legacyAssignee is the single-name key early builds wrote before assignees became a list.
const legacyAssignee = "assignee"

// fields lists the known keys in their canonical order.
var fields = []field{
	strField("id", func(t *Task) *string { return &t.ID }),
	strField("title", func(t *Task) *string { return &t.Title }),
	strField("status", func(t *Task) *string { return (*string)(&t.Status) }),
	strField("doer", func(t *Task) *string { return (*string)(&t.Doer) }),
	listField("assignees", func(t *Task) *[]string { return &t.Assignees }),
	{"claim",
		func(t *Task) *yaml.Node {
			if t.Claim == nil {
				return nullNode()
			}
			return flowMap("by", strNode(t.Claim.By), "at", timeNode(t.Claim.At))
		},
		func(t *Task, n *yaml.Node) error { return n.Decode(&t.Claim) }},
	strField("priority", func(t *Task) *string { return (*string)(&t.Priority) }),
	strField("discipline", func(t *Task) *string { return &t.Discipline }),
	strField("milestone", func(t *Task) *string { return &t.Milestone }),
	listField("labels", func(t *Task) *[]string { return &t.Labels }),
	strField("asset", func(t *Task) *string { return &t.Asset }),
	{"pipeline",
		func(t *Task) *yaml.Node {
			if t.Pipeline == nil {
				return nullNode()
			}
			return flowMap("name", strNode(t.Pipeline.Name), "at", strNode(t.Pipeline.At))
		},
		func(t *Task, n *yaml.Node) error { return n.Decode(&t.Pipeline) }},
	strField("found_in", func(t *Task) *string { return &t.FoundIn }),
	strField("fixed_in", func(t *Task) *string { return &t.FixedIn }),
	listField("blocked_by", func(t *Task) *[]string { return &t.BlockedBy }),
	strField("moved_from", func(t *Task) *string { return &t.MovedFrom }),
	strField("url", func(t *Task) *string { return &t.URL }),
	{"created",
		func(t *Task) *yaml.Node { return timeNode(t.Created) },
		func(t *Task, n *yaml.Node) error { return n.Decode(&t.Created) }},
}

func nullNode() *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"} }

func strNode(s string) *yaml.Node {
	if s == "" {
		return nullNode()
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func timeNode(t time.Time) *yaml.Node {
	if t.IsZero() {
		return nullNode()
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: t.UTC().Format(time.RFC3339)}
}

func listNode(items []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, s := range items {
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s})
	}
	return n
}

func flowMap(kv ...any) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	for i := 0; i < len(kv); i += 2 {
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kv[i].(string)}, kv[i+1].(*yaml.Node))
	}
	return n
}

// isEmptyNode reports whether a built value carries no information (null or an empty list).
func isEmptyNode(n *yaml.Node) bool {
	return n.Tag == "!!null" || (n.Kind == yaml.SequenceNode && len(n.Content) == 0)
}

// Parse reads a task file whose log times are UTC.
func Parse(data []byte) (*Task, error) { return ParseIn(data, time.UTC) }

// ParseIn reads a task file whose log times are in loc.
func ParseIn(data []byte, loc *time.Location) (*Task, error) {
	sum := sha256.Sum256(data)
	t := &Task{loc: loc, version: hex.EncodeToString(sum[:8])}
	s := string(data)
	if strings.HasPrefix(s, utf8BOM) {
		t.bom = true
		s = strings.TrimPrefix(s, utf8BOM)
	}
	if strings.Contains(s, "\r\n") {
		t.crlf = true
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
		fm, rest = s[4:len(s)-3], ""
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
	t.doc = &doc
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i].Value
		for _, f := range fields {
			if f.key == k {
				if err := f.load(t, m.Content[i+1]); err != nil {
					return nil, &ParseError{Err: fmt.Errorf("%s: %w", k, err)}
				}
			}
		}
	}
	known := KnownKeys()
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i].Value, m.Content[i+1]
		if !slices.Contains(known, k) && customKeyRe.MatchString(k) && v.Kind == yaml.ScalarNode && v.Tag != "!!null" && v.Value != "" {
			if t.Custom == nil {
				t.Custom = map[string]string{}
			}
			t.Custom[k] = v.Value
		}
	}
	// A single `assignee: name` (written by early builds) joins the list; the next write moves it to assignees.
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != legacyAssignee {
			continue
		}
		v := m.Content[i+1]
		var names []string
		switch v.Kind {
		case yaml.ScalarNode:
			if v.Tag != "!!null" {
				names = []string{v.Value}
			}
		case yaml.SequenceNode: // a hand-written list is kept too
			for _, n := range v.Content {
				if n.Kind == yaml.ScalarNode {
					names = append(names, n.Value)
				}
			}
		}
		t.Assignees = CleanNames(append(slices.Clone(t.Assignees), names...))
	}
	if err := t.validate(); err != nil {
		return nil, &ParseError{Err: err}
	}

	if i := markerIndex(rest); i >= 0 {
		t.bodyRaw, t.logRaw = rest[:i], rest[i:]
	} else {
		t.bodyRaw = rest
	}
	t.Body = strings.Trim(t.bodyRaw, "\n")
	t.Log = parseLog(t.logRaw, t.location())
	return t, nil
}

func (t *Task) validate() error {
	if t.ID == "" {
		return errors.New("id is missing")
	}
	for k := range t.Custom {
		if !IsCustomKey(k) {
			return fmt.Errorf("field %q: not a custom field key (lowercase letters and _, not a built-in key)", k)
		}
	}
	if !t.Status.Valid() {
		return fmt.Errorf("status %q is not one of backlog, todo, doing, review, done", t.Status)
	}
	if t.Doer != "" && !t.Doer.Valid() {
		return fmt.Errorf("doer %q is not person or agent", t.Doer)
	}
	if t.Priority != "" && !t.Priority.Valid() {
		return fmt.Errorf("priority %q is not low, normal, high or urgent", t.Priority)
	}
	return nil
}

// markerIndex returns the offset of the log marker line in s, or -1.
func markerIndex(s string) int {
	if strings.HasPrefix(s, LogMarker) {
		return 0
	}
	if i := strings.Index(s, "\n"+LogMarker); i >= 0 {
		return i + 1
	}
	return -1
}

// location returns the time zone of the task's log times.
func (t *Task) location() *time.Location {
	if t.loc == nil {
		return time.UTC
	}
	return t.loc
}

// parseLog reads "- <time> <who>: <text>" lines, with times in loc; indented lines continue the previous
// entry.
func parseLog(raw string, loc *time.Location) []LogEntry {
	var out []LogEntry
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "  ") && len(out) > 0 {
			out[len(out)-1].Text += "\n" + strings.TrimPrefix(line, "  ")
			continue
		}
		rest, ok := strings.CutPrefix(line, "- ")
		if !ok || len(rest) < len(logTimeLayout)+1 {
			continue
		}
		ts, err := time.ParseInLocation(logTimeLayout, rest[:len(logTimeLayout)], loc)
		if err != nil {
			continue
		}
		who, text, ok := strings.Cut(rest[len(logTimeLayout)+1:], ": ")
		if !ok {
			continue
		}
		usage := ""
		i := strings.Index(who, " (+")
		if i < 0 {
			i = strings.Index(who, " (-")
		}
		if i >= 0 {
			who, usage = who[:i], strings.TrimSuffix(who[i+2:], ")")
		}
		out = append(out, LogEntry{Time: ts, Who: who, Text: text, Usage: usage})
	}
	return out
}

// appendLog adds a log line, adding the marker and heading first if the file has none.
func (t *Task) appendLog(at time.Time, who, suffix, text string) {
	if t.logRaw == "" {
		body := t.renderBody()
		if body != "" && !strings.HasSuffix(body, "\n\n") {
			if strings.HasSuffix(body, "\n") {
				body += "\n"
			} else {
				body += "\n\n"
			}
		}
		t.bodyRaw, t.Body = body, strings.Trim(body, "\n")
		t.logRaw = LogMarker + "\n" + logHeading + "\n"
	} else if !strings.HasSuffix(t.logRaw, "\n") {
		t.logRaw += "\n"
	}
	text = strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n  ")
	who = strings.ReplaceAll(CleanName(who, 80), ": ", ":")
	if who == "" {
		who = "unknown"
	}
	t.logRaw += fmt.Sprintf("- %s %s%s: %s\n", at.In(t.location()).Format(logTimeLayout), who, suffix, text)
	t.Log = parseLog(t.logRaw, t.location())
}

func (t *Task) renderBody() string {
	if t.Body == strings.Trim(t.bodyRaw, "\n") {
		return t.bodyRaw
	}
	if t.Body == "" {
		return "\n"
	}
	return "\n" + t.Body + "\n\n"
}

// Render returns the task file's bytes. Front-matter values are only rewritten where they changed, unknown
// keys and their comments are kept, and line endings / byte order mark match the file as read.
func (t *Task) Render() ([]byte, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	if t.doc == nil {
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, f := range fields {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: f.key}, f.build(t))
		}
		for _, k := range sortedKeys(t.Custom) {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, customNode(t.Custom[k]))
		}
		t.doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{m}}
		if t.bodyRaw == "" && t.Body != "" {
			t.bodyRaw = "\n" + t.Body + "\n\n"
		} else if t.bodyRaw == "" {
			t.bodyRaw = "\n"
		}
		if t.logRaw == "" {
			t.logRaw = LogMarker + "\n" + logHeading + "\n"
		}
	} else if err := t.updateDoc(); err != nil {
		return nil, err
	}

	var fm bytes.Buffer
	enc := yaml.NewEncoder(&fm)
	enc.SetIndent(2)
	if err := enc.Encode(t.doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	out := "---\n" + fm.String() + "---\n" + t.renderBody() + t.logRaw
	if t.crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	if t.bom {
		out = utf8BOM + out
	}
	return []byte(out), nil
}

// updateDoc writes changed known fields into the parsed front matter, leaving everything else as it was.
func (t *Task) updateDoc() error {
	m := t.doc.Content[0]
	t.updateCustom(m)
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == legacyAssignee { // its value is in Assignees now
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			break
		}
	}
	for fi, f := range fields {
		want := f.build(t)
		idx := -1
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == f.key {
				idx = i + 1
				break
			}
		}
		if idx < 0 {
			if isEmptyNode(want) {
				continue
			}
			// Insert after the nearest preceding known key that is present, else at the top.
			at := 0
			for j := fi - 1; j >= 0 && at == 0; j-- {
				for i := 0; i+1 < len(m.Content); i += 2 {
					if m.Content[i].Value == fields[j].key {
						at = i + 2
						break
					}
				}
			}
			key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: f.key}
			m.Content = append(m.Content[:at], append([]*yaml.Node{key, want}, m.Content[at:]...)...)
			continue
		}
		old := m.Content[idx]
		same, err := sameValue(f, old, want)
		if err != nil {
			return err
		}
		if !same {
			want.LineComment, want.HeadComment, want.FootComment = old.LineComment, old.HeadComment, old.FootComment
			m.Content[idx] = want
		}
	}
	return nil
}

// updateCustom brings the custom keys in the front matter in line with t.Custom: changed values are replaced
// (comments kept), removed ones deleted, new ones added at the end. Other keys are left alone.
func (t *Task) updateCustom(m *yaml.Node) {
	known := KnownKeys()
	seen := map[string]bool{}
	for i := 0; i+1 < len(m.Content); {
		k, v := m.Content[i].Value, m.Content[i+1]
		want, ok := t.Custom[k]
		if slices.Contains(known, k) || !customKeyRe.MatchString(k) || seen[k] || (v.Kind != yaml.ScalarNode && !ok) {
			i += 2 // built-in, not a field key, a repeat, or a list/map nobody set: left alone
			continue
		}
		seen[k] = true
		switch {
		case !ok && v.Tag != "!!null" && v.Value != "":
			m.Content = append(m.Content[:i], m.Content[i+2:]...) // cleared
			continue
		case ok && (v.Kind != yaml.ScalarNode || v.Value != want): // a list or map set as a field becomes text
			n := customNode(want)
			n.LineComment, n.HeadComment, n.FootComment = v.LineComment, v.HeadComment, v.FootComment
			m.Content[i+1] = n
		}
		i += 2
	}
	for _, k := range sortedKeys(t.Custom) {
		if !seen[k] {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, customNode(t.Custom[k]))
		}
	}
}

func customNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// sameValue reports whether the existing node already holds the value want encodes, whatever its style.
func sameValue(f field, old, want *yaml.Node) (bool, error) {
	var tmp Task
	if err := f.load(&tmp, old); err != nil {
		return false, nil
	}
	a, err := yaml.Marshal(f.build(&tmp))
	if err != nil {
		return false, err
	}
	b, err := yaml.Marshal(want)
	if err != nil {
		return false, err
	}
	return bytes.Equal(a, b), nil
}
