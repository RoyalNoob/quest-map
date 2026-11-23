package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// MaxAttachment is the largest file a task attachment may be.
const MaxAttachment = 10 << 20

// Delete removes a task file and its attachments for good. Only people do this (the web UI); agents have no
// tool for it. The activity file keeps the task's history, with an edit record saying it was deleted.
func (p *Project) Delete(id string, by Actor) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, err := p.readLocked(id)
	var pe *ParseError
	archived := false
	switch {
	case err == nil:
		archived = t.Archived
	case errors.As(err, &pe):
		// A file that does not parse can still be deleted.
		archived = filepath.Base(filepath.Dir(pe.Path)) == "archive"
	default:
		return err
	}
	if err := removeWithRetry(p.taskPath(id, archived)); err != nil {
		return err
	}
	if err := os.RemoveAll(p.attachmentDir(id)); err != nil {
		return fmt.Errorf("%s deleted, but its attachments were not: %w", id, err)
	}
	return p.board.appendActivity(p.activityPath(), by.record(p.board.now(), KindEdit, id, func(r *Record) {
		r.Fields, r.Text = []string{"deleted"}, "deleted"
	}))
}

func (p *Project) attachmentDir(id string) string { return filepath.Join(p.dir, "attachments", id) }

// AttachmentPath returns the file of a task attachment, or an error if name is not a plain file name.
func (p *Project) AttachmentPath(id, name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\:`) {
		return "", fmt.Errorf("bad attachment name %q", name)
	}
	return filepath.Join(p.attachmentDir(id), name), nil
}

// SaveAttachment stores a file for a task under attachments/<id>/ and returns the name it was saved as
// (made safe, and made unique if the name is taken). Files over MaxAttachment are refused.
func (p *Project) SaveAttachment(id, name string, data []byte) (string, error) {
	if len(data) > MaxAttachment {
		return "", fmt.Errorf("attachment is %d MB; the limit is %d MB", len(data)>>20, MaxAttachment>>20)
	}
	if _, err := p.Get(id); err != nil {
		var pe *ParseError
		if !errors.As(err, &pe) {
			return "", err
		}
	}
	name = safeFileName(name)
	dir := p.attachmentDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if err := errors.Join(werr, cerr); err != nil {
			os.Remove(path)
			return "", err
		}
		return name, nil
	}
}

// safeFileName keeps letters, digits, dot, dash and underscore; anything else becomes "_".
func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if s == "" || s == "_" {
		s = "file"
	}
	if len(s) > 100 {
		ext := filepath.Ext(s)
		s = s[:100-len(ext)] + ext
	}
	return s
}
