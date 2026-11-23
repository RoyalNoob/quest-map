package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/plugins"
	"github.com/RoyalNoob/quest-map/web"
)

// UI plugins (§8.5). Plugin files are served without the token to a sandboxed iframe; the CSP on every one of
// them keeps the plugin inside its folder, with no network, no cookies and no access to the page.

// maxPluginValue caps one stored value (JSON); maxPluginBlob one image (like attachments).
const (
	maxPluginValue = 1 << 20
	maxPluginBlob  = 10 << 20
)

// blobTypes are the images a plugin may store, by their sniffed type. No SVG: it can hold script.
var blobTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// pluginDir is the plugins/ folder next to custom.css in the user's config folder.
func (s *Server) pluginDir() string {
	if s.css == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.css), "plugins")
}

// pluginList is the plugins found at one time; a view loads several files, so the list is read again only
// after pluginCacheTTL (a plugin someone is editing in the folder shows up that much later).
type pluginList struct {
	at       time.Time
	list     []*plugins.Plugin
	problems []plugins.Problem
}

const pluginCacheTTL = 2 * time.Second

func (s *Server) loadPlugins() ([]*plugins.Plugin, []plugins.Problem) {
	s.pluginMu.Lock()
	defer s.pluginMu.Unlock()
	if c := s.pluginCache; !c.at.IsZero() && time.Since(c.at) < pluginCacheTTL {
		return c.list, c.problems
	}
	builtin, _ := fs.Sub(web.Files, "plugins")
	list, probs := plugins.Load(builtin, s.pluginDir())
	s.pluginCache = pluginList{time.Now(), list, probs}
	return list, probs
}

func (s *Server) plugin(name string) (*plugins.Plugin, error) {
	ps, _ := s.loadPlugins()
	for _, p := range ps {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, apiError{http.StatusNotFound, "no plugin " + name}
}

func (s *Server) pluginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plugins", s.api(func(r *http.Request) (any, error) {
		ps, _ := s.loadPlugins()
		if ps == nil {
			ps = []*plugins.Plugin{}
		}
		return map[string]any{"plugins": ps}, nil
	}))
	mux.HandleFunc("GET /api/plugins/{name}/storage", s.api(s.apiPluginList))
	mux.HandleFunc("GET /api/plugins/{name}/storage/{key...}", s.apiPluginGet)
	mux.HandleFunc("PUT /api/plugins/{name}/storage/{key...}", s.api(s.apiPluginPut))
	mux.HandleFunc("DELETE /api/plugins/{name}/storage/{key...}", s.api(s.apiPluginDelete))
	mux.HandleFunc("GET /api/plugins/{name}/blobs/{key...}", s.apiPluginBlobGet)
	mux.HandleFunc("PUT /api/plugins/{name}/blobs/{key...}", s.api(s.apiPluginBlobPut))
	mux.HandleFunc("GET /plugins/{name}/{file...}", s.servePluginFile)
}

// pluginCSP allows only the plugin's own files (base) and forces the sandbox even when a file is opened on its
// own: an opaque origin, so no cookies, no storage of the page and no /api. allow-downloads is for CSV files.
func pluginCSP(r *http.Request, base string) string {
	own := "http://" + r.Host + base
	return "default-src 'none'; script-src " + own + "; style-src " + own + " 'unsafe-inline'; img-src " + own +
		" data: blob:; font-src " + own + "; media-src " + own + " blob:; connect-src 'none'; form-action 'none'; base-uri 'none'; " +
		"frame-ancestors http://127.0.0.1:" + portOf(r.Host) + " http://localhost:" + portOf(r.Host) + "; sandbox allow-scripts allow-downloads"
}

func portOf(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[i+1:]
	}
	return "80"
}

