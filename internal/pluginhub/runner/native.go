package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	"cyberstrike-ai/internal/processguard"
)

type Native struct {
	driver       string
	downloader   Downloader
	gitProxyMode string
	gitProxy     string
	mu           sync.Mutex
	processes    map[string]*applicationProcess
}

type applicationProcess struct {
	version string
	cmd     *exec.Cmd
	group   processguard.Group
	done    chan struct{}
	state   State
}

func NewNative(driver string, downloader Downloader, proxyMode, proxyURL string) *Native {
	return &Native{driver: driver, downloader: downloader, gitProxyMode: proxyMode, gitProxy: proxyURL,
		processes: map[string]*applicationProcess{}}
}

func (n *Native) Probe(_ context.Context, isolation string) Probe {
	if isolation == "strict" {
		return Probe{Code: "strict_container_required", Guidance: "Choose a container distribution for strict isolation."}
	}
	if n.driver == "git" {
		if _, err := exec.LookPath("git"); err != nil {
			return Probe{Code: "git_missing", Guidance: "Install Git with your platform package manager and retry."}
		}
	}
	return Probe{Available: true}
}

func (n *Native) Prepare(ctx context.Context, spec Spec) error {
	if !n.Probe(ctx, spec.Plan.Isolation).Available {
		return fmt.Errorf("native runtime unavailable")
	}
	d, err := spec.Distribution()
	if err != nil || d.Type != n.driver || spec.Declaration.Launch == nil {
		return fmt.Errorf("native launch declaration unavailable")
	}
	platform := spec.Plan.Platform
	if platform.OS != runtime.GOOS || platform.Arch != runtime.GOARCH {
		return fmt.Errorf("native artifact platform differs from host")
	}
	parent := filepath.Dir(spec.VersionDir())
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	if _, err := os.Stat(spec.VersionDir()); err == nil {
		return nil
	}
	stage, err := os.MkdirTemp(parent, ".stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	slog(spec, "Preparing approved "+n.driver+" artifact")
	if n.driver == "release" {
		err = n.unpackRelease(ctx, spec, d, stage)
	} else {
		err = n.buildGit(ctx, spec, d, stage)
	}
	if err != nil {
		return err
	}
	if _, err := nativeEntrypoint(spec, d, stage); err != nil {
		return err
	}
	return os.Rename(stage, spec.VersionDir())
}

func (n *Native) unpackRelease(ctx context.Context, spec Spec, d manifest.Distribution, stage string) error {
	for _, asset := range d.Assets {
		if asset.OS != spec.Plan.Platform.OS || asset.Arch != spec.Plan.Platform.Arch || asset.URL != spec.Plan.Artifact.AssetURL {
			continue
		}
		file, err := os.CreateTemp(filepath.Dir(stage), ".archive-")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		sum, err := n.downloader.Download(ctx, asset.URL, file)
		closeErr := file.Close()
		if err != nil || closeErr != nil || sum != spec.Plan.Artifact.SHA256 {
			return fmt.Errorf("release archive transfer or digest verification failed")
		}
		return extract(ctx, file.Name(), stage, asset.Format, asset.StripComponents)
	}
	return fmt.Errorf("approved release asset is unavailable for host platform")
}

func (n *Native) buildGit(ctx context.Context, spec Spec, d manifest.Distribution, stage string) error {
	if !manifest.IsCommitSHA(spec.Plan.Artifact.Commit) {
		return fmt.Errorf("missing pinned commit")
	}
	if err := manifest.ValidateHTTPS(spec.Plan.Artifact.Repository); err != nil {
		return err
	}
	home := filepath.Join(stage, ".csai-build")
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	env, err := cleanEnv(home)
	if err != nil {
		return err
	}
	gitEnv := append(slices.Clone(env), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	switch n.gitProxyMode {
	case "fixed":
		gitEnv = append(gitEnv, "HTTPS_PROXY="+n.gitProxy, "https_proxy="+n.gitProxy)
	case "", "system":
		for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
			if value, ok := os.LookupEnv(name); ok {
				gitEnv = append(gitEnv, name+"="+value)
			}
		}
	}
	group, err := processguard.New("plugin-build-" + spec.VersionID)
	if err != nil {
		return err
	}
	defer closeGroup(group)
	git := func(args ...string) error {
		// Ignore host and repository-wide recursive fetching or alternate transports.
		base := []string{"-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never",
			"-c", "http.followRedirects=false", "-c", "submodule.recurse=false"}
		return runBuild(ctx, group, spec, stage, gitEnv, "git", append(base, args...))
	}
	if err := git("init", "--quiet", "--template="); err != nil {
		return err
	}
	if err := git("fetch", "--quiet", "--depth=1", "--no-tags", "--no-recurse-submodules",
		spec.Plan.Artifact.Repository, spec.Plan.Artifact.Commit); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(stage, ".git", "FETCH_HEAD"))
	if err != nil || !strings.HasPrefix(string(data), spec.Plan.Artifact.Commit+"\t") {
		return fmt.Errorf("Git fetch did not return the approved commit")
	}
	if err := git("checkout", "--quiet", "--detach", spec.Plan.Artifact.Commit); err != nil {
		return err
	}
	// The build-time route prefix is public and contains no gateway credentials.
	if spec.Declaration.Web.Route == "build_base_path" {
		for name, value := range spec.Declaration.Launch.Environment.Env {
			if value == "web_base_path" {
				env = append(env, name+"="+spec.WebBasePath())
			}
		}
	}
	// Place dependency caches beside the repository for reproducible build steps.
	for _, step := range d.Build {
		if !slices.Contains([]string{"go", "npm", "pnpm", "yarn", "cargo", "make", "python3", "node"}, step.Program) {
			return fmt.Errorf("build tool is not supported")
		}
		dir := stage
		if step.WorkDir != "" {
			dir, err = existingPath(stage, step.WorkDir)
			if err != nil {
				return err
			}
		}
		if err := runBuild(ctx, group, spec, dir, env, step.Program, step.Args); err != nil {
			return err
		}
	}
	return nil
}

