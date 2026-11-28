package runner

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/pluginhub/manifest"
)

func TestPluginModelTemplatesAndContainerPreflight(t *testing.T) {
	spec := Spec{Root: t.TempDir(), InstallID: "fixture", VersionID: "version",
		Plan: &manifest.InstallPlan{Platform: manifest.Platform{OS: "linux", Arch: "arm64"}},
		Model: &ModelConfig{GatewayRoot: "http://host.docker.internal:40331", DockerHostMapping: "host.docker.internal:host-gateway",
			Env: map[string]string{"OPENAI_API_KEY": "private-fixture-token"}, Templates: []ModelTemplate{
				{Manifest: manifest.ConfigTemplate{Format: "json", Path: "nested/provider.json"}, Values: map[string]string{"/provider/key": "private-fixture-token"}},
				{Manifest: manifest.ConfigTemplate{Format: "yaml", Path: "settings.yaml"}, Values: map[string]string{"/model": "fixture-model"}},
				{Manifest: manifest.ConfigTemplate{Format: "env", Path: "model.env"}, Values: map[string]string{"API_KEY": "private-fixture-token"}},
			}}}
	if err := os.MkdirAll(spec.VersionDir(), 0700); err != nil {
		t.Fatal(err)
	}
	env, sensitive, err := renderModelConfig(spec)
	if err != nil || len(sensitive) < 2 || env["OPENAI_API_KEY"] != "private-fixture-token" {
		t.Fatalf("model env: %v", err)
	}
	root := env["CSAI_PLUGIN_MODEL_CONFIG_DIR"]
	var config struct{ Provider struct{ Key string } }
	raw, err := os.ReadFile(filepath.Join(root, "nested", "provider.json"))
	if err != nil || json.Unmarshal(raw, &config) != nil || config.Provider.Key != "private-fixture-token" {
		t.Fatal("JSON template not populated")
	}
	info, err := os.Stat(filepath.Join(root, "model.env"))
	if err != nil || info.Mode().Perm()&0077 != 0 && os.PathSeparator == '/' {
		t.Fatal("model config is accessible outside the runner account")
	}
	for _, check := range []struct{ path, text string }{{"settings.yaml", "fixture-model"}, {"model.env", "API_KEY=private-fixture-token"}} {
		raw, err := os.ReadFile(filepath.Join(root, check.path))
		if err != nil || !strings.Contains(string(raw), check.text) {
			t.Fatalf("template %s: %v", check.path, err)
		}
	}
	d := NewDocker()
	checked := false
	d.run = func(_ context.Context, _ io.Writer, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--add-host host.docker.internal:host-gateway") ||
			!strings.Contains(joined, "/_csai_gateway/ready") ||
			strings.Contains(joined, "private-fixture-token") {
			t.Fatal("unsafe or incomplete private network preflight")
		}
		checked = true
		return nil, nil
	}
	if err := d.probeModel(context.Background(), spec, containerSpec{image: "fixture@sha256:locked"}, webNetwork(spec)); err != nil || !checked {
		t.Fatalf("container preflight: %v", err)
	}
}
