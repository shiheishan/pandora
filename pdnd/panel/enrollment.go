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
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

const enrollmentBeginDomainV1 = "PANDORA-NODE-ENROLL-BEGIN-V1"
const enrollmentRequestDomainV1 = "PANDORA-NODE-ENROLLMENT-V1"

type EnrollmentJournal struct {
	FormatVersion   int             `json:"format_version"`
	Phase           string          `json:"phase"`
	Server          string          `json:"server"`
	BootstrapToken  string          `json:"bootstrap_token"`
	NodeName        string          `json:"node_name"`
	RequestID       string          `json:"request_id"`
	PrivateKey      string          `json:"private_key"`
	RuntimeToken    string          `json:"runtime_token"`
	BeginBody       json.RawMessage `json:"begin_body"`
	EnrollmentID    string          `json:"enrollment_id,omitempty"`
	NodeID          string          `json:"node_id,omitempty"`
	Serial          int             `json:"serial,omitempty"`
	State           string          `json:"state,omitempty"`
	CommitAttempted bool            `json:"commit_attempted,omitempty"`
	ExpiresAt       time.Time       `json:"expires_at,omitempty"`
	ConfigKeyID     string          `json:"config_key_id,omitempty"`
	ConfigPublicKey string          `json:"config_public_key,omitempty"`
}

type EnrollmentEvidence struct {
	AgentVersion    string `json:"agent_version"`
	Architecture    string `json:"architecture"`
	BinarySHA256    string `json:"binary_sha256"`
	ConfigSHA256    string `json:"config_sha256"`
	UnitSHA256      string `json:"unit_sha256"`
	PreflightSHA256 string `json:"preflight_sha256"`
}

type enrollmentWire struct {
	EnrollmentID    string    `json:"enrollment_id"`
	NodeID          string    `json:"node_id"`
	Serial          int       `json:"serial"`
	State           string    `json:"state"`
	ExpiresAt       time.Time `json:"expires_at"`
	ConfigKeyID     string    `json:"config_key_id"`
	ConfigPublicKey string    `json:"config_public_key"`
}

func enrollmentPath(identityPath string) string {
	if identityPath == "" {
		identityPath = DefaultIdentityPath
	}
	return identityPath + ".enrollment.pending.json"
}

func EnrollmentJournalPath(identityPath string) string { return enrollmentPath(identityPath) }

func saveEnrollmentJournal(path string, journal *EnrollmentJournal) error {
	if journal == nil || journal.FormatVersion != 1 || journal.PrivateKey == "" || journal.RuntimeToken == "" {
		return fmt.Errorf("invalid enrollment journal")
	}
	body, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, body, 0600)
}

func writeFileAtomic(path string, body []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".enrollment-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	// Unix filesystems support syncing a directory handle to persist the
	// rename. Windows does not expose that operation and returns
	// ERROR_ACCESS_DENIED even though the file was durably flushed and the
	// atomic replace already succeeded. Keep the strict directory barrier on
	// Unix while allowing the supported Windows development/test path.
	if err := dir.Sync(); err != nil && !(runtime.GOOS == "windows" && os.IsPermission(err)) {
		return err
	}
	ok = true
	return nil
}

func loadEnrollmentJournal(path string) (*EnrollmentJournal, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out EnrollmentJournal
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	priv, err := base64.StdEncoding.DecodeString(out.PrivateKey)
	if err != nil || len(priv) != ed25519.PrivateKeySize || out.FormatVersion != 1 {
		return nil, fmt.Errorf("invalid enrollment journal")
	}
	return &out, nil
}

func LoadEnrollmentJournal(identityPath string) (*EnrollmentJournal, error) {
	return loadEnrollmentJournal(enrollmentPath(identityPath))
}

func BeginEnrollment(ctx context.Context, opts BootstrapOptions) (*EnrollmentJournal, error) {
	server, err := validateSignedServer(opts.Server)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.Token) == "" || strings.TrimSpace(opts.Name) == "" {
		return nil, fmt.Errorf("token and name are required")
	}
	path := enrollmentPath(opts.Path)
	journal, err := loadEnrollmentJournal(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if journal == nil {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		runtimeRaw := make([]byte, 32)
		if _, err := rand.Read(runtimeRaw); err != nil {
			return nil, err
		}
		runtimeToken := base64.RawURLEncoding.EncodeToString(runtimeRaw)
		runtimeHash := sha256.Sum256([]byte(runtimeToken))
		requestID := uuid.NewString()
		body, err := json.Marshal(map[string]any{
			"token": opts.Token, "node_name": opts.Name, "request_id": requestID,
			"public_key":           base64.StdEncoding.EncodeToString(pub),
			"runtime_token_sha256": base64.StdEncoding.EncodeToString(runtimeHash[:]),
			"agent_version":        "pandora-native", "hostname": hostname(), "cpu_cores": runtime.NumCPU(),
			"memory_mb": 0, "disk_gb": 0, "public_ipv4": "",
		})
		if err != nil {
			return nil, err
		}
		journal = &EnrollmentJournal{FormatVersion: 1, Phase: "prepared", Server: server, BootstrapToken: opts.Token,
			NodeName: opts.Name, RequestID: requestID, PrivateKey: base64.StdEncoding.EncodeToString(priv),
			RuntimeToken: runtimeToken, BeginBody: body}
		if err := saveEnrollmentJournal(path, journal); err != nil {
			return nil, err
		}
	} else if journal.Server != server || journal.NodeName != opts.Name ||
		(journal.EnrollmentID == "" && journal.BootstrapToken != opts.Token) {
		return nil, fmt.Errorf("existing enrollment journal belongs to a different request")
	}
	if journal.EnrollmentID != "" {
		return journal, nil
	}
	priv, _ := base64.StdEncoding.DecodeString(journal.PrivateKey)
	hash := sha256.Sum256(journal.BeginBody)
	payload := []byte(enrollmentBeginDomainV1 + "\nPOST\n/v1/nodes/enrollments\n" + journal.RequestID + "\n" + base64.StdEncoding.EncodeToString(hash[:]))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/v1/nodes/enrollments", bytes.NewReader(journal.BeginBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Enrollment-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(priv), payload)))
	resp, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: rejectCredentialRedirect}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("enrollment begin rejected (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out enrollmentWire
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.EnrollmentID == "" || out.NodeID == "" || out.Serial <= 0 || out.State == "" || out.ConfigKeyID == "" || out.ConfigPublicKey == "" {
		return nil, fmt.Errorf("enrollment response is incomplete")
	}
	journal.EnrollmentID = out.EnrollmentID
	journal.NodeID, journal.Serial, journal.State, journal.ExpiresAt = out.NodeID, out.Serial, out.State, out.ExpiresAt
	journal.ConfigKeyID, journal.ConfigPublicKey = out.ConfigKeyID, out.ConfigPublicKey
	journal.Phase = "pending_durable"
	journal.BootstrapToken = ""
	journal.BeginBody = nil
	if err := saveEnrollmentJournal(path, journal); err != nil {
		return nil, err
	}
	return journal, nil
}

