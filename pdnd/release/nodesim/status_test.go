package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpdateCreatesNonNilNodes(t *testing.T) {
	b := NewBoard("", nil)
	b.Update("srv1", func(s *ServerStatus) {
		if s.Nodes == nil {
			t.Fatal("Nodes 应为非 nil map")
		}
		if s.Name != "srv1" {
			t.Fatalf("Name=%q", s.Name)
		}
	})
}

func TestRecordErrorSnapshotErrorsSum(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	b := NewBoard("", func() time.Time { return fixed })
	b.RecordError("a", errString("e1"))
	b.RecordError("b", errString("e2"))
	snap := b.Snapshot()
	if snap.Errors != 2 {
		t.Fatalf("Errors 总和应为 2，实际 %d", snap.Errors)
	}
	if snap.Servers["a"].LastError != "e1" || snap.Servers["b"].LastError != "e2" {
		t.Fatalf("LastError: a=%q b=%q", snap.Servers["a"].LastError, snap.Servers["b"].LastError)
	}
}

func TestSnapshotDeepCopy(t *testing.T) {
	b := NewBoard("", nil)
	b.Update("s", func(s *ServerStatus) {
		s.Nodes["n1"] = &NodeStatus{Users: []string{"u1"}}
	})
	snap := b.Snapshot()
	b.Update("s", func(s *ServerStatus) {
		s.Nodes["n1"].Users = []string{"u2"}
	})
	if len(snap.Servers["s"].Nodes["n1"].Users) != 1 || snap.Servers["s"].Nodes["n1"].Users[0] != "u1" {
		t.Fatalf("快照 Users 不应被后续 Update 改变: %v", snap.Servers["s"].Nodes["n1"].Users)
	}
}

func TestWriteRoundTripAndPerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上文件权限语义不同")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	b := NewBoard(path, func() time.Time { return fixed })
	b.Update("sim", func(s *ServerStatus) {
		s.Enrolled = true
		s.HeartbeatsSent = 3
	})
	if err := b.Write(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "status.json" {
		t.Fatalf("目录应只有 status.json，实际: %v", entries)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("权限应为 0644，实际 %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Snapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(fixed) {
		t.Fatalf("UpdatedAt: got %v want %v", got.UpdatedAt, fixed)
	}
	if got.Servers["sim"] == nil || !got.Servers["sim"].Enrolled || got.Servers["sim"].HeartbeatsSent != 3 {
		t.Fatalf("%+v", got.Servers["sim"])
	}
}

func TestWriteEmptyPath(t *testing.T) {
	b := NewBoard("", nil)
	if err := b.Write(); err != nil {
		t.Fatal(err)
	}
}

func TestBoardConcurrent(t *testing.T) {
	b := NewBoard("", nil)
	const goroutines = 20
	const per = 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		name := "srv" + string(rune('a'+g%5))
		go func(n string) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				b.Update(n, func(s *ServerStatus) {
					s.HeartbeatsSent++
				})
				b.RecordError(n, errString("x"))
			}
		}(name)
	}
	wg.Wait()
	snap := b.Snapshot()
	var hb int
	var errs int
	for _, s := range snap.Servers {
		hb += s.HeartbeatsSent
		errs += s.Errors
	}
	if hb != goroutines*per {
		t.Fatalf("HeartbeatsSent 总和 %d want %d", hb, goroutines*per)
	}
	if errs != goroutines*per {
		t.Fatalf("Errors 总和 %d want %d", errs, goroutines*per)
	}
	if snap.Errors != errs {
		t.Fatalf("Snapshot.Errors %d vs sum %d", snap.Errors, errs)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestSnapshotDeepCopyRosterHistory(t *testing.T) {
	fixed := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	b := NewBoard("", nil)
	b.Update("s", func(s *ServerStatus) {
		s.Nodes["n1"] = &NodeStatus{
			RosterHistory: []RosterApply{{Version: "v1", AppliedAt: fixed, Via: "full"}},
		}
	})
	snap := b.Snapshot()
	snap.Servers["s"].Nodes["n1"].RosterHistory[0].Version = "mutated"
	snap2 := b.Snapshot()
	if snap2.Servers["s"].Nodes["n1"].RosterHistory[0].Version != "v1" {
		t.Fatalf("Board 内 History 被快照修改污染: %v", snap2.Servers["s"].Nodes["n1"].RosterHistory)
	}
}

func TestWriteOmitsZeroTimes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	b := NewBoard(path, nil)
	b.Update("sim", func(s *ServerStatus) {
		s.Enrolled = true
	})
	if err := b.Write(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{"connected_at", "first_connected_at", "disconnected_at", "roster_applied_at"} {
		if strings.Contains(text, "\""+key+"\"") {
			t.Fatalf("零值时间不应出现在 JSON: %s", key)
		}
	}

	connAt := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	b.Update("sim", func(s *ServerStatus) {
		s.ConnectedAt = connAt
	})
	if err := b.Write(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"connected_at\"") {
		t.Fatal("设了 ConnectedAt 后 JSON 应含 connected_at")
	}
}
