package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"cyberstrike-ai/internal/pluginhub/manifest"
)

var ErrArtifactUnavailable = errors.New("plugin artifact unavailable for selected platform")

func (e *Egress) LockRelease(ctx context.Context, d manifest.Distribution, platform manifest.Platform) (manifest.Artifact, error) {
	for _, asset := range d.Assets {
		if asset.OS != platform.OS || asset.Arch != platform.Arch {
			continue
		}
		digest := asset.SHA256
		method := "declared_checksum"
		if digest == "" {
			var err error
			digest, err = e.Download(ctx, asset.URL, io.Discard)
			if err != nil {
				return manifest.Artifact{}, err
			}
			method = "reviewed_observed_digest"
		} else if !validRegistryDigest("sha256:" + digest) {
			return manifest.Artifact{}, fmt.Errorf("invalid declared release checksum")
		}
		return manifest.Artifact{Driver: "release", AssetURL: asset.URL, SHA256: digest, Verification: method}, nil
	}
	return manifest.Artifact{}, ErrArtifactUnavailable
}

type manifestDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Platform  struct {
		OS           string   `json:"os"`
		Architecture string   `json:"architecture"`
		Variant      string   `json:"variant"`
		OSFeatures   []string `json:"os.features"`
	} `json:"platform"`
}

type registryDocument struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType"`
	Manifests     []manifestDescriptor `json:"manifests"`
	Config        manifestDescriptor   `json:"config"`
	Layers        []manifestDescriptor `json:"layers"`
}

const registryAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

func (e *Egress) LockImage(ctx context.Context, service manifest.Service, os, arch string) (manifest.LockedService, error) {
	repo, tag, digest, err := manifest.ParseImageReference(service.Image)
	if err != nil {
		return manifest.LockedService{}, err
	}
	host, path := normalizeRegistry(repo)
	base := "https://" + host + "/v2/" + path
	reference := tag
	if digest != "" {
		reference = digest
	}
	response, err := e.registryFetch(ctx, base+"/manifests/"+reference, path, registryAccept, "")
	if err != nil {
		return manifest.LockedService{}, err
	}
	doc, actual, err := verifyRegistryDocument(response, digest)
	if err != nil {
		return manifest.LockedService{}, err
	}
	entry := manifest.LockedService{Name: service.Name, Repository: repo, RequestedImage: service.Image}
	isIndex := doc.MediaType == "application/vnd.oci.image.index.v1+json" || doc.MediaType == "application/vnd.docker.distribution.manifest.list.v2+json" || len(doc.Manifests) > 0
	if isIndex {
		entry.IndexDigest = actual
		digest, err = selectPlatform(doc.Manifests, os, arch)
		if err != nil {
			return manifest.LockedService{}, err
		}
		response, err = e.registryFetch(ctx, base+"/manifests/"+digest, path, registryAccept, response.token)
		if err != nil {
			return manifest.LockedService{}, err
		}
		doc, actual, err = verifyRegistryDocument(response, digest)
		if err != nil {
			return manifest.LockedService{}, err
		}
	}
	if !validRegistryDigest(doc.Config.Digest) || len(doc.Manifests) != 0 || doc.Layers == nil {
		return manifest.LockedService{}, fmt.Errorf("registry manifest does not describe a runnable image")
	}
	cfg, err := e.registryFetch(ctx, base+"/blobs/"+doc.Config.Digest, path, "", response.token)
	if err != nil {
		return manifest.LockedService{}, err
	}
	if "sha256:"+manifest.Digest(cfg.body) != doc.Config.Digest {
		return manifest.LockedService{}, fmt.Errorf("registry image configuration checksum mismatch")
	}
	var platform struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	}
	if json.Unmarshal(cfg.body, &platform) != nil || platform.OS != os || platform.Architecture != arch {
		return manifest.LockedService{}, ErrArtifactUnavailable
	}
	entry.ManifestDigest = actual
	return entry, nil
}