func enrollmentDo(ctx context.Context, j *EnrollmentJournal, method, path string, body []byte, out any) error {
	privRaw, err := base64.StdEncoding.DecodeString(j.PrivateKey)
	if err != nil {
		return err
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		return err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	hash := sha256.Sum256(body)
	payload := []byte(enrollmentRequestDomainV1 + "\n" + method + "\n" + path + "\n" + j.EnrollmentID + "\n" + j.NodeID + "\n" +
		fmt.Sprintf("%d", j.Serial) + "\n" + ts + "\n" + nonce + "\n" + base64.StdEncoding.EncodeToString(hash[:]))
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(j.Server, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Id", j.NodeID)
	req.Header.Set("X-Node-Serial", fmt.Sprintf("%d", j.Serial))
	req.Header.Set("X-Node-Ts", ts)
	req.Header.Set("X-Node-Nonce", nonce)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privRaw), payload)))
	resp, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: rejectCredentialRedirect}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func EnrollmentStatus(ctx context.Context, identityPath string, j *EnrollmentJournal) error {
	path := "/v1/nodes/enrollments/" + j.EnrollmentID + "/status"
	var out struct {
		State string `json:"state"`
	}
	if err := enrollmentDo(ctx, j, http.MethodGet, path, nil, &out); err != nil {
		return err
	}
	j.State = out.State
	if out.State == "expired" || out.State == "aborted" {
		j.CommitAttempted = false
		j.Phase = "terminal_" + out.State
	}
	return saveEnrollmentJournal(enrollmentPath(identityPath), j)
}

func CommitEnrollment(ctx context.Context, identityPath string, j *EnrollmentJournal, evidence EnrollmentEvidence) (*Identity, error) {
	if j == nil || j.State != "pending" {
		return nil, fmt.Errorf("enrollment is not pending")
	}
	// Once the request can reach the server, a transport error is ambiguous: the
	// transaction may already be committed. Persist that boundary before I/O so
	// installers can only roll forward until signed status proves otherwise.
	j.CommitAttempted = true
	j.Phase = "commit_attempted"
	if err := saveEnrollmentJournal(enrollmentPath(identityPath), j); err != nil {
		return nil, err
	}
	body, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	path := "/v1/nodes/enrollments/" + j.EnrollmentID + "/commit"
	var out struct {
		State string `json:"state"`
	}
	if err := enrollmentDo(ctx, j, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	if out.State != "committed" {
		return nil, fmt.Errorf("enrollment commit returned state %q", out.State)
	}
	j.State = out.State
	if err := saveEnrollmentJournal(enrollmentPath(identityPath), j); err != nil {
		return nil, err
	}
	return PromoteCommittedEnrollment(identityPath, j)
}

func PromoteCommittedEnrollment(identityPath string, j *EnrollmentJournal) (*Identity, error) {
	if j == nil || j.State != "committed" {
		return nil, fmt.Errorf("enrollment is not committed")
	}
	identity := &Identity{Server: j.Server, NodeID: j.NodeID, Serial: j.Serial, PrivateKey: j.PrivateKey,
		ConfigPublicKey: j.ConfigPublicKey, ConfigKeyID: j.ConfigKeyID, RuntimeToken: j.RuntimeToken}
	if identityPath == "" {
		identityPath = DefaultIdentityPath
	}
	if err := SaveIdentity(identityPath, identity); err != nil {
		return nil, err
	}
	j.State = "committed"
	j.Phase = "local_promoted"
	if err := saveEnrollmentJournal(enrollmentPath(identityPath), j); err != nil {
		return nil, err
	}
	return identity, nil
}

func AbortEnrollment(ctx context.Context, identityPath string, j *EnrollmentJournal, reason string) error {
	body, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return err
	}
	path := "/v1/nodes/enrollments/" + j.EnrollmentID + "/abort"
	var out struct {
		State string `json:"state"`
	}
	if err := enrollmentDo(ctx, j, http.MethodPost, path, body, &out); err != nil {
		return err
	}
	j.State = out.State
	j.Phase = "aborted"
	return saveEnrollmentJournal(enrollmentPath(identityPath), j)
}
