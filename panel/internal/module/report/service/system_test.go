package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
)

func saveOffsitePaths() []string {
	return offsiteConfigPaths
}

func TestProbeBackupUnreadable(t *testing.T) {
	orig := saveOffsitePaths()
	offsiteConfigPaths = nil
	t.Cleanup(func() { offsiteConfigPaths = orig })

	e := newTestEnv()
	missing := filepath.Join(t.TempDir(), "no-such-backup-dir")
	e.svc.opt.BackupDir = missing

	st := e.svc.probeBackup(testNow)
	if st.Readable || !strings.Contains(st.Message, missing) {
		t.Fatalf("probeBackup unreadable dir: %+v", st)
	}

	e.svc.opt.BackupDir = ""
	st = e.svc.probeBackup(testNow)
	if st.Readable || !strings.Contains(st.Message, "backup.dir") {
		t.Fatalf("empty BackupDir: %+v", st)
	}
}

func TestProbeBackupLatestNeedsChecksumAndSeal(t *testing.T) {
	orig := saveOffsitePaths()
	offsiteConfigPaths = nil
	t.Cleanup(func() { offsiteConfigPaths = orig })

	dir := t.TempDir()
	e := newTestEnv()
	e.svc.opt.BackupDir = dir

	writeDump := func(name string, body string, mod time.Time) int64 {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return int64(len(body))
	}
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t2h := testNow.Add(-2 * time.Hour)
	t1h := testNow.Add(-1 * time.Hour)
	t3h := testNow.Add(-3 * time.Hour)

	szA := writeDump("a.dump.age", "aaa", t2h)
	touch("a.dump.age.sha256")
	touch("a.dump.age.seal")
	szB := writeDump("b.dump.age", "bb", t1h)
	touch("b.dump.age.sha256")
	szC := writeDump("c.dump.age", "c", t3h)

	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := e.svc.probeBackup(testNow)
	if st.Count != 3 || st.Latest == nil || st.Latest.Name != "a.dump.age" {
		t.Fatalf("Latest: %+v", st.Latest)
	}
	if st.LatestAgeHours == nil || *st.LatestAgeHours != 2 {
		t.Fatalf("LatestAgeHours=%v want 2", st.LatestAgeHours)
	}
	if st.Stale {
		t.Fatal("expected fresh backup")
	}
	if st.MissingChecksum == nil || *st.MissingChecksum != 1 {
		t.Fatalf("MissingChecksum=%v want 1", st.MissingChecksum)
	}
	if st.MissingSeal == nil || *st.MissingSeal != 2 {
		t.Fatalf("MissingSeal=%v want 2", st.MissingSeal)
	}
	if st.TotalBytes != szA+szB+szC {
		t.Fatalf("TotalBytes=%d want %d", st.TotalBytes, szA+szB+szC)
	}
	if len(st.Recent) != 3 || st.Recent[0].Name != "b.dump.age" || st.Recent[1].Name != "a.dump.age" || st.Recent[2].Name != "c.dump.age" {
		t.Fatalf("Recent order: %+v", st.Recent)
	}
	if st.OffsiteConfigured {
		t.Fatal("offsite should be false")
	}

	if err := os.WriteFile(filepath.Join(dir, "backup-webdav.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	st = e.svc.probeBackup(testNow)
	if !st.OffsiteConfigured {
		t.Fatal("offsite should be true")
	}
}

func TestProbeBackupStale(t *testing.T) {
	orig := saveOffsitePaths()
	offsiteConfigPaths = nil
	t.Cleanup(func() { offsiteConfigPaths = orig })

	dir := t.TempDir()
	e := newTestEnv()
	e.svc.opt.BackupDir = dir

	mod := testNow.Add(-50 * time.Hour)
	name := "old.dump.age"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, suf := range []string{".sha256", ".seal"} {
		if err := os.WriteFile(filepath.Join(dir, name+suf), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(dir, name), mod, mod); err != nil {
		t.Fatal(err)
	}

	st := e.svc.probeBackup(testNow)
	if !st.Stale {
		t.Fatal("expected stale")
	}

	dir2 := t.TempDir()
	e.svc.opt.BackupDir = dir2
	if err := os.WriteFile(filepath.Join(dir2, "incomplete.dump.age"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st = e.svc.probeBackup(testNow)
	if !st.Stale || st.Latest != nil || st.LatestAgeHours != nil {
		t.Fatalf("no complete backup: Latest=%v Stale=%v Age=%v", st.Latest, st.Stale, st.LatestAgeHours)
	}
}

func setupSystemStatusOK(t *testing.T) (*testEnv, string) {
	orig := saveOffsitePaths()
	offsiteConfigPaths = nil
	t.Cleanup(func() { offsiteConfigPaths = orig })

	dir := t.TempDir()
	e := newTestEnv()
	e.svc.opt.BackupDir = dir

	mod := testNow.Add(-1 * time.Hour)
	name := "ok.dump.age"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, suf := range []string{".sha256", ".seal"} {
		if err := os.WriteFile(filepath.Join(dir, name+suf), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(dir, name), mod, mod); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup-webdav.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	e.store.dbStats.SizeBytes = 100
	e.store.dbStats.Connections = 3
	e.store.dbStats.MaxConnections = 100
	e.ports.nodeCounts = nodesvc.ReportNodeCounts{Total: 2, Online: 2}
	e.ports.configLagging = 0
	e.ports.stuck = 0
	e.ports.channels = map[string]notifysvc.ChannelDeliveryCounts{
		"email":    {Queued: 1},
		"telegram": {},
	}
	return e, dir
}

func componentByKey(comps []Component, key string) Component {
	for _, c := range comps {
		if c.Key == key {
			return c
		}
	}
	return Component{}
}

func TestSystemStatusAllOK(t *testing.T) {
	e, _ := setupSystemStatusOK(t)
	ctx := context.Background()

	out, err := e.svc.SystemStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"postgres", "valkey", "node_fabric", "payment_callbacks", "mail", "telegram", "sse", "backup"}
	if len(out.Components) != 8 {
		t.Fatalf("components len=%d", len(out.Components))
	}
	for i, k := range wantKeys {
		if out.Components[i].Key != k {
			t.Fatalf("component[%d]=%q want %q", i, out.Components[i].Key, k)
		}
	}
	if componentByKey(out.Components, "sse").State != StateUnknown {
		t.Fatal("sse should be unknown when streams not wired")
	}
	if componentByKey(out.Components, "sse").Message != "实时推送未启用" {
		t.Fatalf("sse message %q", componentByKey(out.Components, "sse").Message)
	}
	for _, k := range []string{"postgres", "valkey", "node_fabric", "payment_callbacks", "mail", "telegram", "backup"} {
		if componentByKey(out.Components, k).State != StateOK {
			t.Fatalf("%s state=%q want ok", k, componentByKey(out.Components, k).State)
		}
	}
	if out.State != StateOK {
		t.Fatalf("State=%q want ok", out.State)
	}
	pg := componentByKey(out.Components, "postgres")
	if pg.Metrics["size_bytes"] != int64(100) {
		t.Fatalf("size_bytes=%v", pg.Metrics["size_bytes"])
	}
	if out.Database == nil {
		t.Fatal("Database should be set")
	}
}

func TestSystemStatusDegrades(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(e *testEnv, dir string)
		checkKey   string
		checkState string
		wantState  string
		post       func(t *testing.T, out SystemStatus)
	}{
		{
			name: "postgres down",
			mutate: func(e *testEnv, _ string) {
				e.store.errFor["Ping"] = errBoom
			},
			checkKey: "postgres", checkState: StateDown, wantState: "degraded",
		},
		{
			name: "postgres warn stats",
			mutate: func(e *testEnv, _ string) {
				e.store.errFor["DatabaseStats"] = errBoom
			},
			checkKey: "postgres", checkState: StateWarn, wantState: "degraded",
			post: func(t *testing.T, out SystemStatus) {
				if out.Database != nil || out.DatabaseError == "" {
					t.Fatalf("Database=%v DatabaseError=%q", out.Database, out.DatabaseError)
				}
			},
		},
		{
			name: "valkey down",
			mutate: func(e *testEnv, _ string) {
				e.redis.err = errBoom
			},
			checkKey: "valkey", checkState: StateDown, wantState: "degraded",
		},
		{
			name: "node_fabric warn",
			mutate: func(e *testEnv, _ string) {
				e.ports.nodeCounts.Online = 1
			},
			checkKey: "node_fabric", checkState: StateWarn, wantState: "degraded",
		},
		{
			name: "node_fabric unknown",
			mutate: func(e *testEnv, _ string) {
				e.ports.errFor["ConfigLaggingCount"] = errBoom
			},
			checkKey: "node_fabric", checkState: StateUnknown, wantState: StateOK,
		},
		{
			name: "payment_callbacks warn",
			mutate: func(e *testEnv, _ string) {
				e.ports.stuck = 2
			},
			checkKey: "payment_callbacks", checkState: StateWarn, wantState: "degraded",
		},
		{
			name: "mail warn",
			mutate: func(e *testEnv, _ string) {
				e.ports.channels["email"] = notifysvc.ChannelDeliveryCounts{Retrying: 1}
			},
			checkKey: "mail", checkState: StateWarn, wantState: "degraded",
		},
		{
			name: "mail telegram unknown",
			mutate: func(e *testEnv, _ string) {
				e.ports.errFor["DeliveryCountsByChannel"] = errBoom
			},
			checkKey: "mail", checkState: StateUnknown, wantState: StateOK,
			post: func(t *testing.T, out SystemStatus) {
				if componentByKey(out.Components, "telegram").State != StateUnknown {
					t.Fatal("telegram should be unknown")
				}
			},
		},
		{
			name: "backup warn no offsite",
			mutate: func(e *testEnv, dir string) {
				_ = os.Remove(filepath.Join(dir, "backup-webdav.json"))
			},
			checkKey: "backup", checkState: StateWarn, wantState: "degraded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, dir := setupSystemStatusOK(t)
			tc.mutate(e, dir)
			out, err := e.svc.SystemStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			c := componentByKey(out.Components, tc.checkKey)
			if c.State != tc.checkState {
				t.Fatalf("%s state=%q want %q", tc.checkKey, c.State, tc.checkState)
			}
			if out.State != tc.wantState {
				t.Fatalf("State=%q want %q", out.State, tc.wantState)
			}
			if tc.post != nil {
				tc.post(t, out)
			}
		})
	}
}

func TestSystemStatusChannelsEmptyOK(t *testing.T) {
	e, _ := setupSystemStatusOK(t)
	// 没有投递记录时，GROUP BY 结果为空 map
	e.ports.channels = map[string]notifysvc.ChannelDeliveryCounts{}

	out, err := e.svc.SystemStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for _, ch := range []string{"mail", "telegram"} {
		c := componentByKey(out.Components, ch)
		if c.State != StateOK {
			t.Fatalf("%s state=%q want %q", ch, c.State, StateOK)
		}
		if c.Metrics["queued"] != int64(0) || c.Metrics["retrying"] != int64(0) || c.Metrics["failed_total"] != int64(0) {
			t.Fatalf("%s metrics mismatch: %+v", ch, c.Metrics)
		}
	}
}

type fakeStreamCounter struct {
	n    int
	down bool
}

func (f fakeStreamCounter) Count() int { return f.n }
func (f fakeStreamCounter) Live() bool { return !f.down }

func TestSystemStatusSSEWithStreams(t *testing.T) {
	e, _ := setupSystemStatusOK(t)
	e.svc.UseStreams(fakeStreamCounter{n: 7})

	out, err := e.svc.SystemStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sse := componentByKey(out.Components, "sse")
	if sse.State != StateOK {
		t.Fatalf("state %q", sse.State)
	}
	if sse.Metrics["connections"] != 7 {
		t.Fatalf("connections %v", sse.Metrics["connections"])
	}
	if sse.Message != "" {
		t.Fatalf("message %q", sse.Message)
	}
}

func TestSystemStatusSSESubscriptionDown(t *testing.T) {
	e, _ := setupSystemStatusOK(t)
	e.svc.UseStreams(fakeStreamCounter{n: 0, down: true})

	out, err := e.svc.SystemStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sse := componentByKey(out.Components, "sse"); sse.State != StateWarn {
		t.Fatalf("state %q", sse.State)
	}
}
