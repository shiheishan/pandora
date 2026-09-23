package clientauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

const keyringEpochDomain = "AEGIS-CLIENT-KEYRING-EPOCH-V1"

type KeyStatus string

const (
	KeyActive     KeyStatus = "active"
	KeyVerifyOnly KeyStatus = "verify-only"
	KeyRetired    KeyStatus = "retired"
)

type KeyMetadata struct {
	KID             string
	Version         int32
	Status          KeyStatus
	Algorithm       string
	Purpose         string
	CreatedAt       time.Time
	ActivatedAt     time.Time
	VerifyOnlySince *time.Time
	RetiredAt       *time.Time
}

type keyEntry struct {
	metadata KeyMetadata
	material [32]byte
}

type JWTKeyring struct {
	entries []keyEntry
	active  int
}

type AEADKeyring struct {
	entries []keyEntry
	active  int
}

type Keyrings struct {
	JWT       JWTKeyring
	AEAD      AEADKeyring
	epochB64U string
}

func LoadKeyrings(jwtJSON, aeadJSON []byte) (*Keyrings, error) {
	jwt, err := LoadJWTKeyring(jwtJSON)
	if err != nil {
		return nil, fmt.Errorf("JWT keyring: %w", err)
	}
	aead, err := LoadAEADKeyring(aeadJSON)
	if err != nil {
		return nil, fmt.Errorf("AEAD keyring: %w", err)
	}
	kids := make(map[string]struct{}, len(jwt.entries)+len(aead.entries))
	materials := make(map[[32]byte]struct{}, len(jwt.entries)+len(aead.entries))
	for _, entry := range append(append([]keyEntry(nil), jwt.entries...), aead.entries...) {
		if _, exists := kids[entry.metadata.KID]; exists {
			return nil, fmt.Errorf("%w: kid is reused across keyrings", ErrMalformed)
		}
		kids[entry.metadata.KID] = struct{}{}
		if _, exists := materials[entry.material]; exists {
			return nil, fmt.Errorf("%w: decoded key material is reused", ErrMalformed)
		}
		materials[entry.material] = struct{}{}
	}
	epoch := KeyringEpoch(jwtJSON, aeadJSON)
	return &Keyrings{
		JWT:       jwt,
		AEAD:      aead,
		epochB64U: EncodeBase64URLNoPad(epoch[:]),
	}, nil
}

func (rings *Keyrings) EpochB64U() (string, error) {
	if rings == nil || rings.epochB64U == "" {
		return "", fmt.Errorf("%w: uninitialized keyrings", ErrMalformed)
	}
	return rings.epochB64U, nil
}

// ValidateKeyringTransition validates one conservative offline rotation step.
// Every historical entry remains present, the previous active becomes
// verify-only, and exactly one later active entry is appended to each ring.
// Retirement/destruction requires a separately reviewed controller contract;
// this snapshot-only helper intentionally rejects it rather than losing history.
func ValidateKeyringTransition(previous, next *Keyrings) error {
	if previous == nil || next == nil {
		return fmt.Errorf("%w: nil keyring transition", ErrMalformed)
	}
	if err := validateRingTransition(previous.JWT.entries, next.JWT.entries); err != nil {
		return fmt.Errorf("JWT transition: %w", err)
	}
	if err := validateRingTransition(previous.AEAD.entries, next.AEAD.entries); err != nil {
		return fmt.Errorf("AEAD transition: %w", err)
	}
	if err := validateCrossRingHistory(previous, next); err != nil {
		return err
	}
	return nil
}

