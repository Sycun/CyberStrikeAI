package deepassessment

import "cyberstrike-ai/internal/surfaceingest"

// Scope and surface types stay available to the assessment package. The
// parser itself lives in the shared surfaceingest package.
type (
	ScopeSeed       = surfaceingest.ScopeSeed
	SurfaceEndpoint = surfaceingest.SurfaceEndpoint
	SurfaceHit      = surfaceingest.SurfaceHit
)

func ExtractSurface(text string, seeds []ScopeSeed) ([]SurfaceHit, bool) {
	return surfaceingest.ExtractSurface(text, seeds)
}

func ExtractDeclaredTargets(text string) []SurfaceHit {
	return surfaceingest.ExtractDeclaredTargets(text)
}

func ExcludedTargets(objective, scope string) []ScopeSeed {
	return surfaceingest.ExcludedTargets(objective, scope)
}

func SeedsFromHits(hits []SurfaceHit) []ScopeSeed {
	return surfaceingest.SeedsFromHits(hits)
}

func WithoutExcluded(hits []SurfaceHit, excluded []ScopeSeed) []SurfaceHit {
	return surfaceingest.WithoutExcluded(hits, excluded)
}
