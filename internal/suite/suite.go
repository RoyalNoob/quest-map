// Package suite loads suites: named packages of configuration that tailor the board's five project concepts
// to a kind of project (spec §4.1). Built-in suites are embedded; files in the board's suites/ folder extend
// them (keys they set win) or define new ones.
package suite

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/RoyalNoob/quest-map/internal/adapter"
	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/filter"
)

// Default is the suite a project uses when neither it nor board.toml names one.
const Default = "gamedev-unreal"

//go:embed *.toml
var builtins embed.FS

// Words are the names the UI and agent replies use for the generic concepts.
type Words struct {
	Release    string `toml:"release" json:"release"`
	Releases   string `toml:"releases" json:"releases"`
	Target     string `toml:"target" json:"target"`
	Item       string `toml:"item" json:"item"`
	Feedback   string `toml:"feedback" json:"feedback"`
	Discipline string `toml:"discipline" json:"discipline"`
}

// Pipeline is a named list of steps an item goes through.
type Pipeline struct {
	Name  string   `toml:"name" json:"name"`
	Steps []string `toml:"steps" json:"steps"`
}

// Intake are the values a feedback report's task gets.
type Intake struct {
	Status     string   `toml:"status" json:"status"`
	Doer       string   `toml:"doer" json:"doer"`
	Discipline string   `toml:"discipline" json:"discipline"`
	Priority   string   `toml:"priority" json:"priority,omitempty"`
	Labels     []string `toml:"labels" json:"labels"`
}

// Adapters names the built-in adapters a suite switches on.
type Adapters struct {
	Assets string   `toml:"assets" json:"assets"`
	Locks  []string `toml:"locks" json:"locks"`
	VCS    []string `toml:"vcs" json:"vcs"`
}

// View is a saved way to look at the board: which tasks, how they are grouped and sorted, what cards show.
type View struct {
	Name   string   `toml:"name" json:"name"`
	Filter string   `toml:"filter,omitempty" json:"filter"` // filter language, as in the search box
	Lanes  string   `toml:"lanes,omitempty" json:"lanes"`   // "", discipline or milestone
	Sort   string   `toml:"sort,omitempty" json:"sort"`     // "" (id), priority, updated, created or title
	Fields []string `toml:"fields,omitempty" json:"fields"` // card fields; empty means the default set
	Layout string   `toml:"layout,omitempty" json:"layout"` // board (default) or table
}

// Template is a starting point for a new task, e.g. a bug report with repro steps. Fields it sets fill the
// ones a new task leaves empty; labels are added.
type Template struct {
	Name       string   `toml:"name" json:"name"`
	Title      string   `toml:"title,omitempty" json:"title"` // a title start for the UI, e.g. "Bug: "
	Body       string   `toml:"body,omitempty,multiline" json:"body"`
	Status     string   `toml:"status,omitempty" json:"status"` // backlog or todo
	Doer       string   `toml:"doer,omitempty" json:"doer"`
	Priority   string   `toml:"priority,omitempty" json:"priority"`
	Discipline string   `toml:"discipline,omitempty" json:"discipline"`
	Labels     []string `toml:"labels,omitempty" json:"labels"`
	Pipeline   string   `toml:"pipeline,omitempty" json:"pipeline"`
}

// Template returns the template with this name.
func (s *Suite) Template(name string) (Template, bool) {
	for _, t := range s.Templates {
		if strings.EqualFold(t.Name, strings.TrimSpace(name)) {
			return t, true
		}
	}
	return Template{}, false
}

// TemplateNames lists the suite's template names.
func (s *Suite) TemplateNames() []string {
	out := make([]string, len(s.Templates))
	for i, t := range s.Templates {
		out[i] = t.Name
	}
	return out
}

// Field is a custom field: an extra front-matter key the UI shows and edits and the filter understands, e.g.
// platform or ue_map. Values are text.
type Field struct {
	Key    string   `toml:"key" json:"key"`       // front-matter key and filter key: lowercase letters and _
	Label  string   `toml:"label" json:"label"`   // shown in the UI; default: the key
	Type   string   `toml:"type" json:"type"`     // text (default) or choice
	Values []string `toml:"values" json:"values"` // choice: the values offered (others stay valid, shown as unknown)
	Help   string   `toml:"help" json:"help"`     // a placeholder or hint
}

