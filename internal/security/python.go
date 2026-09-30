package security

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// envPython 是运维者显式指定解释器的入口，优先级最高。
const envPython = "CYBERSTRIKE_PYTHON"

// ResolvePythonCommand 把配方里裸写的 python/python3 绑定到项目虚拟环境的解释器，
// 这样不经 run.sh 的 `source venv/bin/activate` 直接跑二进制时，Python 工具仍然
// 用的是装好依赖的 venv，而不是静默回落到系统 python3。
// 非解释器命令、以及已经写了路径的命令都原样返回。
func ResolvePythonCommand(command string) string {
	name := strings.TrimSpace(command)
	// filepath.Base 不相等说明命令已带目录，属于调用方的明确决定，不改写。
	if name == "" || filepath.Base(name) != name || !isPythonLookup(name) {
		return command
	}
	for _, candidate := range pythonInterpreterCandidates(name) {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return command
}

func isPythonLookup(name string) bool {
	name = strings.TrimSuffix(name, ".exe")
	if name == "python" {
		return true
	}
	rest, ok := strings.CutPrefix(name, "python")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

func pythonInterpreterCandidates(want string) []string {
	if override := strings.TrimSpace(os.Getenv(envPython)); override != "" {
		return []string{override}
	}

	dir, names := "bin", []string{want}
	for _, fallback := range []string{"python3", "python"} {
		if fallback != want {
			names = append(names, fallback)
		}
	}
	if runtime.GOOS == "windows" {
		dir, names = "Scripts", []string{"python.exe", "python3.exe"}
	}

	envs := make([]string, 0, 3)
	if venv := strings.TrimSpace(os.Getenv("VIRTUAL_ENV")); venv != "" {
		envs = append(envs, venv)
	}
	for _, root := range projectRoots() {
		envs = append(envs, filepath.Join(root, "venv"))
	}

	candidates := make([]string, 0, len(envs)*len(names))
	for _, env := range envs {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(env, dir, name))
		}
	}
	return candidates
}

// projectRoots 覆盖两种部署形态：二进制与 venv 同目录（run.sh 的产物），
// 以及从仓库根目录 `go run`。
func projectRoots() []string {
	roots := make([]string, 0, 2)
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, wd)
	}
	return roots
}
