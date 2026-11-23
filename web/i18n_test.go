package web

import (
	"encoding/json"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Spec §4.4 / Localization done-when: a test fails on a UI string that is not in the catalog, or a key
// missing from en.json. Catalog keys are flat; plurals are <key>.one / <key>.other.

func catalog(t *testing.T, name string) map[string]string {
	t.Helper()
	data, err := fs.ReadFile(Files, path.Join("i18n", name))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func file(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(Files, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var pluralSuffix = regexp.MustCompile(`\.(zero|one|two|few|many|other)$`)

// has reports whether the catalog has key, or its plural forms.
func has(cat map[string]string, key string) bool {
	_, ok := cat[key]
	_, plural := cat[key+".other"]
	return ok || plural
}

// TestKeysUsedAreInEnglish: every key the page and the script name is in en.json.
func TestKeysUsedAreInEnglish(t *testing.T) {
	en := catalog(t, "en.json")
	html, js := file(t, "index.html"), file(t, "app.js")
	spaces := map[string]bool{}
	for k := range en {
		spaces[strings.SplitN(k, ".", 2)[0]] = true
	}
	var used []string
	for _, m := range regexp.MustCompile(`data-i18n(?:-[a-z-]+)?="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		used = append(used, m[1])
	}
	// The first argument of t()/tx(), and any other literal in a catalog namespace (t(cond ? "a.b" : "a.c")).
	for _, m := range regexp.MustCompile(`\btx?\(\s*"([^"]+)"`).FindAllStringSubmatch(js, -1) {
		used = append(used, m[1])
	}
	for _, m := range regexp.MustCompile(`"([a-z_]+\.[a-z0-9_.]*[a-z0-9])"`).FindAllStringSubmatch(js, -1) {
		if spaces[strings.SplitN(m[1], ".", 2)[0]] {
			used = append(used, m[1])
		}
	}
	if len(used) < 100 {
		t.Fatalf("found only %d keys in use; the patterns no longer match the code", len(used))
	}
	for _, k := range used {
		if strings.HasSuffix(k, ".") || strings.HasSuffix(k, "_") { // a prefix: t("field." + key)
			if !slices.ContainsFunc(slices.Collect(maps.Keys(en)), func(x string) bool { return strings.HasPrefix(x, k) }) {
				t.Errorf("no key starts with %q", k)
			}
			continue
		}
		if !has(en, k) {
			t.Errorf("key %q is used but not in en.json", k)
		}
	}
}

var placeholder = regexp.MustCompile(`\{\w+\}`)

func placeholders(s string) []string {
	p := placeholder.FindAllString(s, -1)
	slices.Sort(p)
	return slices.Compact(p)
}

// TestTranslationsAreComplete: the built-in languages have every English key, no others, and the same
// placeholders. A language without plural forms (ja, zh-TW) needs only <key>.other.
func TestTranslationsAreComplete(t *testing.T) {
	en := catalog(t, "en.json")
	names, _ := fs.Glob(Files, "i18n/*.json")
	if len(names) < 3 {
		t.Fatalf("catalogs = %v, want en, ja and zh-TW at least", names)
	}
	for _, n := range names {
		name := path.Base(n)
		if name == "en.json" {
			continue
		}
		cat := catalog(t, name)
		for k, v := range en {
			base := pluralSuffix.ReplaceAllString(k, "")
			if base != k {
				if !has(cat, base) {
					t.Errorf("%s: no %s.other", name, base)
				}
				continue
			}
			got, ok := cat[k]
			if !ok {
				t.Errorf("%s: missing %q", name, k)
				continue
			}
			if !slices.Equal(placeholders(got), placeholders(v)) {
				t.Errorf("%s: %q has placeholders %v, English %v", name, k, placeholders(got), placeholders(v))
			}
		}
		for k, v := range cat {
			base := pluralSuffix.ReplaceAllString(k, "")
			if _, ok := en[k]; !ok && !(base != k && has(en, base)) {
				t.Errorf("%s: %q is not an English key", name, k)
			}
			if base != k && has(en, base) && !slices.Equal(placeholders(v), placeholders(en[base+".other"])) {
				t.Errorf("%s: %q has placeholders %v, English %v", name, k, placeholders(v), placeholders(en[base+".other"]))
			}
		}
	}
}

// htmlExempt is text in index.html that is not UI text: the product name and sample values.
var htmlExempt = map[string]bool{"quest-map": true, "RPG-3, RPG-9": true, "BR": true, "br": true,
	"git@github.com:acme/bridge-racer.git": true, `D:\Projects\MyGame`: true}

var letters = regexp.MustCompile(`[A-Za-z]{2}`)

// TestNoHardCodedTextInPage: every text node and title/placeholder/aria-label in index.html comes from the
// catalog (data-i18n on its element, data-i18n-<attr> for an attribute).
func TestNoHardCodedTextInPage(t *testing.T) {
	html := regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(file(t, "index.html"), "")
	void := map[string]bool{"input": true, "img": true, "link": true, "meta": true, "br": true, "hr": true}
	type open struct {
		tag        string
		translated bool
	}
	var stack []open
	attr := regexp.MustCompile(`\s(title|placeholder|aria-label)="([^"]*)"`)
	for _, tok := range regexp.MustCompile(`<[^>]+>|[^<]+`).FindAllString(html, -1) {
		switch {
		case strings.HasPrefix(tok, "</"):
			if n := len(stack); n > 0 {
				stack = stack[:n-1]
			}
		case strings.HasPrefix(tok, "<!"):
		case strings.HasPrefix(tok, "<"):
			tag := strings.ToLower(regexp.MustCompile(`^<([a-zA-Z0-9]+)`).FindStringSubmatch(tok)[1])
			for _, m := range attr.FindAllStringSubmatch(tok, -1) {
				if letters.MatchString(m[2]) && !htmlExempt[m[2]] && !strings.Contains(tok, "data-i18n-"+m[1]+"=") {
					t.Errorf("%s=%q in %s is not from the catalog (add data-i18n-%s)", m[1], m[2], tok, m[1])
				}
			}
			if !void[tag] && !strings.HasSuffix(tok, "/>") {
				stack = append(stack, open{tag, strings.Contains(tok, ` data-i18n="`)})
			}
		default:
			text := strings.TrimSpace(tok)
			if !letters.MatchString(text) || htmlExempt[text] || len(stack) == 0 {
				continue
			}
			parent := stack[len(stack)-1]
			if parent.translated || parent.tag == "kbd" || parent.tag == "title" || parent.tag == "script" || parent.tag == "style" {
				continue
			}
			t.Errorf("text %q in <%s> is not from the catalog (add data-i18n)", text, parent.tag)
		}
	}
}

// TestNoHardCodedTextInScript catches the usual ways UI text is put on screen in app.js with a string literal
// instead of t(…): toasts, dialogs, errors, element text and the title / placeholder / aria-label attributes.
// A line that really needs a literal (a path, text written into a file) says so with "i18n-ok".
func TestNoHardCodedTextInScript(t *testing.T) {
	lit := `(?:"(?:[^"\\]|\\.)*[A-Za-z]{2}(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*[A-Za-z]{2}(?:[^'\\]|\\.)*'|` + "`[^`]*[A-Za-z]{2}[^`]*`" + `)`
	pats := []*regexp.Regexp{
		regexp.MustCompile(`\btoast\(\s*` + lit),
		regexp.MustCompile(`\b(?:confirm|prompt|alert)\(\s*` + lit),
		regexp.MustCompile(`new Error\(\s*` + lit),
		regexp.MustCompile(`\bel\("[a-z0-9]+",\s*(?:\{[^{}]*\}|null|\{\}),\s*` + lit),
		regexp.MustCompile(`\.(?:textContent|title|placeholder)\s*=\s*` + lit),
		regexp.MustCompile(`(?:\btitle|placeholder|"aria-label"):\s*` + lit),
	}
	// ${…} in a template literal is code, not text.
	interp := regexp.MustCompile(`\$\{[^}]*\}`)
	for i, line := range strings.Split(file(t, "app.js"), "\n") {
		if strings.Contains(line, "i18n-ok") || strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code := interp.ReplaceAllString(line, "")
		for _, p := range pats {
			if p.MatchString(code) {
				t.Errorf("app.js:%d: UI text is not from the catalog: %s", i+1, strings.TrimSpace(line))
				break
			}
		}
	}
}
