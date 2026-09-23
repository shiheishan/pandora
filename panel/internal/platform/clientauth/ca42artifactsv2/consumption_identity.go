package ca42artifactsv2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const consumptionIdentityDomain = "PANDORA\x00CA42-CONSUMPTION-IDENTITY\x00V1\x00"

// ConsumptionIdentity is an opaque, time-bound projection of a fully
// reverified ConsumptionClaim. Future nonce/journal coordinators must accept
// this capability and call VerifiedCopyAt; they must not accept Values,
// Snapshot, canonical bytes, hashes, or caller scalars as authority.
type ConsumptionIdentity struct {
	claim       ConsumptionClaim
	values      ConsumptionIdentityValues
	fingerprint [sha256.Size]byte
	mintedAt    time.Time
	parsed      bool
}

// ConsumptionIdentityValues is a forgeable diagnostic projection. It exists
// so protocol packages can encode a capability after re-verifying the owning
// ConsumptionIdentity. A Values value by itself grants no persistence,
// execution, recovery, journal, nonce, or admission authority.
type ConsumptionIdentityValues struct {
	ClaimFormat              string
	ClaimSHA256              string
	ClaimCanonicalSHA256     string
	NonceID                  string
	ArtifactSetFormat        string
	ArtifactSetBindingSHA256 string
	ProfileID                string
	ProfileSHA256            string
	ReleaseID                string
	ReleaseRunID             string
	AttemptID                string
	Architecture             string
	EffectiveNotBefore       time.Time
	EffectiveNotAfter        time.Time
}

// IdentityAt derives an opaque consumer identity only from a live claim. It
// replays the complete artifact graph through ConsumptionClaim.VerifiedCopyAt.
func (claim ConsumptionClaim) IdentityAt(now time.Time) (ConsumptionIdentity, error) {
	verified, err := claim.VerifiedCopyAt(now)
	if err != nil {
		return ConsumptionIdentity{}, err
	}
	canonicalDigest := sha256.Sum256(verified.canonical)
	values := ConsumptionIdentityValues{
		ClaimFormat:              ConsumptionClaimFormat,
		ClaimSHA256:              hex.EncodeToString(verified.sha256[:]),
		ClaimCanonicalSHA256:     hex.EncodeToString(canonicalDigest[:]),
		NonceID:                  verified.nonceID,
		ArtifactSetFormat:        verified.snapshot.Format,
		ArtifactSetBindingSHA256: verified.snapshot.BindingSHA256,
		ProfileID:                verified.snapshot.ProfileID,
		ProfileSHA256:            verified.snapshot.ProfileSHA256,
		ReleaseID:                verified.snapshot.ReleaseID,
		ReleaseRunID:             verified.snapshot.ReleaseRunID,
		AttemptID:                verified.snapshot.AttemptID,
		Architecture:             verified.snapshot.Architecture,
		EffectiveNotBefore:       verified.snapshot.EffectiveNotBefore.UTC(),
		EffectiveNotAfter:        verified.snapshot.EffectiveNotAfter.UTC(),
	}
	mintedAt := now.UTC()
	fingerprint, err := consumptionIdentityFingerprint(values, mintedAt)
	if err != nil {
		return ConsumptionIdentity{}, err
	}
	return ConsumptionIdentity{claim: verified, values: values, fingerprint: fingerprint, mintedAt: mintedAt, parsed: true}, nil
}

// VerifiedCopyAt replays the original claim and compares the complete opaque
// identity. Expired claims cannot be revived from an earlier identity.
func (identity ConsumptionIdentity) VerifiedCopyAt(now time.Time) (ConsumptionIdentity, error) {
	if !identity.parsed || identity.fingerprint == ([sha256.Size]byte{}) || identity.mintedAt.IsZero() || now.UTC().Before(identity.mintedAt) {
		return ConsumptionIdentity{}, errors.New("CA42 consumption identity capability invalid")
	}
	fingerprint, err := consumptionIdentityFingerprint(identity.values, identity.mintedAt)
	if err != nil || fingerprint != identity.fingerprint {
		return ConsumptionIdentity{}, errors.New("CA42 consumption identity changed")
	}
	verified, err := identity.claim.IdentityAt(now)
	if err != nil {
		return ConsumptionIdentity{}, err
	}
	if verified.fingerprint != identity.fingerprint {
		return ConsumptionIdentity{}, errors.New("CA42 consumption identity binding changed")
	}
	return verified, nil
}

// ValuesAt returns a value copy only after re-verifying the owning capability.
// The returned projection is diagnostic/encoding data and is never authority.
func (identity ConsumptionIdentity) ValuesAt(now time.Time) (ConsumptionIdentityValues, error) {
	verified, err := identity.VerifiedCopyAt(now)
	if err != nil {
		return ConsumptionIdentityValues{}, err
	}
	return verified.values, nil
}

func consumptionIdentityFingerprint(values ConsumptionIdentityValues, mintedAt time.Time) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	fields := []string{
		values.ClaimFormat, values.ClaimSHA256, values.ClaimCanonicalSHA256, values.NonceID,
		values.ArtifactSetFormat, values.ArtifactSetBindingSHA256, values.ProfileID, values.ProfileSHA256,
		values.ReleaseID, values.ReleaseRunID, values.AttemptID, values.Architecture,
		strconv.FormatInt(values.EffectiveNotBefore.UTC().Unix(), 10),
		strconv.FormatInt(values.EffectiveNotAfter.UTC().Unix(), 10),
		strconv.FormatInt(mintedAt.UTC().Unix(), 10),
	}
	for _, value := range fields {
		if value == "" || strings.ContainsAny(value, "\r\n\x00=") {
			return empty, errors.New("CA42 consumption identity value invalid")
		}
	}
	if values.ClaimFormat != ConsumptionClaimFormat || values.ArtifactSetFormat != Format ||
		values.EffectiveNotBefore.IsZero() || !values.EffectiveNotAfter.After(values.EffectiveNotBefore) || mintedAt.IsZero() ||
		mintedAt.Before(values.EffectiveNotBefore) || !mintedAt.Before(values.EffectiveNotAfter) {
		return empty, errors.New("CA42 consumption identity semantics invalid")
	}
	canonical := []byte(strings.Join(fields, "\n") + "\n")
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(consumptionIdentityDomain))
	_, _ = hasher.Write(canonical)
	var result [sha256.Size]byte
	copy(result[:], hasher.Sum(nil))
	return result, nil
}
