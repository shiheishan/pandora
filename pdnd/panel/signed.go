package panel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

const DefaultIdentityPath = "/var/lib/pandora-native/identity.json"

type Identity struct {
	Server          string `json:"server"`
	NodeID          string `json:"node_id"`
	Serial          int    `json:"serial"`
	PrivateKey      string `json:"private_key"`
	ConfigPublicKey string `json:"config_public_key"`
	ConfigKeyID     string `json:"config_key_id"`
	RuntimeToken    string `json:"runtime_token"`
}

type BootstrapOptions struct {
	Server string
	Token  string
	Name   string
	Path   string
}

func Bootstrap(ctx context.Context, opts BootstrapOptions) (*Identity, error) {
	if strings.TrimSpace(opts.Server) == "" || strings.TrimSpace(opts.Token) == "" || strings.TrimSpace(opts.Name) == "" {
		return nil, fmt.Errorf("server, token and name are required")
	}
	server, err := validateSignedServer(opts.Server)
	if err != nil {
		return nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"token": opts.Token, "node_name": opts.Name,
		"public_key":    base64.StdEncoding.EncodeToString(pub),
		"agent_version": "pandora-native", "hostname": hostname(),
		"cpu_cores": runtime.NumCPU(),
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/v1/nodes/bootstrap", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: rejectCredentialRedirect}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("bootstrap rejected (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		NodeID          string `json:"node_id"`
		Serial          int    `json:"serial"`
		ConfigPublicKey string `json:"config_public_key"`
		ConfigKeyID     string `json:"config_key_id"`
		RuntimeToken    string `json:"runtime_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.NodeID == "" || out.Serial <= 0 || out.ConfigPublicKey == "" || out.ConfigKeyID == "" || out.RuntimeToken == "" {
		return nil, fmt.Errorf("bootstrap response is incomplete")
	}
	identity := &Identity{Server: server, NodeID: out.NodeID, Serial: out.Serial,
		PrivateKey: base64.StdEncoding.EncodeToString(priv), ConfigPublicKey: out.ConfigPublicKey, ConfigKeyID: out.ConfigKeyID, RuntimeToken: out.RuntimeToken}
	path := opts.Path
	if path == "" {
		path = DefaultIdentityPath
	}
	if err := SaveIdentity(path, identity); err != nil {
		return nil, err
	}
	return identity, nil
}

func validateSignedServer(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid panel server URL")
	}
	if u.Scheme == "https" {
		return base, nil
	}
	host := u.Hostname()
	if u.Scheme == "http" && (strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return base, nil
	}
	return "", fmt.Errorf("panel server URL must use HTTPS (HTTP is allowed only on loopback)")
}

// CanonicalSignedServer exposes the same strict HTTPS origin normalization used
// by signed requests so CLI identity checks cannot compare ambiguous strings.
func CanonicalSignedServer(raw string) (string, error) { return validateSignedServer(raw) }

func rejectCredentialRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func SaveIdentity(path string, identity *Identity) error {
	if identity == nil || identity.NodeID == "" || identity.PrivateKey == "" {
		return fmt.Errorf("invalid identity")
	}
	body, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, body, 0600)
}

func LoadIdentity(path string) (*Identity, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var identity Identity
	if err := json.Unmarshal(body, &identity); err != nil {
		return nil, err
	}
	priv, err := base64.StdEncoding.DecodeString(identity.PrivateKey)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid identity private key")
	}
	return &identity, nil
}

type signedClient struct {
	identity     *Identity
	identityPath string
	private      ed25519.PrivateKey
	http         *http.Client
}

type SignedClient = signedClient

func (c *SignedClient) ConfigSigningKeyID() string {
	return c.identity.ConfigKeyID
}

type SignedConfig struct {
	ConfigContract       string          `json:"config_contract"`
	TenantID             string          `json:"tenant_id"`
	NodeID               string          `json:"node_id"`
	ReleaseID            string          `json:"release_id"`
	Generation           uint64          `json:"generation"`
	ContentSHA256        string          `json:"content_sha256"`
	SourceManifest       json.RawMessage `json:"source_manifest"`
	SourceManifestSHA256 string          `json:"source_manifest_sha256"`
	IssuedAt             time.Time       `json:"issued_at"`
	Version              int             `json:"version"`
	Payload              json.RawMessage `json:"payload"`
	Hash                 string          `json:"hash"`
	Signature            string          `json:"signature"`
	KeyID                string          `json:"key_id"`
	ExpiresAt            time.Time       `json:"expires_at"`
	Sources              []string        `json:"sources"`
}

type HeartbeatInput struct {
	AgentVersion         string `json:"agent_version"`
	RuntimeVersion       string `json:"runtime_version"`
	ConfigSigningKeyID   string `json:"config_signing_key_id"`
	ConfigVersion        int    `json:"applied_config_version"`
	ConfigHash           string `json:"applied_config_hash"`
	AppliedReleaseID     string `json:"applied_effective_release_id,omitempty"`
	AppliedGeneration    uint64 `json:"applied_effective_generation,omitempty"`
	AppliedContentSHA256 string `json:"applied_effective_content_sha256,omitempty"`
	CPUCores             int    `json:"cpu_cores"`
	MemoryMB             int    `json:"memory_mb"`
	DiskGB               int    `json:"disk_gb"`
	RuntimeStatus        string `json:"runtime_status"`
}

type HeartbeatOutput struct {
	NodeStatus           string `json:"node_status"`
	DesiredConfigVersion int    `json:"desired_config_version"`
	DesiredReleaseID     string `json:"desired_effective_release_id,omitempty"`
	DesiredGeneration    uint64 `json:"desired_effective_generation,omitempty"`
	IntervalSeconds      int    `json:"interval_seconds"`
}

func (c *SignedClient) Heartbeat(ctx context.Context, in HeartbeatInput) (*HeartbeatOutput, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var out HeartbeatOutput
	if err := c.Do(ctx, http.MethodPost, "/v1/nodes/heartbeat", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SignedClient) Config(ctx context.Context) (*SignedConfig, error) {
	if err := c.RefreshConfigSigningKey(ctx); err != nil {
		return nil, err
	}
	var out SignedConfig
	if err := c.Do(ctx, http.MethodGet, "/v1/nodes/effective-config", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SignedClient) ReportConfig(ctx context.Context, version int, phase, detail string) error {
	body, err := json.Marshal(map[string]any{"version": version, "phase": phase, "detail": detail})
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPost, "/v1/nodes/config/report", body, nil)
}

func (c *SignedClient) ReportEffectiveConfig(ctx context.Context, cfg *SignedConfig, phase, detail string) error {
	if cfg == nil {
		return fmt.Errorf("effective config is required")
	}
	releaseID, err := uuid.Parse(cfg.ReleaseID)
	if err != nil || releaseID == uuid.Nil || releaseID.String() != cfg.ReleaseID {
		return fmt.Errorf("effective release id must be a non-zero canonical UUID")
	}
	reportID := uuid.NewSHA1(releaseID, []byte(phase)).String()
	body, err := json.Marshal(map[string]any{
		"report_id": reportID, "release_id": cfg.ReleaseID,
		"generation": cfg.Generation, "content_sha256": cfg.ContentSHA256,
		"phase": phase, "detail": detail,
	})
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPost, "/v1/nodes/config/report", body, nil)
}

func (c *SignedClient) VerifyConfig(cfg *SignedConfig) error {
	if cfg == nil || len(cfg.Payload) == 0 {
		return fmt.Errorf("empty signed config")
	}
	if cfg.ConfigContract != "" {
		return c.verifyEffectiveConfig(cfg)
	}
	if c.identity.ConfigKeyID == "" || cfg.KeyID != c.identity.ConfigKeyID {
		return fmt.Errorf("config key id mismatch")
	}
	pubRaw, err := base64.StdEncoding.DecodeString(c.identity.ConfigPublicKey)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid config public key")
	}
	sum := sha256.Sum256(cfg.Payload)
	if base64.StdEncoding.EncodeToString(sum[:]) != cfg.Hash {
		return fmt.Errorf("config hash mismatch")
	}
	sig, err := base64.StdEncoding.DecodeString(cfg.Signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pubRaw), append(sum[:], []byte(cfg.ExpiresAt.UTC().Format(time.RFC3339))...), sig) {
		return fmt.Errorf("config signature invalid")
	}
	if time.Now().After(cfg.ExpiresAt) {
		return fmt.Errorf("config expired")
	}
	return nil
}

func NewSignedClient(identity *Identity) (*SignedClient, error) {
	return NewSignedClientAt(identity, "")
}

func NewSignedClientAt(identity *Identity, identityPath string) (*SignedClient, error) {
	if identity == nil {
		return nil, fmt.Errorf("identity is required")
	}
	server, err := validateSignedServer(identity.Server)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(identity.PrivateKey)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid identity private key")
	}
	identity.Server = server
	return &SignedClient{identity: identity, identityPath: identityPath, private: ed25519.PrivateKey(raw), http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: rejectCredentialRedirect}}, nil
}

func (c *SignedClient) Do(ctx context.Context, method, path string, body []byte, out any) error {
	ts := time.Now().UTC().Format(time.RFC3339)
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		return fmt.Errorf("generate request nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	sum := sha256.Sum256(body)
	payload := []byte("PANDORA-NODE-REQUEST-V2\n" + method + "\n" + path + "\n" + c.identity.NodeID + "\n" + ts + "\n" + nonce + "\n" + base64.StdEncoding.EncodeToString(sum[:]))
	sig := ed25519.Sign(c.private, payload)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.identity.Server, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Id", c.identity.NodeID)
	req.Header.Set("X-Node-Ts", ts)
	req.Header.Set("X-Node-Nonce", nonce)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(sig))
	if path == "/v1/nodes/config-signing-key" {
		req.Header.Set("X-Config-Key-Id", c.identity.ConfigKeyID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func hostname() string { h, _ := os.Hostname(); return h }
