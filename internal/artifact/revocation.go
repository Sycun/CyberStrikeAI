package artifact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RevokedEntry is one revocation record. Digest-level revocation is what makes a
// specific build unreinstallable; publisher-level revocation stops every artifact
// from that publisher.
type RevokedEntry struct {
	Reason     string    `json:"reason"`
	RevokedAt  time.Time `json:"revokedAt"`
	References string    `json:"references,omitempty"`
}

// Revocations is the client-enforced block list. Checking happens twice - at
// install/load and before every invocation - because push-only revocation was
// shown to be insufficient.
type Revocations struct {
	mu         sync.RWMutex
	Digests    map[string]RevokedEntry `json:"digests"`
	Publishers map[string]RevokedEntry `json:"publishers"`
}

func NewRevocations() *Revocations {
	return &Revocations{
		Digests:    map[string]RevokedEntry{},
		Publishers: map[string]RevokedEntry{},
	}
}

// LoadRevocations reads the block list. A missing file is an empty list, not an
// error: no revocations published yet is a valid state, a corrupt file is not.
func LoadRevocations(path string) (*Revocations, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewRevocations(), nil
		}
		return nil, fmt.Errorf("artifact: read revocation list: %w", err)
	}
	list := NewRevocations()
	if err := json.Unmarshal(data, list); err != nil {
		return nil, fmt.Errorf("artifact: parse revocation list: %w", err)
	}
	if list.Digests == nil {
		list.Digests = map[string]RevokedEntry{}
	}
	if list.Publishers == nil {
		list.Publishers = map[string]RevokedEntry{}
	}
	return list, nil
}

// Merge folds a refreshed list in, returning how many entries were new. Revoked
// entries are never removed by a merge: an operator edits the file to un-revoke.
func (r *Revocations) Merge(next *Revocations) int {
	if r == nil || next == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	added := 0
	for digest, entry := range next.Digests {
		if _, exists := r.Digests[digest]; !exists {
			r.Digests[digest] = entry
			added++
		}
	}
	for publisher, entry := range next.Publishers {
		if _, exists := r.Publishers[publisher]; !exists {
			r.Publishers[publisher] = entry
			added++
		}
	}
	return added
}

// Check reports whether this bundle is revoked.
func (r *Revocations) Check(bundle Bundle) error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	digest := bundle.Manifest.Digest()
	if entry, ok := r.Digests[digest]; ok {
		return fmt.Errorf("%w: digest %s (%s)", ErrRevoked, short(digest), entry.Reason)
	}
	if entry, ok := r.Publishers[strings.TrimSpace(bundle.Manifest.Publisher)]; ok {
		return fmt.Errorf("%w: publisher %s (%s)", ErrRevoked, bundle.Manifest.Publisher, entry.Reason)
	}
	if bundle.Signature != nil {
		if entry, ok := r.Publishers[bundle.Signature.KeyID]; ok {
			return fmt.Errorf("%w: signing key %s (%s)", ErrRevoked, bundle.Signature.KeyID, entry.Reason)
		}
	}
	return nil
}

// CheckProvenance is the pre-invocation form of the check: it works from the
// identity recorded on an installed capability instead of requiring the whole
// bundle, so enforcement does not stop at install time.
func (r *Revocations) CheckProvenance(publisher, digest string) error {
	if r == nil {
		return nil
	}
	publisher = strings.TrimSpace(publisher)
	digest = strings.TrimSpace(digest)
	if digest == "" && publisher == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if digest != "" {
		if entry, ok := r.Digests[digest]; ok {
			return fmt.Errorf("%w: digest %s (%s)", ErrRevoked, short(digest), entry.Reason)
		}
	}
	if publisher != "" {
		if entry, ok := r.Publishers[publisher]; ok {
			return fmt.Errorf("%w: publisher %s (%s)", ErrRevoked, publisher, entry.Reason)
		}
	}
	return nil
}

// RevokedDigests and RevokedPublishers expose the current sets for reporting.
func (r *Revocations) RevokedDigests() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.Digests))
	for digest := range r.Digests {
		out = append(out, digest)
	}
	sort.Strings(out)
	return out
}

func (r *Revocations) RevokedPublishers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.Publishers))
	for publisher := range r.Publishers {
		out = append(out, publisher)
	}
	sort.Strings(out)
	return out
}

func short(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

// Quarantine moves a submitted artifact aside instead of deleting it: the reference
// package index keeps quarantined items visible for triage, which is what makes a
// revocation reviewable after the fact.
func Quarantine(artifactDir, destination string, id string) (string, error) {
	source := filepath.Join(artifactDir, id)
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("artifact: quarantine source: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("artifact: quarantine source %s is not a directory", source)
	}
	target := filepath.Join(destination, fmt.Sprintf("%s-%d", id, time.Now().Unix()))
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return "", err
	}
	if err := os.Rename(source, target); err != nil {
		return "", fmt.Errorf("artifact: quarantine move: %w", err)
	}
	return target, nil
}
