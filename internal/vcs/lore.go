package vcs

import (
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Lore workspaces (verified against Lore 0.14 and a Lore server): the workspace root holds a .lore folder whose
// config.toml has a top-level remote_url naming only the server ("lore://lore.example.com:41337"), and whose
// id file holds the repository id (16 bytes; `lore status` prints it as 32 hex digits). The key is
// lore:<host>[:<port>]/<id hex>. A remote_url with a path keeps that path instead. A repository made with
// --offline has an empty remote_url and gives no key.

// LoreRoot returns the top folder of the Lore workspace that holds dir: the nearest folder, dir itself or
// above, with a .lore/config.toml file. "" if there is none. It runs no lore process.
func LoreRoot(dir string) string {
	if dir == "" {
		return ""
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		if fi, err := os.Stat(filepath.Join(d, ".lore", "config.toml")); err == nil && !fi.IsDir() {
			return d
		}
		up := filepath.Dir(d)
		if up == d {
			return ""
		}
		d = up
	}
}

// loreRemote reads remote_url from the .lore/config.toml of the workspace holding dir ("" if none).
func loreRemote(dir string) string {
	root := LoreRoot(dir)
	if root == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(root, ".lore", "config.toml"))
	if err != nil {
		return ""
	}
	var cfg struct {
		RemoteURL string `toml:"remote_url"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	remote := strings.TrimRight(strings.TrimSpace(cfg.RemoteURL), "/")
	if u, err := url.Parse(remote); err == nil && u.Host != "" && strings.Trim(u.Path, "/") == "" {
		id, err := os.ReadFile(filepath.Join(root, ".lore", "id"))
		if err != nil || len(id) != 16 {
			return ""
		}
		remote += "/" + hex.EncodeToString(id)
	}
	return remote
}

// NormalizeLore turns a Lore remote URL (lore://host[:port]/name) into the key lore:<host>[:<port>]/<name>:
// lowercase host, no user, no default port 41337, path case kept.
func NormalizeLore(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !isLoreScheme(u.Scheme) {
		return "", false
	}
	host, port := u.Hostname(), u.Port()
	path := strings.Trim(u.Path, "/")
	if host == "" || path == "" {
		return "", false
	}
	key := "lore:" + strings.ToLower(host)
	if port != "" && port != "41337" {
		key += ":" + port
	}
	return key + "/" + path, true
}

// isLoreScheme reports a Lore URL scheme (lore, or a variant such as lores).
func isLoreScheme(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "lore")
}
