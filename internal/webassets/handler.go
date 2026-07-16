// Package webassets serves a built SPA without allowing it to intercept API
// routes. It accepts a directory so the Go backend stays buildable before the
// frontend build is introduced by the web task.
package webassets

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Handler serves static files and falls back to index.html for client-side
// routes. A nil filesystem is an explicit unavailable state, not a fake page.
type Handler struct {
	files fs.FS
}

// NewDir creates a handler backed by a build directory. An empty directory is
// accepted and will return 404 until frontend assets are supplied.
func NewDir(root string) (*Handler, error) {
	if root == "" {
		return &Handler{}, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		return &Handler{}, err
	}
	if !info.IsDir() {
		return &Handler{}, fmt.Errorf("web assets root is not a directory")
	}
	return &Handler{files: os.DirFS(root)}, nil
}

// NewFS creates a handler from a test or embedded filesystem.
func NewFS(files fs.FS) *Handler { return &Handler{files: files} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.files == nil {
		http.NotFound(w, r)
		return
	}
	path := filepath.ToSlash(r.URL.Path)
	raw := strings.TrimPrefix(path, "/")
	if raw != "" && !fs.ValidPath(raw) {
		http.NotFound(w, r)
		return
	}
	if path == "/" {
		path = "/index.html"
	}
	if serveFile(h.files, w, r, path) {
		return
	}
	if r.Method == http.MethodGet && filepath.Ext(path) == "" && serveFile(h.files, w, r, "/index.html") {
		return
	}
	http.NotFound(w, r)
}

func serveFile(files fs.FS, w http.ResponseWriter, r *http.Request, path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean[0] == '/' {
		clean = clean[1:]
	}
	if clean == "." || !fs.ValidPath(clean) {
		return false
	}
	info, err := fs.Stat(files, clean)
	if err != nil || info.IsDir() {
		return false
	}
	file, err := files.Open(clean)
	if err != nil {
		return false
	}
	defer file.Close()
	if seeker, ok := file.(io.ReadSeeker); ok {
		http.ServeContent(w, r, filepath.Base(clean), info.ModTime(), seeker)
		return true
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return false
	}
	http.ServeContent(w, r, filepath.Base(clean), info.ModTime(), bytes.NewReader(data))
	return true
}
