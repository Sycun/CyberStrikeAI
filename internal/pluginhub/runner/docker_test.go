package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/pluginhub/manifest"

	"github.com/google/uuid"
)

func TestPluginDockerAndComposeFixtures(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///fixture.sock")
	for _, test := range []struct{ mode, isolation, version string }{
		{"docker", "standard", "24.0.9"}, {"compose", "standard", "27.5.1"},
		{"docker", "strict", "28.0.1"}, {"compose", "strict", "28.0.1"},
	} {
		t.Run(test.mode+"/"+test.isolation, func(t *testing.T) {
			mode := test.mode
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			defer server.Close()
			port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
			digest := "sha256:" + strings.Repeat("1", 64)
			web := manifest.Service{Name: "web", Role: "web", ContainerPort: 80, Resources: manifest.Resources{MemoryMiB: 128, PIDs: 64},
				Environment: manifest.Environment{StaticEnv: map[string]string{"GREETING": "hello $ demo"}}}
			items := []manifest.Service{web}
			locks := []manifest.LockedService{{Name: "web", Repository: "example.org/web", ManifestDigest: digest}}
			if mode == "compose" {
				web.SecretEnv = map[string]manifest.SecretUse{"DB_URI": {Kind: "connection_uri", SecretRef: "db-password", Scheme: "postgresql", Service: "db", Username: "app", Database: "demo"}}
				web.DependsOn = map[string]string{"db": "service_healthy"}
				items = []manifest.Service{web, {Name: "db", Role: "dependency", ContainerPort: 5432, Resources: manifest.Resources{MemoryMiB: 128, PIDs: 64},
					Environment: manifest.Environment{SecretEnv: map[string]manifest.SecretUse{"PASSWORD_FILE": {Kind: "file", SecretRef: "db-password"}}},
					Healthcheck: &manifest.Healthcheck{Kind: "tcp", TimeoutSeconds: 1, IntervalSeconds: 2, Retries: 2}}}
				locks = append(locks, manifest.LockedService{Name: "db", Repository: "example.org/db", ManifestDigest: digest})
			}
			spec := Spec{InstallID: uuid.NewString(), VersionID: uuid.NewString(), Root: t.TempDir(),
				Declaration: &manifest.Declaration{Distribution: []manifest.Distribution{{Type: "container", Mode: mode, Services: items}},
					Web: manifest.Web{HealthPath: "/"}, Secrets: map[string]manifest.Secret{"db-password": {Generator: "random_base64url", Bytes: 32}}},
				Plan: &manifest.InstallPlan{Platform: manifest.Platform{OS: "linux", Arch: "arm64"}, Isolation: test.isolation,
					Artifact: manifest.Artifact{Driver: "container", Services: locks}}}
			d := NewDocker()
			var mu sync.Mutex
			started, removed := false, 0
			networks := map[string]bool{}
			d.run = func(ctx context.Context, _ io.Writer, args ...string) ([]byte, error) {
				mu.Lock()
				defer mu.Unlock()
				switch args[0] {
				case "version":
					return []byte("linux/" + test.version), nil
				case "pull":
					return nil, nil
				case "image":
					return []byte("linux/arm64"), nil
				case "compose":
					if args[1] == "version" {
						return []byte("2.39.0"), nil
					}
					raw, err := os.ReadFile(composeFile(spec))
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(raw), "service_healthy") || !strings.Contains(string(raw), "postgresql://app:") ||
						!strings.Contains(string(raw), "@db:5432/demo") || !strings.Contains(string(raw), "/run/csai/secrets/db-password") ||
						!strings.Contains(string(raw), `"internal":true`) || !strings.Contains(string(raw), `"pids_limit":64`) ||
						!strings.Contains(string(raw), "hello $$ demo") {
						t.Fatal("normalized Compose fixture lacks isolation, dependencies or injected config")
					}
					var compose struct {
						Services map[string]struct {
							Networks map[string]json.RawMessage `json:"networks"`
							CapAdd   []string                   `json:"cap_add"`
							CapDrop  []string                   `json:"cap_drop"`
						}
						Networks map[string]struct {
							Name     string
							Driver   string
							Internal bool
						}
					}
					if err := json.Unmarshal(raw, &compose); err != nil {
						t.Fatal(err)
					}
					if len(compose.Services["web"].Networks) != 2 || compose.Services["web"].Networks["private"] == nil ||
						compose.Services["web"].Networks["web"] == nil || len(compose.Services["db"].Networks) != 1 ||
						compose.Services["db"].Networks["private"] == nil {
						t.Fatal("Compose exposed a dependency or omitted a WebUI attachment")
					}
					if len(compose.Networks) != 2 || !compose.Networks["private"].Internal || compose.Networks["web"].Internal {
						t.Fatal("Compose did not separate the private and published bridges")
					}
					if !slices.Equal(compose.Services["web"].CapAdd, initializationCaps(test.isolation, items[0])) ||
						!slices.Equal(compose.Services["db"].CapAdd, initializationCaps(test.isolation, items[1])) ||
						!slices.Equal(compose.Services["web"].CapDrop, []string{"ALL"}) {
						t.Fatal("Compose did not apply the expected capability profile")
					}
					started = true
					for _, network := range compose.Networks {
						if network.Driver != "bridge" {
							t.Fatal("Compose network is not a bridge")
						}
						networks[network.Name] = network.Internal
					}
					return nil, nil
				case "network":
					switch args[1] {
					case "ls":
						name := strings.TrimSuffix(strings.TrimPrefix(args[3], "name=^"), "$")
						if _, found := networks[name]; found {
							return []byte(name), nil
						}
						return nil, nil
					case "create":
						networks[args[len(args)-1]] = slices.Contains(args, "--internal")
						return nil, nil
					case "inspect":
						name := args[2]
						return json.Marshal([]map[string]any{{"Name": name, "Labels": operatorLabels(spec), "Internal": networks[name]}})
					case "rm":
						delete(networks, args[2])
						return nil, nil
					}
				case "create":
					if !strings.Contains(strings.Join(args, " "), "127.0.0.1::80") || !strings.Contains(strings.Join(args, " "), "--pids-limit 64") {
						t.Fatal("Docker publishing or limits missing")
					}
					internal, found := networks[webNetwork(spec).name]
					if !found || internal {
						t.Fatal("Single container is missing a published bridge")
					}
					var added []string
					for i, arg := range args[:len(args)-1] {
						if arg == "--cap-add" {
							added = append(added, args[i+1])
						}
					}
					if !slices.Equal(added, initializationCaps(test.isolation, web)) {
						t.Fatal("Docker did not apply the expected capability profile")
					}
					return nil, nil
				case "start":
					started = true
					return nil, nil
				case "ps":
					if started {
						return []byte("control-plane-owned"), nil
					}
					return nil, nil
				case "inspect":
					return []byte(fmt.Sprintf(`[{"Id":"owned","Config":{"Labels":{%q:%q,%q:%q}},"State":{"Running":true},"HostConfig":{"Memory":%d,"PidsLimit":64},"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":%q}]}}}]`, installLabel, spec.InstallID, versionLabel, spec.VersionID, 128<<20, port)), nil
				case "logs":
					return nil, nil
				case "rm":
					removed++
					if removed == len(items) {
						started = false
					}
					return nil, nil
				}
				return nil, fmt.Errorf("unexpected command")
			}
			if err := d.Prepare(ctx, spec); err != nil {
				t.Fatal(err)
			}
			state, err := d.Start(ctx, spec)
			p, _ := strconv.Atoi(port)
			if err != nil || state.Port != p || state.Endpoint != server.URL {
				t.Fatalf("start: %+v %v", state, err)
			}
			if mode == "compose" {
				data, err := os.ReadFile(filepath.Join(spec.VersionDir(), "secret-mounts", "db-password"))
				if err != nil || len(data) < 32 {
					t.Fatal("Compose secret file missing")
				}
				again, err := instanceSecret(spec, "db-password")
				if err != nil || string(data) != again {
					t.Fatal("cross-service secret was not shared")
				}
			}
			if err := d.Remove(ctx, spec); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			if removed != len(items) || len(networks) != 0 {
				t.Fatal("owned Docker resources survived removal")
			}
			mu.Unlock()
			if _, err := os.Stat(spec.VersionDir()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("staged Docker directory survived removal")
			}
		})
	}
}

