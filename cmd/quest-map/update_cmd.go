package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/RoyalNoob/quest-map/internal/server"
)

const repo = "RoyalNoob/quest-map"

// releaseBase is where release files are downloaded from (a test server in tests).
var releaseBase = "https://github.com/" + repo + "/releases"

// latestURL answers with the latest release (GitHub API; a test server in tests).
var latestURL = "https://api.github.com/repos/" + repo + "/releases/latest"

// currentVersion is the release this binary was built from: set by GoReleaser, else the module version
// `go install …@vX.Y.Z` records, else "dev".
func currentVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// cmdUpdate installs the latest release over this binary and restarts the board's server if one runs.
func cmdUpdate(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "quest-map update:", err)
		return 1
	}
	root, rest, err := boardRoot(args)
	if err != nil {
		return fail(err)
	}
	_, flags, err := parseFlags(rest, nil, []string{"check", "force"})
	if err != nil {
		return fail(err)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	latest, err := latestTag(client)
	if err != nil {
		return fail(err)
	}
	cur := currentVersion()
	switch {
	case !flags["force"] && newer(latest, cur) <= 0:
		fmt.Fprintf(stdout, "quest-map %s is the latest release\n", cur)
		return 0
	case flags["check"]:
		fmt.Fprintf(stdout, "quest-map %s is available (this is %s); run quest-map update\n", latest, cur)
		return 0
	case version == "dev" && !flags["force"]:
		// Not a release build (go install or from source): replacing it would mix two install methods.
		fmt.Fprintf(stdout, "quest-map %s is available; this build is from go install or source, so update it with:\n"+
			"  go install github.com/%s/cmd/quest-map@latest\nthen run quest-map restart (or quest-map update --force to switch to the release binary)\n", latest, repo)
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	bin, err := download(client, latest)
	if err != nil {
		return fail(err)
	}
	if err := replaceExe(exe, bin); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "updated quest-map %s → %s (%s)\n", cur, latest, exe)
	if server.Running(root) != nil {
		cmd := exec.Command(exe, "restart", "--board", root)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Run(); err != nil {
			return fail(fmt.Errorf("restart the server: %w", err))
		}
	}
	return 0
}

func get(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "quest-map-update")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func latestTag(client *http.Client) (string, error) {
	data, err := get(client, latestURL)
	if err != nil {
		return "", fmt.Errorf("find the latest release: %w", err)
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.TagName == "" {
		return "", fmt.Errorf("find the latest release: unexpected answer from %s", latestURL)
	}
	return r.TagName, nil
}

// assetName is the release archive for this platform (GoReleaser's name_template).
func assetName() string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return "quest-map_" + runtime.GOOS + "_" + runtime.GOARCH + ext
}

// download fetches this platform's archive of release tag, checks it against checksums.txt and returns the
// binary inside it.
func download(client *http.Client, tag string) ([]byte, error) {
	base := releaseBase + "/download/" + tag + "/"
	name := assetName()
	sums, err := get(client, base+"checksums.txt")
	if err != nil {
		return nil, err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("release %s has no %s for this platform", tag, name)
	}
	archive, err := get(client, base+name)
	if err != nil {
		return nil, err
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("%s does not match its checksum; nothing was changed", name)
	}
	return extract(name, archive)
}

// extract returns the quest-map binary from a release archive.
func extract(name string, archive []byte) ([]byte, error) {
	bin := "quest-map"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == bin {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return nil, err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if path.Base(h.Name) == bin && h.Typeflag == tar.TypeReg {
				return io.ReadAll(tr)
			}
		}
	}
	return nil, fmt.Errorf("%s holds no %s", name, bin)
}

// replaceExe puts bin in place of exe. A running program cannot be overwritten on Windows but can be renamed,
// so the old binary is moved aside to exe.old (removed by the next update) before the new one takes its name.
func replaceExe(exe string, bin []byte) error {
	old := exe + ".old"
	os.Remove(old) // left by an earlier update
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return errors.Join(err, fmt.Errorf("the old binary is at %s: %w", old, rerr))
		}
		return err
	}
	if runtime.GOOS != "windows" {
		os.Remove(old) // the running process keeps its open file
	}
	return nil
}

// newer compares two versions like v1.2.3: >0 when a is newer than b. A pre-release (v1.2.0-rc.1) is older
// than its release. Anything that is not such a version (dev builds) is older than every release.
func newer(a, b string) int {
	pa, preA, okA := semver(a)
	pb, preB, okB := semver(b)
	switch {
	case !okA && !okB:
		return 0
	case !okB:
		return 1
	case !okA:
		return -1
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] - pb[i]
		}
	}
	switch {
	case preA && !preB:
		return -1
	case preB && !preA:
		return 1
	}
	return 0 // ponytail: two pre-releases of one version compare equal; GitHub's latest is never one
}

func semver(v string) (out [3]int, pre, ok bool) {
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "+")
	v, _, pre = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false, false
		}
		out[i] = n
	}
	return out, pre, true
}
