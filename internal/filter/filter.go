// Package filter parses the task filter language shared by the UI search box, task_find and task_claim.
//
// A filter is space-separated terms that must all match: key:value terms such as status:doing or
// label:"art review", and free words for full-text search. Any term can be negated with a leading "-".
package filter

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// Term is one filter term. Key is empty for a free word or quoted phrase.
type Term struct {
	Neg   bool
	Key   string
	Value string
}

// String renders the term so Parse reads it back the same. (The language has no escape for a double quote
// inside a value, so such quotes are dropped.)
func (t Term) String() string {
	v := t.Value
	if v == "" || strings.ContainsAny(v, " \t\":") || strings.HasPrefix(v, "-") {
		v = `"` + strings.ReplaceAll(v, `"`, ``) + `"`
	}
	s := v
	if t.Key != "" {
		s = t.Key + ":" + v
	}
	if t.Neg {
		s = "-" + s
	}
	return s
}

// Query is a parsed filter.
type Query struct {
	Terms []Term
}

func (q Query) String() string {
	parts := make([]string, len(q.Terms))
	for i, t := range q.Terms {
		parts[i] = t.String()
	}
	return strings.Join(parts, " ")
}

// Has reports whether the query has a term with this key (negated or not).
func (q Query) Has(key string) bool {
	for _, t := range q.Terms {
		if t.Key == key {
			return true
		}
	}
	return false
}

var yesNo = func(v string) error {
	if v != "yes" && v != "no" {
		return fmt.Errorf("use yes or no, not %q", v)
	}
	return nil
}

var anyValue = func(string) error { return nil }

// keys are the known filter keys and their value checks.
var keys = map[string]func(string) error{
	"status": func(v string) error {
		if !board.Status(v).Valid() {
			return fmt.Errorf("%q is not one of backlog, todo, doing, review, done", v)
		}
		return nil
	},
	"doer": func(v string) error {
		if !board.Doer(v).Valid() {
			return fmt.Errorf("%q is not person or agent", v)
		}
		return nil
	},
	"priority": func(v string) error {
		if !board.Priority(v).Valid() {
			return fmt.Errorf("%q is not low, normal, high or urgent", v)
		}
		return nil
	},
	"discipline": anyValue,
	"milestone":  anyValue,
	"assignee":   anyValue,
	"label":      anyValue,
	"asset":      anyValue,
	"model":      anyValue,
	"found": func(v string) error { // latest, latest/<platform>, or a build id
		if v == "latest/" {
			return fmt.Errorf("latest/ needs a platform, like latest/ps5")
		}
		return nil
	},
	"blocked": yesNo,
	"stale":   yesNo,
	"spent":   yesNo,
}

// Keys returns the known filter keys, sorted.
func Keys() []string {
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Parse reads a filter string. Errors name the bad term and say what is allowed.
func Parse(s string) (Query, error) { return ParseFields(s, nil) }

// ParseFields is Parse that also knows a suite's custom field keys (spec §4.1), e.g. platform:ps5. Their
// values are any text.
func ParseFields(s string, fields []string) (Query, error) {
	var q Query
	toks, err := tokenize(s)
	if err != nil {
		return q, err
	}
	for _, tok := range toks {
		t := Term{}
		raw := tok.text
		if strings.HasPrefix(raw, "-") && len(raw) > 1 {
			t.Neg, raw = true, raw[1:]
		}
		if tok.quotedAll {
			t.Value = tok.value
			if searchable(t.Value) {
				q.Terms = append(q.Terms, t)
			}
			continue
		}
		key, val, hasColon := strings.Cut(raw, ":")
		if hasColon && isKeyword(key) && !strings.HasPrefix(val, "//") {
			check, known := keys[strings.ToLower(key)]
			if !known && slices.Contains(fields, strings.ToLower(key)) {
				check, known = anyValue, true
			}
			if !known {
				return q, fmt.Errorf("unknown filter %q: use one of %s, or quote it to search for the text", key+":", strings.Join(append(Keys(), fields...), ", "))
			}
			t.Key = strings.ToLower(key)
			t.Value = unquote(val)
			if lowerValues[t.Key] {
				t.Value = strings.ToLower(t.Value)
			}
			if t.Value == "" {
				return q, fmt.Errorf("%s: needs a value", t.Key)
			}
			if err := check(t.Value); err != nil {
				return q, fmt.Errorf("%s: %w", t.Key, err)
			}
		} else {
			t.Value = unquote(raw)
			if !searchable(t.Value) {
				continue // a lone "-", "" or punctuation: nothing to search for
			}
		}
		q.Terms = append(q.Terms, t)
	}
	return q, nil
}

// lowerValues are the keys whose values are fixed words, matched in any case (Status:Doing).
var lowerValues = map[string]bool{"status": true, "doer": true, "priority": true, "blocked": true, "stale": true, "spent": true}

// isKeyword reports whether s looks like a filter key – ASCII letters and underscores – so free words such as
// "10:30", URLs ("https://…", see Parse) or Japanese text before a colon are not mistaken for keys.
func isKeyword(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_') {
			return false
		}
	}
	return true
}

// searchable reports whether a free word has a letter or digit in it.
func searchable(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0
}

type token struct {
	text      string // raw token, quotes included
	value     string // for a fully quoted token: the text inside the quotes
	quotedAll bool   // the whole token (after an optional "-") was one quoted phrase
}

// tokenize splits on whitespace outside double quotes.
func tokenize(s string) ([]token, error) {
	var toks []token
	var cur strings.Builder
	inQuote, started := false, false
	flush := func() {
		if !started {
			return
		}
		text := cur.String()
		tk := token{text: text}
		inner := strings.TrimPrefix(text, "-")
		if len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"' && strings.Count(inner, `"`) == 2 {
			tk.quotedAll, tk.value = true, inner[1:len(inner)-1]
		}
		toks = append(toks, tk)
		cur.Reset()
		started = false
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
			started = true
		case unicode.IsSpace(r) && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unclosed quote in filter %q", s)
	}
	flush()
	return toks, nil
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
