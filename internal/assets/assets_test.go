package assets

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"cyberstrike-ai/web"
)

// TestEmbedCapturesWholeWebTree 是对 embed 模式写错（比如漏掉一个子树）的唯一
// 可靠防线：逐文件比对磁盘与内嵌副本。
func TestEmbedCapturesWholeWebTree(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "web"))
	if err != nil {
		t.Fatal(err)
	}
	disk := map[string]int64{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(path, ".go") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		disk[filepath.ToSlash(rel)] = info.Size()
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	var total int64
	embedded := map[string]int64{}
	if err := fs.WalkDir(web.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			t.Errorf("embedded asset %s is empty", path)
		}
		embedded[path] = info.Size()
		total += info.Size()
		return nil
	}); err != nil {
		t.Fatalf("walk embedded fs: %v", err)
	}

	for name, size := range disk {
		if got, ok := embedded[name]; !ok {
			t.Errorf("%s exists on disk but is not embedded", name)
		} else if got != size {
			t.Errorf("%s embedded as %d bytes, disk has %d", name, got, size)
		}
	}
	if len(embedded) != len(disk) {
		t.Errorf("embedded %d files, disk has %d", len(embedded), len(disk))
	}
	// index.html + style.css + logo.png alone are ~2MB; a much smaller total means
	// a pattern silently dropped part of the tree.
	if total < 2<<20 {
		t.Errorf("embedded payload only %d bytes, expected the whole web bundle", total)
	}
}

func TestTemplatesKeepLoadHTMLGlobNames(t *testing.T) {
	tmpl, err := Templates("")
	if err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}
	for _, name := range []string{"index.html", "api-docs.html"} {
		if tmpl.Lookup(name) == nil {
			t.Errorf("template %q not registered under its base filename", name)
		}
	}

	buf := new(bytes.Buffer)
	if err := tmpl.ExecuteTemplate(buf, "index.html", struct{ Version string }{"v-test-9f31"}); err != nil {
		t.Fatalf("execute index.html: %v", err)
	}
	if !strings.Contains(buf.String(), "v-test-9f31") {
		t.Error("rendered index.html lost the injected version value")
	}
}

// TestGinServesEmbeddedAssetsWithoutCWD 复现 app.go 的接线并断言它不依赖启动目录。
func TestGinServesEmbeddedAssetsWithoutCWD(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	t.Chdir(t.TempDir())
	if dir := ResolveWebDir(""); dir != "" {
		t.Fatalf("expected the embedded fallback, resolved %q instead", dir)
	}

	tmpl, err := Templates("")
	if err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}
	router := gin.New()
	router.SetHTMLTemplate(tmpl)
	router.StaticFS("/static", http.FS(StaticFS("")))
	router.GET("/", func(c *gin.Context) { c.HTML(http.StatusOK, "index.html", gin.H{"Version": "v-cwd-free"}) })
	router.GET("/api-docs", func(c *gin.Context) { c.HTML(http.StatusOK, "api-docs.html", nil) })

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	index := httpGet(t, srv.URL+"/")
	if index.code != http.StatusOK || !strings.Contains(index.body, "v-cwd-free") {
		t.Errorf("GET / -> %d, version present=%v", index.code, strings.Contains(index.body, "v-cwd-free"))
	}
	if doc := httpGet(t, srv.URL+"/api-docs"); doc.code != http.StatusOK || len(doc.body) == 0 {
		t.Errorf("GET /api-docs -> %d, %d bytes", doc.code, len(doc.body))
	}
	favicon := httpGet(t, srv.URL+"/static/favicon.ico")
	if favicon.code != http.StatusOK || len(favicon.body) == 0 {
		t.Errorf("GET /static/favicon.ico -> %d, %d bytes", favicon.code, len(favicon.body))
	}
	if miss := httpGet(t, srv.URL+"/static/nope/nope.js"); miss.code != http.StatusNotFound {
		t.Errorf("missing asset should 404, got %d", miss.code)
	}
}

func TestDiskWebDirWinsOverEmbed(t *testing.T) {
	root := t.TempDir()
	webDir := filepath.Join(root, "web")
	if err := os.MkdirAll(filepath.Join(webDir, "static", "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(webDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(webDir, "templates", "index.html"), "<p>disk-marker {{.Version}}</p>")
	writeFile(t, filepath.Join(webDir, "static", "css", "style.css"), "disk-css-marker")
	t.Chdir(root)

	if got := ResolveWebDir(""); got != webDir {
		t.Fatalf("ResolveWebDir() = %q, want %q", got, webDir)
	}

	tmpl, err := Templates(webDir)
	if err != nil {
		t.Fatalf("parse disk templates: %v", err)
	}
	buf := new(bytes.Buffer)
	if err := tmpl.ExecuteTemplate(buf, "index.html", struct{ Version string }{"v1"}); err != nil {
		t.Fatalf("execute disk index.html: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "<p>disk-marker") {
		t.Errorf("disk template not used, got %q", buf.String())
	}

	data, err := fs.ReadFile(StaticFS(webDir), "css/style.css")
	if err != nil {
		t.Fatalf("read disk static: %v", err)
	}
	if string(data) != "disk-css-marker" {
		t.Errorf("disk static not used, got %q", data)
	}
}

func TestResolveWebDirIgnoresPartialLayout(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web", "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if got := ResolveWebDir(""); got != "" {
		t.Fatalf("partial web/ should not win, got %q", got)
	}
	if got := ResolveWebDir(filepath.Join(root, "web")); got != "" {
		t.Fatalf("explicit partial path should be rejected, got %q", got)
	}
}

type httpResult struct {
	code int
	body string
}

func httpGet(t *testing.T, url string) httpResult {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body of %s: %v", url, err)
	}
	return httpResult{code: resp.StatusCode, body: buf.String()}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
