package nodesim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// 回执送不到（5xx）时留给下一轮补报，用户同步照常（pdnd：回执失败不再冻结名单）；
// 收下之后不再重报。
func TestCurrentReportsRetryUntilSettled(t *testing.T) {
	g := newFakeGateway(t, 1, 5, 1, 60)
	g.fail500["POST /v1/nodes/config/report"] = true
	const reportKey = "POST /v1/nodes/config/report"
	opt := testOptions(g.srv.URL)
	opt.Stagger = 0
	runFor(t, g, opt, 1200*time.Millisecond, func() {
		// 起跑那一次 switched 与下一轮的补报都被 500 挡回
		g.waitFor("two failed switched reports", 4*time.Second, func() bool { return g.hits[reportKey] >= 2 })
		g.read(func() { delete(g.fail500, reportKey) })
		g.waitFor("reports settled", 4*time.Second, func() bool { return g.phases["health_passed"] == 1 })
	})
	g.read(func() {
		// 2 次被挡 + switched + health_passed，之后的轮次只换 204、不再上报
		if g.hits[reportKey] != 4 || g.phases["switched"] != 1 || g.phases["health_passed"] != 1 {
			t.Fatalf("report hits %d phases %v", g.hits[reportKey], g.phases)
		}
		effs, users := g.hits["GET /v1/nodes/effective-config"], g.hits["GET /api/v1/server/UniProxy/user"]
		if effs < 4 || users < effs-1 {
			// 补报失败的那几轮与 pdnd 一样照常同步用户（最后一轮可能被收尾截断）
			t.Fatalf("effective-config %d user pulls %d", effs, users)
		}
	})
}

// 面板明确拒收（4xx）的回执同样不再重报。
func TestCurrentRejectedReportIsNotRetried(t *testing.T) {
	g := newFakeGateway(t, 1, 5, 1, 60)
	g.reject409["health_passed"] = true
	opt := testOptions(g.srv.URL)
	opt.Stagger = 0
	runFor(t, g, opt, 3300*time.Millisecond, nil)
	g.read(func() {
		if g.hits["POST /v1/nodes/config/report"] != 2 || g.phases["switched"] != 1 || g.hits["GET /v1/nodes/effective-config"] < 3 {
			t.Fatalf("hits %v phases %v", g.hits, g.phases)
		}
	})
}

// 换钥检查平时不到点不问；面板轮换密钥后新发布验不过，立刻强制问一次换钥再验。
func TestCurrentVerifyFailureForcesKeyRefresh(t *testing.T) {
	g := newFakeGateway(t, 1, 5, 30, 60)
	opt := testOptions(g.srv.URL)
	opt.Stream = true
	opt.Stagger = 0
	nodeID := g.order[0]
	s, _ := runFor(t, g, opt, 600*time.Millisecond, func() {
		g.waitFor("started", 3*time.Second, func() bool { return g.phases["switched"] == 1 && g.nodes[nodeID].streams > 0 })
		g.rotateConfigKey()
		g.bumpGeneration(nodeID)
		g.pushEvent(nodeID, nodefabric.EventSyncConfig, nodefabric.SyncConfigPayload{})
		g.waitFor("re-apply under the new key", 3*time.Second, func() bool { return g.phases["switched"] == 2 })
	})
	newKey, _ := g.keys()
	g.read(func() {
		if g.hits["GET /v1/nodes/config-signing-key"] != 2 || g.phases["failed"] != 0 {
			t.Fatalf("key checks %d phases %v", g.hits["GET /v1/nodes/config-signing-key"], g.phases)
		}
		last := g.beats[len(g.beats)-1]
		if last.ConfigSigningKeyID != newKey.KeyID() {
			t.Fatalf("heartbeat still pins %s", last.ConfigSigningKeyID)
		}
	})
	if s.KeyTransitions != 1 || s.ForcedKeyChecks != 1 || s.VerifyFailures != 0 {
		t.Fatalf("summary %+v", s)
	}
}

