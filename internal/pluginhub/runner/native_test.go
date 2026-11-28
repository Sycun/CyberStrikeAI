package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberstrike-ai/internal/pluginhub/manifest"

	"github.com/google/uuid"
)

type archiveDownloader []byte

func (d archiveDownloader) Download(_ context.Context, _ string, output io.Writer) (string, error) {
	_, err := output.Write(d)
	return manifest.Digest(d), err
}

func TestPluginNativeFixtureWeb(t *testing.T) {
	port := os.Getenv("FIXTURE_PORT")
	if port == "" {
		return
	}
	if secret := os.Getenv("FIXTURE_SECRET"); secret != "" {
		fmt.Fprintln(os.Stdout, "fixture output", secret)
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if nonce := os.Getenv("CSAI_PLUGIN_READY_TOKEN"); nonce != "" {
			w.Header().Set("X-CSAI-Plugin-Ready", nonce)
		}
		_, _ = io.WriteString(w, "ready")
	})
	if err := http.ListenAndServe(os.Getenv("FIXTURE_HOST")+":"+port, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPluginNativeReleaseAndPortLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture archives the parent test executable; Windows job behavior has separate platform tests")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	archive := fixtureArchive(t, "fixture-web", binary)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n := NewNative("release", archiveDownloader(archive), "direct", "")
	declaration := &manifest.Declaration{
		Distribution: []manifest.Distribution{{Type: "release", Assets: []manifest.Asset{
			{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "https://example.com/web.tar.gz", Format: "tar_gz"},
		}}},
		Launch: &manifest.Launch{Entrypoint: "fixture-web", Args: []string{"-test.run=^TestPluginNativeFixtureWeb$"},
			Environment: manifest.Environment{Env: map[string]string{"FIXTURE_HOST": "listen_host", "FIXTURE_PORT": "listen_port"},
				SecretEnv: map[string]manifest.SecretUse{"FIXTURE_SECRET": {Kind: "value", SecretRef: "fixture-secret"}}}},
		Web:     manifest.Web{HealthPath: "/health"},
		Secrets: map[string]manifest.Secret{"fixture-secret": {Generator: "random_hex", Bytes: 20}},
	}
	var calls sync.Mutex
	var lines []string
	spec := Spec{InstallID: uuid.NewString(), VersionID: uuid.NewString(), Root: t.TempDir(), Declaration: declaration,
		Plan: &manifest.InstallPlan{Isolation: "standard", Platform: manifest.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
			Artifact: manifest.Artifact{Driver: "release", AssetURL: "https://example.com/web.tar.gz", SHA256: manifest.Digest(archive)}},
		Log: func(line string) { calls.Lock(); defer calls.Unlock(); lines = append(lines, line) }}
	spec.Plan.Artifact.SHA256 = strings.Repeat("0", 64)
	if err := n.Prepare(ctx, spec); err == nil {
		t.Fatal("preparation accepted archive with mismatched SHA256")
	}
	spec.Plan.Artifact.SHA256 = manifest.Digest(archive)
	if err := n.Prepare(ctx, spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Stop(context.Background(), spec) })
	state, err := n.Start(ctx, spec)
	if err != nil || state.Port <= 0 || !state.Running {
		t.Fatalf("start: %+v %v", state, err)
	}
	if state.readyNonce != "" {
		t.Fatal("default readiness generated a custom header token")
	}
	if err := state.CheckHealth(ctx, declaration.Web); err != nil {
		t.Fatal(err)
	}
	if err := n.Stop(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := state.CheckHealth(ctx, declaration.Web); err == nil {
		t.Fatal("native listener retained after stop")
	}
	declaration.Web.ReadyHeaderCheck = true
	withHeader, err := n.Start(ctx, spec)
	if err != nil || withHeader.readyNonce == "" {
		t.Fatalf("opt-in native header check did not activate: %v", err)
	}
	if err := withHeader.CheckHealth(ctx, declaration.Web); err != nil {
		t.Fatal(err)
	}
	sanitized := func() string { calls.Lock(); defer calls.Unlock(); return strings.Join(lines, "\n") }()
	if !strings.Contains(sanitized, "fixture output [redacted]") {
		t.Fatalf("secret line was not redacted: %s", sanitized)
	}
	if err := n.Remove(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spec.VersionDir()); !os.IsNotExist(err) {
		t.Fatalf("artifact retained: %v", err)
	}
}

func TestPluginHealthRedirectAndHeaderCheck(t *testing.T) {
	var redirected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("X-CSAI-Plugin-Ready", "expected")
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/unavailable":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			redirected.Store(true)
		}
	}))
	defer server.Close()

	state := State{Running: true, Endpoint: server.URL, readyNonce: "expected"}
	web := manifest.Web{HealthPath: "/health", ReadyHeaderCheck: true}
	if err := state.CheckHealth(context.Background(), web); err == nil {
		t.Fatal("default readiness accepted a login redirect")
	}
	web.AllowHealthRedirect = true
	if err := state.CheckHealth(context.Background(), web); err != nil {
		t.Fatal(err)
	}
	if redirected.Load() {
		t.Fatal("readiness followed a login redirect")
	}
	state.readyNonce = "incorrect"
	if err := state.CheckHealth(context.Background(), web); err == nil {
		t.Fatal("readiness accepted a wrong header")
	}
	state.readyNonce = ""
	web.HealthPath = "/unavailable"
	if err := state.CheckHealth(context.Background(), web); err == nil {
		t.Fatal("redirect allowance accepted a server error")
	}
}

func TestPluginGitFixtureBuildsPinnedSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mock of the Git transport")
	}
	bin := t.TempDir()
	commit := strings.Repeat("b", 40)
	script := "#!/bin/sh\nset -eu\ncase \"$*\" in\n*" + "' init '*" + ") mkdir -p .git ;;\n" +
		"*' fetch '* ) printf '" + commit + "\\tfixture\\n' > .git/FETCH_HEAD ;;\n" +
		"*' checkout '* ) printf 'module example.org/demo\\n\\ngo 1.25.0\\n' > go.mod\n" +
		"printf 'package main\\nfunc main() {}\\n' > main.go ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	n := NewNative("git", nil, "direct", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	spec := Spec{InstallID: uuid.NewString(), VersionID: uuid.NewString(), Root: t.TempDir(),
		Declaration: &manifest.Declaration{Launch: &manifest.Launch{Entrypoint: "fixture-web"},
			Distribution: []manifest.Distribution{{Type: "git", Build: []manifest.BuildStep{{Program: "go", Args: []string{"build", "-buildvcs=false", "-o", "fixture-web", "."}}}}}},
		Plan: &manifest.InstallPlan{Isolation: "standard", Platform: manifest.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
			Artifact: manifest.Artifact{Driver: "git", Repository: "https://github.com/example/passive-web", Commit: commit}}, Log: func(s string) { t.Log(s) }}
	if err := n.Prepare(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(spec.VersionDir(), "fixture-web")); err != nil {
		t.Fatal(err)
	}
	if n.Probe(ctx, "strict").Available {
		t.Fatal("native driver accepted strict isolation")
	}
}

func TestPluginArchiveRejectsTraversal(t *testing.T) {
	archive := t.TempDir() + "/invalid.tar.gz"
	if err := os.WriteFile(archive, fixtureArchive(t, "../outside", []byte("sample")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := extract(context.Background(), archive, t.TempDir(), "tar_gz", 0); err == nil {
		t.Fatal("archive traversal accepted")
	}
}

func fixtureArchive(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	g := gzip.NewWriter(&archive)
	writer := tar.NewWriter(g)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0700, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
