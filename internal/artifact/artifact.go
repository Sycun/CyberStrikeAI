// Package artifact is the review pipeline's data plane: how a submitted capability
// is identified, authenticated, compared against what a human already approved,
// and revoked after the fact.
//
// Two rules from the research report are structural here rather than configurable:
// there is no "ignore unverified" escape valve, and a submission can never widen
// the capability set an operator approved - an increase in class or grants always
// routes back to a human.
package artifact

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Manifest is what a publisher submits. It mirrors the capability manifest fields
// that carry security meaning, because those are what a reviewer approves.
type Manifest struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Title       string    `json:"title,omitempty"`
	Class       string    `json:"class"`
	Permission  string    `json:"permission"`
	Approval    string    `json:"approval,omitempty"`
	Runtime     string    `json:"runtime"`
	Grants      []string  `json:"grants,omitempty"`
	Evidence    bool      `json:"evidence,omitempty"`
	Payloads    []File    `json:"payloads"`
	Publisher   string    `json:"publisher"`
	SubmittedAt time.Time `json:"submittedAt"`
}

// File is one content file inside an artifact.
type File struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Bytes  int    `json:"bytes"`
	// Text carries the reviewable source. Executable artifacts must be readable:
	// code that can reach operator credentials cannot be obfuscated, which is the
	// deliberate opposite of the reference product's encrypted plugins.
	Text string `json:"text,omitempty"`
}

// ErrUnsigned is returned for an artifact with no valid signature.
var ErrUnsigned = errors.New("artifact: signature is missing or invalid")

// ErrUntrustedPublisher means the signing key is not in the operator's trust store.
var ErrUntrustedPublisher = errors.New("artifact: publisher key is not trusted")

// ErrRevoked means the artifact or its publisher is on the revocation list.
var ErrRevoked = errors.New("artifact: revoked by the registry")

// CanonicalBytes is the signed form: sorted keys, no whitespace variance. The
// signature covers identity plus every security-relevant field, so editing a class
// or a grant set invalidates it.
func (m Manifest) CanonicalBytes() []byte {
	type canonicalFile struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
		Bytes  int    `json:"bytes"`
	}
	type canonical struct {
		ID         string          `json:"id"`
		Version    string          `json:"version"`
		Class      string          `json:"class"`
		Permission string          `json:"permission"`
		Approval   string          `json:"approval"`
		Runtime    string          `json:"runtime"`
		Grants     []string        `json:"grants"`
		Evidence   bool            `json:"evidence"`
		Publisher  string          `json:"publisher"`
		Payloads   []canonicalFile `json:"payloads"`
	}
	payloads := make([]canonicalFile, 0, len(m.Payloads))
	for _, file := range m.Payloads {
		payloads = append(payloads, canonicalFile{Path: file.Path, Digest: file.Digest, Bytes: file.Bytes})
	}
	grants := append([]string(nil), m.Grants...)
	sort.Strings(grants)
	doc, err := json.Marshal(canonical{
		ID: strings.TrimSpace(m.ID), Version: strings.TrimSpace(m.Version), Class: strings.TrimSpace(m.Class),
		Permission: strings.TrimSpace(m.Permission), Approval: strings.TrimSpace(m.Approval),
		Runtime: strings.TrimSpace(m.Runtime), Grants: grants, Evidence: m.Evidence,
		Publisher: strings.TrimSpace(m.Publisher), Payloads: payloads,
	})
	if err != nil {
		return []byte(m.ID)
	}
	return doc
}

// Digest is the artifact identity used by the revocation list. Pinning the digest
// is what makes a revoked build unreinstallable even under a new version tag.
func (m Manifest) Digest() string {
	sum := sha256.Sum256(m.CanonicalBytes())
	return hex.EncodeToString(sum[:])
}

// Signature is a detached Ed25519 signature over the canonical bytes.
type Signature struct {
	Publisher string `json:"publisher"`
	KeyID     string `json:"keyId"`
	Sig       string `json:"sig"`
	SignedAt  string `json:"signedAt"`
}