func validateRingTransition(previous, next []keyEntry) error {
	if len(previous) == 0 || len(next) != len(previous)+1 {
		return fmt.Errorf("%w: rotation must append exactly one key", ErrMalformed)
	}
	nextByKID := make(map[string]keyEntry, len(next))
	nextByVersion := make(map[int32]keyEntry, len(next))
	for _, entry := range next {
		nextByKID[entry.metadata.KID] = entry
		nextByVersion[entry.metadata.Version] = entry
	}
	previousKIDs := make(map[string]struct{}, len(previous))
	maxVersion := previous[0].metadata.Version
	maxCreated := previous[0].metadata.CreatedAt
	maxActivated := previous[0].metadata.ActivatedAt
	previousActive := 0
	for _, old := range previous {
		previousKIDs[old.metadata.KID] = struct{}{}
		if old.metadata.Version > maxVersion {
			maxVersion = old.metadata.Version
		}
		if old.metadata.CreatedAt.After(maxCreated) {
			maxCreated = old.metadata.CreatedAt
		}
		if old.metadata.ActivatedAt.After(maxActivated) {
			maxActivated = old.metadata.ActivatedAt
		}
		if old.metadata.Status == KeyActive {
			previousActive++
		}
		current, exists := nextByKID[old.metadata.KID]
		if !exists {
			return fmt.Errorf("%w: historical key removed", ErrMalformed)
		}
		if current.metadata.Version != old.metadata.Version || current.metadata.Algorithm != old.metadata.Algorithm || current.metadata.Purpose != old.metadata.Purpose ||
			current.metadata.CreatedAt != old.metadata.CreatedAt || current.metadata.ActivatedAt != old.metadata.ActivatedAt || current.material != old.material {
			return fmt.Errorf("%w: existing key identity or material changed", ErrMalformed)
		}
		if byVersion := nextByVersion[old.metadata.Version]; byVersion.metadata.KID != old.metadata.KID {
			return fmt.Errorf("%w: existing version reassigned", ErrMalformed)
		}
		if err := validateRotationStatusTransition(old.metadata, current.metadata); err != nil {
			return err
		}
	}
	if previousActive != 1 {
		return fmt.Errorf("%w: previous ring has no unique active key", ErrMalformed)
	}
	newCount := 0
	for _, entry := range next {
		if _, existed := previousKIDs[entry.metadata.KID]; existed {
			continue
		}
		newCount++
		if entry.metadata.Status != KeyActive || entry.metadata.Version <= maxVersion || !entry.metadata.CreatedAt.After(maxCreated) || !entry.metadata.ActivatedAt.After(maxActivated) {
			return fmt.Errorf("%w: new key must be a strictly later active", ErrMalformed)
		}
	}
	if newCount != 1 {
		return fmt.Errorf("%w: rotation requires exactly one new active key", ErrMalformed)
	}
	return nil
}

func validateRotationStatusTransition(previous, next KeyMetadata) error {
	switch previous.Status {
	case KeyActive:
		if next.Status == KeyVerifyOnly && next.VerifyOnlySince != nil && next.RetiredAt == nil && next.VerifyOnlySince.After(previous.ActivatedAt) {
			return nil
		}
	case KeyVerifyOnly:
		if next.Status == KeyVerifyOnly && equalOptionalTime(previous.VerifyOnlySince, next.VerifyOnlySince) && next.RetiredAt == nil {
			return nil
		}
	case KeyRetired:
		if next.Status == KeyRetired && equalOptionalTime(previous.VerifyOnlySince, next.VerifyOnlySince) && equalOptionalTime(previous.RetiredAt, next.RetiredAt) {
			return nil
		}
	}
	return fmt.Errorf("%w: illegal key lifecycle transition", ErrMalformed)
}

func validateCrossRingHistory(previous, next *Keyrings) error {
	type ringIdentity uint8
	const (
		jwtRing ringIdentity = iota + 1
		aeadRing
	)
	previousKIDs := make(map[string]ringIdentity, len(previous.JWT.entries)+len(previous.AEAD.entries))
	previousMaterials := make(map[[32]byte]ringIdentity, len(previous.JWT.entries)+len(previous.AEAD.entries))
	for _, item := range []struct {
		ring    ringIdentity
		entries []keyEntry
	}{{jwtRing, previous.JWT.entries}, {aeadRing, previous.AEAD.entries}} {
		for _, entry := range item.entries {
			previousKIDs[entry.metadata.KID] = item.ring
			previousMaterials[entry.material] = item.ring
		}
	}
	for _, item := range []struct {
		ring    ringIdentity
		entries []keyEntry
	}{{jwtRing, next.JWT.entries}, {aeadRing, next.AEAD.entries}} {
		for _, entry := range item.entries {
			if previousRing, existed := previousKIDs[entry.metadata.KID]; existed && previousRing != item.ring {
				return fmt.Errorf("%w: kid migrated across keyrings", ErrMalformed)
			}
			if previousRing, existed := previousMaterials[entry.material]; existed && previousRing != item.ring {
				return fmt.Errorf("%w: decoded material migrated across keyrings", ErrMalformed)
			}
		}
	}
	return nil
}

func equalOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func KeyringEpoch(jwtJSON, aeadJSON []byte) [32]byte {
	input := make([]byte, 0, len(keyringEpochDomain)+2+len(jwtJSON)+len(aeadJSON))
	input = append(input, keyringEpochDomain...)
	input = append(input, 0)
	input = append(input, jwtJSON...)
	input = append(input, 0)
	input = append(input, aeadJSON...)
	return sha256.Sum256(input)
}

func (ring JWTKeyring) Active() (KeyMetadata, error) {
	if ring.active < 0 || ring.active >= len(ring.entries) {
		return KeyMetadata{}, fmt.Errorf("%w: uninitialized JWT keyring", ErrMalformed)
	}
	return cloneMetadata(ring.entries[ring.active].metadata), nil
}

func (ring AEADKeyring) Active() (KeyMetadata, error) {
	if ring.active < 0 || ring.active >= len(ring.entries) {
		return KeyMetadata{}, fmt.Errorf("%w: uninitialized AEAD keyring", ErrMalformed)
	}
	return cloneMetadata(ring.entries[ring.active].metadata), nil
}

func (ring JWTKeyring) SignHS256(signingInput []byte) (KeyMetadata, []byte, error) {
	if ring.active < 0 || ring.active >= len(ring.entries) {
		return KeyMetadata{}, nil, fmt.Errorf("%w: uninitialized JWT keyring", ErrMalformed)
	}
	entry := ring.entries[ring.active]
	mac := hmac.New(sha256.New, entry.material[:])
	_, _ = mac.Write(signingInput)
	return cloneMetadata(entry.metadata), mac.Sum(nil), nil
}

func (ring JWTKeyring) VerifyHS256(kid string, signingInput, signature []byte) error {
	entry, err := ring.lookup(kid)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, entry.material[:])
	_, _ = mac.Write(signingInput)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return ErrVerificationFailed
	}
	return nil
}

func (ring JWTKeyring) lookup(kid string) (keyEntry, error) {
	for _, entry := range ring.entries {
		if entry.metadata.KID == kid {
			if entry.metadata.Status == KeyRetired {
				return keyEntry{}, fmt.Errorf("%w: retired JWT key", ErrVerificationFailed)
			}
			return entry, nil
		}
	}
	return keyEntry{}, fmt.Errorf("%w: unknown JWT kid", ErrVerificationFailed)
}

func (ring AEADKeyring) SealRandom(plaintext, aad []byte) (KeyMetadata, AESGCMSealed, error) {
	if ring.active < 0 || ring.active >= len(ring.entries) {
		return KeyMetadata{}, AESGCMSealed{}, fmt.Errorf("%w: uninitialized AEAD keyring", ErrMalformed)
	}
	entry := ring.entries[ring.active]
	sealed, err := SealRandomAES256GCM(entry.material[:], plaintext, aad)
	return cloneMetadata(entry.metadata), sealed, err
}

func (ring AEADKeyring) SealWithNonce(nonce, plaintext, aad []byte) (KeyMetadata, AESGCMSealed, error) {
	if ring.active < 0 || ring.active >= len(ring.entries) {
		return KeyMetadata{}, AESGCMSealed{}, fmt.Errorf("%w: uninitialized AEAD keyring", ErrMalformed)
	}
	entry := ring.entries[ring.active]
	sealed, err := SealAES256GCM(entry.material[:], nonce, plaintext, aad)
	return cloneMetadata(entry.metadata), sealed, err
}

