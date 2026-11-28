package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/pluginhub/manifest"
)

const installLabel = "io.csai.plugin.install"
const versionLabel = "io.csai.plugin.version"

type dockerCommand func(context.Context, io.Writer, ...string) ([]byte, error)

type dockerLogStream struct {
	cancel context.CancelFunc
	done   <-chan struct{}
}

type Docker struct {
	run    dockerCommand
	mu     sync.Mutex
	follow map[string]dockerLogStream
}

func NewDocker() *Docker {
	return &Docker{run: localDockerCommand, follow: map[string]dockerLogStream{}}
}

func localDockerCommand(ctx context.Context, output io.Writer, args ...string) ([]byte, error) {
	executable, err := exec.LookPath("docker")
	if err != nil {
		return nil, err
	}
	target := os.Getenv("DOCKER_HOST")
	if target == "" {
		cmd := exec.CommandContext(ctx, executable, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		value, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("local Docker context unavailable")
		}
		target = strings.TrimSpace(string(value))
	}
	if !strings.HasPrefix(target, "unix:///") && !strings.HasPrefix(target, "npipe:////./pipe/") {
		return nil, fmt.Errorf("Docker context is not a local socket")
	}
	cmd := exec.CommandContext(ctx, executable, append([]string{"--host", target}, args...)...)
	env := []string{}
	for _, name := range []string{"PATH", "HOME", "USERPROFILE", "SystemRoot", "DOCKER_CONFIG", "TMPDIR", "TMP", "TEMP"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	cmd.Env = env
	var capture limitWriter
	if output == nil {
		cmd.Stdout = &capture
	} else {
		cmd.Stdout, cmd.Stderr = output, output
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Docker %s failed: %w", args[0], err)
	}
	if capture.overflow {
		return nil, fmt.Errorf("Docker output exceeded limit")
	}
	return capture.Bytes(), nil
}

type limitWriter struct {
	bytes.Buffer
	overflow bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.Len()+len(p) > 2<<20 {
		l.overflow = true
		return len(p), nil
	}
	return l.Buffer.Write(p)
}

func runningInContainer() bool {
	for _, path := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func (d *Docker) Probe(ctx context.Context, isolation string) Probe {
	return d.probe(ctx, isolation, false)
}

func dockerRemediation(code string) (installCmd, downloadURL string) {
	switch runtime.GOOS {
	case "linux":
		switch code {
		case "docker_compose_missing":
			return `sudo apt-get update && sudo apt-get install -y docker-compose-plugin`, "https://docs.docker.com/compose/install/linux/"
		case "docker_daemon_unavailable":
			return `sudo systemctl enable --now docker && sudo usermod -aG docker "$USER"`, "https://docs.docker.com/engine/install/linux-postinstall/"
		default:
			return `sudo apt-get update && sudo apt-get install -y docker.io docker-compose-plugin && sudo systemctl enable --now docker && sudo usermod -aG docker "$USER"`, "https://docs.docker.com/engine/install/"
		}
	case "darwin":
		if code == "docker_daemon_unavailable" {
			return "open -a Docker", "https://www.docker.com/products/docker-desktop/"
		}
		return "brew install --cask docker && open -a Docker", "https://www.docker.com/products/docker-desktop/"
	case "windows":
		return "winget install -e --id Docker.DockerDesktop", "https://www.docker.com/products/docker-desktop/"
	default:
		return "", "https://docs.docker.com/get-docker/"
	}
}

func dockerUnavailableProbe(code, guidance string) Probe {
	cmd, url := dockerRemediation(code)
	return Probe{Code: code, Guidance: guidance, InstallCommand: cmd, DownloadURL: url}
}

func (d *Docker) probe(ctx context.Context, isolation string, compose bool) Probe {
	guidance := "Install Docker Desktop and start its local engine."
	if runtime.GOOS == "linux" {
		guidance = "Install and start a local Docker Engine using your distribution instructions."
	}
	if runningInContainer() {
		if _, err := os.Stat("/var/run/docker.sock"); err != nil && os.Getenv("DOCKER_HOST") == "" {
			return dockerUnavailableProbe("docker_socket_missing", "Configure a local Docker socket for CSAI or select an approved native distribution.")
		}
	}
	out, err := d.run(ctx, nil, "version", "--format", "{{.Server.Os}}/{{.Server.Version}}")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return dockerUnavailableProbe("docker_cli_missing", guidance)
		}
		return dockerUnavailableProbe("docker_daemon_unavailable", guidance+" Check your local context and socket permissions.")
	}
	osName, version, _ := strings.Cut(strings.TrimSpace(string(out)), "/")
	major, _, _ := strings.Cut(version, ".")
	majorNumber, _ := strconv.Atoi(major)
	minimum := 24
	if isolation == "strict" {
		minimum = 28
	}
	if osName != "linux" || majorNumber < minimum {
		return dockerUnavailableProbe("docker_engine_unsupported", fmt.Sprintf(
			"Use a local Linux-container Docker Engine version %d or later for %s isolation.", minimum, isolation))
	}
	if compose {
		if _, err := d.run(ctx, nil, "compose", "version", "--short"); err != nil {
			return dockerUnavailableProbe("docker_compose_missing", "Install the Docker Compose v2 CLI plugin.")
		}
	}
	if majorNumber < 28 {
		return Probe{Available: true, Guidance: "Docker versions before 28 can expose loopback-published ports to hosts on the same L2 network."}
	}
	return Probe{Available: true}
}

