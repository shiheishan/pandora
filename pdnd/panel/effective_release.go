package panel

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
)

const effectiveReleaseContract = "aegis-node-effective-config-release-v1"

func effectiveReleasePreimage(cfg *SignedConfig) ([]byte, error) {
	if cfg == nil || cfg.ConfigContract != effectiveReleaseContract {
		return nil, errors.New("unsupported effective config contract")
	}
	for field, value := range map[string]string{
		"tenant_id": cfg.TenantID, "node_id": cfg.NodeID, "release_id": cfg.ReleaseID,
	} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return nil, fmt.Errorf("%s must be a canonical lowercase UUID", field)
		}
	}
	if cfg.Generation == 0 || cfg.Generation > math.MaxInt64 {
		return nil, errors.New("generation must be a positive PostgreSQL bigint")
	}
	for field, value := range map[string]string{
		"content_sha256": cfg.ContentSHA256, "source_manifest_sha256": cfg.SourceManifestSHA256,
	} {
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(raw) != sha256.Size || base64.StdEncoding.EncodeToString(raw) != value {
			return nil, fmt.Errorf("%s must be canonical base64 for exactly 32 bytes", field)
		}
	}
	key, err := base64.RawURLEncoding.DecodeString(cfg.KeyID)
	if err != nil || len(key) != 8 || base64.RawURLEncoding.EncodeToString(key) != cfg.KeyID {
		return nil, errors.New("key_id must be canonical base64url for exactly 8 bytes")
	}
	issued, expires := cfg.IssuedAt.UTC(), cfg.ExpiresAt.UTC()
	if issued.IsZero() || expires.IsZero() || issued.Nanosecond()%1000 != 0 || expires.Nanosecond()%1000 != 0 {
		return nil, errors.New("effective release timestamps must use microsecond precision")
	}
	if !expires.After(issued) || expires.Sub(issued) > 10*time.Minute {
		return nil, errors.New("invalid effective release delivery window")
	}
	var b strings.Builder
	b.WriteString(effectiveReleaseContract + "\n")
	b.WriteString("tenant_id=" + cfg.TenantID + "\n")
	b.WriteString("node_id=" + cfg.NodeID + "\n")
	b.WriteString("release_id=" + cfg.ReleaseID + "\n")
	b.WriteString("generation=" + strconv.FormatUint(cfg.Generation, 10) + "\n")
	b.WriteString("content_sha256=" + cfg.ContentSHA256 + "\n")
	b.WriteString("source_manifest_sha256=" + cfg.SourceManifestSHA256 + "\n")
	b.WriteString("key_id=" + cfg.KeyID + "\n")
	b.WriteString("issued_at=" + issued.Format(time.RFC3339Nano) + "\n")
	b.WriteString("expires_at=" + expires.Format(time.RFC3339Nano) + "\n")
	return []byte(b.String()), nil
}

func (c *SignedClient) verifyEffectiveConfig(cfg *SignedConfig) error {
	if c.identity.ConfigKeyID == "" || cfg.KeyID != c.identity.ConfigKeyID {
		return errors.New("config key id mismatch")
	}
	if cfg.NodeID != c.identity.NodeID {
		return errors.New("effective config node identity mismatch")
	}
	pub, err := base64.StdEncoding.DecodeString(c.identity.ConfigPublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid config public key")
	}
	keySum := sha256.Sum256(pub)
	if base64.RawURLEncoding.EncodeToString(keySum[:8]) != cfg.KeyID {
		return errors.New("config key id does not match pinned public key")
	}
	contentSum := sha256.Sum256(cfg.Payload)
	if base64.StdEncoding.EncodeToString(contentSum[:]) != cfg.ContentSHA256 || cfg.Hash != cfg.ContentSHA256 {
		return errors.New("effective config content hash mismatch")
	}
	manifestSum := sha256.Sum256(cfg.SourceManifest)
	if base64.StdEncoding.EncodeToString(manifestSum[:]) != cfg.SourceManifestSHA256 {
		return errors.New("effective config source manifest hash mismatch")
	}
	preimage, err := effectiveReleasePreimage(cfg)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(cfg.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(sig) != cfg.Signature ||
		!ed25519.Verify(ed25519.PublicKey(pub), preimage, sig) {
		return errors.New("effective config signature invalid")
	}
	now := time.Now().UTC()
	if now.Before(cfg.IssuedAt.UTC()) {
		return errors.New("effective config is not yet valid")
	}
	if !now.Before(cfg.ExpiresAt.UTC()) {
		return errors.New("effective config expired")
	}
	return nil
}
