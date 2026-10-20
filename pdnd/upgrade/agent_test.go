package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func agentTestKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func agentTestManifest(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, serial uint64, binary []byte, includeLocalArch bool) ([]byte, []byte, string) {
	t.Helper()
	sum := sha256.Sum256(binary)
	sha := hex.EncodeToString(sum[:])
	local := ArchKey(runtime.GOARCH)
	other := "linux-arm64"
	if runtime.GOARCH == "arm64" {
		other = "linux-amd64"
	}
	arts := map[string]Artifact{
		other: {File: "pandora-native-other", SHA256: strings.Repeat("b", 64)},
	}
	if includeLocalArch {
		arts[local] = Artifact{File: "pandora-native-local", SHA256: sha}
	}
	m := Manifest{
		Schema:    1,
		Version:   "v2026.10.21",
		Serial:    serial,
		Artifacts: arts,
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	sig := helperSign(t, priv, pub, manifest)
	return manifest, sig, sha
}

func newTestAgent(t *testing.T, pub ed25519.PublicKey, srv *httptest.Server, opts func(*Agent)) *Agent {
	t.Helper()
	a := &Agent{
		Dir:         t.TempDir(),
		Keyring:     NewKeyring(pub),
		BuildSerial: 100,
		GOARCH:      runtime.GOARCH,
		Transport:   srv.Client().Transport,
	}
	if opts != nil {
		opts(a)
	}
	return a
}

func TestAgentStageSuccess(t *testing.T) {
	pub, priv := agentTestKeys(t)
	binary := []byte("pandora-binary-payload-v1")
	manifest, sig, _ := agentTestManifest(t, priv, pub, 200, binary, true)

	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(binary)
	}))
	t.Cleanup(srv.Close)

	a := newTestAgent(t, pub, srv, nil)
	jobID := "550e8400-e29b-41d4-a716-446655440000"
	err := a.Stage(context.Background(), StageRequest{
		JobID:            jobID,
		Manifest:         manifest,
		Signature:        sig,
		BinaryURL:        srv.URL + "/bin",
		ConfirmWithinSec: 180,
	})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 download, got %d", hits.Load())
	}

	next, err := os.ReadFile(filepath.Join(a.Dir, FileNext))
	if err != nil || !bytesEqual(next, binary) {
		t.Fatalf("next content mismatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Dir, FileNextTmp)); err == nil {
		t.Fatal("next.tmp should not exist")
	}
	manOnDisk, _ := os.ReadFile(filepath.Join(a.Dir, FileManifest))
	if !bytesEqual(manOnDisk, manifest) {
		t.Fatal("manifest bytes mismatch")
	}
	sigOnDisk, _ := os.ReadFile(filepath.Join(a.Dir, FileSignature))
	if !bytesEqual(sigOnDisk, sig) {
		t.Fatal("signature bytes mismatch")
	}
	var req Request
	reqRaw, _ := os.ReadFile(filepath.Join(a.Dir, FileRequest))
	if err := json.Unmarshal(reqRaw, &req); err != nil {
		t.Fatalf("request json: %v", err)
	}
	if req.JobID != jobID || req.ConfirmWithinSec != 180 {
		t.Fatalf("request fields: %+v", req)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAgentStageBadSignature(t *testing.T) {
	pub, priv := agentTestKeys(t)
	binary := []byte("x")
	manifest, sig, _ := agentTestManifest(t, priv, pub, 200, binary, true)
	sig[len(sig)-2] ^= 0xff

	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)

	a := newTestAgent(t, pub, srv, nil)
	err := a.Stage(context.Background(), StageRequest{
		JobID: "job-1", Manifest: manifest, Signature: sig, BinaryURL: srv.URL + "/b",
	})
	code, _ := ReasonOf(err, "")
	if code != ReasonSignatureInvalid {
		t.Fatalf("code=%q err=%v", code, err)
	}
	for _, name := range []string{FileNext, FileNextTmp, FileRequest, FileManifest} {
		if _, e := os.Stat(filepath.Join(a.Dir, name)); e == nil {
			t.Fatalf("unexpected file %s", name)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("server hits=%d", hits.Load())
	}
}

func TestAgentStageSerialAndArch(t *testing.T) {
	pub, priv := agentTestKeys(t)
	binary := []byte("x")

	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	a := newTestAgent(t, pub, srv, nil)

	manifest100, sig100, _ := agentTestManifest(t, priv, pub, 100, binary, true)
	err := a.Stage(context.Background(), StageRequest{
		JobID: "job-1", Manifest: manifest100, Signature: sig100, BinaryURL: srv.URL,
	})
	code, _ := ReasonOf(err, "")
	if code != ReasonSerialNotNewer {
		t.Fatalf("serial: code=%q err=%v", code, err)
	}

	manifestNoArch, sigNoArch, _ := agentTestManifest(t, priv, pub, 200, binary, false)
	err = a.Stage(context.Background(), StageRequest{
		JobID: "job-1", Manifest: manifestNoArch, Signature: sigNoArch, BinaryURL: srv.URL,
	})
	code, _ = ReasonOf(err, "")
	if code != ReasonManifestInvalid {
		t.Fatalf("arch: code=%q err=%v", code, err)
	}
}

func TestAgentStageDownload404(t *testing.T) {
	pub, priv := agentTestKeys(t)
	binary := []byte("x")
	manifest, sig, _ := agentTestManifest(t, priv, pub, 200, binary, true)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	a := newTestAgent(t, pub, srv, nil)
	err := a.Stage(context.Background(), StageRequest{
		JobID: "job-1", Manifest: manifest, Signature: sig, BinaryURL: srv.URL,
	})
	code, _ := ReasonOf(err, "")
	if code != ReasonDownloadFailed {
		t.Fatalf("code=%q err=%v", code, err)
	}
	for _, name := range []string{FileNext, FileNextTmp, FileRequest, FileManifest} {
		if _, e := os.Stat(filepath.Join(a.Dir, name)); e == nil {
			t.Fatalf("unexpected %s", name)
		}
	}
}

func TestAgentStageMaxBytes(t *testing.T) {
	pub, priv := agentTestKeys(t)
	payload := bytesRepeat('a', 17)
	manifest, sig, _ := agentTestManifest(t, priv, pub, 200, payload, true)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	a := newTestAgent(t, pub, srv, func(a *Agent) { a.MaxBytes = 16 })
	err := a.Stage(context.Background(), StageRequest{
		JobID: "job-1", Manifest: manifest, Signature: sig, BinaryURL: srv.URL,
	})
	code, _ := ReasonOf(err, "")
	if code != ReasonDownloadFailed {
		t.Fatalf("code=%q err=%v", code, err)
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestAgentStageSHA256Mismatch(t *testing.T) {
	pub, priv := agentTestKeys(t)
	binary := []byte("real-body")
	manifest, sig, _ := agentTestManifest(t, priv, pub, 200, []byte("other-body"), true)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binary)
	}))
	t.Cleanup(srv.Close)
	a := newTestAgent(t, pub, srv, nil)
	err := a.Stage(context.Background(), StageRequest{
		JobID: "job-1", Manifest: manifest, Signature: sig, BinaryURL: srv.URL,
	})
	code, _ := ReasonOf(err, "")
	if code != ReasonSHA256Mismatch {
		t.Fatalf("code=%q err=%v", code, err)
	}
	for _, name := range []string{FileNext, FileNextTmp, FileManifest} {
		if _, e := os.Stat(filepath.Join(a.Dir, name)); e == nil {
			t.Fatalf("unexpected %s", name)
		}
	}
}

