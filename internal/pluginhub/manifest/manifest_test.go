package manifest

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func declarationFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/web-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPluginManifestDeclaration(t *testing.T) {
	raw := declarationFixture(t)
	got, err := DecodeDeclaration([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata.ID != "log-review" || got.LLM.Selection != "multi" || len(got.Distribution) != 3 {
		t.Fatalf("unexpected declaration: %#v", got.Metadata)
	}
	fromJSON, err := DecodeDeclaration(got.Canonical)
	if err != nil || Digest(fromJSON.Canonical) != Digest(got.Canonical) {
		t.Fatalf("canonical declaration changed after decoding JSON: %v", err)
	}
}

func TestPluginManifestNativeEnvironmentAndSingleActive(t *testing.T) {
	raw := declarationFixture(t)
	raw = strings.Replace(raw, "\n  entrypoint: log-review\n", `
  entrypoint: log-review
  staticEnv: {APP_TITLE: Review}
  secretEnv:
    APP_PASSWORD_FILE: {kind: file, secretRef: db-password}
`, 1)
	raw = strings.Replace(raw, "llm:\n", "llm:\n  selection: single_active\n", 1)
	for _, suffix := range []string{"BASE_URL", "TOKEN", "MODEL", "PROVIDER"} {
		raw = strings.ReplaceAll(raw, "REVIEW_OPENAI_"+suffix, "REVIEW_"+suffix)
		raw = strings.ReplaceAll(raw, "REVIEW_ANTHROPIC_"+suffix, "REVIEW_"+suffix)
	}
	got, err := DecodeDeclaration([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.Launch.StaticEnv["APP_TITLE"] != "Review" || got.Launch.SecretEnv["APP_PASSWORD_FILE"].SecretRef != "db-password" {
		t.Fatal("native environment not decoded")
	}
	for _, test := range []struct{ protocol, url string }{{OpenAI, "gateway_v1_url"}, {Anthropic, "gateway_root_url"}} {
		bindings, err := got.LLM.ActiveBindings(test.protocol)
		if err != nil || len(bindings) != 1 || bindings[0].Env["REVIEW_BASE_URL"] != test.url {
			t.Fatalf("wrong active binding: %+v, %v", bindings, err)
		}
	}
	if _, err := got.LLM.ActiveBindings(""); err == nil {
		t.Fatal("single-active selection accepted a missing protocol")
	}
	for _, test := range []struct{ name, declaration string }{
		{"multi collision", strings.Replace(raw, "single_active", "multi", 1)},
		{"undeclared secret", strings.Replace(raw, "APP_PASSWORD_FILE: {kind: file, secretRef: db-password}", "APP_PASSWORD_FILE: {kind: file, secretRef: missing}", 1)},
		{"reserved native env", strings.Replace(raw, "APP_TITLE", "PATH", 1)},
		{"collision with runtime", strings.Replace(raw, "APP_TITLE", "REVIEW_MODEL", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeDeclaration([]byte(test.declaration)); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}

func TestPluginManifestRejectsUnsafeOrInvalidDeclarations(t *testing.T) {
	raw := declarationFixture(t)
	for _, test := range []struct{ name, document string }{
		{"duplicate key", raw + "\nkind: WebPlugin\n"},
		{"multiple documents", raw + "\n---\n{}"},
		{"anchor", strings.Replace(raw, "kind: WebPlugin", "kind: &name WebPlugin", 1)},
		{"tag", strings.Replace(raw, "kind: WebPlugin", "kind: !!str WebPlugin", 1)},
		{"unknown field", raw + "\nunknown: true\n"},
		{"missing required launch", strings.Replace(raw, "launch:", "unexpected:", 1)},
		{"ambiguous path", strings.Replace(raw, "healthPath: /healthz", "healthPath: /../healthz", 1)},
		{"nonportable path", strings.Replace(raw, "entrypoint: log-review.exe", "entrypoint: NUL.exe", 1)},
		{"unapproved URL credentials", strings.ReplaceAll(raw, "https://git.example.org", "https://user:secret@git.example.org")},
		{"loopback URL", strings.ReplaceAll(raw, "https://git.example.org", "https://127.0.0.1")},
		{"bad Git ref", strings.Replace(raw, "refs/tags/v1.0.0", "refs/tags/../main", 1)},
		{"implicit OCI tag", strings.Replace(raw, "log-review:1.0.0", "log-review", 1)},
		{"wrong protocol URL", strings.Replace(raw, "gateway_root_url", "gateway_v1_url", 1)},
		{"nested dependency cycle", strings.Replace(raw, "role: dependency", "role: dependency\n        depends_on: {review: service_started}", 1)},
		{"privileged service", strings.Replace(raw, "containerPort: 8080", "containerPort: 8080\n        privileged: true", 1)},
		{"unexpected gateway egress", strings.Replace(raw, "managed_dependency, managed_model_gateway", "managed_dependency", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeDeclaration([]byte(test.document)); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}

func TestPluginManifestLockedPlans(t *testing.T) {
	base := InstallPlan{
		Version: 1, ID: "348e8ed0-1534-4bb1-b28b-390e315f739e",
		InstallID: "0eb46cec-7a99-4c60-9c4a-b237860e6cca", ActorID: "admin",
		ManifestSHA256: strings.Repeat("a", 64), PolicySHA256: strings.Repeat("b", 64),
		ExpiresAt: time.Now().UTC().Add(time.Minute), Platform: Platform{OS: "linux", Arch: "amd64"}, Isolation: "strict",
	}
	verify := func(a Artifact, valid bool) {
		t.Helper()
		base.Artifact = a
		raw, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		checked, err := DecodeInstallPlan(raw)
		if (err == nil) != valid || (err == nil && len(checked.Canonical) == 0) {
			t.Fatalf("artifact %+v: err=%v want valid=%v", a, err, valid)
		}
	}
	verify(Artifact{Driver: "release", AssetURL: "https://example.org/asset.tgz", SHA256: strings.Repeat("c", 64), Verification: "declared_checksum"}, true)
	git := Artifact{Driver: "git", Repository: "https://github.com/example/repo", RequestedRef: "refs/tags/v1", Commit: strings.Repeat("d", 40)}
	verify(git, true)
	git.Commit = "refs/tags/v1"
	verify(git, false)
	digest := "sha256:" + strings.Repeat("e", 64)
	container := Artifact{Driver: "container", Services: []LockedService{
		{Name: "web", RequestedImage: "example.org/app:v1", Repository: "example.org/app", ManifestDigest: digest},
	}}
	verify(container, true)
	container.Services[0].Repository = "example.org/different"
	verify(container, false)
}
