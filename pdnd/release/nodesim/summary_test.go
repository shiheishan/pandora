package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSummarizeCounts(t *testing.T) {
	t1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 1, 10, 1, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 10, 2, 0, 0, time.UTC)
	disc := time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)

	snap := Snapshot{
		Errors: 2,
		Servers: map[string]*ServerStatus{
			"sA": {
				Enrolled:         true,
				Connected:        true,
				FirstConnectedAt: t1,
				ConnectedAt:      t2,
				Nodes: map[string]*NodeStatus{
					"n1": {FullsReceived: 1, DeltasReceived: 2},
				},
			},
			"sB": {
				Enrolled:         true,
				Connected:        false,
				FirstConnectedAt: t3,
				ConnectedAt:      t2, // 未连接，不计入 connected min/max
				DisconnectedAt:   disc,
				Nodes: map[string]*NodeStatus{
					"n1": {FullsReceived: 3, DeltasReceived: 0},
				},
			},
			"sC": {
				Enrolled:         true,
				Connected:        true,
				Tombstoned:       true,
				FirstConnectedAt: t2,
				ConnectedAt:      t3,
				Nodes: map[string]*NodeStatus{
					"n1": {FullsReceived: 0, DeltasReceived: 4},
				},
			},
		},
	}

	s := summarize(snap, "")
	if s.Servers != 3 || s.Enrolled != 3 || s.Connected != 2 || s.Tombstoned != 1 || s.Errors != 2 || s.Nodes != 3 {
		t.Fatalf("计数: %+v", s)
	}
	if s.Fulls != 4 || s.Deltas != 6 {
		t.Fatalf("rosters fulls/deltas: fulls=%d deltas=%d", s.Fulls, s.Deltas)
	}
	if !s.FirstConnectedMin.Equal(t1) || !s.FirstConnectedMax.Equal(t3) {
		t.Fatalf("first_connected: min=%v max=%v", s.FirstConnectedMin, s.FirstConnectedMax)
	}
	if !s.ConnectedMin.Equal(t2) || !s.ConnectedMax.Equal(t3) {
		t.Fatalf("connected: min=%v max=%v", s.ConnectedMin, s.ConnectedMax)
	}
	if !s.DisconnectedMax.Equal(disc) {
		t.Fatalf("disconnected max=%v", s.DisconnectedMax)
	}
}

func TestSummarizeVersionGroups(t *testing.T) {
	tEarly := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	tLate := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	tMid := time.Date(2026, 4, 1, 8, 30, 0, 0, time.UTC)

	snap := Snapshot{
		Servers: map[string]*ServerStatus{
			"s1": {
				Nodes: map[string]*NodeStatus{
					"a": {RosterVersion: "v2", RosterAppliedAt: tEarly},
					"b": {RosterVersion: "v2", RosterAppliedAt: tLate},
					"c": {RosterVersion: "v2", RosterAppliedAt: tMid},
					"d": {RosterVersion: "v1", RosterAppliedAt: tMid},
					"e": {RosterVersion: ""},
				},
			},
		},
	}

	s := summarize(snap, "")
	if len(s.Versions) != 2 {
		t.Fatalf("版本组数 %d", len(s.Versions))
	}
	if s.Versions[0].Version != "v2" || s.Versions[0].Nodes != 3 {
		t.Fatalf("第一组应为 v2×3: %+v", s.Versions[0])
	}
	if !s.Versions[0].AppliedMin.Equal(tEarly) || !s.Versions[0].AppliedMax.Equal(tLate) {
		t.Fatalf("v2 applied: min=%v max=%v", s.Versions[0].AppliedMin, s.Versions[0].AppliedMax)
	}
	if s.Versions[1].Version != "v1" || s.Versions[1].Nodes != 1 {
		t.Fatalf("第二组应为 v1×1: %+v", s.Versions[1])
	}
	if !s.Versions[1].AppliedMin.Equal(tMid) || !s.Versions[1].AppliedMax.Equal(tMid) {
		t.Fatalf("v1 applied: min=%v max=%v", s.Versions[1].AppliedMin, s.Versions[1].AppliedMax)
	}
}