func runBuild(ctx context.Context, group processguard.Group, spec Spec, dir string, env []string, program string, args []string) error {
	path, err := exec.LookPath(program)
	if err != nil {
		return fmt.Errorf("approved build program %s unavailable", program)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir, cmd.Env = dir, env
	log := &lineLogger{emit: spec.Log}
	cmd.Stdout, cmd.Stderr = log, log
	if err := startGuarded(group, cmd); err != nil {
		return err
	}
	err = cmd.Wait()
	log.flush()
	if err != nil {
		return fmt.Errorf("%s build step failed: %w", program, err)
	}
	return nil
}

func nativeEntrypoint(spec Spec, distribution manifest.Distribution, versionDir string) (string, error) {
	entry := spec.Declaration.Launch.Entrypoint
	if distribution.Entrypoint != "" {
		entry = distribution.Entrypoint
	}
	for _, asset := range distribution.Assets {
		if asset.URL == spec.Plan.Artifact.AssetURL && asset.OS == runtime.GOOS && asset.Arch == runtime.GOARCH && asset.Entrypoint != "" {
			entry = asset.Entrypoint
		}
	}
	if runtime.GOOS == "windows" && filepath.Ext(entry) == "" {
		entry += ".exe"
	}
	if distribution.OutputRoot != "" {
		entry = filepath.Join(distribution.OutputRoot, entry)
	}
	resolved, err := existingPath(versionDir, entry)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0100 == 0) {
		return "", fmt.Errorf("approved native entrypoint is not executable")
	}
	return resolved, nil
}

func (n *Native) Start(ctx context.Context, spec Spec) (State, error) {
	d, err := spec.Distribution()
	if err != nil {
		return State{}, err
	}
	path, err := nativeEntrypoint(spec, d, spec.VersionDir())
	if err != nil {
		return State{}, err
	}
	if state, err := n.Inspect(ctx, spec); err != nil || state.Running {
		return state, err
	}
	if err := n.Stop(ctx, spec); err != nil {
		return State{}, err
	}
	modelEnv, modelSecrets, err := renderModelConfig(spec)
	if err != nil {
		return State{}, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		state, err := n.startOnce(ctx, spec, path, modelEnv, modelSecrets)
		if err == nil {
			return state, nil
		}
		if cleanup := n.Stop(ctx, spec); cleanup != nil {
			return State{}, cleanup
		}
		if ctx.Err() != nil {
			return State{}, ctx.Err()
		}
	}
	return State{}, fmt.Errorf("native application could not claim a healthy loopback endpoint")
}