// 没带已应用版本却拿到 204 是面板出错，与 pdnd 一样报错，不能当成「没变化」。
func TestCurrent204WithoutAppliedReleaseIsAnError(t *testing.T) {
	g := newFakeGateway(t, 1, 1, 1, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	rec := ltkit.NewRecorder("nodes", time.Second)
	c, err := newSignedClient(srv.URL, g.manifest().Nodes[0], true,
		&observer{rec: rec, fleet: newFleetStats(), maxSamples: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.keyCheckEvery = time.Hour
	if _, _, err := c.configSince(context.Background(), "", ""); err == nil || !strings.Contains(err.Error(), "no effective config") {
		t.Fatalf("want an error, got %v", err)
	}
	cfg, unchanged, err := c.configSince(context.Background(), "0190b4a6-0000-7000-8000-000000000001/3", "")
	if err != nil || !unchanged || cfg != nil {
		t.Fatalf("cfg=%v unchanged=%v err=%v", cfg, unchanged, err)
	}
	// 第二次没到换钥检查的点：只发了一次换钥请求
	if n := endpoint(rec.Snapshot(), "node:GET "+pathConfigKey).Count; n != 1 {
		t.Fatalf("key checks %d", n)
	}
}

func TestReportSettled(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, true},
		{&statusError{code: 409}, true},
		{&statusError{code: 422}, true},
		{&statusError{code: 408}, false},
		{&statusError{code: 429}, false},
		{&statusError{code: 500}, false},
		{&statusError{code: 503}, false},
		{context.DeadlineExceeded, false},
	}
	for _, c := range cases {
		if got := reportSettled(c.err); got != c.want {
			t.Fatalf("reportSettled(%v) = %v", c.err, got)
		}
	}
}

// 抖动与 pdnd panel.Jitter 同口径（±10%），同一种子下每条节拍的序列可复现。
func TestJitterBoundsAndReproducibility(t *testing.T) {
	const d = 15 * time.Second
	a, b, other := newJitterSource(42, 3), newJitterSource(42, 3), newJitterSource(42, 4)
	var seqA, seqOther []time.Duration
	lo, hi := d, d
	for range 2000 {
		x := jitter(a.pull, d)
		if x != jitter(b.pull, d) {
			t.Fatal("same seed and node must give the same pull sequence")
		}
		seqA = append(seqA, x)
		seqOther = append(seqOther, jitter(other.pull, d))
		lo, hi = min(lo, x), max(hi, x)
	}
	if lo < d*9/10 || hi > d*11/10 || lo > d*92/100 || hi < d*108/100 {
		t.Fatalf("jitter range [%v, %v] for %v", lo, hi, d)
	}
	same := 0
	for i := range seqA {
		if seqA[i] == seqOther[i] {
			same++
		}
	}
	if same > 20 {
		t.Fatalf("different nodes share %d of 2000 jitter draws", same)
	}
	if jitter(a.pull, 0) != 0 || jitter(a.pull, 3) != 3 {
		t.Fatal("non-positive or tiny intervals pass through unchanged")
	}
}

// current 的节拍每拍都重排、带抖动；legacy 是固定周期的 ticker。
func TestBeatModes(t *testing.T) {
	src := newJitterSource(1, 0)
	cur := newBeat(time.Hour, false, src.pull)
	defer cur.stop()
	old := newBeat(time.Hour, true, src.pull)
	defer old.stop()
	if cur.timer == nil || cur.ticker != nil || old.ticker == nil || old.timer != nil {
		t.Fatal("current must use a timer and legacy a ticker")
	}
	fast := newBeat(20*time.Millisecond, false, src.push)
	defer fast.stop()
	for range 3 {
		select {
		case <-fast.C():
			fast.fired(20 * time.Millisecond)
		case <-time.After(time.Second):
			t.Fatal("current beat did not re-arm after firing")
		}
	}
}

func TestRunRejectsUnknownBehavior(t *testing.T) {
	g := newFakeGateway(t, 1, 1, 1, 1)
	opt := testOptions(g.srv.URL)
	opt.Behavior = "pdnd-2024"
	if _, err := Run(context.Background(), g.manifest(), opt, ltkit.NewRecorder("nodes", time.Second)); err == nil ||
		!strings.Contains(err.Error(), "-node-behavior") {
		t.Fatalf("want a -node-behavior error, got %v", err)
	}
}

// nodes 报告按节点数折出每节点每分钟各端点请求数，生效配置的 200 与 204 分开。
func TestReportsPerNodePerMinute(t *testing.T) {
	g := newFakeGateway(t, 2, 5, 1, 1)
	_, rep := runFor(t, g, testOptions(g.srv.URL), 2600*time.Millisecond, nil)
	if rep.PerUnit == nil || rep.PerUnit.Unit != "node" || rep.PerUnit.Units != 2 || rep.PerUnit.Window != "run" {
		t.Fatalf("per unit %+v", rep.PerUnit)
	}
	eff := endpoint(rep, "node:GET /v1/nodes/effective-config")
	if eff.PerUnitPerMinByCode["204"] <= 0 || eff.PerUnitPerMinByCode["200"] <= 0 || rep.Totals.PerUnitPerMin <= eff.PerUnitPerMin {
		t.Fatalf("effective-config %v %v total %v", eff.PerUnitPerMin, eff.PerUnitPerMinByCode, rep.Totals.PerUnitPerMin)
	}
	if rep.Meta["node_behavior"] != BehaviorCurrent {
		t.Fatalf("meta %v", rep.Meta)
	}
}
