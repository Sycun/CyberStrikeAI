package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"

	"cyberstrike-ai/internal/pluginhub/manifest"
)

func TestPluginReleaseChecksumAndImageIndex(t *testing.T) {
	const config = `{"architecture":"arm64","os":"linux"}`
	configDigest := "sha256:" + manifest.Digest([]byte(config))
	child := fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q},"layers":[]}`, configDigest)
	childDigest := "sha256:" + manifest.Digest([]byte(child))
	index := fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"digest":%q,"platform":{"architecture":"arm64","os":"linux","variant":"v8"}}]}`, childDigest)
	indexDigest := "sha256:" + manifest.Digest([]byte(index))
	visited := map[string]bool{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		visited[request.URL.Path] = true
		body, digest := "", ""
		switch request.URL.Path {
		case "/v2/example/web/manifests/" + indexDigest:
			body, digest = index, indexDigest
		case "/v2/example/web/manifests/" + childDigest:
			body, digest = child, childDigest
		case "/v2/example/web/blobs/" + configDigest:
			body = config
		case "/asset.tar.gz":
			body = "fixture archive"
		default:
			t.Fatalf("unexpected request: %s", request.URL.Path)
		}
		response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
		response.Header.Set("Docker-Content-Digest", digest)
		return response, nil
	})}
	egress := &Egress{client: client}
	ctx := context.Background()
	plan, err := egress.LockImage(ctx, manifest.Service{Name: "web", Image: "example/web:stable@" + indexDigest}, "linux", "arm64")
	if err != nil || plan.ManifestDigest != childDigest || plan.IndexDigest != indexDigest || plan.Repository != "example/web" {
		t.Fatalf("image lock %+v %v", plan, err)
	}
	if !visited["/v2/example/web/blobs/"+configDigest] {
		t.Fatal("image configuration did not establish the platform")
	}
	d := manifest.Distribution{Type: "release", Assets: []manifest.Asset{
		{OS: "darwin", Arch: "arm64", URL: "https://example.org/asset.tar.gz", SHA256: manifest.Digest([]byte("fixture archive"))},
	}}
	a, err := egress.LockRelease(ctx, d, manifest.Platform{OS: "darwin", Arch: "arm64"})
	if err != nil || a.Verification != "declared_checksum" || a.SHA256 != d.Assets[0].SHA256 || visited["/asset.tar.gz"] {
		t.Fatalf("declared checksum planning downloaded the archive or returned a bad lock: %+v %v", a, err)
	}
	d.Assets[0].SHA256 = ""
	observed, err := egress.LockRelease(ctx, d, manifest.Platform{OS: "darwin", Arch: "arm64"})
	if err != nil || !visited["/asset.tar.gz"] || observed.SHA256 != a.SHA256 || observed.Verification != "reviewed_observed_digest" {
		t.Fatalf("missing checksum did not trigger online locking: %+v %v", observed, err)
	}
}

func TestPluginGitHubMetadataUsesConfiguredToken(t *testing.T) {
	inspector := NewGitHubInspectorWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.github.com" || req.Header.Get("Authorization") != "Bearer test-placeholder" {
			t.Fatal("GitHub metadata authorization was not scoped to the API host")
		}
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"0"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}), "test-placeholder")
	if _, err := inspector.Inspect(context.Background(), "https://github.com/example/web", ""); err != ErrRateLimited {
		t.Fatalf("rate-limit diagnosis: %v", err)
	}
}

func TestPluginGHCRBlobRedirectAllowsDeliveryWithoutAuthorization(t *testing.T) {
	egress, err := NewEgress(config.PluginEgress{ProxyMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := http.NewRequest(http.MethodGet, "https://ghcr.io/v2/example/web/blobs/sha256:placeholder", nil)
	blob, _ := http.NewRequest(http.MethodGet, "https://pkg-containers.githubusercontent.com/blob", nil)
	blob.Header.Set("Authorization", "Bearer fixture-token")
	if err := egress.client.CheckRedirect(blob, []*http.Request{origin}); err != nil {
		t.Fatal(err)
	}
	if blob.Header.Get("Authorization") != "" {
		t.Fatal("registry credentials reached the blob delivery host")
	}
}