func (d *Docker) ProbeCompose(ctx context.Context, isolation string) Probe {
	return d.probe(ctx, isolation, true)
}

func (d *Docker) Prepare(ctx context.Context, spec Spec) error {
	distribution, err := spec.Distribution()
	if err != nil {
		return err
	}
	if probe := d.probe(ctx, spec.Plan.Isolation, distribution.Mode == "compose"); !probe.Available {
		return fmt.Errorf("%s: %s", probe.Code, probe.Guidance)
	}
	platform := spec.Plan.Platform.OS + "/" + spec.Plan.Platform.Arch
	for _, service := range spec.Plan.Artifact.Services {
		image := service.Repository + "@" + service.ManifestDigest
		info, err := d.run(ctx, nil, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image)
		if err != nil || strings.TrimSpace(string(info)) != platform {
			if _, err := d.run(ctx, nil, "pull", "--platform", platform, image); err != nil {
				return err
			}
			info, err = d.run(ctx, nil, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image)
			if err != nil || strings.TrimSpace(string(info)) != platform {
				return fmt.Errorf("local image platform differs from locked platform")
			}
		}
	}
	return os.MkdirAll(spec.VersionDir(), 0700)
}

type containerSpec struct {
	service manifest.Service
	name    string
	image   string
	env     map[string]string
	mounts  []dockerMount
}

type dockerMount struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

func projectName(spec Spec) string {
	return "csai-p-" + manifest.Digest([]byte(spec.InstallID + "/" + spec.VersionID))[:24]
}

func serviceName(spec Spec, service string) string {
	return projectName(spec) + "-" + manifest.Digest([]byte(service))[:10]
}

func operatorLabels(spec Spec) map[string]string {
	return map[string]string{installLabel: spec.InstallID, versionLabel: spec.VersionID}
}

