//go:build ca42e2e && linux && (amd64 || arm64)

package ca42authority

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const CA42E2EAuthorityMarker = "pandora-ca42-e2e-authority-fixture-v1"

// CA42E2EAuthorityInput is intentionally available only to the disposable
// native E2E binary. Private root keys must never cross into an ordinary build.
type CA42E2EAuthorityInput struct {
	Roots           RootKeyset
	RootPrivate     [RequiredRootCount]ed25519.PrivateKey
	ReleasePublic   ed25519.PublicKey
	Architecture    string
	HostIdentity    [sha256.Size]byte
	RootRunner      [sha256.Size]byte
	ReleaseManifest [sha256.Size]byte
	ReleaseID       string
	ReleaseRunID    string
	AttemptID       string
	Now             time.Time
}

// BuildCA42E2EAuthorityDescriptor emits the production-canonical descriptor
// and immediately verifies it with the production parser. It is not an
// alternate authority parser or opener.
func BuildCA42E2EAuthorityDescriptor(input CA42E2EAuthorityInput) ([]byte, Descriptor, error) {
	var empty Descriptor
	now := input.Now.UTC().Truncate(time.Second)
	if now.IsZero() || input.Architecture != runtime.GOARCH || len(input.ReleasePublic) != ed25519.PublicKeySize ||
		input.ReleaseManifest == ([sha256.Size]byte{}) || input.HostIdentity == ([sha256.Size]byte{}) ||
		input.RootRunner == ([sha256.Size]byte{}) || input.ReleaseID == "" || input.ReleaseRunID == "" || input.AttemptID == "" {
		return nil, empty, errors.New("CA42 E2E authority input invalid")
	}
	if _, err := validateRootKeyset(input.Roots); err != nil {
		return nil, empty, err
	}
	for index := range input.RootPrivate {
		if len(input.RootPrivate[index]) != ed25519.PrivateKeySize ||
			!ed25519.PublicKey(input.RootPrivate[index].Public().(ed25519.PublicKey)).Equal(input.Roots.Keys[index].PublicKey) {
			return nil, empty, errors.New("CA42 E2E authority root private/public mismatch")
		}
	}
	signerSHA := sha256.Sum256(input.ReleasePublic)
	ledgerSHA := sha256.Sum256([]byte("ca42-e2e-ledger:" + input.AttemptID))
	values := []string{
		Format, SignatureAlgorithm, SignatureDomain, Purpose, Status, input.Roots.ID,
		hex.EncodeToString(ledgerSHA[:]), "1", "1", strings.Repeat("0", sha256.Size*2), ModeNormal,
		input.Architecture, hex.EncodeToString(input.HostIdentity[:]), input.ReleaseID, input.ReleaseRunID,
		input.AttemptID, hex.EncodeToString(input.ReleaseManifest[:]), "release-signer-e2e",
		base64.StdEncoding.EncodeToString(input.ReleasePublic), hex.EncodeToString(signerSHA[:]),
		hex.EncodeToString(input.RootRunner[:]), strconv.FormatInt(now.Add(-time.Minute).Unix(), 10),
		strconv.FormatInt(now.Add(20*time.Minute).Unix(), 10), strconv.FormatInt(now.Add(-2*time.Minute).Unix(), 10),
	}
	keyIDs := [RequiredQuorum]string{input.Roots.Keys[0].ID, input.Roots.Keys[1].ID}
	message, err := SignatureInput(values, keyIDs)
	if err != nil {
		return nil, empty, err
	}
	var raw strings.Builder
	for index, value := range values {
		raw.WriteString(fieldNames[index] + "=" + value + "\n")
	}
	for index := range keyIDs {
		raw.WriteString(fieldNames[24+index*2] + "=" + keyIDs[index] + "\n")
		raw.WriteString(fieldNames[25+index*2] + "=" + base64.StdEncoding.EncodeToString(ed25519.Sign(input.RootPrivate[index], message)) + "\n")
	}
	data := []byte(raw.String())
	descriptor, err := ParseAndVerify(data, input.Roots, input.Architecture, input.HostIdentity, now)
	if err != nil {
		return nil, empty, err
	}
	return data, descriptor, nil
}