// Bundle is a signed artifact as it travels through the pipeline or an offline pack.
type Bundle struct {
	Manifest  Manifest   `json:"manifest"`
	Signature *Signature `json:"signature"`
}

// Key is a publisher signing key entry in the trust store.
type Key struct {
	KeyID     string
	Publisher string
	Public    ed25519.PublicKey
	AddedAt   time.Time
	Revoked   bool
}

// TrustStore holds the publisher keys an operator trusts. Only keys added here can
// authenticate an artifact, and there is deliberately no option to accept an
// unverified one: the reference product's ignoreUnverified flag is the failure mode
// we are not copying.
type TrustStore struct {
	keys        map[string]Key
	byPublisher map[string][]string
}

func NewTrustStore() *TrustStore {
	return &TrustStore{keys: map[string]Key{}, byPublisher: map[string][]string{}}
}

// AddKey registers a publisher key. Re-adding a revoked key is refused.
func (t *TrustStore) AddKey(key Key) error {
	if strings.TrimSpace(key.KeyID) == "" || len(key.Public) != ed25519.PublicKeySize {
		return fmt.Errorf("artifact: invalid publisher key")
	}
	if key.Revoked {
		return fmt.Errorf("artifact: refusing to re-add revoked key %s", key.KeyID)
	}
	if existing, ok := t.keys[key.KeyID]; ok && existing.Revoked {
		return fmt.Errorf("artifact: key %s was revoked", key.KeyID)
	}
	t.keys[key.KeyID] = key
	t.byPublisher[key.Publisher] = append(t.byPublisher[key.Publisher], key.KeyID)
	return nil
}

// RevokeKey marks a key untrusted; artifacts signed with it stop verifying.
func (t *TrustStore) RevokeKey(keyID string) {
	key, ok := t.keys[keyID]
	if !ok {
		return
	}
	key.Revoked = true
	t.keys[keyID] = key
}

// Lookup returns a trusted key.
func (t *TrustStore) Lookup(keyID string) (Key, bool) {
	key, ok := t.keys[keyID]
	if !ok || key.Revoked {
		return Key{}, false
	}
	return key, true
}

// Verify authenticates a bundle against the trust store.
func (t *TrustStore) Verify(bundle Bundle) error {
	if bundle.Signature == nil {
		return ErrUnsigned
	}
	key, ok := t.Lookup(bundle.Signature.KeyID)
	if !ok {
		return ErrUntrustedPublisher
	}
	if key.Publisher != bundle.Manifest.Publisher {
		return fmt.Errorf("%w: key %s belongs to %s, artifact claims %s",
			ErrUntrustedPublisher, key.KeyID, key.Publisher, bundle.Manifest.Publisher)
	}
	signature, err := hex.DecodeString(strings.TrimSpace(bundle.Signature.Sig))
	if err != nil {
		return fmt.Errorf("%w: signature is not hex", ErrUnsigned)
	}
	if !ed25519.Verify(key.Public, bundle.Manifest.CanonicalBytes(), signature) {
		return ErrUnsigned
	}
	if err := validateManifestShape(bundle.Manifest); err != nil {
		return err
	}
	return nil
}

func validateManifestShape(m Manifest) error {
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.Version) == "" {
		return errors.New("artifact: id and version are required")
	}
	switch strings.ToLower(strings.TrimSpace(m.Class)) {
	case "readonly", "mutating", "destructive":
	default:
		return fmt.Errorf("artifact: unknown class %q", m.Class)
	}
	if len(m.Payloads) == 0 {
		return errors.New("artifact: an artifact with no payload files cannot be reviewed")
	}
	for _, file := range m.Payloads {
		if strings.TrimSpace(file.Path) == "" || len(file.Digest) != 64 {
			return fmt.Errorf("artifact: payload %q has no content digest", file.Path)
		}
	}
	return nil
}