func TestSummarizeTargetFromHistory(t *testing.T) {
	tA := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	tB := time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC)

	snap := Snapshot{
		Servers: map[string]*ServerStatus{
			"sA": {
				Nodes: map[string]*NodeStatus{
					"nA": {RosterVersion: "v1", RosterAppliedAt: tA},
				},
			},
			"sB": {
				Nodes: map[string]*NodeStatus{
					"nB": {
						RosterVersion: "v2",
						RosterHistory: []RosterApply{
							{Version: "v1", AppliedAt: tB, Via: "delta"},
						},
					},
				},
			},
			"sC": {
				Nodes: map[string]*NodeStatus{
					"nC": {RosterVersion: "v0"},
				},
			},
		},
	}

	s := summarize(snap, "v1")
	if s.Target == nil {
		t.Fatal("应有 Target")
	}
	if s.Target.Reached != 2 || s.Target.Total != 3 {
		t.Fatalf("reached=%d total=%d", s.Target.Reached, s.Target.Total)
	}
	if len(s.Target.Missing) != 1 || s.Target.Missing[0] != "sC/nC" {
		t.Fatalf("missing=%v", s.Target.Missing)
	}
	if !s.Target.AppliedMin.Equal(tA) || !s.Target.AppliedMax.Equal(tB) {
		t.Fatalf("applied min=%v max=%v", s.Target.AppliedMin, s.Target.AppliedMax)
	}
}

func TestPrintSummaryFormat(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	at := since.Add(1500 * time.Millisecond)
	tStr := at.UTC().Format(time.RFC3339Nano)

	sum := Summary{
		Servers: 1, Enrolled: 1, Connected: 1, Tombstoned: 0, Errors: 0, Nodes: 1,
		FirstConnectedMin: at, FirstConnectedMax: at,
		ConnectedMin: at, ConnectedMax: at,
		Fulls: 2, Deltas: 1,
		Versions: []VersionGroup{{
			Version: "v9", Nodes: 1, AppliedMin: at, AppliedMax: at,
		}},
		Target: &VersionReach{
			Version: "v9", Reached: 0, Total: 1,
			AppliedMin: time.Time{}, AppliedMax: time.Time{},
			Missing: []string{"host/n1"},
		},
	}

	var buf bytes.Buffer
	if err := printSummary(&buf, sum, since); err != nil {
		t.Fatal(err)
	}

	want := strings.Join([]string{
		"servers=1 enrolled=1 connected=1 tombstoned=0 errors=0 nodes=1",
		"first_connected min=" + tStr + " (+1.500s) max=" + tStr + " (+1.500s)",
		"connected min=" + tStr + " (+1.500s) max=" + tStr + " (+1.500s)",
		"disconnected max=-",
		"rosters fulls=2 deltas=1",
		"version v9 nodes=1 applied_min=" + tStr + " (+1.500s) applied_max=" + tStr + " (+1.500s)",
		"target v9 reached=0/1 applied_min=- applied_max=-",
		"missing host/n1",
	}, "\n") + "\n"

	if buf.String() != want {
		t.Fatalf("输出不一致\n--- got ---\n%s--- want ---\n%s", buf.String(), want)
	}
}

func TestRunSummary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	snap := Snapshot{
		Servers: map[string]*ServerStatus{
			"sim": {
				Enrolled: true,
				Nodes: map[string]*NodeStatus{
					"n1": {RosterVersion: "v1", RosterAppliedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
				},
			},
		},
	}
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runSummary([]string{"--status-file", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	first := strings.SplitN(stdout.String(), "\n", 2)[0]
	if !strings.HasPrefix(first, "servers=") {
		t.Fatalf("首行: %q", first)
	}

	stdout.Reset()
	stderr.Reset()
	code = runSummary([]string{"--status-file", path, "--version", "v-none"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("未达版本应返回 1，实际 %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	code = runSummary([]string{}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("缺 status-file 应返回 2，实际 %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	code = runSummary([]string{"--status-file", filepath.Join(dir, "missing.json")}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("文件不存在应返回 2，实际 %d", code)
	}
}
