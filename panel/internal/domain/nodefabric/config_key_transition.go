package nodefabric

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/google/uuid"
)

const ConfigKeyTransitionContract = "pandora-config-signing-key-transition-v1"
const configKeyTransitionWindow = 15 * time.Minute

type ConfigKeyTransition struct {
	Contract    string    `json:"contract"`
	NodeID      string    `json:"node_id"`
	FromKeyID   string    `json:"from_key_id"`
	ToKeyID     string    `json:"to_key_id"`
	ToPublicKey string    `json:"to_public_key"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Signature   string    `json:"signature"`
}

func configKeyTransitionPreimage(in ConfigKeyTransition) ([]byte, error) {
	if in.Contract != ConfigKeyTransitionContract {
		return nil, errors.New("unsupported config key transition contract")
	}
	nodeID, err := uuid.Parse(in.NodeID)
	if err != nil || nodeID == uuid.Nil || nodeID.String() != in.NodeID {
		return nil, errors.New("node_id must be a canonical non-zero UUID")
	}
	decodeKeyID := func(name, value string) error {
		raw, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(raw) != 8 || base64.RawURLEncoding.EncodeToString(raw) != value {
			return fmt.Errorf("%s must be canonical base64url for 8 bytes", name)
		}
		return nil
	}
	if err := decodeKeyID("from_key_id", in.FromKeyID); err != nil {
		return nil, err
	}
	if err := decodeKeyID("to_key_id", in.ToKeyID); err != nil {
		return nil, err
	}
	if in.FromKeyID == in.ToKeyID {
		return nil, errors.New("config key transition must change key id")
	}
	pub, err := base64.StdEncoding.DecodeString(in.ToPublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(pub) != in.ToPublicKey {
		return nil, errors.New("to_public_key must be canonical Ed25519 public key")
	}
	sum := sha256.Sum256(pub)
	if base64.RawURLEncoding.EncodeToString(sum[:8]) != in.ToKeyID {
		return nil, errors.New("to_key_id does not match to_public_key")
	}
	issued, expires := in.IssuedAt.UTC(), in.ExpiresAt.UTC()
	if issued.IsZero() || expires.IsZero() || issued.Nanosecond()%1000 != 0 || expires.Nanosecond()%1000 != 0 ||
		!expires.After(issued) || expires.Sub(issued) > configKeyTransitionWindow {
		return nil, errors.New("invalid config key transition delivery window")
	}
	return []byte(ConfigKeyTransitionContract + "\n" +
		"node_id=" + in.NodeID + "\n" +
		"from_key_id=" + in.FromKeyID + "\n" +
		"to_key_id=" + in.ToKeyID + "\n" +
		"to_public_key=" + in.ToPublicKey + "\n" +
		"issued_at=" + issued.Format(time.RFC3339Nano) + "\n" +
		"expires_at=" + expires.Format(time.RFC3339Nano) + "\n"), nil
}

func (s *Service) ConfigSigningKeyTransition(nodeID, pinnedKeyID string, now time.Time) (*ConfigKeyTransition, error) {
	pinnedKeyID = strings.TrimSpace(pinnedKeyID)
	if pinnedKeyID == s.signer.KeyID() {
		return nil, nil
	}
	if s.previousSigner == nil || pinnedKeyID == "" || pinnedKeyID != s.previousSigner.KeyID() {
		return nil, httpx.New(httpx.CodeConflict, "no trusted config signing key transition is available")
	}
	issued := now.UTC().Add(-time.Minute).Truncate(time.Microsecond)
	out := &ConfigKeyTransition{Contract: ConfigKeyTransitionContract, NodeID: nodeID,
		FromKeyID: s.previousSigner.KeyID(), ToKeyID: s.signer.KeyID(),
		ToPublicKey: base64.StdEncoding.EncodeToString(s.signer.PublicKey()),
		IssuedAt:    issued, ExpiresAt: issued.Add(configKeyTransitionWindow)}
	preimage, err := configKeyTransitionPreimage(*out)
	if err != nil {
		return nil, err
	}
	out.Signature = base64.StdEncoding.EncodeToString(s.previousSigner.Sign(preimage))
	return out, nil
}

func verifyConfigKeyTransition(pub ed25519.PublicKey, in ConfigKeyTransition, now time.Time) error {
	preimage, err := configKeyTransitionPreimage(in)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(in.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(sig) != in.Signature ||
		!ed25519.Verify(pub, preimage, sig) {
		return errors.New("config key transition signature invalid")
	}
	if now.UTC().Before(in.IssuedAt.UTC()) || !now.UTC().Before(in.ExpiresAt.UTC()) {
		return errors.New("config key transition is outside its delivery window")
	}
	return nil
}