// pluginTypes are set explicitly: on Windows mime.TypeByExtension reads the registry, which may say anything.
var pluginTypes = map[string]string{".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json", ".html": "text/html; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".woff2": "font/woff2", ".txt": "text/plain; charset=utf-8"}

// servePluginFile serves /plugins/<name>/<file>: the plugin's own files, and under _qm/ the frame page and the
// plugin API script every plugin shares.
func (s *Server) servePluginFile(w http.ResponseWriter, r *http.Request) {
	name, file := r.PathValue("name"), r.PathValue("file")
	if !fs.ValidPath(file) {
		http.NotFound(w, r)
		return
	}
	var data []byte
	var err error
	if rest, ok := strings.CutPrefix(file, "_qm/"); ok {
		if _, perr := s.plugin(name); perr != nil {
			http.NotFound(w, r)
			return
		}
		data, err = fs.ReadFile(web.Files, path.Join("frame", rest))
	} else {
		p, perr := s.plugin(name)
		if perr != nil {
			http.NotFound(w, r)
			return
		}
		data, err = fs.ReadFile(p.FS, file)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(path.Ext(file))
	ct := pluginTypes[ext]
	if ct == "" {
		if ct = mime.TypeByExtension(ext); ct == "" {
			ct = "application/octet-stream"
		}
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Security-Policy", pluginCSP(r, "/plugins/"+name+"/"))
	h.Set("Referrer-Policy", "no-referrer")
	// The sandboxed frame has an opaque origin and loads ES modules in CORS mode. Plugin files are public
	// anyway (no token); nothing is sent with credentials.
	h.Set("Access-Control-Allow-Origin", "*")
	w.Write(data)
}

// pluginStore returns the plugin's storage folder: projects/<slug>/plugins/<name>/ with ?project=, else
// plugins/<name>/ at the board root. The plugin must have the storage permission.
func (s *Server) pluginStore(r *http.Request) (dir, slug, name string, err error) {
	name = r.PathValue("name")
	p, err := s.plugin(name)
	if err != nil {
		return "", "", "", err
	}
	if !p.Has(plugins.PermStorage) {
		return "", "", "", apiError{http.StatusForbidden, "plugin " + name + " has no storage permission"}
	}
	if proj := r.URL.Query().Get("project"); proj != "" {
		pr, err := s.projectArg(proj)
		if err != nil {
			return "", "", "", err
		}
		return filepath.Join(pr.Dir(), "plugins", name), pr.Slug(), name, nil
	}
	return filepath.Join(s.Board.Root(), "plugins", name), "", name, nil
}

func (s *Server) pluginKey(r *http.Request) (path, slug, name, key string, err error) {
	dir, slug, name, err := s.pluginStore(r)
	if err != nil {
		return "", "", "", "", err
	}
	key = r.PathValue("key")
	if !plugins.ValidKey(key) {
		return "", "", "", "", badRequest("key %q: use a relative path of [a-z0-9._-] segments", key)
	}
	return filepath.Join(dir, filepath.FromSlash(key)), slug, name, key, nil
}

// apiPluginList: ?prefix= keys under the plugin's folder, sorted.
func (s *Server) apiPluginList(r *http.Request) (any, error) {
	dir, _, _, err := s.pluginStore(r)
	if err != nil {
		return nil, err
	}
	prefix := r.URL.Query().Get("prefix")
	keys := []string{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if k := filepath.ToSlash(rel); plugins.ValidKey(k) && strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

// apiPluginGet returns the stored JSON value; a missing key is null, not an error.
func (s *Server) apiPluginGet(w http.ResponseWriter, r *http.Request) {
	p, _, _, _, err := s.pluginKey(r)
	if err != nil {
		s.api(func(*http.Request) (any, error) { return nil, err })(w, r)
		return
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		data, err = []byte("null"), nil
	}
	if err == nil && !json.Valid(data) {
		// An image stored with putBlob (or a hand-edited file): not a value to hand over as JSON.
		err = apiError{http.StatusUnsupportedMediaType, "key " + r.PathValue("key") + " does not hold a JSON value (an image is read with blobURL)"}
	}
	if err != nil {
		s.api(func(*http.Request) (any, error) { return nil, err })(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *Server) apiPluginPut(r *http.Request) (any, error) {
	p, slug, name, key, err := s.pluginKey(r)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxPluginValue+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPluginValue {
		return nil, apiError{http.StatusRequestEntityTooLarge, "a stored value is at most 1 MB"}
	}
	if !json.Valid(data) {
		return nil, badRequest("the value is not JSON")
	}
	return nil, s.writePluginKey(p, slug, name, key, data)
}

// apiPluginBlobPut stores an image (api.storage.putBlob) under a key of the same key space as values.
func (s *Server) apiPluginBlobPut(r *http.Request) (any, error) {
	p, slug, name, key, err := s.pluginKey(r)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxPluginBlob+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPluginBlob {
		return nil, apiError{http.StatusRequestEntityTooLarge, "an image is at most 10 MB"}
	}
	if ct := http.DetectContentType(data); !blobTypes[ct] {
		return nil, badRequest("not a PNG, JPEG, GIF or WebP image (%s)", ct)
	}
	return nil, s.writePluginKey(p, slug, name, key, data)
}

// apiPluginBlobGet returns a stored image with its sniffed type; anything else is not served as a blob.
func (s *Server) apiPluginBlobGet(w http.ResponseWriter, r *http.Request) {
	fail := func(err error) { s.api(func(*http.Request) (any, error) { return nil, err })(w, r) }
	p, _, _, _, err := s.pluginKey(r)
	if err != nil {
		fail(err)
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		fail(err)
		return
	}
	ct := http.DetectContentType(data)
	if !blobTypes[ct] {
		fail(apiError{http.StatusUnsupportedMediaType, "key does not hold an image"})
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Write(data)
}

// writePluginKey writes data to the file p of key, atomically, and tells the UI.
func (s *Server) writePluginKey(p, slug, name, key string, data []byte) error {
	dir := strings.TrimSuffix(p, filepath.FromSlash(key))
	// A key is a file or a folder, not both: "a" holding a value blocks "a/b", and the other way round.
	for k := path.Dir(key); k != "."; k = path.Dir(k) {
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(k))); err == nil && !fi.IsDir() {
			return apiError{http.StatusConflict, "key " + key + ": key " + k + " already holds a value"}
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return apiError{http.StatusConflict, "key " + key + ": keys below it hold values"}
	}
	if err := board.WriteFileAtomic(p, data); err != nil {
		return err
	}
	s.hub.publish(Event{Kind: "storage", Project: slug, ID: name, Key: key})
	return nil
}

func (s *Server) apiPluginDelete(r *http.Request) (any, error) {
	p, slug, name, key, err := s.pluginKey(r)
	if err != nil {
		return nil, err
	}
	if err := board.RemoveFile(p); err != nil {
		return nil, err
	}
	s.hub.publish(Event{Kind: "storage", Project: slug, ID: name, Key: key})
	return nil, nil
}
