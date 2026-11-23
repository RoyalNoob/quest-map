package board

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestFormatTokens(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 850: "850", 1000: "1k", 4200: "4.2k", 42_000: "42k", 184_400: "184k", 1_300_000: "1.3M", 2_000_000: "2M"} {
		if got := FormatTokens(n); got != want {
			t.Errorf("FormatTokens(%d) = %s, want %s", n, got, want)
		}
	}
}

func TestNoteRecordTextKeepsCannotContinue(t *testing.T) {
	long := strings.Repeat("x", 250)
	if got := noteRecordText(long); got != long[:noteTextMax] {
		t.Errorf("plain cut = %d runes", len([]rune(got)))
	}
	got := noteRecordText(long + " Cannot continue: no SDK")
	if len([]rune(got)) != noteTextMax || !strings.HasSuffix(got, CannotContinue) {
		t.Errorf("cut = %q", got)
	}
	if short := "cannot continue"; noteRecordText(short) != short {
		t.Error("short note changed")
	}
}

func TestFormatAndParseDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{20 * time.Second: "<1m", 25 * time.Minute: "25m", time.Hour: "1h", 135 * time.Minute: "2h15m", -5 * time.Minute: "-5m"} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%s) = %s, want %s", d, got, want)
		}
	}
	for in, want := range map[string]time.Duration{"25m": 25 * time.Minute, "1h30m": 90 * time.Minute, "90s": 90 * time.Second, "15": 15 * time.Minute, " 2h ": 2 * time.Hour} {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %s, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "soon", "1d"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) succeeded", in)
		}
	}
}

func TestUsageSuffix(t *testing.T) {
	for _, c := range []struct {
		u    *Usage
		want string
	}{
		{nil, ""},
		{&Usage{}, ""},
		{&Usage{Tokens: Tokens{Input: 2000, Output: 40_000}, Time: 25 * time.Minute}, " (+42k tok, +25m)"},
		{&Usage{Time: time.Hour}, " (+1h)"},
		{&Usage{Tokens: Tokens{Total: -500}}, " (-500 tok)"},
	} {
		if got := c.u.Suffix(); got != c.want {
			t.Errorf("Suffix(%+v) = %q, want %q", c.u, got, c.want)
		}
	}
}

func TestNewIDSortsByTimeAndIsMonotonic(t *testing.T) {
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 1000; i++ {
		ids = append(ids, newID(at)) // same millisecond
	}
	ids = append(ids, newID(at.Add(time.Millisecond)))
	if !sort.StringsAreSorted(ids) {
		t.Error("ids are not increasing")
	}
	for _, id := range ids {
		if len(id) != 26 {
			t.Fatalf("id %q has length %d", id, len(id))
		}
	}
	if ids[0][:10] != ids[999][:10] {
		t.Error("time prefix differs within one millisecond")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	for _, content := range []string{"one", "two"} {
		if err := writeFileAtomic(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != content {
			t.Errorf("content = %q", got)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %d entries", len(entries))
	}
}

func TestAppendAfterPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	task := "RPG-1"
	if err := appendRecords(path, Record{ID: newID(time.Now()), Kind: KindNote, Task: &task}); err != nil {
		t.Fatal(err)
	}
	// A crash cut the next write short: no newline at the end.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"id":"01JD","kind":"no`)
	f.Close()
	if err := appendRecords(path, Record{ID: newID(time.Now()), Kind: KindNote, Task: &task}); err != nil {
		t.Fatal(err)
	}
	recs, bad, err := readRecords(path)
	if err != nil || len(recs) != 2 || bad != 1 {
		t.Errorf("records %d, bad %d, err %v", len(recs), bad, err)
	}
}

func TestRemoveWithRetry(t *testing.T) {
	dir := t.TempDir()
	if err := removeWithRetry(filepath.Join(dir, "gone.md")); err != nil {
		t.Errorf("missing file: %v", err)
	}
	path := filepath.Join(dir, "busy.md")
	os.WriteFile(path, []byte("x"), 0o644)
	// On Windows an open file cannot be deleted; the retry waits until the other holder closes it.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(150*time.Millisecond, func() { f.Close() })
	if err := removeWithRetry(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file still there")
	}
}
