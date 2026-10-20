package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	helperOldBin = "old-binary"
	helperNewBin = "new-binary"
)

type helperEnv struct {
	t      *testing.T
	parent string
	work   string
	bin    string
	jobID  string
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	newSHA string
	fake   *fakeService
	now    time.Time
	helper *Helper
}

type fakeService struct {
	restarts    int
	onRestart   func(n int) error
	failRestart int // 第 n 次 Restart 返回错误，0 表示不失败
}

func (f *fakeService) Restart(ctx context.Context, unit string) error {
	f.restarts++
	if f.failRestart > 0 && f.restarts == f.failRestart {
		return errors.New("restart failed")
	}
	if f.onRestart != nil {
		return f.onRestart(f.restarts)
	}
	return nil
}

func sha256HexOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newHelperEnv(t *testing.T) *helperEnv {
	t.Helper()
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	upgrade := filepath.Join(parent, StateSubdir)
	work := filepath.Join(root, "work")
	binDir := filepath.Join(root, "bin")
	bin := filepath.Join(binDir, "pandora-native")

	if err := os.MkdirAll(upgrade, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(helperOldBin), 0o755); err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeService{}
	start := time.Unix(1_700_000_000, 0)
	e := &helperEnv{
		t:      t,
		parent: parent,
		work:   work,
		bin:    bin,
		jobID:  "550e8400-e29b-41d4-a716-446655440000",
		pub:    pub,
		priv:   priv,
		newSHA: sha256HexOf(helperNewBin),
		fake:   fake,
		now:    start,
	}
	e.helper = &Helper{
		StateParent:  parent,
		WorkDir:      work,
		BinPath:      bin,
		Keyring:      NewKeyring(pub),
		BuildSerial:  100,
		GOARCH:       runtime.GOARCH,
		Service:      fake,
		Now:          func() time.Time { return e.now },
		Sleep:        e.advanceSleep,
		PollInterval: time.Second,
	}
	return e
}

func (e *helperEnv) advanceSleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.now = e.now.Add(d)
	return nil
}

func (e *helperEnv) manifestBytes() []byte {
	m := Manifest{
		Schema:  1,
		Version: "v2026.10.21",
		Serial:  200,
		Artifacts: map[string]Artifact{
			ArchKey(runtime.GOARCH): {
				File:   "pandora-native-linux",
				SHA256: e.newSHA,
			},
		},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		e.t.Fatal(err)
	}
	return raw
}

func (e *helperEnv) stage() {
	e.stageTo(filepath.Join(e.parent, StateSubdir))
}