func (ring AEADKeyring) Open(kid string, version int32, nonce, ciphertext, tag, aad []byte) ([]byte, error) {
	for _, entry := range ring.entries {
		if entry.metadata.KID == kid && entry.metadata.Version == version {
			if entry.metadata.Status == KeyRetired {
				return nil, fmt.Errorf("%w: retired AEAD key", ErrVerificationFailed)
			}
			return OpenAES256GCM(entry.material[:], nonce, ciphertext, tag, aad)
		}
	}
	return nil, fmt.Errorf("%w: unknown AEAD kid/version", ErrVerificationFailed)
}

func (rings *Keyrings) SelfTest() error {
	if rings == nil {
		return fmt.Errorf("%w: nil keyrings", ErrMalformed)
	}
	signingInput := []byte("clientauth-keyring-self-test")
	jwtMeta, signature, err := rings.JWT.SignHS256(signingInput)
	if err != nil {
		return fmt.Errorf("JWT sign self-test: %w", err)
	}
	if err := rings.JWT.VerifyHS256(jwtMeta.KID, signingInput, signature); err != nil {
		return fmt.Errorf("JWT self-test: %w", err)
	}
	aad := []byte("clientauth-aead-self-test-aad")
	plaintext := []byte("clientauth-aead-self-test")
	aeadMeta, sealed, err := rings.AEAD.SealRandom(plaintext, aad)
	if err != nil {
		return fmt.Errorf("AEAD seal self-test: %w", err)
	}
	opened, err := rings.AEAD.Open(aeadMeta.KID, aeadMeta.Version, sealed.Nonce, sealed.Ciphertext, sealed.Tag, aad)
	if err != nil || !bytes.Equal(opened, plaintext) {
		return fmt.Errorf("AEAD open self-test: %w", err)
	}
	return nil
}

type jwtJSONEntry struct {
	KID             string  `json:"kid"`
	Version         int64   `json:"version"`
	Status          string  `json:"status"`
	Algorithm       string  `json:"alg"`
	Purpose         string  `json:"purpose"`
	CreatedAt       string  `json:"created_at"`
	ActivatedAt     string  `json:"activated_at"`
	VerifyOnlySince *string `json:"verify_only_since"`
	RetiredAt       *string `json:"retired_at"`
	SecretB64U      string  `json:"secret_b64u"`
}

type aeadJSONEntry struct {
	KID             string  `json:"kid"`
	Version         int64   `json:"version"`
	Status          string  `json:"status"`
	Algorithm       string  `json:"alg"`
	Purpose         string  `json:"purpose"`
	CreatedAt       string  `json:"created_at"`
	ActivatedAt     string  `json:"activated_at"`
	VerifyOnlySince *string `json:"verify_only_since"`
	RetiredAt       *string `json:"retired_at"`
	KeyB64U         string  `json:"key_b64u"`
}

func LoadJWTKeyring(encoded []byte) (JWTKeyring, error) {
	raw, err := decodeStrictArray(encoded, []string{"kid", "version", "status", "alg", "purpose", "created_at", "activated_at", "verify_only_since", "retired_at", "secret_b64u"})
	if err != nil {
		return JWTKeyring{}, err
	}
	entries := make([]keyEntry, 0, len(raw))
	for _, item := range raw {
		var value jwtJSONEntry
		if err := json.Unmarshal(item, &value); err != nil {
			return JWTKeyring{}, fmt.Errorf("%w: JWT entry types", ErrMalformed)
		}
		entry, err := buildKeyEntry(value.KID, value.Version, value.Status, value.Algorithm, value.Purpose, value.CreatedAt, value.ActivatedAt, value.VerifyOnlySince, value.RetiredAt, value.SecretB64U, "HS256", "client-jwt-signing")
		if err != nil {
			return JWTKeyring{}, err
		}
		entries = append(entries, entry)
	}
	active, err := validateKeyEntries(entries)
	if err != nil {
		return JWTKeyring{}, err
	}
	return JWTKeyring{entries: entries, active: active}, nil
}

