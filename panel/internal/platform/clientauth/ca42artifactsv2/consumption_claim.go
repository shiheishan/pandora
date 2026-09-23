package ca42artifactsv2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const ConsumptionClaimFormat = "client-auth-00042-consumption-claim-v2"

const (
	consumptionClaimDomain = "PANDORA\x00CA42-CONSUMPTION-CLAIM\x00V2\x00"
	// nonceIDDomain is intentionally stable across claim-envelope versions so
	// a rolling upgrade cannot turn an already-consumed attestation nonce into
	// a new replay-store key.
	nonceIDDomain = "PANDORA\x00CA42-NONCE-ID\x00V1\x00"
)

var consumptionClaimFieldNames = [...]string{
	"format",
	"artifact_set_format",
	"artifact_set_binding_sha256",
	"profile_id",
	"profile_sha256",
	"release_id",
	"release_run_id",
	"attempt_id",
	"architecture",
	"release_manifest_sha256",
	"execution_plan_sha256",
	"trust_capsule_sha256",
	"expected_sha256",
	"attestation_sha256",
	"external_manifest_sha256",
	"credential_source_descriptor_sha256",
	"runtime_closure_manifest_sha256",
	"goose_build_info_sha256",
	"artifact_storage_descriptor_sha256",
	"attestation_nonce",
	"not_before_epoch",
	"not_after_epoch",
}

// ConsumptionClaim is the only capability from which the future global nonce
// store may derive a nonce identity. It cannot be constructed from CLI text or
// a caller-provided Snapshot.
type ConsumptionClaim struct {
	set       Set
	snapshot  Snapshot
	canonical []byte
	sha256    [sha256.Size]byte
	nonceID   string
	parsed    bool
}

// ConsumptionClaimAt derives a time-bound claim from a fully reverified Set.
func (set Set) ConsumptionClaimAt(now time.Time) (ConsumptionClaim, error) {
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return ConsumptionClaim{}, err
	}
	snapshot := verified.snapshot
	canonical, err := consumptionClaimCanonical(consumptionClaimValues(snapshot))
	if err != nil {
		return ConsumptionClaim{}, err
	}
	digest := domainSHA256(consumptionClaimDomain, canonical)
	return ConsumptionClaim{
		set: verified, snapshot: snapshot, canonical: canonical,
		sha256: digest, nonceID: nonceIDForSnapshot(snapshot), parsed: true,
	}, nil
}

func consumptionClaimValues(snapshot Snapshot) []string {
	return []string{
		ConsumptionClaimFormat,
		snapshot.Format,
		snapshot.BindingSHA256,
		snapshot.ProfileID,
		snapshot.ProfileSHA256,
		snapshot.ReleaseID,
		snapshot.ReleaseRunID,
		snapshot.AttemptID,
		snapshot.Architecture,
		snapshot.ReleaseManifestSHA256,
		snapshot.ExecutionPlanSHA256,
		snapshot.TrustCapsuleSHA256,
		snapshot.ExpectedSHA256,
		snapshot.AttestationSHA256,
		snapshot.ExternalManifestSHA256,
		snapshot.CredentialSourceDescriptorSHA256,
		snapshot.RuntimeClosureManifestSHA256,
		snapshot.GooseBuildInfoSHA256,
		snapshot.ArtifactStorageDescriptorSHA256,
		snapshot.AttestationNonce,
		strconv.FormatInt(snapshot.EffectiveNotBefore.Unix(), 10),
		strconv.FormatInt(snapshot.EffectiveNotAfter.Unix(), 10),
	}
}

func nonceIDForSnapshot(snapshot Snapshot) string {
	nonceID := domainSHA256(nonceIDDomain, []byte(snapshot.ProfileSHA256+"\n"+snapshot.AttestationNonce+"\n"))
	return hex.EncodeToString(nonceID[:])
}

// VerifiedCopyAt replays the whole artifact graph before reproducing the
// claim. Expired claims may be inspected only through durable store receipts,
// never reused as fresh authorization.
func (claim ConsumptionClaim) VerifiedCopyAt(now time.Time) (ConsumptionClaim, error) {
	if !claim.parsed || len(claim.canonical) == 0 || claim.sha256 == ([sha256.Size]byte{}) || claim.nonceID == "" {
		return ConsumptionClaim{}, errors.New("CA42 consumption claim capability invalid")
	}
	if domainSHA256(consumptionClaimDomain, claim.canonical) != claim.sha256 {
		return ConsumptionClaim{}, errors.New("CA42 consumption claim identity changed")
	}
	verified, err := claim.set.ConsumptionClaimAt(now)
	if err != nil {
		return ConsumptionClaim{}, err
	}
	if verified.sha256 != claim.sha256 || verified.nonceID != claim.nonceID {
		return ConsumptionClaim{}, errors.New("CA42 consumption claim binding changed")
	}
	return verified, nil
}

func (claim ConsumptionClaim) SHA256HexAt(now time.Time) (string, error) {
	verified, err := claim.VerifiedCopyAt(now)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(verified.sha256[:]), nil
}

func (claim ConsumptionClaim) NonceIDAt(now time.Time) (string, error) {
	verified, err := claim.VerifiedCopyAt(now)
	if err != nil {
		return "", err
	}
	return verified.nonceID, nil
}

// CanonicalBytesAt returns a defensive copy for durable intent hashing. A
// consumer must still accept the ConsumptionClaim capability, not these bytes,
// as its authority input.
func (claim ConsumptionClaim) CanonicalBytesAt(now time.Time) ([]byte, error) {
	verified, err := claim.VerifiedCopyAt(now)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), verified.canonical...), nil
}

func consumptionClaimCanonical(values []string) ([]byte, error) {
	if len(values) != len(consumptionClaimFieldNames) {
		return nil, errors.New("CA42 consumption claim field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00=") {
			return nil, errors.New("CA42 consumption claim value invalid")
		}
		output.WriteString(consumptionClaimFieldNames[index])
		output.WriteByte('=')
		output.WriteString(value)
		output.WriteByte('\n')
	}
	return []byte(output.String()), nil
}

func domainSHA256(domain string, payload []byte) [sha256.Size]byte {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(domain))
	_, _ = hasher.Write(payload)
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest
}