func (e *helperEnv) stageTo(upgradeDir string) {
	req := Request{JobID: e.jobID, ConfirmWithinSec: 180}
	reqRaw, _ := json.Marshal(req)
	paths := map[string][]byte{
		FileRequest:   reqRaw,
		FileNext:      []byte(helperNewBin),
		FileManifest:  e.manifestBytes(),
		FileSignature: helperSign(e.t, e.priv, e.pub, e.manifestBytes()),
	}
	for name, data := range paths {
		if err := os.WriteFile(filepath.Join(upgradeDir, name), data, 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *helperEnv) upgradeDir() string {
	return filepath.Join(e.parent, StateSubdir)
}

func (e *helperEnv) writeHealthy() error {
	state, err := openStateDir(e.parent)
	if err != nil {
		return err
	}
	defer state.Close()
	raw, _ := json.Marshal(Marker{JobID: e.jobID, SHA256: e.newSHA})
	return writeState(state, FileHealthy, raw)
}

func (e *helperEnv) readResult() (*Result, error) {
	state, err := openStateDir(e.parent)
	if err != nil {
		return nil, err
	}
	defer state.Close()
	raw, err := readState(state, FileResult, MaxSmallFileBytes)
	if err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (e *helperEnv) assertBinWant(want string) {
	b, err := os.ReadFile(e.bin)
	if err != nil {
		e.t.Fatal(err)
	}
	if string(b) != want {
		e.t.Fatalf("bin content %q, want %q", b, want)
	}
}

func (e *helperEnv) assertWorkEmpty() {
	entries, err := os.ReadDir(e.work)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		e.t.Fatal(err)
	}
	if len(entries) > 0 {
		e.t.Fatalf("work dir not empty: %v", entries)
	}
}

func (e *helperEnv) assertStateCleared() {
	dir := e.upgradeDir()
	for _, name := range []string{FileRequest, FileNext, FileManifest, FileSignature, FileApplying, FileHealthy, FileResult} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			e.t.Fatalf("state still has %s", name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			e.t.Fatal(err)
		}
	}
}

func TestHelper_Success(t *testing.T) {
	e := newHelperEnv(t)
	e.fake.onRestart = func(n int) error {
		if n == 1 {
			return e.writeHealthy()
		}
		return nil
	}
	e.stage()
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertBinWant(helperNewBin)
	prev, err := os.ReadFile(e.bin + ".prev")
	if err != nil || string(prev) != helperOldBin {
		t.Fatalf(".prev: %v content %q", err, prev)
	}
	e.assertStateCleared()
	e.assertWorkEmpty()
	if e.fake.restarts != 1 {
		t.Fatalf("restarts %d, want 1", e.fake.restarts)
	}
}

func TestHelper_ConfirmTimeout(t *testing.T) {
	e := newHelperEnv(t)
	e.stage()
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertBinWant(helperOldBin)
	res, err := e.readResult()
	if err != nil {
		t.Fatal(err)
	}
	if res.JobID != e.jobID || res.Phase != PhaseRolledBack || res.ReasonCode != ReasonConfirmTimeout {
		t.Fatalf("result %+v", res)
	}
	if e.fake.restarts != 2 {
		t.Fatalf("restarts %d, want 2", e.fake.restarts)
	}
	e.assertWorkEmpty()
}

func TestHelper_HealthyWrongSHA(t *testing.T) {
	e := newHelperEnv(t)
	e.fake.onRestart = func(n int) error {
		if n == 1 {
			state, err := openStateDir(e.parent)
			if err != nil {
				return err
			}
			defer state.Close()
			raw, _ := json.Marshal(Marker{JobID: e.jobID, SHA256: strings.Repeat("0", 64)})
			return writeState(state, FileHealthy, raw)
		}
		return nil
	}
	e.stage()
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertBinWant(helperOldBin)
	res, _ := e.readResult()
	if res == nil || res.ReasonCode != ReasonConfirmTimeout {
		t.Fatalf("result %+v", res)
	}
}

func TestHelper_RollbackPrevMissing(t *testing.T) {
	e := newHelperEnv(t)
	e.fake.onRestart = func(n int) error {
		if n == 1 {
			_ = os.Remove(e.bin + ".prev")
		}
		return nil
	}
	e.stage()
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertBinWant(helperNewBin)
	res, err := e.readResult()
	if err != nil {
		t.Fatal(err)
	}
	if res.ReasonCode != ReasonRollbackFailed || res.Phase != PhaseFailed {
		t.Fatalf("result %+v", res)
	}
	e.assertWorkEmpty()
}

func TestHelper_FirstRestartFails(t *testing.T) {
	e := newHelperEnv(t)
	e.fake.failRestart = 1
	e.stage()
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertBinWant(helperOldBin)
	res, _ := e.readResult()
	if res == nil || res.ReasonCode != ReasonApplyFailed {
		t.Fatalf("result %+v", res)
	}
	if e.fake.restarts != 2 {
		t.Fatalf("restarts %d, want 2", e.fake.restarts)
	}
}

func TestHelper_ValidationFailures(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(e *helperEnv, manifest []byte, sig []byte) ([]byte, []byte)
		buildSerial uint64
		goarch      string
		wantCode    string
	}{
		{
			name: "bad signature",
			mutate: func(e *helperEnv, manifest []byte, sig []byte) ([]byte, []byte) {
				return manifest, []byte("not-json")
			},
			wantCode: ReasonSignatureInvalid,
		},
		{
			name:        "serial not newer build 200",
			buildSerial: 200,
			mutate: func(e *helperEnv, manifest []byte, sig []byte) ([]byte, []byte) {
				return manifest, sig
			},
			wantCode: ReasonSerialNotNewer,
		},
		{
			name:        "dev build serial 0",
			buildSerial: 0,
			mutate: func(e *helperEnv, manifest []byte, sig []byte) ([]byte, []byte) {
				return manifest, sig
			},
			wantCode: ReasonSerialNotNewer,
		},
		{
			name: "missing arch",
			mutate: func(e *helperEnv, manifest []byte, sig []byte) ([]byte, []byte) {
				m := Manifest{
					Schema: 1, Version: "v1", Serial: 200,
					Artifacts: map[string]Artifact{
						ArchKey("other-" + runtime.GOARCH): {File: "bin", SHA256: e.newSHA}, // 本机架构之外的一项
					},
				}
				raw, _ := json.Marshal(m)
				return raw, helperSign(e.t, e.priv, e.pub, raw)
			},
			wantCode: ReasonManifestInvalid,
		},
		{
			name: "sha256 mismatch",
			mutate: func(e *helperEnv, manifest []byte, sig []byte) ([]byte, []byte) {
				return manifest, sig
			},
			wantCode: ReasonSHA256Mismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHelperEnv(t)
			if tc.buildSerial != 0 {
				e.helper.BuildSerial = tc.buildSerial
			}
			if tc.name == "dev build serial 0" {
				e.helper.BuildSerial = 0
			}
			manifest := e.manifestBytes()
			sig := helperSign(t, e.priv, e.pub, manifest)
			manifest, sig = tc.mutate(e, manifest, sig)

			req := Request{JobID: e.jobID, ConfirmWithinSec: 180}
			reqRaw, _ := json.Marshal(req)
			dir := e.upgradeDir()
			nextBody := helperNewBin
			if tc.name == "sha256 mismatch" {
				nextBody = "tampered-binary"
			}
			for name, data := range map[string][]byte{
				FileRequest: reqRaw, FileNext: []byte(nextBody), FileManifest: manifest, FileSignature: sig,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			if err := e.helper.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			e.assertBinWant(helperOldBin)
			if e.fake.restarts != 0 {
				t.Fatalf("restarts %d", e.fake.restarts)
			}
			res, err := e.readResult()
			if err != nil {
				t.Fatal(err)
			}
			if res.ReasonCode != tc.wantCode {
				t.Fatalf("code %q want %q", res.ReasonCode, tc.wantCode)
			}
			for _, name := range []string{FileRequest, FileNext, FileManifest, FileSignature} {
				if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("state still has %s", name)
				}
			}
		})
	}
}