func (n *Native) startOnce(ctx context.Context, spec Spec, executable string, modelEnv map[string]string, modelSecrets []string) (State, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return State{}, err
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	dataDir := filepath.Join(spec.Root, "data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return State{}, err
	}
	env, err := cleanEnv(spec.Root)
	if err != nil {
		return State{}, err
	}
	values, secrets, err := environment(spec, spec.Declaration.Launch.Environment,
		runtimeValues{host: "127.0.0.1", port: port, dataDir: dataDir, fileDir: filepath.Join(spec.Root, "secrets")}, nil)
	if err != nil {
		return State{}, err
	}
	for name, value := range modelEnv {
		values[name] = value
	}
	secrets = append(secrets, modelSecrets...)
	var nonce string
	if spec.Declaration.Web.ReadyHeaderCheck {
		var randomness [24]byte
		if _, err := rand.Read(randomness[:]); err != nil {
			return State{}, err
		}
		nonce = hex.EncodeToString(randomness[:])
		values["CSAI_PLUGIN_READY_TOKEN"] = nonce
		secrets = append(secrets, nonce)
	}
	dir := spec.VersionDir()
	if spec.Declaration.Launch.WorkDir != "" {
		dir, err = existingPath(dir, spec.Declaration.Launch.WorkDir)
		if err != nil {
			return State{}, err
		}
	}
	cmd := exec.Command(executable, spec.Declaration.Launch.Args...)
	cmd.Dir, cmd.Env = dir, withEnv(env, values)
	output := &lineLogger{emit: spec.Log, secrets: secrets}
	cmd.Stdout, cmd.Stderr = output, output
	group, err := processguard.New("plugin-app-" + spec.VersionID)
	if err != nil {
		return State{}, err
	}
	if err := listener.Close(); err != nil {
		closeGroup(group)
		return State{}, err
	}
	if err := startGuarded(group, cmd); err != nil {
		closeGroup(group)
		return State{}, err
	}

	state := State{Running: true, Endpoint: "http://127.0.0.1:" + strconv.Itoa(port), Port: port, readyNonce: nonce}
	process := &applicationProcess{version: spec.VersionID, cmd: cmd, group: group, done: make(chan struct{}), state: state}
	n.mu.Lock()
	if n.processes[spec.InstallID] != nil {
		n.mu.Unlock()
		go cmd.Wait()
		closeGroup(group)
		return State{}, fmt.Errorf("native process is already supervised")
	}
	n.processes[spec.InstallID] = process
	n.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		output.flush()
		close(process.done)
	}()
	wait, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for {
		if err := state.CheckHealth(wait, spec.Declaration.Web); err == nil {
			select {
			case <-process.done:
				return State{}, fmt.Errorf("native application exited during readiness")
			default:
				slog(spec, "Native application is healthy")
				return state, nil
			}
		}
		select {
		case <-process.done:
			return State{}, fmt.Errorf("native application exited before readiness")
		case <-wait.Done():
			return State{}, fmt.Errorf("native readiness timed out")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (n *Native) Inspect(_ context.Context, spec Spec) (State, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	process := n.processes[spec.InstallID]
	if process == nil {
		return State{}, nil
	}
	if process.version != spec.VersionID {
		return State{}, fmt.Errorf("native process belongs to a different version")
	}
	select {
	case <-process.done:
		return State{}, nil
	default:
		return process.state, nil
	}
}

func (n *Native) Stop(_ context.Context, spec Spec) error {
	n.mu.Lock()
	process := n.processes[spec.InstallID]
	n.mu.Unlock()
	if process == nil {
		return nil
	}
	if process.version != spec.VersionID {
		return nil
	}
	_ = process.cmd.Process.Signal(os.Interrupt)
	select {
	case <-process.done:
	case <-time.After(300 * time.Millisecond):
	}
	if err := closeGroup(process.group); err != nil {
		return err
	}
	n.mu.Lock()
	if n.processes[spec.InstallID] == process {
		delete(n.processes, spec.InstallID)
	}
	n.mu.Unlock()
	return nil
}

func (n *Native) Remove(ctx context.Context, spec Spec) error {
	if err := n.Stop(ctx, spec); err != nil {
		return err
	}
	return os.RemoveAll(spec.VersionDir())
}

func startGuarded(group processguard.Group, cmd *exec.Cmd) error {
	launch, err := group.Prepare(cmd)
	if err != nil {
		return err
	}
	defer launch.Dispose()
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := launch.Commit(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return nil
}

func closeGroup(group processguard.Group) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return group.Close(ctx)
}

func slog(spec Spec, message string) {
	if spec.Log != nil {
		spec.Log(message)
	}
}
