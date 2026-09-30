// Package assets locates the resources shipped with the binary. Everything is
// resolved disk-first so a checkout can be edited without rebuilding, with the
// embedded copy as the fallback that lets a bare binary run from any directory.
package assets

import (
	"html/template"
	"io/fs"
	"os"
	"path/filepath"

	"cyberstrike-ai/web"
)

// ResolveWebDir returns a directory holding static/ and templates/, or "" when
// only the embedded copy is available. explicit is the caller-supplied path;
// the two fallbacks are the layouts run.sh (binary at the repo root) and
// `go run` (binary in the build cache, CWD at the repo root) produce.
func ResolveWebDir(explicit string) string {
	candidates := make([]string, 0, 3)
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "web"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "web"))
	}
	for _, dir := range candidates {
		if hasWebLayout(dir) {
			return dir
		}
	}
	return ""
}

func hasWebLayout(dir string) bool {
	for _, sub := range []string{"static", "templates"} {
		if info, err := os.Stat(filepath.Join(dir, sub)); err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

// StaticFS returns the filesystem rooted at the static asset directory, ready
// to hand to http.FS.
func StaticFS(webDir string) fs.FS {
	if webDir == "" {
		sub, _ := fs.Sub(web.FS, "static")
		return sub
	}
	return os.DirFS(filepath.Join(webDir, "static"))
}

// Templates parses the HTML templates keeping every entry named by its base
// filename — the naming gin's LoadHTMLGlob produced, which is what the
// c.HTML("index.html", ...) call sites look up.
// It must stay html/template: gin's renderer only accepts *template.Template
// from that package, and its auto-escaping is the behaviour in production today.
func Templates(webDir string) (*template.Template, error) {
	t := template.New("")
	if webDir == "" {
		return t.ParseFS(web.FS, "templates/*")
	}
	return t.ParseGlob(filepath.Join(webDir, "templates", "*"))
}
