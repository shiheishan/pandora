package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentMarkHealthy(t *testing.T) {
	targetSHA := strings.Repeat("a", 64)
	a := &Agent{Dir: t.TempDir(), BinarySHA256: targetSHA}

	ok, err := a.MarkHealthy()
	if err != nil || ok {
		t.Fatalf("no applying: ok=%v err=%v", ok, err)
	}
	if _, e := os.Stat(filepath.Join(a.Dir, FileHealthy)); e == nil {
		t.Fatal("healthy should not exist")
	}

	jobID := "550e8400-e29b-41d4-a716-446655440000"
	body, _ := json.Marshal(Marker{JobID: jobID, SHA256: targetSHA})
	if err := os.WriteFile(filepath.Join(a.Dir, FileApplying), body, 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = a.MarkHealthy()
	if err != nil || !ok {
		t.Fatalf("match: ok=%v err=%v", ok, err)
	}
	hRaw, _ := os.ReadFile(filepath.Join(a.Dir, FileHealthy))
	var hm Marker
	if err := json.Unmarshal(hRaw, &hm); err != nil {
		t.Fatal(err)
	}
	if hm.JobID != jobID || hm.SHA256 != targetSHA {
		t.Fatalf("healthy marker: %+v", hm)
	}

	a2 := &Agent{Dir: t.TempDir(), BinarySHA256: strings.Repeat("b", 64)}
	_ = os.WriteFile(filepath.Join(a2.Dir, FileApplying), body, 0o644)
	ok, err = a2.MarkHealthy()
	if err != nil || ok {
		t.Fatalf("mismatch sha: ok=%v err=%v", ok, err)
	}
	if _, e := os.Stat(filepath.Join(a2.Dir, FileHealthy)); e == nil {
		t.Fatal("healthy must not be written on sha mismatch")
	}

	a3 := &Agent{Dir: t.TempDir(), BinarySHA256: targetSHA}
	link := filepath.Join(a3.Dir, "applying-link-target")
	_ = os.WriteFile(link, body, 0o644)
	if err := os.Symlink(link, filepath.Join(a3.Dir, FileApplying)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	ok, err = a3.MarkHealthy()
	if err == nil && ok {
		t.Fatal("symlink applying must not succeed as healthy")
	}
	if _, e := os.Stat(filepath.Join(a3.Dir, FileHealthy)); e == nil {
		t.Fatal("healthy must not exist for symlink applying")
	}
}

func TestAgentResultAndRemove(t *testing.T) {
	a := &Agent{Dir: t.TempDir()}

	r, err := a.Result()
	if err != nil || r != nil {
		t.Fatalf("missing: r=%v err=%v", r, err)
	}

	want := Result{
		JobID:      "job-rollback",
		Phase:      PhaseRolledBack,
		ReasonCode: ReasonConfirmTimeout,
		Detail:     "超时",
	}
	raw, _ := json.Marshal(want)
	if err := os.WriteFile(filepath.Join(a.Dir, FileResult), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := a.Result()
	if err != nil {
		t.Fatal(err)
	}
	if got.JobID != want.JobID || got.Phase != want.Phase || got.ReasonCode != want.ReasonCode || got.Detail != want.Detail {
		t.Fatalf("got %+v", got)
	}

	if err := a.RemoveResult(got); err != nil {
		t.Fatal(err)
	}
	if _, e := os.Stat(filepath.Join(a.Dir, FileResult)); e == nil {
		t.Fatal("result should be removed")
	}

	_ = os.WriteFile(filepath.Join(a.Dir, FileResult), raw, 0o644)
	other := *got
	other.JobID = "other-job"
	if err := a.RemoveResult(&other); err != nil {
		t.Fatal(err)
	}
	if _, e := os.Stat(filepath.Join(a.Dir, FileResult)); e != nil {
		t.Fatal("result must remain when job_id differs")
	}

	bad := Result{JobID: "j", Phase: "STAGED", ReasonCode: "x", Detail: "y"}
	badRaw, _ := json.Marshal(bad)
	_ = os.WriteFile(filepath.Join(a.Dir, FileResult), badRaw, 0o644)
	_, err = a.Result()
	if err == nil {
		t.Fatal("expected error for STAGED phase")
	}
}