// FieldKeys lists the suite's custom field keys.
func (s *Suite) FieldKeys() []string {
	out := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		out[i] = f.Key
	}
	return out
}

var fieldKeyRe = regexp.MustCompile(`^[a-z][a-z_]*$`)

// View values the UI understands.
var (
	ViewLanes   = []string{"", "discipline", "milestone"}
	ViewSorts   = []string{"", "priority", "updated", "created", "title"}
	ViewFields  = []string{"priority", "claim", "steps", "doer", "assignees", "discipline", "milestone", "labels", "spent", "found"}
	ViewLayouts = []string{"", "board", "table"}
)

// Labels are a suite's words and names in one UI language: each table maps a key or name (a discipline, a
// pipeline step, a status, a template, view or field) to its label. A name without a label shows as it is.
type Labels struct {
	Title       string            `toml:"title" json:"title,omitempty"`
	Wording     map[string]string `toml:"wording" json:"wording,omitempty"` // keys of [words]
	Pipelines   map[string]string `toml:"pipelines" json:"pipelines,omitempty"`
	Disciplines map[string]string `toml:"disciplines" json:"disciplines,omitempty"`
	Steps       map[string]string `toml:"steps" json:"steps,omitempty"`
	Statuses    map[string]string `toml:"statuses" json:"statuses,omitempty"`
	Templates   map[string]string `toml:"templates" json:"templates,omitempty"`
	Views       map[string]string `toml:"views" json:"views,omitempty"`
	Fields      map[string]string `toml:"fields" json:"fields,omitempty"`           // custom field labels by key
	FieldsHelp  map[string]string `toml:"fields_help" json:"fields_help,omitempty"` // custom field help by key
}

// mergeLabels adds o's labels to l; o's win.
func (l *Labels) merge(o Labels) {
	if o.Title != "" {
		l.Title = o.Title
	}
	for _, pair := range []struct {
		dst *map[string]string
		src map[string]string
	}{
		{&l.Wording, o.Wording}, {&l.Pipelines, o.Pipelines}, {&l.Disciplines, o.Disciplines}, {&l.Steps, o.Steps}, {&l.Statuses, o.Statuses},
		{&l.Templates, o.Templates}, {&l.Views, o.Views}, {&l.Fields, o.Fields}, {&l.FieldsHelp, o.FieldsHelp},
	} {
		if len(pair.src) == 0 {
			continue
		}
		if *pair.dst == nil {
			*pair.dst = map[string]string{}
		}
		for k, v := range pair.src {
			(*pair.dst)[k] = v
		}
	}
}

// Suite is one suite after loading and merging.
type Suite struct {
	Name            string     `toml:"-" json:"name"`
	Title           string     `toml:"title" json:"title"`
	Words           Words      `toml:"words" json:"words"`
	Disciplines     []string   `toml:"disciplines" json:"disciplines"`
	ReleaseTargets  []string   `toml:"release_targets" json:"release_targets"`
	ReleaseIDFormat string     `toml:"release_id_format" json:"release_id_format"`
	Pipelines       []Pipeline `toml:"pipelines" json:"pipelines"`
	Intake          Intake     `toml:"intake" json:"intake"`
	Adapters        Adapters   `toml:"adapters" json:"adapters"`
	Views           []View     `toml:"views" json:"views"`
	Templates       []Template `toml:"templates" json:"templates"`
	Fields          []Field    `toml:"fields" json:"fields"`
	// Show says when the UI shows a task field (spec §4.1): field key -> terms, any of which shows it. A key not
	// listed is always shown, and a field with a value always shows.
	Show map[string][]string `toml:"show" json:"show"`
	// Lang holds labels per UI language ([lang.ja], [lang.zh-TW]); keys and names stay as they are (§4.4).
	Lang   map[string]Labels `toml:"lang" json:"lang"`
	Source []string          `toml:"-" json:"source"` // where it was read from: "built-in", file paths
}