// Sign produces the detached signature for a manifest with a publisher private key.
// The registry side signs; the client only ever verifies.
func Sign(m Manifest, keyID string, private ed25519.PrivateKey) (*Signature, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, errors.New("artifact: invalid private key")
	}
	return &Signature{
		Publisher: m.Publisher,
		KeyID:     keyID,
		Sig:       hex.EncodeToString(ed25519.Sign(private, m.CanonicalBytes())),
		SignedAt:  time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// GenerateKey makes a publisher key pair; used by registry tooling and tests.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// Delta is the difference between a submission and the last approved version.
type Delta struct {
	NewGrants         []string
	RemovedGrants     []string
	ClassEscalated    bool
	FromClass         string
	ToClass           string
	PermissionChanged bool
	NewVersion        bool
}

// RequiresHumanReReview reports whether any capability increased. This is the
// mechanism the report identifies as the only one proven to catch destructive
// submissions, and the only way a small team survives the volume.
func (d Delta) RequiresHumanReReview() bool {
	return len(d.NewGrants) > 0 || d.ClassEscalated || d.PermissionChanged
}

// Reasons returns the human-readable justification for re-review.
func (d Delta) Reasons() []string {
	var out []string
	if len(d.NewGrants) > 0 {
		out = append(out, fmt.Sprintf("new capabilities: %s", strings.Join(d.NewGrants, ", ")))
	}
	if d.ClassEscalated {
		out = append(out, fmt.Sprintf("class raised from %s to %s", d.FromClass, d.ToClass))
	}
	if d.PermissionChanged {
		out = append(out, "permission key changed")
	}
	return out
}

// classRank orders classes by blast radius so escalation is detectable.
func classRank(class string) int {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case "readonly":
		return 0
	case "mutating":
		return 1
	case "destructive":
		return 2
	default:
		return -1
	}
}

// Diff compares a submission against the previously approved manifest. A missing
// baseline means first publication, which always needs review.
func Diff(submitted, approved *Manifest) Delta {
	if submitted == nil || approved == nil {
		delta := Delta{NewVersion: true}
		if submitted != nil {
			delta.NewGrants = sortedCopy(submitted.Grants)
			delta.ToClass = submitted.Class
		}
		return delta
	}

	known := map[string]bool{}
	for _, grant := range approved.Grants {
		known[canonicalGrant(grant)] = true
	}
	for _, grant := range submitted.Grants {
		delete(known, canonicalGrant(grant))
	}

	delta := Delta{
		NewGrants:         []string{},
		RemovedGrants:     []string{},
		PermissionChanged: strings.TrimSpace(submitted.Permission) != strings.TrimSpace(approved.Permission),
		NewVersion:        strings.TrimSpace(submitted.Version) != strings.TrimSpace(approved.Version),
		FromClass:         approved.Class,
		ToClass:           submitted.Class,
		ClassEscalated:    classRank(submitted.Class) > classRank(approved.Class),
	}
	previous := map[string]bool{}
	for _, grant := range approved.Grants {
		previous[canonicalGrant(grant)] = true
	}
	for _, grant := range submitted.Grants {
		if !previous[canonicalGrant(grant)] {
			delta.NewGrants = append(delta.NewGrants, grant)
		}
	}
	current := map[string]bool{}
	for _, grant := range submitted.Grants {
		current[canonicalGrant(grant)] = true
	}
	for _, grant := range approved.Grants {
		if !current[canonicalGrant(grant)] {
			delta.RemovedGrants = append(delta.RemovedGrants, grant)
		}
	}
	sort.Strings(delta.NewGrants)
	sort.Strings(delta.RemovedGrants)
	return delta
}

func canonicalGrant(grant string) string {
	grant = strings.ToLower(strings.TrimSpace(grant))
	grant = strings.ReplaceAll(grant, " ", "")
	return grant
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// ContentDigest hashes artifact payload text for the static-scan and quarantine
// bookkeeping.
func ContentDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