func selectPlatform(descriptors []manifestDescriptor, os, arch string) (string, error) {
	found := ""
	for _, desc := range descriptors {
		if desc.Platform.OS != os || desc.Platform.Architecture != arch || len(desc.Platform.OSFeatures) != 0 {
			continue
		}
		if desc.Platform.Variant != "" && !(arch == "arm64" && desc.Platform.Variant == "v8") {
			continue
		}
		if !validRegistryDigest(desc.Digest) || (found != "" && found != desc.Digest) {
			return "", fmt.Errorf("ambiguous or invalid registry platform descriptor")
		}
		found = desc.Digest
	}
	if found == "" {
		return "", ErrArtifactUnavailable
	}
	return found, nil
}

type registryResponse struct {
	body   []byte
	digest string
	token  string
}

var registryDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var registryAuthField = regexp.MustCompile(`([a-z]+)="([^"]+)"`)

func validRegistryDigest(s string) bool { return registryDigest.MatchString(s) }

func verifyRegistryDocument(res registryResponse, pinned string) (registryDocument, string, error) {
	var document registryDocument
	actual := "sha256:" + manifest.Digest(res.body)
	if (pinned != "" && pinned != actual) || (res.digest != "" && res.digest != actual) {
		return document, "", fmt.Errorf("registry manifest checksum mismatch")
	}
	if json.Unmarshal(res.body, &document) != nil || document.SchemaVersion != 2 {
		return document, "", fmt.Errorf("unsupported registry manifest")
	}
	return document, actual, nil
}

func (e *Egress) registryFetch(ctx context.Context, address, repository, accept, token string) (registryResponse, error) {
	get := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return e.client.Do(req)
	}
	res, err := get(token)
	if err != nil {
		return registryResponse{}, fmt.Errorf("registry endpoint unavailable")
	}
	if res.StatusCode == http.StatusUnauthorized {
		challenge := res.Header.Get("WWW-Authenticate")
		res.Body.Close()
		token, err = e.registryToken(ctx, challenge, repository)
		if err != nil {
			return registryResponse{}, err
		}
		res, err = get(token)
		if err != nil {
			return registryResponse{}, fmt.Errorf("registry endpoint unavailable after challenge")
		}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return registryResponse{}, ErrArtifactUnavailable
	}
	if res.StatusCode != http.StatusOK {
		return registryResponse{}, fmt.Errorf("registry response rejected (HTTP %d)", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20+1))
	if err != nil || len(body) > 4<<20 {
		return registryResponse{}, fmt.Errorf("registry response exceeds metadata limit")
	}
	return registryResponse{body: body, digest: res.Header.Get("Docker-Content-Digest"), token: token}, nil
}

func (e *Egress) registryToken(ctx context.Context, challenge, repository string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") || len(challenge) > 4096 {
		return "", fmt.Errorf("unsupported registry authentication challenge")
	}
	fields := map[string]string{}
	for _, pair := range registryAuthField.FindAllStringSubmatch(challenge, -1) {
		fields[pair[1]] = pair[2]
	}
	realm, err := url.Parse(fields["realm"])
	scope := "repository:" + repository + ":pull"
	if err != nil || realm.Scheme != "https" || realm.User != nil || realm.Fragment != "" || (fields["scope"] != "" && fields["scope"] != scope) {
		return "", fmt.Errorf("invalid registry pull challenge")
	}
	params := url.Values{"scope": {scope}}
	if fields["service"] != "" {
		params.Set("service", fields["service"])
	}
	realm.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", fmt.Errorf("invalid registry token endpoint")
	}
	res, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry token endpoint unavailable")
	}
	defer res.Body.Close()
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&token) != nil {
		return "", fmt.Errorf("registry pull token unavailable")
	}
	if token.Token == "" {
		token.Token = token.AccessToken
	}
	if token.Token == "" || strings.ContainsAny(token.Token, "\r\n\x00") {
		return "", fmt.Errorf("invalid registry pull token")
	}
	return token.Token, nil
}
