package nodefabric

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
)

const (
	// EffectiveReleaseContract is the v1 wire-domain separator frozen by the
	// amended NODE-ADR-00048-v2. It is also the config_contract sent on the wire.
	EffectiveReleaseContract = "aegis-node-effective-config-release-v1"

	// EffectiveReleaseMaxDeliveryWindow prevents a valid release envelope from
	// becoming a long-lived replay credential.
	EffectiveReleaseMaxDeliveryWindow = 10 * time.Minute
)

// EffectiveReleaseSignatureFields contains only the immutable fields covered by
// the effective-release Ed25519 signature. Payload and source-manifest bytes are
// bound through their independently recomputed SHA-256 values.
type EffectiveReleaseSignatureFields struct {
	TenantID           string
	NodeID             string
	ReleaseID          string
	Generation         uint64
	ContentHash        string
	SourceManifestHash string
	KeyID              string
	IssuedAt           time.Time
	ExpiresAt          time.Time
}

func canonicalEffectiveReleaseUUID(field, value string) (string, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return "", fmt.Errorf("%s must be a canonical lowercase UUID", field)
	}
	return value, nil
}

func canonicalEffectiveReleaseHash(field, value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 || base64.StdEncoding.EncodeToString(raw) != value {
		return "", fmt.Errorf("%s must be canonical base64 for exactly 32 bytes", field)
	}
	return value, nil
}

func canonicalEffectiveReleaseKeyID(value string) (string, error) {
	if value == "" || strings.Contains(value, "=") {
		return "", errors.New("key_id must be non-empty base64url without padding")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 8 || base64.RawURLEncoding.EncodeToString(raw) != value {
		return "", errors.New("key_id must be canonical base64url for exactly 8 bytes without padding")
	}
	return value, nil
}

// EffectiveReleaseSignaturePreimage returns the exact LF-terminated byte record
// signed by the control plane and independently rebuilt by the node agent.
func EffectiveReleaseSignaturePreimage(in EffectiveReleaseSignatureFields) ([]byte, error) {
	tenantID, err := canonicalEffectiveReleaseUUID("tenant_id", in.TenantID)
	if err != nil {
		return nil, err
	}
	nodeID, err := canonicalEffectiveReleaseUUID("node_id", in.NodeID)
	if err != nil {
		return nil, err
	}
	releaseID, err := canonicalEffectiveReleaseUUID("release_id", in.ReleaseID)
	if err != nil {
		return nil, err
	}
	if in.Generation == 0 || in.Generation > math.MaxInt64 {
		return nil, errors.New("generation must be a positive PostgreSQL bigint")
	}
	contentHash, err := canonicalEffectiveReleaseHash("content_sha256", in.ContentHash)
	if err != nil {
		return nil, err
	}
	sourceHash, err := canonicalEffectiveReleaseHash("source_manifest_sha256", in.SourceManifestHash)
	if err != nil {
		return nil, err
	}
	keyID, err := canonicalEffectiveReleaseKeyID(in.KeyID)
	if err != nil {
		return nil, err
	}
	if in.IssuedAt.IsZero() || in.ExpiresAt.IsZero() {
		return nil, errors.New("issued_at and expires_at are required")
	}
	issuedAt := in.IssuedAt.UTC()
	expiresAt := in.ExpiresAt.UTC()
	if issuedAt.Nanosecond()%1000 != 0 || expiresAt.Nanosecond()%1000 != 0 {
		return nil, errors.New("effective release timestamps must use PostgreSQL microsecond precision")
	}
	if !expiresAt.After(issuedAt) {
		return nil, errors.New("expires_at must be after issued_at")
	}
	if expiresAt.Sub(issuedAt) > EffectiveReleaseMaxDeliveryWindow {
		return nil, errors.New("effective release delivery window exceeds ten minutes")
	}

	var b strings.Builder
	b.Grow(420)
	b.WriteString(EffectiveReleaseContract)
	b.WriteByte('\n')
	b.WriteString("tenant_id=")
	b.WriteString(tenantID)
	b.WriteByte('\n')
	b.WriteString("node_id=")
	b.WriteString(nodeID)
	b.WriteByte('\n')
	b.WriteString("release_id=")
	b.WriteString(releaseID)
	b.WriteByte('\n')
	b.WriteString("generation=")
	b.WriteString(strconv.FormatUint(in.Generation, 10))
	b.WriteByte('\n')
	b.WriteString("content_sha256=")
	b.WriteString(contentHash)
	b.WriteByte('\n')
	b.WriteString("source_manifest_sha256=")
	b.WriteString(sourceHash)
	b.WriteByte('\n')
	b.WriteString("key_id=")
	b.WriteString(keyID)
	b.WriteByte('\n')
	b.WriteString("issued_at=")
	b.WriteString(issuedAt.Format(time.RFC3339Nano))
	b.WriteByte('\n')
	b.WriteString("expires_at=")
	b.WriteString(expiresAt.Format(time.RFC3339Nano))
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

// ParseEffectiveReleaseTimestamp rejects alternative spellings before they are
// converted to time.Time and lose their original wire representation.
func ParseEffectiveReleaseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("effective release timestamp must be canonical UTC RFC3339Nano")
	}
	if parsed.Nanosecond()%1000 != 0 {
		return time.Time{}, errors.New("effective release timestamp must use PostgreSQL microsecond precision")
	}
	return parsed, nil
}

// SignEffectiveRelease signs the exact v1 preimage and emits canonical standard
// base64. It refuses a key ID that does not identify the provided signer.
func SignEffectiveRelease(signer *platformcrypto.Signer, in EffectiveReleaseSignatureFields) (string, error) {
	if signer == nil {
		return "", errors.New("effective release signer is required")
	}
	if in.KeyID != signer.KeyID() {
		return "", errors.New("effective release key_id does not match signer")
	}
	preimage, err := EffectiveReleaseSignaturePreimage(in)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signer.Sign(preimage)), nil
}

// VerifyEffectiveReleaseSignature validates the canonical envelope, delivery
// time bounds and Ed25519 signature. Future and expired envelopes are rejected;
// callers that need clock tolerance must explicitly adjust the supplied now.
func VerifyEffectiveReleaseSignature(pub ed25519.PublicKey, expectedKeyID string, in EffectiveReleaseSignatureFields, signature string, now time.Time) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("effective release public key is invalid")
	}
	expectedKeyID, err := canonicalEffectiveReleaseKeyID(expectedKeyID)
	if err != nil {
		return err
	}
	publicSum := sha256.Sum256(pub)
	derivedKeyID := base64.RawURLEncoding.EncodeToString(publicSum[:8])
	if expectedKeyID != derivedKeyID || in.KeyID != expectedKeyID {
		return errors.New("effective release key_id does not match pinned public key")
	}
	preimage, err := EffectiveReleaseSignaturePreimage(in)
	if err != nil {
		return err
	}
	if now.IsZero() {
		return errors.New("verification time is required")
	}
	now = now.UTC()
	if now.Before(in.IssuedAt.UTC()) {
		return errors.New("effective release is not yet valid")
	}
	if !now.Before(in.ExpiresAt.UTC()) {
		return errors.New("effective release has expired")
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(sig) != signature {
		return errors.New("effective release signature must be canonical base64 Ed25519")
	}
	if !platformcrypto.Verify(pub, preimage, sig) {
		return errors.New("effective release signature verification failed")
	}
	return nil
}
