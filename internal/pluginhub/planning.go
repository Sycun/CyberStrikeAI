// Package pluginhub manages the lifecycle of reviewed plugin applications.
package pluginhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/pluginhub/manifest"
	"cyberstrike-ai/internal/pluginhub/source"

	"github.com/google/uuid"
)

var (
	ErrCatalogManifest    = errors.New("catalog entry has no executable distribution declaration")
	ErrManifestRequired   = errors.New("a reviewed declaration is required")
	ErrInvalidManifest    = errors.New("invalid plugin declaration")
	ErrSourceMismatch     = errors.New("manifest repository or revision differs from inspection")
	ErrUnapprovedArtifact = errors.New("the locked plugin version has not been approved")
	ErrRuntimeUnavailable = errors.New("plugin operation worker is not available")
	ErrInvalidRequest     = errors.New("invalid plugin operation or installation request")
	ErrStalePlan          = errors.New("plugin plan failed integrity or scope validation")
)

type PlanRequest struct {
	Actor, InspectionID, CatalogID, InstallID, Driver, Isolation string
	Manifest                                                     json.RawMessage
}

type PlanResult struct {
	Record         database.PluginPlanRecord
	ArtifactDigest string
	HostApproved   bool
}

type planPolicy struct {
	CatalogID      string               `json:"catalog_id,omitempty"`
	Isolation      string               `json:"isolation"`
	Permissions    manifest.Permissions `json:"permissions"`
	ArtifactDigest string               `json:"artifact_digest"`
}

type ArtifactLocker interface {
	LockRelease(context.Context, manifest.Distribution, manifest.Platform) (manifest.Artifact, error)
	LockImage(context.Context, manifest.Service, string, string) (manifest.LockedService, error)
}

