package runner

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"cyberstrike-ai/internal/pluginhub/manifest"
)

type runtimeValues struct {
	host, dataDir, fileDir string
	port                   int
	container              bool
}

func environment(spec Spec, declared manifest.Environment, values runtimeValues, services map[string]manifest.Service) (map[string]string, []string, error) {
	env := map[string]string{}
	secrets := []string{}
	for name, value := range declared.StaticEnv {
		env[name] = value
	}
	for name, key := range declared.Env {
		switch key {
		case "listen_host":
			env[name] = values.host
		case "listen_port":
			env[name] = strconv.Itoa(values.port)
		case "data_dir":
			env[name] = values.dataDir
		case "web_base_path":
			env[name] = spec.WebBasePath()
		}
	}
	for name, ref := range declared.SecretEnv {
		value, err := instanceSecret(spec, ref.SecretRef)
		if err != nil {
			return nil, nil, err
		}
		secrets = append(secrets, value)
		switch ref.Kind {
		case "file":
			if values.container {
				folder := filepath.Join(spec.VersionDir(), "secret-mounts")
				if err := os.MkdirAll(folder, 0700); err != nil {
					return nil, nil, err
				}
				if err := os.WriteFile(filepath.Join(folder, ref.SecretRef), []byte(value), 0444); err != nil && !sameSecret(filepath.Join(folder, ref.SecretRef), value) {
					return nil, nil, err
				}
			}
			env[name] = filepath.ToSlash(filepath.Join(values.fileDir, ref.SecretRef))
		case "connection_uri":
			service, exists := services[ref.Service]
			if !exists || service.ContainerPort < 1 {
				return nil, nil, fmt.Errorf("Secret URI has no locked dependency service")
			}
			uri := url.URL{Scheme: ref.Scheme, User: url.UserPassword(ref.Username, value),
				Host: net.JoinHostPort(ref.Service, strconv.Itoa(service.ContainerPort))}
			if ref.Database != "" {
				uri.Path = "/" + ref.Database
			}
			options := url.Values{}
			for key, value := range ref.Options {
				options.Set(key, value)
			}
			uri.RawQuery = options.Encode()
			env[name] = uri.String()
			secrets = append(secrets, env[name])
		default:
			env[name] = value
		}
	}
	return env, secrets, nil
}

func sameSecret(path, expected string) bool {
	data, err := os.ReadFile(path)
	return err == nil && string(data) == expected
}

func instanceSecret(spec Spec, name string) (string, error) {
	definition, exists := spec.Declaration.Secrets[name]
	if !exists || definition.Bytes < 16 || definition.Bytes > 128 {
		return "", fmt.Errorf("unavailable Secret reference")
	}
	dir := filepath.Join(spec.Root, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	data, err := root.ReadFile(name)
	if err == nil && len(data) > 0 && len(data) <= 256 {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("instance Secret unavailable")
	}
	random := make([]byte, definition.Bytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(random)
	if definition.Generator == "random_hex" {
		value = hex.EncodeToString(random)
	}
	tmp, err := root.OpenFile(name+".new", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := io.WriteString(tmp, value)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		_ = root.Remove(name + ".new")
		return "", fmt.Errorf("could not store instance Secret")
	}
	return value, root.Rename(name+".new", name)
}

func cleanEnv(root string) ([]string, error) {
	temp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(temp, 0700); err != nil {
		return nil, err
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "USERPROFILE=" + root,
		"TMPDIR=" + temp, "TMP=" + temp, "TEMP=" + temp, "GOTOOLCHAIN=local", "GOWORK=off",
		"GOCACHE=" + filepath.Join(root, "go-cache"), "GOMODCACHE=" + filepath.Join(root, "go-mod")}
	for _, key := range []string{"SystemRoot", "WINDIR", "PATHEXT", "LANG"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env, nil
}

func withEnv(env []string, values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	for _, name := range keys {
		env = append(env, name+"="+values[name])
	}
	return env
}

var sensitiveLog = regexp.MustCompile(`(?i)(bearer[[:space:]]+|[a-z0-9_-]*(?:authorization|api[_-]?key|token|password|secret|cookie)["']?[[:space:]]*[:=][[:space:]]*["']?(?:bearer[[:space:]]+)?)[^[:space:],;"']+`)
var uriCredentials = regexp.MustCompile(`(://)[^[:space:]/@]+:[^[:space:]/@]+@`)

func SanitizeLog(line string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			line = strings.ReplaceAll(line, secret, "[redacted]")
			line = strings.ReplaceAll(line, url.QueryEscape(secret), "[redacted]")
		}
	}
	line = sensitiveLog.ReplaceAllString(line, "${1}[redacted]")
	line = uriCredentials.ReplaceAllString(line, "${1}[redacted]@")
	line = strings.ToValidUTF8(line, "")
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\t' || r == 127 {
			return -1
		}
		return r
	}, line)
}

type lineLogger struct {
	mu      sync.Mutex
	emit    func(string)
	secrets []string
	line    []byte
	full    bool
}

func (l *lineLogger) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range b {
		if c == '\n' {
			l.flush()
		} else if len(l.line) < 8192 {
			l.line = append(l.line, c)
		} else {
			l.full = true
		}
	}
	return len(b), nil
}

func (l *lineLogger) flush() {
	if l.emit != nil && l.full {
		l.emit("[oversize output omitted]")
	} else if l.emit != nil && len(l.line) != 0 {
		l.emit(SanitizeLog(string(l.line), l.secrets))
	}
	l.line = l.line[:0]
	l.full = false
}

func scanOutput(stream io.Reader, logger *lineLogger) {
	reader := bufio.NewReaderSize(stream, 8192)
	_, _ = io.Copy(logger, reader)
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.flush()
}