func containerSpecs(spec Spec, distribution manifest.Distribution, modelEnv map[string]string, modelSecrets []string) ([]containerSpec, []string, error) {
	deps := map[string]manifest.Service{}
	images := map[string]string{}
	for _, service := range distribution.Services {
		deps[service.Name] = service
	}
	for _, locked := range spec.Plan.Artifact.Services {
		images[locked.Name] = locked.Repository + "@" + locked.ManifestDigest
	}
	var specs []containerSpec
	secrets := slices.Clone(modelSecrets)
	for _, service := range distribution.Services {
		if images[service.Name] == "" {
			return nil, nil, fmt.Errorf("container service is missing online lock")
		}
		env, masks, err := environment(spec, service.Environment, runtimeValues{
			host: "0.0.0.0", port: service.ContainerPort, dataDir: "/run/csai/data", fileDir: "/run/csai/secrets", container: true,
		}, deps)
		if err != nil {
			return nil, nil, err
		}
		secrets = append(secrets, masks...)
		if service.Role == "web" {
			for name, value := range modelEnv {
				env[name] = value
			}
			delete(env, "CSAI_PLUGIN_MODEL_CONFIG_DIR")
		}
		item := containerSpec{service: service, name: serviceName(spec, service.Name), image: images[service.Name], env: env}
		for _, ref := range service.SecretEnv {
			if ref.Kind == "file" {
				target := "/run/csai/secrets/" + ref.SecretRef
				if !containsMount(item.mounts, target) {
					item.mounts = append(item.mounts, dockerMount{"bind", filepath.Join(spec.VersionDir(), "secret-mounts", ref.SecretRef), target, true})
				}
			}
		}
		for _, mount := range service.DataVolumes {
			if !allowedMountTarget(mount.MountPath) {
				return nil, nil, fmt.Errorf("container data mount target is reserved")
			}
			directory := filepath.Join(spec.Root, "data", service.Name, mount.Name)
			if err := os.MkdirAll(directory, 0700); err != nil {
				return nil, nil, err
			}
			item.mounts = append(item.mounts, dockerMount{"bind", directory, mount.MountPath, false})
		}
		for _, mount := range service.ConfigMounts {
			if !allowedMountTarget(mount.MountPath) {
				return nil, nil, fmt.Errorf("container config mount target is reserved")
			}
			// Phase B mounts only approved files already staged in the private version directory.
			root := spec.VersionDir()
			modelMount := false
			if service.Role == "web" && spec.Declaration.LLM != nil {
				for _, binding := range spec.Declaration.LLM.Bindings {
					if binding.ConfigTemplate != nil && binding.ConfigTemplate.Path == mount.Source {
						modelMount = true
						break
					}
				}
				if modelMount {
					if spec.Model == nil || !slices.ContainsFunc(spec.Model.Templates, func(t ModelTemplate) bool { return t.Manifest.Path == mount.Source }) {
						continue
					}
					root = filepath.Join(root, modelSubdir)
				}
			}
			source, err := existingPath(root, filepath.FromSlash(mount.Source))
			if err != nil {
				return nil, nil, err
			}
			item.mounts = append(item.mounts, dockerMount{"bind", source, mount.MountPath, true})
		}
		specs = append(specs, item)
	}
	return specs, secrets, nil
}

func allowedMountTarget(value string) bool {
	for _, reserved := range []string{"/proc", "/sys", "/dev", "/run/csai/secrets", "/run/docker.sock", "/var/run/docker.sock"} {
		if value == reserved || strings.HasPrefix(value, reserved+"/") {
			return false
		}
	}
	return value != "/"
}

func containsMount(mounts []dockerMount, path string) bool {
	for _, mount := range mounts {
		if mount.Target == path {
			return true
		}
	}
	return false
}

func (d *Docker) Start(ctx context.Context, spec Spec) (State, error) {
	distribution, err := spec.Distribution()
	if err != nil {
		return State{}, err
	}
	if probe := d.probe(ctx, spec.Plan.Isolation, distribution.Mode == "compose"); !probe.Available {
		return State{}, fmt.Errorf("%s: %s", probe.Code, probe.Guidance)
	}
	modelEnv, modelSecrets, err := renderModelConfig(spec)
	if err != nil {
		return State{}, err
	}
	items, secrets, err := containerSpecs(spec, distribution, modelEnv, modelSecrets)
	if err != nil {
		return State{}, err
	}
	if distribution.Mode == "compose" {
		err = d.startCompose(ctx, spec, items)
	} else if len(items) == 1 {
		err = d.startSingle(ctx, spec, items[0])
	} else {
		err = fmt.Errorf("Docker mode expects a single service")
	}
	if err != nil {
		return State{}, err
	}
	state, err := d.Inspect(ctx, spec)
	if err != nil || !state.Running {
		return State{}, fmt.Errorf("Docker service has no verified loopback endpoint")
	}
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for state.CheckHealth(wait, spec.Declaration.Web) != nil {
		select {
		case <-wait.Done():
			return State{}, fmt.Errorf("Docker WebUI readiness check failed")
		case <-time.After(150 * time.Millisecond):
		}
	}
	d.startLogs(spec, items, secrets)
	return state, nil
}

