package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeWorkRecord(t *testing.T, work string, rec workRecord) {
	t.Helper()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileSync(filepath.Join(work, FileApplying), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHelperResume_InstalledHealthy(t *testing.T) {
	e := newHelperEnv(t)
	if err := os.WriteFile(e.bin, []byte(helperNewBin), 0o755); err != nil {
		t.Fatal(err)
	}
	prevSHA := sha256HexOf(helperOldBin)
	writeWorkRecord(t, e.work, workRecord{
		JobID: e.jobID, TargetSHA256: e.newSHA, PrevSHA256: prevSHA,
		ConfirmWithinSec: 180, StartedAt: e.now.Unix(), Stage: stageInstalled,
	})
	e.fake.onRestart = func(n int) error {
		if n == 1 {
			return e.writeHealthy()
		}
		return nil
	}
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.fake.restarts != 1 {
		t.Fatalf("restarts %d want 1", e.fake.restarts)
	}
	e.assertWorkEmpty()
}

func TestHelperResume_InstalledResumed(t *testing.T) {
	e := newHelperEnv(t)
	if err := os.WriteFile(e.bin, []byte(helperNewBin), 0o755); err != nil {
		t.Fatal(err)
	}
	prevSHA := sha256HexOf(helperOldBin)

	t.Run("healthy present no restart", func(t *testing.T) {
		e2 := newHelperEnv(t)
		_ = os.WriteFile(e2.bin, []byte(helperNewBin), 0o755)
		writeWorkRecord(t, e2.work, workRecord{
			JobID: e2.jobID, TargetSHA256: e2.newSHA, PrevSHA256: prevSHA,
			ConfirmWithinSec: 180, StartedAt: e2.now.Unix(), Stage: stageInstalled, Resumed: true,
		})
		if err := e2.writeHealthy(); err != nil {
			t.Fatal(err)
		}
		if err := e2.helper.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if e2.fake.restarts != 0 {
			t.Fatalf("restarts %d", e2.fake.restarts)
		}
		e2.assertWorkEmpty()
	})

	t.Run("no healthy rollback", func(t *testing.T) {
		e3 := newHelperEnv(t)
		_ = os.WriteFile(e3.bin, []byte(helperNewBin), 0o755)
		if err := copyFileSync(e3.bin, e3.bin+".prev", 0o755); err != nil {
			t.Fatal(err)
		}
		// 把 .prev 改回旧版内容以便回退
		if err := os.WriteFile(e3.bin+".prev", []byte(helperOldBin), 0o755); err != nil {
			t.Fatal(err)
		}
		writeWorkRecord(t, e3.work, workRecord{
			JobID: e3.jobID, TargetSHA256: e3.newSHA, PrevSHA256: prevSHA,
			ConfirmWithinSec: 180, StartedAt: e3.now.Unix(), Stage: stageInstalled, Resumed: true,
		})
		if err := e3.helper.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		e3.assertBinWant(helperOldBin)
		res, err := e3.readResult()
		if err != nil || res.ReasonCode != ReasonConfirmTimeout {
			t.Fatalf("res %+v err %v", res, err)
		}
	})
}

func TestHelperResume_InstallingStillOld(t *testing.T) {
	e := newHelperEnv(t)
	prevSHA := sha256HexOf(helperOldBin)
	writeWorkRecord(t, e.work, workRecord{
		JobID: e.jobID, TargetSHA256: e.newSHA, PrevSHA256: prevSHA,
		ConfirmWithinSec: 180, StartedAt: e.now.Unix(), Stage: stageInstalling,
	})
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
	e.assertWorkEmpty()
}

func TestHelperResume_InstallingAlreadyNew(t *testing.T) {
	e := newHelperEnv(t)
	_ = os.WriteFile(e.bin, []byte(helperNewBin), 0o755)
	prevSHA := sha256HexOf(helperOldBin)
	writeWorkRecord(t, e.work, workRecord{
		JobID: e.jobID, TargetSHA256: e.newSHA, PrevSHA256: prevSHA,
		ConfirmWithinSec: 180, StartedAt: e.now.Unix(), Stage: stageInstalling,
	})
	e.fake.onRestart = func(n int) error {
		if n == 1 {
			return e.writeHealthy()
		}
		return nil
	}
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertBinWant(helperNewBin)
	e.assertWorkEmpty()
}

func TestHelperResume_RollingBack(t *testing.T) {
	e := newHelperEnv(t)
	_ = os.WriteFile(e.bin, []byte(helperNewBin), 0o755)
	_ = os.WriteFile(e.bin+".prev", []byte(helperOldBin), 0o755)
	prevSHA := sha256HexOf(helperOldBin)
	writeWorkRecord(t, e.work, workRecord{
		JobID: e.jobID, TargetSHA256: e.newSHA, PrevSHA256: prevSHA,
		ConfirmWithinSec: 180, StartedAt: e.now.Unix(), Stage: stageRollingBack,
		ResultPhase: PhaseRolledBack, ResultCode: ReasonConfirmTimeout, ResultDetail: "custom",
	})
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertBinWant(helperOldBin)
	res, err := e.readResult()
	if err != nil || res.Phase != PhaseRolledBack || res.ReasonCode != ReasonConfirmTimeout {
		t.Fatalf("res %+v err %v", res, err)
	}
	if e.fake.restarts != 1 {
		t.Fatalf("restarts %d want 1", e.fake.restarts)
	}
	e.assertWorkEmpty()
}

func TestHelperResume_NoLoop(t *testing.T) {
	e := newHelperEnv(t)
	_ = os.WriteFile(e.bin, []byte(helperNewBin), 0o755)
	prevSHA := sha256HexOf(helperOldBin)
	writeWorkRecord(t, e.work, workRecord{
		JobID: e.jobID, TargetSHA256: e.newSHA, PrevSHA256: prevSHA,
		ConfirmWithinSec: 180, StartedAt: e.now.Unix(), Stage: stageInstalled,
	})
	e.fake.onRestart = func(n int) error { return e.writeHealthy() }
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarts := e.fake.restarts
	if _, err := os.Lstat(filepath.Join(e.work, FileApplying)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("applying should be gone")
	}
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.fake.restarts != restarts {
		t.Fatalf("second run restarted: %d vs %d", e.fake.restarts, restarts)
	}
}

func TestHelperResume_ContextCancelThenSuccess(t *testing.T) {
	e := newHelperEnv(t)
	_ = os.WriteFile(e.bin, []byte(helperNewBin), 0o755)
	prevSHA := sha256HexOf(helperOldBin)
	writeWorkRecord(t, e.work, workRecord{
		JobID: e.jobID, TargetSHA256: e.newSHA, PrevSHA256: prevSHA,
		ConfirmWithinSec: 180, StartedAt: e.now.Unix(), Stage: stageInstalling,
	})
	ctx, cancel := context.WithCancel(context.Background())
	var firstConfirm bool
	e.helper.Sleep = func(c context.Context, d time.Duration) error {
		if !firstConfirm {
			firstConfirm = true
			cancel()
			return context.Canceled
		}
		return e.advanceSleep(c, d)
	}
	if err := e.helper.Run(ctx); err == nil {
		t.Fatal("expected cancel error")
	}
	if _, err := os.Lstat(filepath.Join(e.work, FileApplying)); err != nil {
		t.Fatal("applying should remain")
	}
	e.helper.Sleep = e.advanceSleep
	e.fake.onRestart = func(n int) error { return e.writeHealthy() }
	if err := e.helper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.assertWorkEmpty()
}
