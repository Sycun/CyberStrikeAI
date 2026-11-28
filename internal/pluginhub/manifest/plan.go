package manifest

import (
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

var hex256 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitSHA = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func IsCommitSHA(value string) bool {
	return commitSHA.MatchString(value)
}

type InstallPlan struct {
	Canonical            []byte    `json:"-"`
	Version              int       `json:"version"`
	ID                   string    `json:"id"`
	InstallID            string    `json:"installId"`
	ActorID              string    `json:"actorId"`
	ManifestSHA256       string    `json:"manifestSha256"`
	PolicySHA256         string    `json:"policySha256"`
	ConfigTemplateSHA256 string    `json:"configTemplateSha256,omitempty"`
	ExpiresAt            time.Time `json:"expiresAt"`
	Platform             Platform  `json:"platform"`
	Isolation            string    `json:"isolation"`
	Artifact             Artifact  `json:"artifact"`
}

type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type Artifact struct {
	Driver       string          `json:"driver"`
	AssetURL     string          `json:"assetURL,omitempty"`
	SHA256       string          `json:"sha256,omitempty"`
	Verification string          `json:"verification,omitempty"`
	Repository   string          `json:"repository,omitempty"`
	RequestedRef string          `json:"requestedRef,omitempty"`
	Commit       string          `json:"commit,omitempty"`
	Services     []LockedService `json:"services,omitempty"`
}

type LockedService struct {
	Name           string `json:"name"`
	RequestedImage string `json:"requestedImage"`
	Repository     string `json:"repository"`
	ManifestDigest string `json:"manifestDigest"`
	IndexDigest    string `json:"indexDigest,omitempty"`
}

func DecodeInstallPlan(data []byte) (*InstallPlan, error) {
	var plan InstallPlan
	canonical, err := decode(data, installPlanSchema, &plan)
	if err != nil {
		return nil, err
	}
	plan.Canonical = canonical
	if err := plan.validate(); err != nil {
		return nil, err
	}
	return &plan, nil
}

func (p InstallPlan) validate() error {
	for _, raw := range []string{p.ID, p.InstallID} {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil || raw != id.String() {
			return fmt.Errorf("expected a canonical non-empty UUID")
		}
	}
	switch a := p.Artifact; a.Driver {
	case "release":
		return ValidateHTTPS(a.AssetURL)
	case "git":
		if err := ValidateHTTPS(a.Repository); err != nil {
			return err
		}
		if err := ValidateGitRef(a.RequestedRef); err != nil {
			return err
		}
		if IsCommitSHA(a.RequestedRef) && a.RequestedRef != a.Commit {
			return fmt.Errorf("commit differs from the requested Git identity")
		}
	case "container":
		if p.Platform.OS != "linux" {
			return fmt.Errorf("locked container artifact requires a Linux image platform")
		}
		names := map[string]bool{}
		for _, service := range a.Services {
			if names[service.Name] {
				return fmt.Errorf("duplicate locked container service")
			}
			names[service.Name] = true
			if err := service.validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s LockedService) validate() error {
	repository, _, digest, err := ParseImageReference(s.RequestedImage)
	if err != nil {
		return err
	}
	if digest != "" && digest != s.ManifestDigest && digest != s.IndexDigest {
		return fmt.Errorf("locked image does not match the requested digest")
	}
	if s.Repository != repository {
		return fmt.Errorf("locked image repository differs from requested image")
	}
	return nil
}
