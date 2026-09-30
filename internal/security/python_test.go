package security

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"

	"go.uber.org/zap"
)

// isolatePythonEnv 让解析结果不受开发者机器上已激活的 venv 影响。
func isolatePythonEnv(t *testing.T) {
	t.Helper()
	t.Setenv("VIRTUAL_ENV", "")
	t.Setenv(envPython, "")
}

func TestResolvePythonCommandBindsProjectVenv(t *testing.T) {
	isolatePythonEnv(t)
	root := t.TempDir()
	bin := venvBinDir(t, root, "python3", "python")
	t.Chdir(root)

	if got := ResolvePythonCommand("python3"); got != filepath.Join(bin, "python3") {
		t.Errorf("python3 -> %q, want %q", got, filepath.Join(bin, "python3"))
	}
	if got := ResolvePythonCommand("python"); got != filepath.Join(bin, "python") {
		t.Errorf("python -> %q, want %q", got, filepath.Join(bin, "python"))
	}
	if got := ResolvePythonCommand("python3.11"); got != filepath.Join(bin, "python3") {
		t.Errorf("python3.11 -> %q, want the venv python3", got)
	}
}

func TestResolvePythonCommandUsesVirtualEnvWhenCwdIsElsewhere(t *testing.T) {
	isolatePythonEnv(t)
	root := t.TempDir()
	bin := venvBinDir(t, root, "python3")
	t.Setenv("VIRTUAL_ENV", filepath.Join(root, "venv"))
	t.Chdir(t.TempDir())

	if got := ResolvePythonCommand("python3"); got != filepath.Join(bin, "python3") {
		t.Fatalf("python3 -> %q, want %q", got, filepath.Join(bin, "python3"))
	}
}

func TestResolvePythonCommandEnvOverrideWins(t *testing.T) {
	isolatePythonEnv(t)
	root := t.TempDir()
	explicit := filepath.Join(root, "custom-python3")
	if err := os.WriteFile(explicit, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPython, explicit)
	venvBinDir(t, root, "python3")
	t.Chdir(root)

	if got := ResolvePythonCommand("python3"); got != explicit {
		t.Fatalf("python3 -> %q, want the %s override %q", got, envPython, explicit)
	}
}

func TestResolvePythonCommandLeavesEverythingElseAlone(t *testing.T) {
	isolatePythonEnv(t)
	root := t.TempDir()
	venvBinDir(t, root, "python3")
	t.Chdir(root)

	for _, command := range []string{
		"nmap",
		"python3-config",
		"pythonwrapper",
		"/usr/bin/python3", // 写明的路径是调用方的决定，不改写
		"/bin/bash",
		"",
	} {
		if got := ResolvePythonCommand(command); got != command {
			t.Errorf("ResolvePythonCommand(%q) = %q, want it unchanged", command, got)
		}
	}
}

func TestResolvePythonCommandWithoutVenvStaysOnPATH(t *testing.T) {
	isolatePythonEnv(t)
	t.Chdir(t.TempDir())
	if got := ResolvePythonCommand("python3"); got != "python3" {
		t.Fatalf("no venv should keep the bare command, got %q", got)
	}
}

// TestExecuteToolRunsPythonRecipeInVenv 覆盖 S5 的后半段：配方仍然只写 `python3`，
// 执行时必须落到 venv 解释器而不是系统 python3。
func TestExecuteToolRunsPythonRecipeInVenv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture interpreter is a POSIX shell script")
	}
	isolatePythonEnv(t)

	root := t.TempDir()
	bin := venvBinDir(t, root)
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte("#!/bin/sh\necho CSAI_VENV_INTERPRETER\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	logger := zap.NewNop()
	executor := NewExecutor(&config.SecurityConfig{
		Tools: []config.ToolConfig{{
			Name:        "pwntools",
			Command:     "python3",
			Description: "fixture recipe",
			Enabled:     true,
			Args:        []string{"-c", "print(1)"},
		}},
	}, mcp.NewServer(logger), logger)

	result, err := executor.ExecuteTool(context.Background(), "pwntools", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool reported error: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "CSAI_VENV_INTERPRETER") {
		t.Fatalf("recipe did not run under the venv interpreter: %q", result.Content[0].Text)
	}
}

func venvBinDir(t *testing.T, root string, names ...string) string {
	t.Helper()
	dir := filepath.Join(root, "venv", "bin")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(root, "venv", "Scripts")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