func LoadAEADKeyring(encoded []byte) (AEADKeyring, error) {
	raw, err := decodeStrictArray(encoded, []string{"kid", "version", "status", "alg", "purpose", "created_at", "activated_at", "verify_only_since", "retired_at", "key_b64u"})
	if err != nil {
		return AEADKeyring{}, err
	}
	entries := make([]keyEntry, 0, len(raw))
	for _, item := range raw {
		var value aeadJSONEntry
		if err := json.Unmarshal(item, &value); err != nil {
			return AEADKeyring{}, fmt.Errorf("%w: AEAD entry types", ErrMalformed)
		}
		entry, err := buildKeyEntry(value.KID, value.Version, value.Status, value.Algorithm, value.Purpose, value.CreatedAt, value.ActivatedAt, value.VerifyOnlySince, value.RetiredAt, value.KeyB64U, "A256GCM", "client-replay-envelope")
		if err != nil {
			return AEADKeyring{}, err
		}
		entries = append(entries, entry)
	}
	active, err := validateKeyEntries(entries)
	if err != nil {
		return AEADKeyring{}, err
	}
	return AEADKeyring{entries: entries, active: active}, nil
}

func buildKeyEntry(kid string, version int64, status, algorithm, purpose, created, activated string, verifyOnly, retired *string, materialB64U, expectedAlgorithm, expectedPurpose string) (keyEntry, error) {
	if !validKID(kid) || version < 1 || version > 2147483647 || algorithm != expectedAlgorithm || purpose != expectedPurpose {
		return keyEntry{}, fmt.Errorf("%w: key identity, algorithm, or purpose", ErrMalformed)
	}
	createdAt, err := ParseMicrosecondUTC(created)
	if err != nil {
		return keyEntry{}, err
	}
	activatedAt, err := ParseMicrosecondUTC(activated)
	if err != nil || activatedAt.Before(createdAt) {
		return keyEntry{}, fmt.Errorf("%w: activated_at lifecycle", ErrMalformed)
	}
	verifyAt, err := optionalMicrosecondUTC(verifyOnly)
	if err != nil {
		return keyEntry{}, err
	}
	retiredAt, err := optionalMicrosecondUTC(retired)
	if err != nil {
		return keyEntry{}, err
	}
	keyStatus := KeyStatus(status)
	switch keyStatus {
	case KeyActive:
		if verifyAt != nil || retiredAt != nil {
			return keyEntry{}, fmt.Errorf("%w: active lifecycle", ErrMalformed)
		}
	case KeyVerifyOnly:
		if verifyAt == nil || !verifyAt.After(activatedAt) || retiredAt != nil {
			return keyEntry{}, fmt.Errorf("%w: verify-only lifecycle", ErrMalformed)
		}
	case KeyRetired:
		if verifyAt == nil || retiredAt == nil || !verifyAt.After(activatedAt) || !retiredAt.After(*verifyAt) {
			return keyEntry{}, fmt.Errorf("%w: retired lifecycle", ErrMalformed)
		}
	default:
		return keyEntry{}, fmt.Errorf("%w: key status", ErrUnsupported)
	}
	material, err := DecodeBase64URLNoPad(materialB64U, 32)
	if err != nil {
		return keyEntry{}, err
	}
	var fixed [32]byte
	copy(fixed[:], material)
	return keyEntry{metadata: KeyMetadata{KID: kid, Version: int32(version), Status: keyStatus, Algorithm: algorithm, Purpose: purpose, CreatedAt: createdAt, ActivatedAt: activatedAt, VerifyOnlySince: verifyAt, RetiredAt: retiredAt}, material: fixed}, nil
}

