// Package runner supervises approved plugin applications.
package runner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cyberstrike-ai/internal/pluginhub/manifest"
)

type Driver interface {
	Probe(context.Context, string) Probe
	Prepare(context.Context, Spec) error
	Start(context.Context, Spec) (State, error)
	Inspect(context.Context, Spec) (State, error)
	Stop(context.Context, Spec) error
	Remove(context.Context, Spec) error
}

type Probe struct {
	Available bool   `json:"available"`
	Code      string `json:"code,omitempty"`
	Guidance  string `json:"guidance,omitempty"`
}

type Spec struct {
	Model       *ModelConfig
	InstallID   string
	VersionID   string
	Root        string
	Declaration *manifest.Declaration
	Plan        *manifest.InstallPlan
	Log         func(string)
}

func (s Spec) WebBasePath() string {
	if s.Declaration.Web.Route == "build_base_path" {
		return "/apps/" + s.InstallID
	}
	return "/"
}

func (s Spec) VersionDir() string { return filepath.Join(s.Root, "versions", s.VersionID) }

func (s Spec) Distribution() (manifest.Distribution, error) {
	for _, distribution := range s.Declaration.Distribution {
		if distribution.Type == s.Plan.Artifact.Driver {
			return distribution, nil
		}
	}
	return manifest.Distribution{}, fmt.Errorf("locked driver missing from approved declaration")
}

type State struct {
	Running    bool
	Endpoint   string
	Port       int
	readyNonce string
}

type Downloader interface {
	Download(context.Context, string, io.Writer) (string, error)
}

func (s State) CheckHealth(ctx context.Context, web manifest.Web) error {
	if !s.Running {
		return fmt.Errorf("application is not running")
	}
	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Endpoint+web.HealthPath, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	status := response.StatusCode
	success := status >= 200 && status < 300
	redirect := web.AllowHealthRedirect && status >= 300 && status < 400 && response.Header.Get("Location") != ""
	if (!success && !redirect) ||
		(s.readyNonce != "" && response.Header.Get("X-CSAI-Plugin-Ready") != s.readyNonce) {
		return fmt.Errorf("readiness check failed")
	}
	return nil
}

func existingPath(root, relative string) (string, error) {
	if !filepath.IsLocal(relative) {
		return "", fmt.Errorf("invalid artifact path")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if _, err := dir.Stat(relative); err != nil {
		return "", err
	}
	// Do not accept even in-root links for executing code or mounting config.
	path := root
	for _, part := range splitPath(relative) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("artifact path is missing or a symlink")
		}
	}
	return path, nil
}

func splitPath(relative string) []string {
	var result []string
	for relative != "." && relative != "" {
		dir, base := filepath.Split(relative)
		result = append([]string{base}, result...)
		relative = filepath.Clean(dir)
	}
	return result
}
