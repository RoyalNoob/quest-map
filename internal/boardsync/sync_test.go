package boardsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// clone makes a working copy of bare with a user set, so it can commit.
func clone(t *testing.T, bare, name string) string {
	dir := t.TempDir()
	gitT(t, dir, "clone", "-q", bare, ".")
	gitT(t, dir, "config", "user.name", name)
	gitT(t, dir, "config", "user.email", name+"@example.invalid")
	return dir
}

// repo returns a bare repository with one commit on main, and a copy of it.
func repo(t *testing.T) (bare, a string) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	bare = t.TempDir()
	gitT(t, bare, "init", "--bare", "-q", "-b", "main")
	a = t.TempDir()
	gitT(t, a, "init", "-q", "-b", "main")
	gitT(t, a, "config", "user.name", "A")
	gitT(t, a, "config", "user.email", "a@example.invalid")
	os.WriteFile(filepath.Join(a, "board.toml"), []byte("# board\n"), 0o644)
	gitT(t, a, "add", "-A")
	gitT(t, a, "commit", "-q", "-m", "board")
	gitT(t, a, "remote", "add", "origin", bare)
	gitT(t, a, "push", "-q", "-u", "origin", "main")
	return bare, a
}

// A board in a subfolder of a bigger repository is not synced: a commit would take what the person staged
// elsewhere, and the merge and push would cover the whole repository.
func TestBoardInSubfolderIsOff(t *testing.T) {
	_, top := repo(t)
	sub := filepath.Join(top, "board")
	os.MkdirAll(sub, 0o755)
	st := New(sub, nil).Status()
	if st.State != StateOff || !strings.Contains(st.Message, "larger git repository") {
		t.Errorf("subfolder board = %+v", st)
	}
	if st := New(top, nil).Status(); st.State != StateOK {
		t.Errorf("top-folder board = %+v", st)
	}
}

// A conflicted binary file (a plugin image) has no markers, so it must stay a conflict until someone picks a
// side and runs git add, instead of the next sync keeping one side silently.
func TestBinaryConflictStaysUnresolved(t *testing.T) {
	bare, a := repo(t)
	b := clone(t, bare, "B")
	img := filepath.Join("plugins", "vision", "c1", "blobs", "x.png")
	for i, dir := range []string{a, b} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(img)), 0o755)
		os.WriteFile(filepath.Join(dir, img), []byte{0x89, 'P', 'N', 'G', 0, byte(i)}, 0o644)
	}
	ctx := context.Background()
	if st := New(a, nil).Sync(ctx); st.State != StateOK {
		t.Fatalf("first copy = %+v", st)
	}
	sb := New(b, nil)
	for range 2 { // the second sync must not commit the merge
		st := sb.Sync(ctx)
		if st.State != StateConflict || !slices.Contains(st.Files, filepath.ToSlash(img)) {
			t.Fatalf("binary conflict = %+v", st)
		}
	}
	gitT(t, b, "checkout", "--theirs", "--", img)
	gitT(t, b, "add", "--", img)
	if st := sb.Sync(ctx); st.State != StateOK {
		t.Errorf("after git add = %+v", st)
	}
}
