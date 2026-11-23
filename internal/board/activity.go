package board

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Record kinds in activity.jsonl.
const (
	KindUsage  = "usage"
	KindStatus = "status"
	KindNote   = "note"
	KindEdit   = "edit"
)

// Usage sources.
const (
	SourceMeasured = "measured"
	SourceSelf     = "self"
	SourceHuman    = "human"
)

// noteTextMax is how much of a note's text a note record keeps.
const noteTextMax = 200

// CannotContinue in a note written with a move to review marks an abandoned claim (§8.4).
const CannotContinue = "cannot continue"

// noteRecordText cuts a note to noteTextMax runes. When the cut would drop CannotContinue, the kept text ends
// with it, so Insights still sees the hand-off.
func noteRecordText(n string) string {
	r := []rune(n)
	if len(r) <= noteTextMax {
		return n
	}
	cut := string(r[:noteTextMax])
	if strings.Contains(strings.ToLower(n), CannotContinue) && !strings.Contains(strings.ToLower(cut), CannotContinue) {
		tail := " … " + CannotContinue
		cut = string(r[:noteTextMax-len([]rune(tail))]) + tail
	}
	return cut
}

// Tokens counts model tokens by type. Names follow Claude Code's OpenTelemetry metrics. Total holds a
// self-reported count that is not split by type.
type Tokens struct {
	Input         int64 `json:"input,omitempty"`
	Output        int64 `json:"output,omitempty"`
	CacheRead     int64 `json:"cacheRead,omitempty"`
	CacheCreation int64 `json:"cacheCreation,omitempty"`
	Total         int64 `json:"total,omitempty"`
}

// Sum returns all tokens, whatever their type.
func (t Tokens) Sum() int64 { return t.Input + t.Output + t.CacheRead + t.CacheCreation + t.Total }

// IsZero reports whether no tokens are recorded.
func (t Tokens) IsZero() bool { return t == Tokens{} }

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheCreation += o.CacheCreation
	t.Total += o.Total
}