func TestAgentStageBusy(t *testing.T) {
	pub, priv := agentTestKeys(t)
	binary := []byte("x")
	manifest, sig, _ := agentTestManifest(t, priv, pub, 200, binary, true)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binary)
	}))
	t.Cleanup(srv.Close)

	t.Run("request", func(t *testing.T) {
		a := newTestAgent(t, pub, srv, nil)
		oldNext := []byte("keep-next")
		_ = os.WriteFile(filepath.Join(a.Dir, FileNext), oldNext, 0o600)
		oldReq := []byte(`{"job_id":"old","confirm_within_sec":30}`)
		_ = os.WriteFile(filepath.Join(a.Dir, FileRequest), oldReq, 0o644)
		err := a.Stage(context.Background(), StageRequest{
			JobID: "job-2", Manifest: manifest, Signature: sig, BinaryURL: srv.URL,
		})
		code, _ := ReasonOf(err, "")
		if code != ReasonBusy {
			t.Fatalf("code=%q", code)
		}
		gotNext, _ := os.ReadFile(filepath.Join(a.Dir, FileNext))
		if !bytesEqual(gotNext, oldNext) {
			t.Fatal("next was modified")
		}
		gotReq, _ := os.ReadFile(filepath.Join(a.Dir, FileRequest))
		if !bytesEqual(gotReq, oldReq) {
			t.Fatal("request was modified")
		}
	})

	t.Run("applying", func(t *testing.T) {
		a := newTestAgent(t, pub, srv, nil)
		_ = os.WriteFile(filepath.Join(a.Dir, FileApplying), []byte(`{"job_id":"j","sha256":"`+strings.Repeat("c", 64)+`"}`), 0o644)
		err := a.Stage(context.Background(), StageRequest{
			JobID: "job-2", Manifest: manifest, Signature: sig, BinaryURL: srv.URL,
		})
		code, _ := ReasonOf(err, "")
		if code != ReasonBusy {
			t.Fatalf("code=%q", code)
		}
	})
}

