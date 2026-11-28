// Package source previews repository metadata without fetching or executing repository code.
package source

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"cyberstrike-ai/internal/pluginhub/manifest"
)

var (
	ErrRateLimited     = errors.New("GitHub metadata rate limit exceeded")
	ErrInvalidSource   = errors.New("expected a public GitHub HTTPS repository URL")
	ErrInvalidRevision = errors.New("invalid Git revision")
	ErrAmbiguousRef    = errors.New("Git ref exists as both a branch and a tag")
	ErrSourceNotFound  = errors.New("repository or revision not found")
	ErrUpstream        = errors.New("repository metadata unavailable")
	ErrManifestInvalid = errors.New("repository plugin manifest is invalid")
)

const gitHubAPI = "https://api.github.com"
const maxAPIResponse = 1 << 20

type GitHubInspector struct {
	client *http.Client
	token  string
}

func NewGitHubInspector() *GitHubInspector {
	return NewGitHubInspectorWithTransport(nil, "")
}

func NewGitHubInspectorWithTransport(transport http.RoundTripper, token string) *GitHubInspector {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &GitHubInspector{token: strings.TrimSpace(token), client: &http.Client{
		Transport: transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

type Inspection struct {
	Repository   string          `json:"repository"`
	RequestedRef string          `json:"requested_ref"`
	ResolvedRef  string          `json:"resolved_ref"`
	Commit       string          `json:"commit"`
	Manifest     json.RawMessage `json:"manifest,omitempty"`
	Evidence     []string        `json:"evidence"`
	Candidates   []string        `json:"candidates"`
	Release      *Release        `json:"release,omitempty"`
	ReleaseState string          `json:"release_state"`
}

type Release struct {
	Tag    string   `json:"tag"`
	Assets []string `json:"assets"`
}

type repository struct {
	Owner string
	Name  string
}

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

func NormalizeGitHubRepository(raw string) (string, error) {
	repo, err := parseRepository(raw)
	if err != nil {
		return "", err
	}
	return "https://github.com/" + repo.Owner + "/" + repo.Name, nil
}

func parseRepository(raw string) (repository, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.ContainsAny(raw, "#%\\") {
		return repository{}, ErrInvalidSource
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), "/")
	if len(parts) != 2 {
		return repository{}, ErrInvalidSource
	}
	owner := strings.ToLower(parts[0])
	name := strings.TrimSuffix(strings.ToLower(parts[1]), ".git")
	if !ownerPattern.MatchString(owner) || !repoPattern.MatchString(name) || name == "." || name == ".." {
		return repository{}, ErrInvalidSource
	}
	return repository{Owner: owner, Name: name}, nil
}

func (g *GitHubInspector) Inspect(ctx context.Context, input, revision string) (*Inspection, error) {
	repo, err := parseRepository(input)
	if err != nil {
		return nil, err
	}
	revision = strings.TrimSpace(revision)
	if err := manifest.ValidateGitRef(revision); err != nil {
		return nil, ErrInvalidRevision
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	endpoint := "/repos/" + repo.Owner + "/" + repo.Name
	var metadata struct {
		DefaultBranch string `json:"default_branch"`
		FullName      string `json:"full_name"`
		Private       bool   `json:"private"`
	}
	if err := g.get(ctx, endpoint, &metadata); err != nil {
		return nil, err
	}
	if metadata.Private || !strings.EqualFold(metadata.FullName, repo.Owner+"/"+repo.Name) {
		return nil, ErrSourceNotFound
	}
	resolvedRef, commit, err := g.resolve(ctx, endpoint, revision, metadata.DefaultBranch)
	if err != nil {
		return nil, err
	}
	var commitData struct {
		SHA    string `json:"sha"`
		Commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	if err := g.get(ctx, endpoint+"/commits/"+commit, &commitData); err != nil {
		return nil, err
	}
	if commitData.SHA != commit || !manifest.IsCommitSHA(commitData.Commit.Tree.SHA) {
		return nil, ErrUpstream
	}
	result := &Inspection{
		Repository: "https://github.com/" + repo.Owner + "/" + repo.Name, RequestedRef: revision,
		ResolvedRef: resolvedRef, Commit: commit, Evidence: []string{}, Candidates: []string{"git"},
	}
	if err := g.inspectTree(ctx, endpoint, commitData.Commit.Tree.SHA, result); err != nil {
		return nil, err
	}
	g.inspectRelease(ctx, endpoint, result)
	return result, nil
}

type gitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func (g *GitHubInspector) resolve(ctx context.Context, endpoint, revision, defaultBranch string) (string, string, error) {
	if manifest.IsCommitSHA(revision) {
		return revision, revision, nil
	}
	if revision == "" {
		if defaultBranch == "" || manifest.ValidateGitRef(defaultBranch) != nil {
			return "", "", ErrUpstream
		}
		revision = "refs/heads/" + defaultBranch
	}
	if strings.HasPrefix(revision, "refs/") {
		object, err := g.getRef(ctx, endpoint, revision)
		if err != nil {
			return "", "", err
		}
		commit, err := g.peel(ctx, endpoint, object)
		return revision, commit, err
	}

	headRef, tagRef := "refs/heads/"+revision, "refs/tags/"+revision
	head, headErr := g.getRef(ctx, endpoint, headRef)
	tag, tagErr := g.getRef(ctx, endpoint, tagRef)
	for _, err := range []error{headErr, tagErr} {
		if err != nil && !errors.Is(err, ErrSourceNotFound) {
			return "", "", err
		}
	}
	switch {
	case headErr == nil && tagErr == nil:
		return "", "", ErrAmbiguousRef
	case headErr == nil:
		commit, err := g.peel(ctx, endpoint, head)
		return headRef, commit, err
	case tagErr == nil:
		commit, err := g.peel(ctx, endpoint, tag)
		return tagRef, commit, err
	default:
		return "", "", ErrSourceNotFound
	}
}

func (g *GitHubInspector) getRef(ctx context.Context, endpoint, ref string) (gitObject, error) {
	if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
		return gitObject{}, ErrInvalidRevision
	}
	var result struct {
		Ref    string    `json:"ref"`
		Object gitObject `json:"object"`
	}
	if err := g.get(ctx, endpoint+"/git/ref/"+strings.TrimPrefix(ref, "refs/"), &result); err != nil {
		return gitObject{}, err
	}
	if result.Ref != ref || !manifest.IsCommitSHA(result.Object.SHA) {
		return gitObject{}, ErrUpstream
	}
	return result.Object, nil
}

func (g *GitHubInspector) peel(ctx context.Context, endpoint string, object gitObject) (string, error) {
	for depth := 0; depth < 8; depth++ {
		if !manifest.IsCommitSHA(object.SHA) {
			return "", ErrUpstream
		}
		switch object.Type {
		case "commit":
			return object.SHA, nil
		case "tag":
			var result struct {
				SHA    string    `json:"sha"`
				Object gitObject `json:"object"`
			}
			if err := g.get(ctx, endpoint+"/git/tags/"+object.SHA, &result); err != nil {
				return "", err
			}
			if result.SHA != object.SHA {
				return "", ErrUpstream
			}
			object = result.Object
		default:
			return "", ErrSourceNotFound
		}
	}
	return "", ErrUpstream
}

func (g *GitHubInspector) inspectTree(ctx context.Context, endpoint, treeSHA string, result *Inspection) error {
	var tree struct {
		SHA       string `json:"sha"`
		Truncated bool   `json:"truncated"`
		Files     []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int    `json:"size"`
		} `json:"tree"`
	}
	if err := g.get(ctx, endpoint+"/git/trees/"+treeSHA, &tree); err != nil {
		return err
	}
	if tree.SHA != treeSHA || tree.Truncated {
		return ErrUpstream
	}
	container := false
	for _, f := range tree.Files {
		if f.Path == "csai-plugin.yaml" {
			if f.Type != "blob" || (f.Mode != "100644" && f.Mode != "100755") ||
				f.Size <= 0 || f.Size > manifest.MaxDocumentBytes || !manifest.IsCommitSHA(f.SHA) {
				return ErrManifestInvalid
			}
			if err := g.readManifest(ctx, endpoint, f.SHA, result); err != nil {
				return err
			}
			result.Evidence = append(result.Evidence, f.Path)
			continue
		}
		if f.Type != "blob" || (f.Mode != "100644" && f.Mode != "100755") {
			continue
		}
		switch f.Path {
		case "Dockerfile", "compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml":
			container = true
			result.Evidence = append(result.Evidence, f.Path)
		case "go.mod", "package.json", "pyproject.toml", "requirements.txt", "Cargo.toml", "pom.xml":
			result.Evidence = append(result.Evidence, f.Path)
		}
	}
	if container {
		result.Candidates = append(result.Candidates, "container")
	}
	return nil
}

func (g *GitHubInspector) readManifest(ctx context.Context, endpoint, blobSHA string, result *Inspection) error {
	var blob struct {
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int    `json:"size"`
	}
	if err := g.get(ctx, endpoint+"/git/blobs/"+blobSHA, &blob); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(blob.Content)
	if err != nil || blob.SHA != blobSHA || blob.Encoding != "base64" || len(raw) != blob.Size {
		return ErrUpstream
	}
	declaration, err := manifest.DecodeDeclaration(raw)
	if err != nil {
		return ErrManifestInvalid
	}
	repository, err := NormalizeGitHubRepository(declaration.Metadata.Repository)
	if err != nil || repository != result.Repository {
		return ErrManifestInvalid
	}
	result.Manifest = declaration.Canonical
	return nil
}

func (g *GitHubInspector) inspectRelease(ctx context.Context, endpoint string, result *Inspection) {
	var data struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	err := ErrSourceNotFound
	if name, tagged := strings.CutPrefix(result.ResolvedRef, "refs/tags/"); tagged {
		err = g.get(ctx, endpoint+"/releases/tags/"+url.PathEscape(name), &data)
	}
	if errors.Is(err, ErrSourceNotFound) {
		err = g.get(ctx, endpoint+"/releases/latest", &data)
	}
	switch {
	case errors.Is(err, ErrSourceNotFound):
		result.ReleaseState = "none"
	case err != nil:
		result.ReleaseState = "unavailable"
	default:
		result.ReleaseState = "found"
		result.Release = &Release{Tag: data.Tag, Assets: []string{}}
		hasPackage := false
		for _, asset := range data.Assets {
			if len(result.Release.Assets) == 32 {
				break
			}
			if len(asset.Name) > 0 && len(asset.Name) <= 240 && !strings.ContainsAny(asset.Name, "/\\\x00\r\n") {
				result.Release.Assets = append(result.Release.Assets, asset.Name)
				name := strings.ToLower(asset.Name)
				hasPackage = hasPackage || strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip")
			}
		}
		if hasPackage {
			result.Candidates = append([]string{"release"}, result.Candidates...)
		}
	}
}

// Only constant-host HTTPS metadata requests are used. Links supplied by the API
// response are never followed.
func (g *GitHubInspector) get(ctx context.Context, path string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, gitHubAPI+path, nil)
	if err != nil {
		return ErrUpstream
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if g.token != "" {
		request.Header.Set("Authorization", "Bearer "+g.token)
	}
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := g.client.Do(request)
	if err != nil {
		return ErrUpstream
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ErrSourceNotFound
	}
	if response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden && response.Header.Get("X-RateLimit-Remaining") == "0") {
		return ErrRateLimited
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w (GitHub status %d)", ErrUpstream, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponse+1))
	if err != nil || len(raw) > maxAPIResponse || json.Unmarshal(raw, out) != nil {
		return ErrUpstream
	}
	return nil
}