func composeFile(spec Spec) string { return filepath.Join(spec.VersionDir(), "compose.generated.json") }

func (d *Docker) startCompose(ctx context.Context, spec Spec, items []containerSpec) error {
	for _, network := range managedNetworks(spec) {
		if _, err := d.findNetwork(ctx, spec, network); err != nil {
			return err
		}
	}
	private, web := privateNetwork(spec), webNetwork(spec)
	if spec.Model != nil {
		if err := d.ensureNetwork(ctx, spec, web); err != nil {
			return err
		}
		for _, item := range items {
			if item.service.Role == "web" {
				if err := d.probeModel(ctx, spec, item, web); err != nil {
					return err
				}
				break
			}
		}
	}
	services := map[string]any{}
	for _, item := range items {
		s := item.service
		env := map[string]string{}
		for key, value := range item.env {
			env[key] = strings.ReplaceAll(value, "$", "$$")
		}
		networks := map[string]any{"private": map[string]any{"aliases": []string{s.Name}}}
		cfg := map[string]any{
			"image": item.image, "container_name": item.name, "platform": spec.Plan.Platform.OS + "/" + spec.Plan.Platform.Arch,
			"environment": env, "labels": operatorLabels(spec),
			"mem_limit": strconv.Itoa(s.Resources.MemoryMiB) + "m", "pids_limit": s.Resources.PIDs,
			"restart": "no", "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"},
			"networks": networks,
		}
		if len(item.mounts) > 0 {
			cfg["volumes"] = item.mounts
		}
		if capabilities := initializationCaps(spec.Plan.Isolation, s); len(capabilities) > 0 {
			cfg["cap_add"] = capabilities
		}
		if s.Role == "web" {
			networks["web"] = map[string]any{}
			cfg["ports"] = []string{"127.0.0.1::" + strconv.Itoa(s.ContainerPort)}
			if spec.Model != nil && spec.Model.DockerHostMapping != "" {
				cfg["extra_hosts"] = []string{spec.Model.DockerHostMapping}
			}
		}
		if s.Healthcheck != nil {
			h := s.Healthcheck
			command := healthArguments(s)
			for i, arg := range command {
				command[i] = strings.ReplaceAll(arg, "$", "$$")
			}
			cfg["healthcheck"] = map[string]any{
				"test": append([]string{"CMD"}, command...), "interval": seconds(h.IntervalSeconds), "timeout": seconds(h.TimeoutSeconds),
				"retries": h.Retries, "start_period": seconds(h.StartPeriodSeconds),
			}
		}
		if len(s.DependsOn) != 0 {
			conditions := map[string]any{}
			for name, condition := range s.DependsOn {
				conditions[name] = map[string]string{"condition": condition}
			}
			cfg["depends_on"] = conditions
		}
		services[s.Name] = cfg
	}
	webConfig := map[string]any{"driver": "bridge", "internal": web.internal, "name": web.name, "labels": operatorLabels(spec)}
	if spec.Model != nil {
		webConfig = map[string]any{"external": true, "name": web.name}
	}
	raw, err := json.Marshal(map[string]any{"services": services, "networks": map[string]any{
		"private": map[string]any{"driver": "bridge", "internal": private.internal, "name": private.name, "labels": operatorLabels(spec)},
		"web":     webConfig,
	}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(composeFile(spec), raw, 0600); err != nil {
		return err
	}
	if spec.Model != nil {
		defer os.Remove(composeFile(spec))
	}
	_, err = d.run(ctx, nil, "compose", "--env-file", os.DevNull, "--project-directory", spec.VersionDir(), "-p", projectName(spec), "-f", composeFile(spec), "up", "-d", "--no-build", "--pull", "never", "--wait")
	return err
}

func (d *Docker) startSingle(ctx context.Context, spec Spec, item containerSpec) error {
	web := webNetwork(spec)
	if err := d.ensureNetwork(ctx, spec, web); err != nil {
		return err
	}
	if spec.Model != nil {
		if err := d.probeModel(ctx, spec, item, web); err != nil {
			return err
		}
	}
	var env strings.Builder
	for key, value := range item.env {
		env.WriteString(key + "=" + value + "\n")
	}
	path := filepath.Join(spec.VersionDir(), "docker.env")
	if err := os.WriteFile(path, []byte(env.String()), 0600); err != nil {
		return err
	}
	defer os.Remove(path)
	s := item.service
	args := []string{"create", "--name", item.name, "--platform", spec.Plan.Platform.OS + "/" + spec.Plan.Platform.Arch,
		"--label", installLabel + "=" + spec.InstallID, "--label", versionLabel + "=" + spec.VersionID,
		"--network", web.name, "--restart", "no", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--memory", strconv.Itoa(s.Resources.MemoryMiB) + "m", "--pids-limit", strconv.Itoa(s.Resources.PIDs),
		"--publish", "127.0.0.1::" + strconv.Itoa(s.ContainerPort), "--env-file", path}
	for _, capability := range initializationCaps(spec.Plan.Isolation, s) {
		args = append(args, "--cap-add", capability)
	}

	if spec.Model != nil && spec.Model.DockerHostMapping != "" {
		args = append(args, "--add-host", spec.Model.DockerHostMapping)
	}
	for _, mount := range item.mounts {
		if strings.ContainsAny(mount.Source+mount.Target, ",\r\n") {
			return fmt.Errorf("unsupported Docker mount path")
		}
		value := "type=bind,source=" + mount.Source + ",target=" + mount.Target
		if mount.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	if _, err := d.run(ctx, nil, append(args, item.image)...); err != nil {
		return err
	}
	if _, err := d.run(ctx, nil, "start", item.name); err != nil {
		return err
	}
	h := s.Healthcheck
	if h == nil {
		return nil
	}
	for attempt := 0; attempt < h.Retries; attempt++ {
		probeCtx, cancel := context.WithTimeout(ctx, time.Duration(h.TimeoutSeconds)*time.Second)
		_, err := d.run(probeCtx, nil, append([]string{"exec", item.name}, healthArguments(s)...)...)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(h.IntervalSeconds) * time.Second):
		}
	}
	return fmt.Errorf("Docker container healthcheck failed")
}