// Pipeline returns the pipeline with this name.
func (s *Suite) Pipeline(name string) (Pipeline, bool) {
	for _, p := range s.Pipelines {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Pipeline{}, false
}

// HasDiscipline reports whether a discipline is in the suite's list.
func (s *Suite) HasDiscipline(d string) bool {
	return slices.ContainsFunc(s.Disciplines, func(x string) bool { return strings.EqualFold(x, d) })
}

// Resolver returns the suite's asset resolver.
func (s *Suite) Resolver() adapter.AssetResolver {
	r, err := adapter.Resolver(s.Adapters.Assets)
	if err != nil {
		r, _ = adapter.Resolver("path")
	}
	return r
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// generic fills what a suite leaves empty, so a minimal suite file still works.
var generic = Suite{
	Words:    Words{Release: "release", Releases: "releases", Target: "target", Item: "item", Feedback: "feedback report", Discipline: "discipline"},
	Intake:   Intake{Status: "backlog", Doer: "person"},
	Adapters: Adapters{Assets: "path"},
}

// Registry holds the suites of one board.
type Registry struct {
	suites map[string]*Suite
}

// Load reads the built-in suites and the board's suites/ folder (none if boardRoot is ""). A board file with a built-in suite's name
// extends it; any other name defines a new suite (on top of generic defaults). Errors name the file.
func Load(boardRoot string) (*Registry, error) {
	r := &Registry{suites: map[string]*Suite{}}
	entries, err := builtins.ReadDir(".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		data, err := builtins.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		s := generic.clone()
		if err := s.merge(data); err != nil {
			return nil, fmt.Errorf("built-in suite %s: %w", name, err)
		}
		s.Name, s.Source = name, []string{"built-in"}
		r.suites[name] = s
	}
	dir := filepath.Join(boardRoot, "suites")
	var files []os.DirEntry
	if boardRoot != "" {
		files, err = os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(f.Name(), ".toml")
		path := filepath.Join(dir, f.Name())
		if !nameRe.MatchString(name) {
			return nil, fmt.Errorf("%s: suite file names use lowercase letters, digits and dashes", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		s := r.suites[name]
		if s == nil {
			s = generic.clone()
			s.Name = name
			r.suites[name] = s
		} else {
			s = s.clone()
			r.suites[name] = s
		}
		if err := s.merge(data); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		s.Source = append(s.Source, path)
	}
	for _, s := range r.suites {
		if err := s.validate(); err != nil {
			return nil, fmt.Errorf("suite %s (%s): %w", s.Name, strings.Join(s.Source, ", "), err)
		}
	}
	return r, nil
}

// Check reports whether data would load as the board file suites/<name>.toml, without writing it: the file
// is merged onto the built-in suite of that name (or the generic defaults) and validated, as Load does. Suite
// files do not depend on each other, so other files in suites/ play no part.
func Check(name string, data []byte) error {
	s, err := withFile(name, data)
	if err != nil {
		return err
	}
	return s.validate()
}

// withFile is the suite name with data as its board file, not yet validated.
func withFile(name string, data []byte) (*Suite, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("suite names use lowercase letters, digits and dashes, like %s", Default)
	}
	s := generic.clone()
	if b, err := builtins.ReadFile(name + ".toml"); err == nil {
		if err := s.merge(b); err != nil {
			return nil, err
		}
	}
	return s, s.merge(data)
}

// Add returns the board file data with one more view or template (entry is a View or a Template), added as
// text at the end so the rest of the file, comments included, stays as it is. Setting a list replaces it, so
// a file that does not set the list yet first gets the list it has now (from the built-in suite). The result
// is checked like any suite file: a name already in the list is an error.
func Add(name string, data []byte, entry any) ([]byte, error) {
	s, err := withFile(name, data)
	if err != nil {
		return nil, err
	}
	var probe map[string]any
	if err := toml.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	var block struct {
		Views     []View     `toml:"views,omitempty"`
		Templates []Template `toml:"templates,omitempty"`
	}
	var key string
	switch e := entry.(type) {
	case View:
		key = "views"
		if _, set := probe[key]; !set {
			block.Views = s.Views
		}
		block.Views = append(slices.Clone(block.Views), e)
	case Template:
		key = "templates"
		if _, set := probe[key]; !set {
			block.Templates = s.Templates
		}
		block.Templates = append(slices.Clone(block.Templates), e)
	default:
		return nil, fmt.Errorf("cannot add a %T to a suite", entry)
	}
	// A list written inline (views = [...]) cannot take an appended [[views]] block: say so, instead of TOML's
	// "already defined".
	if _, set := probe[key]; set && !regexp.MustCompile(`(?m)^[ \t]*\[\[[ \t]*`+key+`[ \t]*\]\]`).Match(data) {
		return nil, fmt.Errorf("suites/%s.toml writes %s as an inline list (%s = [...]); write each one as a [[%s]] block to add more from the UI", name, key, key, key)
	}
	text, err := toml.Marshal(block)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(data)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	if len(out) > 0 {
		out = append(out, '\n')
	}
	if _, set := probe[key]; !set && len(text) > 0 && strings.Count(string(text), "[["+key+"]]") > 1 {
		out = append(out, "# The "+key+" this suite had, kept: a file that sets "+key+" replaces the whole list.\n"...)
	}
	out = append(out, text...)
	if err := Check(name, out); err != nil {
		return nil, err
	}
	return out, nil
}

// BuiltinText returns the TOML a built-in suite ships with ("" if name is not built in).
func BuiltinText(name string) string {
	if !ValidName(name) {
		return ""
	}
	b, _ := builtins.ReadFile(name + ".toml")
	return string(b)
}

// ValidName reports whether name can name a suite file.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// IsBuiltin reports whether a suite with this name is embedded in the binary.
func IsBuiltin(name string) bool {
	_, err := builtins.ReadFile(name + ".toml")
	return ValidName(name) && err == nil
}

// Get returns a suite by name.
func (r *Registry) Get(name string) (*Suite, error) {
	if name == "" {
		name = Default
	}
	if s, ok := r.suites[name]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("unknown suite %q (known: %s)", name, strings.Join(r.Names(), ", "))
}

// Names lists the suite names, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.suites))
	for n := range r.suites {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// merge applies a suite file on top of s: keys the file sets replace s's values.
func (s *Suite) merge(data []byte) error {
	var probe map[string]any
	if err := toml.Unmarshal(data, &probe); err != nil {
		return err
	}
	for k := range probe {
		switch k {
		case "title", "words", "disciplines", "release_targets", "release_id_format", "pipelines", "intake", "adapters", "views", "templates", "fields", "show", "lang":
		default:
			return fmt.Errorf("unknown key %q", k)
		}
	}
	var f Suite
	dec := toml.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return err
	}
	str := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	if _, ok := probe["title"]; ok {
		s.Title = f.Title
	}
	if _, ok := probe["disciplines"]; ok {
		s.Disciplines = f.Disciplines
	}
	if _, ok := probe["release_targets"]; ok {
		s.ReleaseTargets = f.ReleaseTargets
	}
	if _, ok := probe["release_id_format"]; ok {
		s.ReleaseIDFormat = f.ReleaseIDFormat
	}
	if _, ok := probe["pipelines"]; ok {
		s.Pipelines = f.Pipelines
	}
	if _, ok := probe["views"]; ok {
		s.Views = f.Views
	}
	if _, ok := probe["templates"]; ok {
		s.Templates = f.Templates
	}
	if _, ok := probe["fields"]; ok {
		s.Fields = f.Fields
	}
	if _, ok := probe["show"]; ok { // like a list, a file's [show] replaces the whole table
		s.Show = f.Show
	} else if _, ok := probe["fields"]; ok {
		s.pruneShow()
	}
	w := &s.Words
	str(&w.Release, f.Words.Release)
	str(&w.Releases, f.Words.Releases)
	str(&w.Target, f.Words.Target)
	str(&w.Item, f.Words.Item)
	str(&w.Feedback, f.Words.Feedback)
	str(&w.Discipline, f.Words.Discipline)
	in := &s.Intake
	str(&in.Status, f.Intake.Status)
	str(&in.Doer, f.Intake.Doer)
	str(&in.Discipline, f.Intake.Discipline)
	str(&in.Priority, f.Intake.Priority)
	if m, ok := probe["intake"].(map[string]any); ok {
		if _, ok := m["labels"]; ok {
			in.Labels = f.Intake.Labels
		}
	}
	// Labels merge per language and key, so a board file can add one language or fix one label.
	for code, l := range f.Lang {
		if !langCode.MatchString(code) {
			return fmt.Errorf("[lang.%s]: not a language code like ja or zh-TW", code)
		}
		if s.Lang == nil {
			s.Lang = map[string]Labels{}
		}
		cur := s.Lang[code]
		cur.merge(l)
		s.Lang[code] = cur
	}
	str(&s.Adapters.Assets, f.Adapters.Assets)
	if m, ok := probe["adapters"].(map[string]any); ok {
		if _, ok := m["locks"]; ok {
			s.Adapters.Locks = f.Adapters.Locks
		}
		if _, ok := m["vcs"]; ok {
			s.Adapters.VCS = f.Adapters.VCS
		}
	}
	return nil
}

func (s *Suite) validate() error {
	if _, err := adapter.Resolver(s.Adapters.Assets); err != nil {
		return err
	}
	for _, l := range s.Adapters.Locks {
		if _, err := adapter.LockProviderNamed(l); err != nil {
			return err
		}
	}
	for _, v := range s.Adapters.VCS {
		if err := adapter.CheckVCS(v); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, p := range s.Pipelines {
		if p.Name == "" || len(p.Steps) == 0 {
			return fmt.Errorf("pipeline %q needs a name and at least one step", p.Name)
		}
		if seen[strings.ToLower(p.Name)] {
			return fmt.Errorf("pipeline %q is defined twice", p.Name)
		}
		seen[strings.ToLower(p.Name)] = true
	}
	seen = map[string]bool{}
	reserved := append(board.KnownKeys(), filter.Keys()...)
	for i, f := range s.Fields {
		if !fieldKeyRe.MatchString(f.Key) {
			return fmt.Errorf("field %q: keys use lowercase letters and _, e.g. ue_map", f.Key)
		}
		if slices.Contains(reserved, f.Key) {
			return fmt.Errorf("field %q: that key is built in", f.Key)
		}
		if seen[f.Key] {
			return fmt.Errorf("field %q is defined twice", f.Key)
		}
		seen[f.Key] = true
		switch f.Type {
		case "":
			s.Fields[i].Type = "text"
		case "text":
		case "choice":
			if len(f.Values) == 0 {
				return fmt.Errorf("field %q: a choice needs values", f.Key)
			}
		default:
			return fmt.Errorf("field %q: type %q is not text or choice", f.Key, f.Type)
		}
		if f.Label == "" {
			s.Fields[i].Label = f.Key
		}
	}
	for key, terms := range s.Show {
		if err := s.validateShow(key, terms); err != nil {
			return fmt.Errorf("show.%s: %w", key, err)
		}
	}
	seen = map[string]bool{}
	for _, v := range s.Views {
		if err := v.validate(s.FieldKeys()); err != nil {
			return fmt.Errorf("view %q: %w", v.Name, err)
		}
		if seen[strings.ToLower(v.Name)] {
			return fmt.Errorf("view %q is defined twice", v.Name)
		}
		seen[strings.ToLower(v.Name)] = true
	}
	seen = map[string]bool{}
	for _, t := range s.Templates {
		if err := s.validateTemplate(t); err != nil {
			return fmt.Errorf("template %q: %w", t.Name, err)
		}
		if seen[strings.ToLower(t.Name)] {
			return fmt.Errorf("template %q is defined twice", t.Name)
		}
		seen[strings.ToLower(t.Name)] = true
	}
	return nil
}

// ShowFields are the built-in task fields a suite's [show] may hide; the rest (title, status, …) always show.
var ShowFields = []string{"asset", "pipeline", "found_in", "fixed_in", "milestone", "url", "blocked_by"}

// showKeys are the filter keys a [show] term may use, besides custom field keys: the browser checks them
// against the task without the index.
var showKeys = []string{"label", "discipline", "doer", "status", "priority"}

// pruneShow drops inherited [show] rules and terms that name a custom field the suite no longer has: a board
// file that replaces [[fields]] without a [show] of its own would otherwise fail on rules it never wrote (and a
// suite that fails to load takes every suite with it). A rule left with no terms goes, so its field shows.
func (s *Suite) pruneShow() {
	keys := s.FieldKeys()
	known := func(k string) bool { return slices.Contains(ShowFields, k) || slices.Contains(keys, k) }
	for key, terms := range s.Show {
		if !known(key) {
			delete(s.Show, key)
			continue
		}
		var kept []string
		for _, term := range terms {
			k, _, _ := strings.Cut(strings.TrimPrefix(term, "-"), ":")
			if k = strings.ToLower(k); slices.Contains(showKeys, k) || slices.Contains(keys, k) {
				kept = append(kept, term)
			}
		}
		if len(kept) == 0 {
			delete(s.Show, key)
		} else {
			s.Show[key] = kept
		}
	}
}

func (s *Suite) validateShow(key string, terms []string) error {
	if !slices.Contains(ShowFields, key) && !slices.Contains(s.FieldKeys(), key) {
		return fmt.Errorf("not a field that can be hidden: use one of %s or a custom field key", strings.Join(ShowFields, ", "))
	}
	for _, term := range terms {
		q, err := filter.ParseFields(term, s.FieldKeys())
		if err != nil {
			return err
		}
		if len(q.Terms) != 1 || (!slices.Contains(showKeys, q.Terms[0].Key) && !slices.Contains(s.FieldKeys(), q.Terms[0].Key)) {
			return fmt.Errorf("%q: each entry is one term with %s or a custom field key, like \"label:bug\"", term, strings.Join(showKeys, ", "))
		}
	}
	return nil
}

func (s *Suite) validateTemplate(t Template) error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("needs a name")
	}
	switch board.Status(t.Status) {
	case "", board.Backlog, board.Todo:
	default:
		return fmt.Errorf("status %q: new tasks start as backlog or todo", t.Status)
	}
	if t.Doer != "" && !board.Doer(t.Doer).Valid() {
		return fmt.Errorf("doer %q is not person or agent", t.Doer)
	}
	if t.Priority != "" && !board.Priority(t.Priority).Valid() {
		return fmt.Errorf("priority %q is not low, normal, high or urgent", t.Priority)
	}
	if t.Pipeline != "" {
		if _, ok := s.Pipeline(t.Pipeline); !ok {
			return fmt.Errorf("no pipeline %q in this suite", t.Pipeline)
		}
	}
	return nil
}

func (v View) validate(custom []string) error {
	oneOf := func(what, val string, allowed []string) error {
		if !slices.Contains(allowed, val) {
			return fmt.Errorf("%s %q is not one of %s", what, val, strings.Join(slices.DeleteFunc(slices.Clone(allowed), func(x string) bool { return x == "" }), ", "))
		}
		return nil
	}
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("needs a name")
	}
	if _, err := filter.ParseFields(v.Filter, custom); err != nil {
		return fmt.Errorf("filter: %w", err)
	}
	if err := oneOf("lanes", v.Lanes, ViewLanes); err != nil {
		return err
	}
	if err := oneOf("sort", v.Sort, ViewSorts); err != nil {
		return err
	}
	if err := oneOf("layout", v.Layout, ViewLayouts); err != nil {
		return err
	}
	for _, f := range v.Fields {
		if err := oneOf("field", f, append(slices.Clone(ViewFields), custom...)); err != nil {
			return err
		}
	}
	return nil
}

// langCode accepts BCP 47-like codes: ja, zh-TW, pt-BR.
var langCode = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

func (s Suite) clone() *Suite {
	c := s
	c.Disciplines = slices.Clone(s.Disciplines)
	c.ReleaseTargets = slices.Clone(s.ReleaseTargets)
	c.Intake.Labels = slices.Clone(s.Intake.Labels)
	c.Adapters.Locks = slices.Clone(s.Adapters.Locks)
	c.Adapters.VCS = slices.Clone(s.Adapters.VCS)
	c.Source = slices.Clone(s.Source)
	c.Pipelines = nil
	for _, p := range s.Pipelines {
		c.Pipelines = append(c.Pipelines, Pipeline{Name: p.Name, Steps: slices.Clone(p.Steps)})
	}
	c.Views = nil
	for _, v := range s.Views {
		v.Fields = slices.Clone(v.Fields)
		c.Views = append(c.Views, v)
	}
	c.Fields = nil
	for _, f := range s.Fields {
		f.Values = slices.Clone(f.Values)
		c.Fields = append(c.Fields, f)
	}
	c.Show = nil
	for k, v := range s.Show {
		if c.Show == nil {
			c.Show = map[string][]string{}
		}
		c.Show[k] = slices.Clone(v)
	}
	c.Lang = nil
	for code, l := range s.Lang {
		var cl Labels
		cl.merge(l)
		if c.Lang == nil {
			c.Lang = map[string]Labels{}
		}
		c.Lang[code] = cl
	}
	c.Templates = nil
	for _, t := range s.Templates {
		t.Labels = slices.Clone(t.Labels)
		c.Templates = append(c.Templates, t)
	}
	return &c
}