func TestHelper_NextSymlink(t *testing.T) {
	e := newHelperEnv(t)
	dir := e.upgradeDir()
	target := filepath.Join(e.t.TempDir(), "evil-next")
	if err := os.WriteFile(target, []byte(helperNewBin), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, FileNext)); err != nil {
		t.Fatal(err)
	}
	req := Request{JobID: e.jobID, ConfirmWithinSec: 180}
	reqRaw, _ := json.Marshal(req)
	manifest := e.manifestBytes()
	sig := helperSign(t, e.priv, e.pub, manifest)
	for name, data := range map[string][]byte{
		FileRequest: reqRaw, FileManifest: manifest, FileSignature: sig,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertBinWant(helperOldBin)
	if e.fake.restarts != 0 {
		t.Fatalf("restarts %d", e.fake.restarts)
	}
	res, _ := e.readResult()
	if res == nil || res.ReasonCode != ReasonApplyFailed {
		t.Fatalf("result %+v", res)
	}
}

func TestHelper_RequestSymlink(t *testing.T) {
	e := newHelperEnv(t)
	dir := e.upgradeDir()
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, FileRequest)); err != nil {
		t.Fatal(err)
	}
	manifest := e.manifestBytes()
	sig := helperSign(t, e.priv, e.pub, manifest)
	for name, data := range map[string][]byte{
		FileNext: []byte(helperNewBin), FileManifest: manifest, FileSignature: sig,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertBinWant(helperOldBin)
	if e.fake.restarts != 0 {
		t.Fatalf("restarts %d", e.fake.restarts)
	}
	if _, err := os.Lstat(filepath.Join(dir, FileResult)); err == nil {
		t.Fatal("unexpected result file")
	}
	if _, err := os.Lstat(filepath.Join(dir, FileRequest)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("request should be removed from state")
	}
}

func TestHelper_NoOp(t *testing.T) {
	e := newHelperEnv(t)
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.fake.restarts != 0 {
		t.Fatal("unexpected restart")
	}
	e.assertBinWant(helperOldBin)

	missingParent := filepath.Join(e.t.TempDir(), "no-state")
	h := *e.helper
	h.StateParent = missingParent
	if err := h.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHelper_StateDirSymlink(t *testing.T) {
	e := newHelperEnv(t)
	realDir := filepath.Join(e.t.TempDir(), "real-upgrade")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.stageTo(realDir)
	upgrade := e.upgradeDir()
	_ = os.RemoveAll(upgrade)
	if err := os.Symlink(realDir, upgrade); err != nil {
		t.Fatal(err)
	}
	if err := e.helper.Run(context.Background()); err == nil {
		t.Fatal("expected openStateDir to fail on symlink upgrade dir")
	}
	e.assertBinWant(helperOldBin)
	if e.fake.restarts != 0 {
		t.Fatalf("restarts %d", e.fake.restarts)
	}
	for _, name := range []string{FileRequest, FileNext, FileManifest, FileSignature} {
		if _, err := os.Lstat(filepath.Join(realDir, name)); errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("real dir lost %s", name)
		}
	}
}