func TestAgentStageHTTPAndRedirect(t *testing.T) {
	pub, priv := agentTestKeys(t)
	binary := []byte("x")
	manifest, sig, _ := agentTestManifest(t, priv, pub, 200, binary, true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binary)
	}))
	t.Cleanup(srv.Close)

	var hits atomic.Int32
	srvTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
	}))
	t.Cleanup(srvTLS.Close)

	a := newTestAgent(t, pub, srv, nil)
	err := a.Stage(context.Background(), StageRequest{
		JobID: "job-1", Manifest: manifest, Signature: sig,
		BinaryURL: "http://example.com/bin",
	})
	code, _ := ReasonOf(err, "")
	if code != ReasonDownloadFailed {
		t.Fatalf("http: code=%q err=%v", code, err)
	}

	a2 := newTestAgent(t, pub, srvTLS, nil)
	err = a2.Stage(context.Background(), StageRequest{
		JobID: "job-1", Manifest: manifest, Signature: sig, BinaryURL: srvTLS.URL,
	})
	code, _ = ReasonOf(err, "")
	if code != ReasonDownloadFailed {
		t.Fatalf("redirect: code=%q err=%v", code, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("redirect hits=%d", hits.Load())
	}
}

func TestAgentStageURLRedactedInError(t *testing.T) {
	pub, priv := agentTestKeys(t)
	binary := []byte("x")
	manifest, sig, _ := agentTestManifest(t, priv, pub, 200, binary, true)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	a := newTestAgent(t, pub, srv, nil)
	url := srv.URL + "/path?token=secret-value"
	err := a.Stage(context.Background(), StageRequest{
		JobID: "job-1", Manifest: manifest, Signature: sig, BinaryURL: url,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("error leaks token: %s", err.Error())
	}
}

func TestAgentStageInvalidJobID(t *testing.T) {
	pub, priv := agentTestKeys(t)
	manifest, sig, _ := agentTestManifest(t, priv, pub, 200, []byte("x"), true)
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	a := newTestAgent(t, pub, srv, nil)
	err := a.Stage(context.Background(), StageRequest{
		JobID: "../x", Manifest: manifest, Signature: sig, BinaryURL: srv.URL,
	})
	code, _ := ReasonOf(err, "")
	if code != ReasonManifestInvalid {
		t.Fatalf("code=%q err=%v", code, err)
	}
}