// Official image entrypoints may fix directory ownership and switch to the
// service user. Keep this bootstrap profile narrow even in standard mode.
func initializationCaps(isolation string, service manifest.Service) []string {
	if isolation == "strict" {
		return nil
	}
	capabilities := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"}
	if service.Role == "web" {
		capabilities = append(capabilities, "KILL")
	}
	if service.ContainerPort > 0 && service.ContainerPort < 1024 {
		capabilities = append(capabilities, "NET_BIND_SERVICE")
	}
	return capabilities
}

// Run the compatibility probe in the approved Web image on its actual bridge,
// before bringing up the Web process. No gateway credential enters probe argv.
func (d *Docker) probeModel(ctx context.Context, spec Spec, item containerSpec, bridge managedNetwork) error {
	endpoint := spec.Model.GatewayRoot + "/_csai_gateway/ready"
	base := []string{"run", "--rm", "--pull", "never", "--network", bridge.name, "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true", "--pids-limit", "16", "--memory", "96m"}
	if spec.Model.DockerHostMapping != "" {
		base = append(base, "--add-host", spec.Model.DockerHostMapping)
	}
	for _, probe := range [][]string{
		{"--entrypoint", "curl", item.image, "--fail", "--silent", "--max-time", "4", "--output", "/dev/null", endpoint},
		{"--entrypoint", "wget", item.image, "-q", "-T", "4", "-O", "/dev/null", endpoint},
	} {
		child, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, err := d.run(child, nil, append(slices.Clone(base), probe...)...)
		cancel()
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("container cannot reach the private model gateway (curl or wget is required in the Web image)")
}

func healthArguments(s manifest.Service) []string {
	h := s.Healthcheck
	switch h.Kind {
	case "http":
		return []string{"wget", "-q", "-O", "/dev/null", "http://127.0.0.1:" + strconv.Itoa(s.ContainerPort) + h.Path}
	case "tcp":
		return []string{"nc", "-z", "-w", strconv.Itoa(h.TimeoutSeconds), "127.0.0.1", strconv.Itoa(s.ContainerPort)}
	default:
		return append([]string(nil), h.Argv...)
	}
}

func seconds(n int) string { return strconv.Itoa(n) + "s" }

type dockerInspect struct {
	ID         string
	Config     struct{ Labels map[string]string }
	State      struct{ Running bool }
	HostConfig struct {
		Memory    int64
		PidsLimit int64
	}
	NetworkSettings struct {
		Ports map[string][]struct{ HostIP, HostPort string }
	}
}

func (d *Docker) find(ctx context.Context, name string, spec Spec) (*dockerInspect, error) {
	raw, err := d.run(ctx, nil, "ps", "-aq", "--filter", "name=^/"+name+"$")
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil, err
	}
	raw, err = d.run(ctx, nil, "inspect", "--type", "container", name)
	if err != nil {
		return nil, err
	}
	var items []dockerInspect
	if json.Unmarshal(raw, &items) != nil || len(items) != 1 || items[0].ID == "" ||
		items[0].Config.Labels[installLabel] != spec.InstallID || items[0].Config.Labels[versionLabel] != spec.VersionID {
		return nil, fmt.Errorf("Docker container ownership not verified")
	}
	return &items[0], nil
}