func TestPluginDockerMissingCLIReportsGuidance(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///fixture.sock")
	d := NewDocker()
	d.run = func(context.Context, io.Writer, ...string) ([]byte, error) { return nil, exec.ErrNotFound }
	if probe := d.Probe(context.Background(), "strict"); probe.Available || probe.Guidance == "" {
		t.Fatalf("probe %+v", probe)
	}
	spec := Spec{
		InstallID: uuid.NewString(),
		VersionID: uuid.NewString(),
		Root:      t.TempDir(),
		Declaration: &manifest.Declaration{
			Distribution: []manifest.Distribution{{
				Type:     "container",
				Mode:     "docker",
				Services: []manifest.Service{{Name: "web", Role: "web", ContainerPort: 80}},
			}},
		},
		Plan: &manifest.InstallPlan{Artifact: manifest.Artifact{Driver: "container"}},
	}
	if err := d.Remove(context.Background(), spec); err != nil {
		t.Fatalf("expected uninstall of never-prepared container plugin to succeed without docker: %v", err)
	}
}

func TestPluginRuntimeLogRedactsBearerJSONAndDSN(t *testing.T) {
	input := "authorization: Bearer fixture-secret-1; \"db_password\":\"fixture-secret-2\" postgresql://user:fixture-secret-3@db:5432/test"
	if output := SanitizeLog(input, nil); strings.Contains(output, "fixture-secret") || strings.Count(output, "[redacted]") != 3 {
		t.Fatalf("sensitive fields preserved: %s", output)
	}
}

func TestPluginDockerVersionAndInitializationCapabilities(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///fixture.sock")
	for _, test := range []struct {
		version, isolation  string
		available, advisory bool
	}{
		{"23.0.6", "standard", false, false},
		{"24.0.9", "standard", true, true},
		{"27.5.1", "standard", true, true},
		{"27.5.1", "strict", false, false},
		{"28.0.1", "strict", true, false},
	} {
		d := NewDocker()
		d.run = func(context.Context, io.Writer, ...string) ([]byte, error) {
			return []byte("linux/" + test.version), nil
		}
		for _, probe := range []Probe{d.Probe(context.Background(), test.isolation), d.ProbeCompose(context.Background(), test.isolation)} {
			if probe.Available != test.available || (probe.Available && (probe.Guidance != "") != test.advisory) {
				t.Fatalf("unexpected probe for %s/%s: %+v", test.version, test.isolation, probe)
			}
		}
	}
	dependency := manifest.Service{Role: "dependency", ContainerPort: 5432}
	web := manifest.Service{Role: "web", ContainerPort: 80}
	common := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"}
	if got := initializationCaps("standard", dependency); !slices.Equal(got, common) {
		t.Fatalf("unexpected database capabilities: %v", got)
	}
	if got := initializationCaps("standard", web); !slices.Equal(got, append(common, "KILL", "NET_BIND_SERVICE")) {
		t.Fatalf("unexpected WebUI capabilities: %v", got)
	}
	if got := initializationCaps("strict", web); len(got) != 0 {
		t.Fatalf("strict allowed extra capabilities: %v", got)
	}
}
