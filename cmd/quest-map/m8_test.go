package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/RoyalNoob/quest-map/internal/board"
)

func TestExport(t *testing.T) {
	root := t.TempDir()
	b, _ := board.Open(root)
	p, _ := b.CreateProject("rpg", "RPG", "Role play")
	me := board.Actor{Kind: "person", Name: "me"}
	p.Create(&board.Task{Title: "Bridge collision", Doer: board.Agent, Discipline: "code", Body: "Falls through."}, me, nil)
	p.Create(&board.Task{Title: "Rig the troll", Doer: board.Person, Labels: []string{"art"}}, me, nil)
	p.Note("RPG-1", me, "seen on Win64", nil)

	var out, errOut bytes.Buffer
	if code := run([]string{"export", "--board", root}, &out, &errOut); code != 0 {
		t.Fatalf("export: %d %s", code, errOut.String())
	}
	md := out.String()
	for _, want := range []string{"## Role play (RPG)", "#### RPG-1 – Bridge collision", "discipline code", "Falls through.", "me: seen on Win64", "labels art"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}

	file := filepath.Join(t.TempDir(), "board.json")
	if code := run([]string{"export", "--board", root, "--format=json", "--project", "RPG", "--out", file}, &out, &errOut); code != 0 {
		t.Fatalf("export json: %d %s", code, errOut.String())
	}
	data, _ := os.ReadFile(file)
	var ps []exportProject
	if err := json.Unmarshal(data, &ps); err != nil || len(ps) != 1 || len(ps[0].Tasks) != 2 || ps[0].Tasks[0].ID != "RPG-1" || len(ps[0].Tasks[0].Log) == 0 {
		t.Fatalf("json export = %s (%v)", data, err)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		sign int
	}{
		{"v1.2.0", "v1.1.9", 1}, {"v1.0.0", "v1.0.0", 0}, {"v0.9.0", "v0.10.0", -1},
		{"v1.0.0", "dev", 1}, {"dev", "v1.0.0", -1}, {"v1.0.1", "v1.0.0-rc.1", 1},
		{"v1.2.0", "v1.2.0-rc.1", 1}, {"v1.2.0-rc.1", "v1.2.0", -1}, {"v1.2.0-rc.1", "v1.1.9", 1},
	} {
		got := newer(c.a, c.b)
		if (got > 0) != (c.sign > 0) || (got < 0) != (c.sign < 0) {
			t.Errorf("newer(%s, %s) = %d, want sign %d", c.a, c.b, got, c.sign)
		}
	}
}

// releaseServer serves a fake GitHub release v9.9.9 with this platform's archive holding bin.
func releaseServer(t *testing.T, bin []byte, corrupt bool) {
	t.Helper()
	name := assetName()
	exe := "quest-map"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	var archive bytes.Buffer
	if strings.HasSuffix(name, ".zip") {
		zw := zip.NewWriter(&archive)
		w, _ := zw.Create(exe)
		w.Write(bin)
		zw.Close()
	} else {
		gz := gzip.NewWriter(&archive)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: exe, Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
		tw.Write(bin)
		tw.Close()
		gz.Close()
	}
	sum := sha256.Sum256(archive.Bytes())
	if corrupt {
		sum[0] ^= 0xff
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"tag_name":"v9.9.9"}`)) })
	mux.HandleFunc("/download/v9.9.9/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + name + "\n"))
	})
	mux.HandleFunc("/download/v9.9.9/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(archive.Bytes()) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oldBase, oldLatest := releaseBase, latestURL
	releaseBase, latestURL = srv.URL, srv.URL+"/latest"
	t.Cleanup(func() { releaseBase, latestURL = oldBase, oldLatest })
}

func TestDownloadAndReplace(t *testing.T) {
	releaseServer(t, []byte("new binary"), false)
	bin, err := download(http.DefaultClient, "v9.9.9")
	if err != nil || string(bin) != "new binary" {
		t.Fatalf("download = %q, %v", bin, err)
	}
	exe := filepath.Join(t.TempDir(), "quest-map.exe")
	os.WriteFile(exe, []byte("old binary"), 0o755)
	if err := replaceExe(exe, bin); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new binary" {
		t.Errorf("exe = %q", got)
	}
}

func TestDownloadRefusesABadChecksum(t *testing.T) {
	releaseServer(t, []byte("evil"), true)
	if _, err := download(http.DefaultClient, "v9.9.9"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("download with a bad checksum: %v", err)
	}
}

func TestUpdateCheck(t *testing.T) {
	releaseServer(t, []byte("x"), false)
	var out, errOut bytes.Buffer
	if code := run([]string{"update", "--check", "--board", t.TempDir()}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "v9.9.9 is available") {
		t.Fatalf("update --check = %d %q %q", code, out.String(), errOut.String())
	}
	// The test binary is a dev build: update points at go install and leaves the binary alone.
	out.Reset()
	if code := run([]string{"update", "--board", t.TempDir()}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "go install") {
		t.Fatalf("update of a dev build = %d %q %q", code, out.String(), errOut.String())
	}
}