func validateKeyEntries(entries []keyEntry) (int, error) {
	if len(entries) == 0 {
		return 0, fmt.Errorf("%w: empty keyring", ErrMalformed)
	}
	kids := make(map[string]struct{}, len(entries))
	versions := make(map[int32]struct{}, len(entries))
	materials := make(map[[32]byte]struct{}, len(entries))
	active := -1
	previousRank := -1
	for index, entry := range entries {
		if _, ok := kids[entry.metadata.KID]; ok {
			return 0, fmt.Errorf("%w: duplicate kid", ErrMalformed)
		}
		if _, ok := versions[entry.metadata.Version]; ok {
			return 0, fmt.Errorf("%w: duplicate version", ErrMalformed)
		}
		if _, ok := materials[entry.material]; ok {
			return 0, fmt.Errorf("%w: duplicate decoded key material", ErrMalformed)
		}
		kids[entry.metadata.KID] = struct{}{}
		versions[entry.metadata.Version] = struct{}{}
		materials[entry.material] = struct{}{}
		if index > 0 {
			previous := entries[index-1].metadata
			if entry.metadata.Version <= previous.Version || !entry.metadata.CreatedAt.After(previous.CreatedAt) || !entry.metadata.ActivatedAt.After(previous.ActivatedAt) {
				return 0, fmt.Errorf("%w: entries must increase by version and lifecycle time", ErrMalformed)
			}
		}
		rank := statusRank(entry.metadata.Status)
		if rank < previousRank {
			return 0, fmt.Errorf("%w: active/verify-only version order", ErrMalformed)
		}
		previousRank = rank
		if entry.metadata.Status == KeyActive {
			if active >= 0 {
				return 0, fmt.Errorf("%w: multiple active keys", ErrMalformed)
			}
			active = index
		}
	}
	if active < 0 {
		return 0, fmt.Errorf("%w: no active key", ErrMalformed)
	}
	return active, nil
}

func statusRank(status KeyStatus) int {
	switch status {
	case KeyRetired:
		return 0
	case KeyVerifyOnly:
		return 1
	default:
		return 2
	}
}

func optionalMicrosecondUTC(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := ParseMicrosecondUTC(*value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func validKID(value string) bool {
	if len(value) < 1 || len(value) > 64 || value[0] < 'A' || value[0] > 'Z' && (value[0] < 'a' || value[0] > 'z') {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func decodeStrictArray(encoded []byte, expectedFields []string) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(encoded)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return nil, fmt.Errorf("%w: keyring must be a JSON array", ErrMalformed)
	}
	if err := rejectDuplicateJSONKeys(encoded); err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("%w: invalid or empty keyring", ErrMalformed)
	}
	allowed := make(map[string]struct{}, len(expectedFields))
	for _, field := range expectedFields {
		allowed[field] = struct{}{}
	}
	for _, item := range raw {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(item, &object); err != nil || len(object) != len(expectedFields) {
			return nil, fmt.Errorf("%w: keyring entry field set", ErrMalformed)
		}
		for field := range object {
			if _, ok := allowed[field]; !ok {
				return nil, fmt.Errorf("%w: unknown keyring field %q", ErrMalformed, field)
			}
		}
	}
	return raw, nil
}

func rejectDuplicateJSONKeys(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON data", ErrMalformed)
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate object key %q", name)
			}
			seen[name] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("unterminated array")
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	return nil
}

// Metadata returns a stable copy sorted by version. No key material is exposed.
func (ring JWTKeyring) Metadata() []KeyMetadata  { return keyMetadata(ring.entries) }
func (ring AEADKeyring) Metadata() []KeyMetadata { return keyMetadata(ring.entries) }

func keyMetadata(entries []keyEntry) []KeyMetadata {
	result := make([]KeyMetadata, 0, len(entries))
	for _, entry := range entries {
		result = append(result, cloneMetadata(entry.metadata))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	return result
}

func cloneMetadata(metadata KeyMetadata) KeyMetadata {
	result := metadata
	if metadata.VerifyOnlySince != nil {
		value := *metadata.VerifyOnlySince
		result.VerifyOnlySince = &value
	}
	if metadata.RetiredAt != nil {
		value := *metadata.RetiredAt
		result.RetiredAt = &value
	}
	return result
}