func (d *Docker) Inspect(ctx context.Context, spec Spec) (State, error) {
	distribution, err := spec.Distribution()
	if err != nil {
		return State{}, err
	}
	var state State
	for _, s := range distribution.Services {
		item, err := d.find(ctx, serviceName(spec, s.Name), spec)
		if err != nil || item == nil || !item.State.Running {
			return State{}, err
		}
		if item.HostConfig.Memory != int64(s.Resources.MemoryMiB)<<20 || item.HostConfig.PidsLimit != int64(s.Resources.PIDs) {
			return State{}, fmt.Errorf("Docker resource limits differ from approved limits")
		}
		if s.Role == "web" {
			ports := item.NetworkSettings.Ports[strconv.Itoa(s.ContainerPort)+"/tcp"]
			if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" {
				return State{}, fmt.Errorf("Docker port is not exclusively mapped to loopback")
			}
			port, err := strconv.Atoi(ports[0].HostPort)
			if err != nil || port < 1 || port > 65535 {
				return State{}, fmt.Errorf("invalid Docker port")
			}
			state = State{Running: true, Port: port, Endpoint: "http://" + net.JoinHostPort("127.0.0.1", ports[0].HostPort)}
		}
	}
	return state, nil
}

func (d *Docker) Stop(ctx context.Context, spec Spec) error {
	d.mu.Lock()
	stream, following := d.follow[spec.VersionID]
	if following {
		stream.cancel()
		delete(d.follow, spec.VersionID)
	}
	d.mu.Unlock()
	if following {
		select {
		case <-stream.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	distribution, err := spec.Distribution()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(spec.VersionDir()); errors.Is(statErr, os.ErrNotExist) {
		// Prepare() only creates VersionDir() after verifying Docker and pulling images.
		// When it is absent, no container or managed Docker network was ever created.
		return nil
	}
	for _, s := range stopOrder(distribution.Services) {
		item, err := d.find(ctx, serviceName(spec, s.Name), spec)
		if err != nil {
			return err
		}
		if item != nil {
			if item.State.Running {
				_, _ = d.run(ctx, nil, "stop", "--time", "5", item.ID)
			}
			if _, err := d.run(ctx, nil, "rm", "--force", item.ID); err != nil {
				return err
			}
		}
	}
	var cleanupError error
	for _, network := range managedNetworks(spec) {
		found, err := d.findNetwork(ctx, spec, network)
		if err == nil && found {
			_, err = d.run(ctx, nil, "network", "rm", network.name)
		}
		cleanupError = errors.Join(cleanupError, err)
	}
	return cleanupError
}

func stopOrder(services []manifest.Service) []manifest.Service {
	byName := make(map[string]manifest.Service, len(services))
	for _, service := range services {
		byName[service.Name] = service
	}
	seen := map[string]bool{}
	var result []manifest.Service
	var visit func(manifest.Service)
	visit = func(value manifest.Service) {
		if seen[value.Name] {
			return
		}
		seen[value.Name] = true
		for name := range value.DependsOn {
			visit(byName[name])
		}
		result = append(result, value)
	}
	for _, service := range services {
		visit(service)
	}
	slices.Reverse(result)
	return result
}

type managedNetwork struct {
	name     string
	internal bool
}

func privateNetwork(spec Spec) managedNetwork {
	// Keep the original project network name so reconciliation can retire it.
	return managedNetwork{name: projectName(spec), internal: true}
}

func webNetwork(spec Spec) managedNetwork {
	return managedNetwork{name: projectName(spec) + "-web"}
}

func managedNetworks(spec Spec) []managedNetwork {
	return []managedNetwork{webNetwork(spec), privateNetwork(spec)}
}

func (d *Docker) findNetwork(ctx context.Context, spec Spec, network managedNetwork) (bool, error) {
	name := network.name
	raw, err := d.run(ctx, nil, "network", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return false, err
	}
	raw, err = d.run(ctx, nil, "network", "inspect", name)
	if err != nil {
		return false, err
	}
	var result []struct {
		Name     string
		Labels   map[string]string
		Internal bool
	}
	if json.Unmarshal(raw, &result) != nil || len(result) != 1 || result[0].Name != name || result[0].Internal != network.internal ||
		result[0].Labels[installLabel] != spec.InstallID || result[0].Labels[versionLabel] != spec.VersionID {
		return false, fmt.Errorf("Docker network ownership not verified")
	}
	return true, nil
}

func (d *Docker) ensureNetwork(ctx context.Context, spec Spec, network managedNetwork) error {
	if exists, err := d.findNetwork(ctx, spec, network); err != nil || exists {
		return err
	}
	args := []string{"network", "create", "--driver", "bridge", "--label", installLabel + "=" + spec.InstallID,
		"--label", versionLabel + "=" + spec.VersionID}
	if network.internal {
		args = append(args, "--internal")
	}
	_, err := d.run(ctx, nil, append(args, network.name)...)
	return err
}

func (d *Docker) Remove(ctx context.Context, spec Spec) error {
	if err := d.Stop(ctx, spec); err != nil && !errors.Is(err, exec.ErrNotFound) {
		return err
	}
	return os.RemoveAll(spec.VersionDir())
}

func (d *Docker) startLogs(spec Spec, items []containerSpec, secrets []string) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	d.mu.Lock()
	if old, ok := d.follow[spec.VersionID]; ok {
		old.cancel()
	}
	d.follow[spec.VersionID] = dockerLogStream{cancel: cancel, done: done}
	d.mu.Unlock()
	go func() {
		defer close(done)
		var group sync.WaitGroup
		for _, item := range items {
			group.Add(1)
			go func(name string) {
				defer group.Done()
				logger := &lineLogger{emit: spec.Log, secrets: secrets}
				_, _ = d.run(ctx, logger, "logs", "--tail", "50", "--follow", name)
				logger.flush()
			}(item.name)
		}
		group.Wait()
	}()
}
