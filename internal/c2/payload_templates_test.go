package c2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

var embeddedPayloadTemplateNames = []string{
	"beacon.go.tmpl",
	"proc_hide_unix.go.tmpl",
	"proc_hide_windows.go.tmpl",
}

// TestPayloadTemplatesResolveWithoutCWD 是 S5 的回归哨兵：出桩不得依赖启动目录。
func TestPayloadTemplatesResolveWithoutCWD(t *testing.T) {
	t.Chdir(t.TempDir())

	b := NewPayloadBuilder(nil, zap.NewNop(), "", t.TempDir())
	if b.tmplDir != "" {
		t.Fatalf("default tmplDir should stay empty to use the embedded copy, got %q", b.tmplDir)
	}
	if _, err := os.Stat("internal/c2/payload_templates"); err == nil {
		t.Fatal("chdir did not move out of the source tree")
	}

	for _, name := range embeddedPayloadTemplateNames {
		data, err := b.readTemplate(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Errorf("embedded %s is empty", name)
		}
	}

	beacon, err := b.readTemplate("beacon.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(beacon), "package main") || !strings.Contains(string(beacon), "{{.ServerURL}}") {
		t.Error("embedded beacon template is not the parameterised Go source")
	}
}

func TestPayloadBuilderExplicitTemplateDirWins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "beacon.go.tmpl"), []byte("disk marker"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewPayloadBuilder(nil, zap.NewNop(), dir, t.TempDir())

	data, err := b.readTemplate("beacon.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "disk marker" {
		t.Fatalf("explicit tmplDir ignored, got %q", data)
	}
}