// Record is one line of a project's activity.jsonl.
type Record struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Task    *string   `json:"task"` // nil: not tied to a task (usage overhead)
	Actor   string    `json:"actor,omitempty"`
	Session string    `json:"session,omitempty"`
	Client  string    `json:"client,omitempty"`
	Model   string    `json:"model,omitempty"`
	// Origin is the id of the board copy (machine) that wrote the record, so records can be told apart after
	// two copies of a board are merged (§3.5).
	Origin string `json:"origin,omitempty"`

	// usage
	Source  string   `json:"source,omitempty"`
	Tokens  *Tokens  `json:"tokens,omitempty"`
	TimeS   int64    `json:"time_s,omitempty"`
	CostUSD *float64 `json:"cost_usd,omitempty"`

	// status: old and new status. edit of a move: To is the new id (on the old task), From the old id (on
	// the new task).
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`

	// note: first noteTextMax characters; edit: optional description (e.g. "moved to ART-3")
	Text string `json:"text,omitempty"`
	// edit and kb: names of the changed fields
	Fields []string `json:"fields,omitempty"`
	// kb: the knowledge base page written (its slug)
	Page string `json:"page,omitempty"`
	// edit of a renumber: records of the old id (From) written by this origin before this record belong to
	// the renumbered task. Empty: they all stay with the task that kept the id.
	TakeOrigin string `json:"take_origin,omitempty"`
}

// TaskID returns the record's task, or "" for records not tied to a task.
func (r Record) TaskID() string {
	if r.Task == nil {
		return ""
	}
	return *r.Task
}

// Usage is one report of tokens and working time.
type Usage struct {
	Tokens  Tokens
	Time    time.Duration // active working time; negative only for human corrections
	Source  string        // SourceMeasured, SourceSelf (default) or SourceHuman
	Model   string
	CostUSD *float64 // only when the measured source supplies one
}

// IsZero reports whether the report carries no tokens and no time.
func (u *Usage) IsZero() bool { return u == nil || (u.Tokens.IsZero() && u.Time == 0) }

// Spent is the usage total of a task (or of anything else records are summed over).
type Spent struct {
	Tokens   Tokens
	Time     time.Duration
	CostUSD  float64 // only costs that measured sources supplied; priced cost is computed elsewhere
	Records  int     // usage records counted
	Self     int     // of which self-reported
	Reported int     // all usage records, including self-reports skipped because the session was measured
	Models   map[string]Tokens
}

// add counts one usage record. counted is false for a self-report that measured data replaces: it only
// adds to Reported.
func (s *Spent) add(r Record, counted bool) {
	s.Reported++
	if !counted {
		return
	}
	s.Records++
	if r.Source == SourceSelf {
		s.Self++
	}
	if r.Tokens != nil {
		s.Tokens.add(*r.Tokens)
		if r.Model != "" {
			if s.Models == nil {
				s.Models = map[string]Tokens{}
			}
			m := s.Models[r.Model]
			m.add(*r.Tokens)
			s.Models[r.Model] = m
		}
	}
	s.Time += time.Duration(r.TimeS) * time.Second
	if r.CostUSD != nil {
		s.CostUSD += *r.CostUSD
	}
}

// Suffix renders the readable log suffix for a usage report, e.g. " (+42k tok, +25m)", or "".
func (u *Usage) Suffix() string {
	if u.IsZero() {
		return ""
	}
	var parts []string
	if n := u.Tokens.Sum(); n != 0 {
		parts = append(parts, signed(n < 0)+FormatTokens(abs(n))+" tok")
	}
	if u.Time != 0 {
		d := u.Time
		parts = append(parts, signed(d < 0)+FormatDuration(time.Duration(abs(int64(d)))))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func signed(neg bool) string {
	if neg {
		return "-"
	}
	return "+"
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// FormatTokens renders a token count briefly: 850, 4.2k, 42k, 1.3M.
func FormatTokens(n int64) string {
	f := float64(n)
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 10_000:
		return trimZero(fmt.Sprintf("%.1f", f/1000)) + "k"
	case n < 1_000_000:
		return fmt.Sprintf("%.0fk", math.Round(f/1000))
	default:
		return trimZero(fmt.Sprintf("%.1f", f/1_000_000)) + "M"
	}
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// FormatDuration renders working time in hours and minutes: 2h15m, 25m, 3h, or <1m.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		return "-" + FormatDuration(-d)
	}
	m := int64(d.Round(time.Minute) / time.Minute)
	switch {
	case d > 0 && m == 0:
		return "<1m"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	default:
		return fmt.Sprintf("%dh%dm", m/60, m%60)
	}
}

// ParseDuration reads working time such as 25m, 1h30m, 90s or 2h. A bare number means minutes.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(n * float64(time.Minute)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("duration %q: use a form like 25m or 1h30m", s)
	}
	return d, nil
}

var (
	idMu     sync.Mutex
	idLastMs uint64
	idLast   [10]byte
)

// newID returns a ULID: 48-bit millisecond time plus 80 random bits, Crockford base32, so ids sort by time.
// Ids made in the same millisecond increase monotonically, so records of one write keep their order.
func newID(t time.Time) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var b [16]byte
	ms := uint64(t.UnixMilli())
	idMu.Lock()
	if ms <= idLastMs {
		ms = idLastMs
		for i := 9; i >= 0; i-- { // increment the 80-bit random part
			idLast[i]++
			if idLast[i] != 0 {
				break
			}
		}
	} else {
		if _, err := rand.Read(idLast[:]); err != nil {
			idMu.Unlock()
			panic(err) // crypto/rand does not fail on supported platforms
		}
		idLastMs = ms
	}
	copy(b[6:], idLast[:])
	idMu.Unlock()
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	// 128 bits → 26 base32 characters (the first carries 3 bits).
	var out [26]byte
	var hi, lo uint64
	for i := 0; i < 8; i++ {
		hi = hi<<8 | uint64(b[i])
		lo = lo<<8 | uint64(b[8+i])
	}
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// appendRecords adds records to an activity file, one JSON object per line.
func appendRecords(path string, recs ...Record) error {
	if len(recs) == 0 {
		return nil
	}
	return gated(func() error { return appendRecordsNow(path, recs) })
}

func appendRecordsNow(path string, recs []Record) error {
	var buf bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	// A crash in the middle of an earlier append can leave a last line without its newline. Start on a new
	// line so the broken line stays alone (readRecords skips it) and this write is not glued onto it.
	if fi, err := f.Stat(); err != nil {
		f.Close()
		return err
	} else if fi.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, fi.Size()-1); err != nil {
			f.Close()
			return err
		}
		if last[0] != '\n' {
			buf2 := append([]byte{'\n'}, buf.Bytes()...)
			buf.Reset()
			buf.Write(buf2)
		}
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readRecords reads an activity file. Records are sorted by id (time order) and duplicates – which a
// union merge of two copies of the file leaves behind – are dropped. Lines that are not valid records are
// skipped and counted.
func readRecords(path string) (recs []Record, bad int, err error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil || r.ID == "" || r.Kind == "" {
			bad++
			continue
		}
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, bad, err
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	return recs, bad, nil
}

// SumUsage totals usage records. Self-reported records from a session that also has measured records are
// ignored, so a session is never counted twice.
func SumUsage(recs []Record) Spent {
	return sumUsage(recs, measuredSessions(recs))
}

// measuredSessions returns the sessions that have measured usage records in one activity file. To total a
// subset of records (one task), pass it the whole file: a session's measured records may sit on another task
// or on none.
func measuredSessions(recs []Record) map[string]bool {
	measured := map[string]bool{}
	for _, r := range recs {
		if r.Kind == KindUsage && r.Source == SourceMeasured && r.Session != "" {
			measured[r.Session] = true
		}
	}
	return measured
}

// sumUsage totals usage records, skipping self-reported records from the measured sessions.
func sumUsage(recs []Record, measured map[string]bool) Spent {
	s := Spent{Models: map[string]Tokens{}}
	for _, r := range recs {
		if r.Kind == KindUsage {
			s.add(r, countsUsage(r, measured))
		}
	}
	return s
}

// countsUsage reports whether a usage record counts: a self-report from a session with measured records in
// the same activity file does not.
func countsUsage(r Record, measured map[string]bool) bool {
	return !(r.Source == SourceSelf && measured[r.Session])
}
