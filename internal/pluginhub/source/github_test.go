package source

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestPluginGitHubInspectorUsesOnlyPinnedMetadata(t *testing.T) {
	commit := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	blob := strings.Repeat("c", 40)
	tag := strings.Repeat("d", 40)
	raw, err := os.ReadFile("../manifest/testdata/web-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "https://git.example.org/examples/log-review-demo.git", "https://github.com/example/review"))
	prefix := "/repos/example/review"
	responses := map[string]string{
		prefix:                        `{"full_name":"Example/Review","default_branch":"main"}`,
		prefix + "/git/ref/tags/v1":   fmt.Sprintf(`{"ref":"refs/tags/v1","object":{"type":"tag","sha":%q}}`, tag),
		prefix + "/git/tags/" + tag:   fmt.Sprintf(`{"sha":%q,"object":{"type":"commit","sha":%q}}`, tag, commit),
		prefix + "/commits/" + commit: fmt.Sprintf(`{"sha":%q,"commit":{"tree":{"sha":%q}}}`, commit, tree),
		prefix + "/git/trees/" + tree: fmt.Sprintf(`{"sha":%q,"tree":[{"path":"csai-plugin.yaml","type":"blob","mode":"100644","sha":%q,"size":%d},{"path":"Dockerfile","type":"blob","mode":"100644"}]}`, tree, blob, len(raw)),
		prefix + "/git/blobs/" + blob: fmt.Sprintf(`{"sha":%q,"encoding":"base64","size":%d,"content":%q}`, blob, len(raw), base64.StdEncoding.EncodeToString(raw)),
		prefix + "/releases/tags/v1":  `{"tag_name":"v1","assets":[{"name":"review.tar.gz","browser_download_url":"http://127.0.0.1/private"}]}`,
	}
	calls := map[string]int{}
	inspector := NewGitHubInspector()
	inspector.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" || request.Method != http.MethodGet {
			t.Fatalf("unexpected metadata request %s %s", request.Method, request.URL)
		}
		calls[request.URL.Path]++
		response, ok := responses[request.URL.Path]
		if !ok && request.URL.Path != prefix+"/git/ref/heads/v1" {
			t.Fatalf("unexpected request: %s", request.URL)
		}
		status := http.StatusOK
		if !ok {
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: http.Header{}}, nil
	})
	result, err := inspector.Inspect(context.Background(), "https://github.com/Example/Review.git", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Repository != "https://github.com/example/review" || result.Commit != commit || result.ResolvedRef != "refs/tags/v1" ||
		len(result.Manifest) == 0 || strings.Join(result.Candidates, ",") != "release,git,container" {
		t.Fatalf("unexpected inspection: %+v", result)
	}
	if len(calls) != len(responses)+1 {
		t.Fatalf("unexpected metadata calls: %+v", calls)
	}

	responses[prefix+"/git/ref/heads/v1"] = fmt.Sprintf(`{"ref":"refs/heads/v1","object":{"type":"commit","sha":%q}}`, commit)
	if _, err := inspector.Inspect(context.Background(), "https://github.com/example/review", "v1"); !errors.Is(err, ErrAmbiguousRef) {
		t.Fatalf("expected an ambiguous ref error, got %v", err)
	}
}

func TestPluginGitHubInspectorRejectsURLsAndRedirects(t *testing.T) {
	inspector := NewGitHubInspector()
	calls := 0
	inspector.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")),
			Header: http.Header{"Location": []string{"http://127.0.0.1/private"}}}, nil
	})
	for _, input := range []string{
		"http://github.com/example/review", "https://github.com@127.0.0.1/example/review",
		"https://github.com:443/example/review", "https://github.com/example/%2e%2e",
		"https://github.com/example/review?url=http://127.0.0.1",
	} {
		if _, err := inspector.Inspect(context.Background(), input, ""); !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("accepted %q: %v", input, err)
		}
	}
	if _, err := inspector.Inspect(context.Background(), "https://github.com/example/review", "--upload-pack"); !errors.Is(err, ErrInvalidRevision) {
		t.Fatalf("accepted invalid revision: %v", err)
	}
	if calls != 0 {
		t.Fatal("invalid inputs reached the HTTP client")
	}
	if _, err := inspector.Inspect(context.Background(), "https://github.com/example/review", ""); !errors.Is(err, ErrUpstream) || calls != 1 {
		t.Fatalf("redirect followed or unexpectedly accepted: %d calls (%v)", calls, err)
	}
}
