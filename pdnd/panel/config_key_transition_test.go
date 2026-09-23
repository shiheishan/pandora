package panel

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRefreshConfigSigningKeyPersistsOldKeyAuthenticatedTransition(t *testing.T) {
	oldPublic, oldPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldSum, newSum := sha256.Sum256(oldPublic), sha256.Sum256(newPublic)
	oldID := base64.RawURLEncoding.EncodeToString(oldSum[:8])
	newID := base64.RawURLEncoding.EncodeToString(newSum[:8])
	nodeID := uuid.NewString()
	issued := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	transition := ConfigKeyTransition{Contract: configKeyTransitionContract, NodeID: nodeID,
		FromKeyID: oldID, ToKeyID: newID, ToPublicKey: base64.StdEncoding.EncodeToString(newPublic),
		IssuedAt: issued, ExpiresAt: issued.Add(configKeyTransitionWindow)}
	preimage, err := configKeyTransitionPreimage(transition)
	if err != nil {
		t.Fatal(err)
	}
	transition.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(oldPrivate, preimage))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/config-signing-key" || r.Header.Get("X-Config-Key-Id") != oldID {
			t.Fatalf("unexpected transition request: path=%s key=%s", r.URL.Path, r.Header.Get("X-Config-Key-Id"))
		}
		_ = json.NewEncoder(w).Encode(transition)
	}))
	defer server.Close()
	identity := &Identity{Server: server.URL, NodeID: nodeID, Serial: 1,
		PrivateKey: base64.StdEncoding.EncodeToString(oldPrivate), ConfigPublicKey: base64.StdEncoding.EncodeToString(oldPublic),
		ConfigKeyID: oldID, RuntimeToken: "runtime"}
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := SaveIdentity(path, identity); err != nil {
		t.Fatal(err)
	}
	client, err := NewSignedClientAt(identity, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RefreshConfigSigningKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigKeyID != newID || loaded.ConfigPublicKey != base64.StdEncoding.EncodeToString(newPublic) {
		t.Fatalf("transition was not persisted: %+v", loaded)
	}
}