func (m *Manager) CreatePlan(ctx context.Context, request PlanRequest) (PlanResult, error) {
	if (request.InspectionID == "") == (request.CatalogID == "") || request.Actor == "" ||
		(request.Driver != "" && !slices.Contains([]string{"auto", "release", "git", "container"}, request.Driver)) {
		return PlanResult{}, ErrInvalidRequest
	}
	if request.Isolation == "" {
		request.Isolation = "standard"
	}
	if request.Isolation != "standard" && request.Isolation != "strict" {
		return PlanResult{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	now := time.Now().UTC()
	var inspected source.Inspection
	if request.CatalogID != "" {
		if len(request.Manifest) != 0 {
			return PlanResult{}, ErrInvalidRequest
		}
		data, ok := m.CatalogManifest(ctx, request.CatalogID)
		if !ok {
			return PlanResult{}, ErrCatalogManifest
		}
		request.Manifest = data
	} else {
		payload, err := m.db.GetPluginInspection(ctx, request.InspectionID, request.Actor, now)
		if err != nil {
			return PlanResult{}, err
		}
		if err := json.Unmarshal([]byte(payload), &inspected); err != nil {
			return PlanResult{}, err
		}
		if len(request.Manifest) == 0 {
			request.Manifest = inspected.Manifest
		}
	}
	if len(request.Manifest) == 0 {
		return PlanResult{}, ErrManifestRequired
	}
	declaration, err := manifest.DecodeDeclaration(request.Manifest)
	if err != nil {
		return PlanResult{}, ErrInvalidManifest
	}
	repository, err := source.NormalizeGitHubRepository(declaration.Metadata.Repository)
	if err != nil || (inspected.Repository != "" && inspected.Repository != repository) {
		return PlanResult{}, ErrSourceMismatch
	}
	if request.InstallID != "" {
		install, err := m.db.RuntimePluginInstall(ctx, request.InstallID)
		if err != nil {
			return PlanResult{}, err
		}
		if install.Source != repository || install.PluginID != declaration.Metadata.ID || install.Observed == "removed" {
			return PlanResult{}, ErrSourceMismatch
		}
	} else {
		request.InstallID = uuid.NewString()
	}
	a, platform, err := m.lockArtifact(ctx, declaration, inspected, repository, request)
	if err != nil {
		return PlanResult{}, err
	}
	artifactDigest := ArtifactDigest(a)
	policy, err := json.Marshal(planPolicy{CatalogID: request.CatalogID, Isolation: request.Isolation,
		Permissions: declaration.Permissions, ArtifactDigest: artifactDigest})
	if err != nil {
		return PlanResult{}, err
	}
	now = time.Now().UTC()
	if request.CatalogID != "" {
		request.InspectionID = uuid.NewString()
		payload, err := json.Marshal(source.Inspection{Repository: repository, Manifest: declaration.Canonical})
		if err != nil {
			return PlanResult{}, err
		}
		if err := m.db.SavePluginInspection(ctx, request.InspectionID, request.Actor, string(payload), now, now.Add(10*time.Minute)); err != nil {
			return PlanResult{}, err
		}
	}
	plan := manifest.InstallPlan{
		Version: 1, ID: uuid.NewString(), InstallID: request.InstallID, ActorID: request.Actor,
		ManifestSHA256: manifest.Digest(declaration.Canonical), PolicySHA256: manifest.Digest(policy),
		ExpiresAt: now.Add(5 * time.Minute), Platform: platform, Isolation: request.Isolation, Artifact: a,
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return PlanResult{}, err
	}
	checked, err := manifest.DecodeInstallPlan(raw)
	if err != nil {
		return PlanResult{}, err
	}
	record := database.PluginPlanRecord{ID: plan.ID, InstallID: plan.InstallID, ActorUserID: plan.ActorID, InspectionID: request.InspectionID,
		ManifestJSON: string(declaration.Canonical), ManifestDigest: plan.ManifestSHA256, PolicyJSON: string(policy),
		PlanJSON: string(checked.Canonical), PlanDigest: manifest.Digest(checked.Canonical), ExpiresAt: plan.ExpiresAt}
	if err := m.db.SavePluginPlan(ctx, record, now); err != nil {
		return PlanResult{}, err
	}
	return PlanResult{Record: record, ArtifactDigest: artifactDigest, HostApproved: m.approved(plan.ManifestSHA256, artifactDigest)}, nil
}

func (m *Manager) lockArtifact(ctx context.Context, doc *manifest.Declaration, inspected source.Inspection, repo string, request PlanRequest) (manifest.Artifact, manifest.Platform, error) {
	auto := request.Driver == "" || request.Driver == "auto"
	priority := []string{"release", "git", "container"}
	if auto {
		var available, unavailable []string
		for _, name := range priority {
			hasDistribution := slices.ContainsFunc(doc.Distribution, func(d manifest.Distribution) bool {
				return d.Type == name
			})
			if !hasDistribution || (request.Isolation == "strict" && name != "container") {
				continue
			}
			probe, cancel := context.WithTimeout(ctx, 3*time.Second)
			usable := m.drivers[name].Probe(probe, request.Isolation).Available
			cancel()
			if usable {
				available = append(available, name)
			} else {
				unavailable = append(unavailable, name)
			}
		}
		priority = append(available, unavailable...)
	}
	var firstError error
	for _, driver := range priority {
		if (!auto && request.Driver != driver) || (request.Isolation == "strict" && driver != "container") {
			continue
		}
		for _, distribution := range doc.Distribution {
			if distribution.Type != driver {
				continue
			}
			platform := manifest.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
			var artifact manifest.Artifact
			var err error
			switch driver {
			case "release":
				artifact, err = m.locker.LockRelease(ctx, distribution, platform)
			case "git":
				artifact, err = lockGit(distribution, repo, inspected)
			case "container":
				platform.OS = "linux"
				artifact.Driver = "container"
				for _, service := range distribution.Services {
					var serviceLock manifest.LockedService
					serviceLock, err = m.locker.LockImage(ctx, service, platform.OS, platform.Arch)
					if err != nil {
						break
					}
					artifact.Services = append(artifact.Services, serviceLock)
				}
			}
			if err == nil {
				return artifact, platform, nil
			}
			if !auto || errors.Is(err, ErrSourceMismatch) {
				return manifest.Artifact{}, platform, err
			}
			if firstError == nil {
				firstError = err
			}
		}
	}
	if firstError == nil {
		firstError = source.ErrArtifactUnavailable
	}
	return manifest.Artifact{}, manifest.Platform{}, firstError
}

func lockGit(distribution manifest.Distribution, canonical string, inspected source.Inspection) (manifest.Artifact, error) {
	repo, err := source.NormalizeGitHubRepository(distribution.URL)
	rev := distribution.Revision
	if err != nil || repo != canonical || !manifest.IsCommitSHA(inspected.Commit) ||
		(rev != "" && rev != inspected.Commit && rev != inspected.ResolvedRef &&
			rev != strings.TrimPrefix(strings.TrimPrefix(inspected.ResolvedRef, "refs/heads/"), "refs/tags/")) {
		return manifest.Artifact{}, fmt.Errorf("%w: repeat inspection using the declared ref", ErrSourceMismatch)
	}
	return manifest.Artifact{Driver: "git", Repository: repo, Commit: inspected.Commit, RequestedRef: inspected.ResolvedRef}, nil
}

// ArtifactDigest identifies the complete ordered lock, including OCI child manifests.
func ArtifactDigest(a manifest.Artifact) string {
	raw, _ := json.Marshal(a)
	return manifest.Digest(raw)
}

func (m *Manager) approved(document, artifact string) bool {
	return slices.Contains(m.approvals[document], artifact)
}

func (m *Manager) ApprovalPolicy() string {
	return m.approvalPolicy
}

func (m *Manager) CanConfirm(declaration, artifact string) bool {
	return m.approvalPolicy == config.PluginApprovalAdminConfirm || m.approved(declaration, artifact)
}
