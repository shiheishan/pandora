package nodesim

import (
	"context"
	"encoding/base64"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// testOptions 是秒级以内的节拍：兜底节拍放到一小时，只有面板下发的 base_config 能让它们动。
func testOptions(url string) Options {
	opt := DefaultOptions()
	opt.NodeURL = url
	opt.Duration = 0
	opt.Stagger = 200 * time.Millisecond
	opt.Stream = false
	opt.OnlineRatio = 1
	opt.TrafficMiB = 1
	opt.Progress = 0
	opt.Seed = 42
	opt.PullInterval, opt.PushInterval = time.Hour, time.Hour
	opt.StatusInterval = 400 * time.Millisecond
	opt.HealthWindow = 100 * time.Millisecond
	return opt
}

// runFor 跑 d 之后取消，返回整机状态与报告；during 在跑的同时执行（可空）。
func runFor(t *testing.T, g *fakeGateway, opt Options, d time.Duration, during func()) (fleetSummary, ltkit.Report) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rec := ltkit.NewRecorder("nodes", time.Second)
	type result struct {
		s   fleetSummary
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := Run(ctx, g.manifest(), opt, rec)
		done <- result{s, err}
	}()
	if during != nil {
		during()
	}
	time.Sleep(d)
	cancel()
	res := <-done
	if res.err != nil {
		t.Fatal(res.err)
	}
	return res.s, rec.Snapshot()
}

func endpoint(rep ltkit.Report, name string) ltkit.EndpointStats {
	for _, e := range rep.Endpoints {
		if e.Endpoint == name {
			return e
		}
	}
	return ltkit.EndpointStats{}
}

func TestSignedChannelVerifiedEndToEnd(t *testing.T) {
	for _, behavior := range []string{BehaviorCurrent, BehaviorLegacy} {
		t.Run(behavior, func(t *testing.T) { testSignedChannelVerifiedEndToEnd(t, behavior) })
	}
}

func testSignedChannelVerifiedEndToEnd(t *testing.T, behavior string) {
	g := newFakeGateway(t, 3, 20, 1, 1)
	opt := testOptions(g.srv.URL)
	opt.Behavior = behavior
	s, rep := runFor(t, g, opt, 2600*time.Millisecond, nil)
	eff := endpoint(rep, "node:GET /v1/nodes/effective-config")

	g.read(func() {
		if g.sigFail != 0 || g.authFail != 0 || g.sigOK == 0 {
			t.Fatalf("sigOK=%d sigFail=%d authFail=%d", g.sigOK, g.sigFail, g.authFail)
		}
		// 每个节点先 switched，稳定窗口过后的下一轮再 health_passed
		if g.phases["switched"] < 3 || g.phases["health_passed"] < 3 || g.phases["failed"] != 0 {
			t.Fatalf("phases %v", g.phases)
		}
		if g.pushes == 0 || g.alives == 0 {
			t.Fatalf("pushes=%d alives=%d", g.pushes, g.alives)
		}
		applied := 0
		for _, b := range g.beats {
			if b.ConfigSigningKeyID != g.signer.KeyID() || b.Metrics == nil || b.AgentVersion != pdndAgentVersion {
				t.Fatalf("heartbeat %+v", b)
			}
			if b.AppliedReleaseID != "" && b.AppliedGeneration == 1 && b.AppliedContentSHA256 != "" {
				applied++
			}
		}
		if applied < 3 {
			t.Fatalf("only %d heartbeats carried the applied release", applied)
		}
		keys, effs := g.hits["GET /v1/nodes/config-signing-key"], g.hits["GET /v1/nodes/effective-config"]
		if behavior == BehaviorLegacy {
			// 老 pdnd 每次拉配置之前先问一遍签名密钥，拉的总是全量，回执每轮重报
			if keys != effs || eff.Codes["204"] != 0 || g.phases["switched"] < effs-3 {
				t.Fatalf("legacy: key refresh %d, effective-config %d (%v), phases %v", keys, effs, eff.Codes, g.phases)
			}
			for _, h := range g.appliedHdr {
				if h != "" {
					t.Fatalf("legacy sent %s: %q", nodefabric.AppliedEffectiveReleaseHeader, h)
				}
			}
			return
		}
		// 新 pdnd：换钥十分钟一查（这里只有起跑那一次），回执收下即停，
		// 之后每轮带已应用版本、换回 204
		if keys != 3 || g.phases["switched"] != 3 || g.phases["health_passed"] != 3 {
			t.Fatalf("current: key refresh %d, phases %v", keys, g.phases)
		}
		if eff.Codes["200"] != 3 || eff.Codes["204"] == 0 || eff.Flags[flagUnchanged] != eff.Codes["204"] || effs < 3*2 {
			t.Fatalf("current: effective-config hits %d codes %v flags %v", effs, eff.Codes, eff.Flags)
		}
		sent := 0
		for i, h := range g.appliedHdr {
			if h == "" {
				continue
			}
			sent++
			id, gen, ok := nodefabric.ParseAppliedEffectiveRelease(h)
			if !ok || gen != 1 || g.effCodes[i] != 204 || uuid.MustParse(id).String() != id {
				t.Fatalf("applied header %q answered %d", h, g.effCodes[i])
			}
		}
		if sent != effs-3 {
			t.Fatalf("applied header sent %d times over %d fetches", sent, effs)
		}
	})
	if s.Started != 3 || s.VerifyFailures != 0 {
		t.Fatalf("summary %+v", s)
	}
	if p := strictProblems(rep, s); len(p) != 0 {
		t.Fatalf("strict problems %v", p)
	}
	for _, name := range []string{"node:GET /v1/nodes/effective-config", "node:POST /v1/nodes/heartbeat",
		"node:POST /v1/nodes/config/report", "node:GET /api/v1/server/UniProxy/user", "node:POST /api/v1/server/UniProxy/push"} {
		if endpoint(rep, name).Count == 0 {
			t.Fatalf("no observations for %s", name)
		}
	}
}

func TestConfigVerificationRejectsForeignKey(t *testing.T) {
	g := newFakeGateway(t, 1, 5, 1, 1)
	m := g.manifest()
	other := make([]byte, 32)
	other[0] = 1
	m.Nodes[0].ConfigPublicKey = base64.StdEncoding.EncodeToString(other) // 钥 ID 不变、公钥被换
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	s, err := Run(ctx, m, testOptions(g.srv.URL), ltkit.NewRecorder("nodes", time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.VerifyFailures == 0 || s.Started != 0 {
		t.Fatalf("summary %+v", s)
	}
	g.read(func() {
		if g.phases["failed"] == 0 || g.phases["switched"] != 0 {
			t.Fatalf("phases %v", g.phases)
		}
	})
}

// 清单里没有私钥的节点照 pdnd 退到兼容通道：/config 带 ETag 换 304，状态走 /status。
func TestCompatChannelWithoutIdentity(t *testing.T) {
	g := newFakeGateway(t, 1, 5, 1, 1)
	m := g.manifest()
	m.Nodes[0].PrivateKey = ""
	ctx, cancel := context.WithTimeout(context.Background(), 2300*time.Millisecond)
	defer cancel()
	s, err := Run(ctx, m, testOptions(g.srv.URL), ltkit.NewRecorder("nodes", time.Second))
	if err != nil {
		t.Fatal(err)
	}
	g.read(func() {
		if g.sigOK+g.sigFail != 0 || g.hits["POST /api/v1/server/UniProxy/status"] == 0 || g.pushes == 0 {
			t.Fatalf("hits %v", g.hits)
		}
		if len(g.configINM) < 2 || g.configINM[0] != "" || g.configINM[1] == "" {
			t.Fatalf("config If-None-Match sequence %q", g.configINM)
		}
	})
	if s.Started != 1 || s.ConfigApplied != 1 {
		t.Fatalf("summary %+v", s)
	}
}

func TestUsersETagRoundTrip(t *testing.T) {
	g := newFakeGateway(t, 1, 10, 1, 60)
	g.badUsers = 1 // 第一份用户列表解析失败，带回一个不该被记住的 ETag
	_, rep := runFor(t, g, testOptions(g.srv.URL), 2800*time.Millisecond, nil)

	g.read(func() {
		if len(g.userINM) < 3 {
			t.Fatalf("user pulls %v", g.userINM)
		}
		if g.userINM[0] != "" || g.userINM[1] != "" {
			t.Fatalf("ETag of an unparsed list was reused: %q", g.userINM)
		}
		if g.userCodes[1] != 200 || g.userINM[2] == "" || g.userCodes[2] != 304 {
			t.Fatalf("inm=%q codes=%v", g.userINM, g.userCodes)
		}
	})
	if endpoint(rep, "node:GET /api/v1/server/UniProxy/user").Flags[flagETag304] == 0 {
		t.Fatal("etag_304 was not flagged")
	}
}

func TestStreamEventsDriveRepullAnd304(t *testing.T) {
	for _, behavior := range []string{BehaviorCurrent, BehaviorLegacy} {
		t.Run(behavior, func(t *testing.T) { testStreamEventsDriveRepullAnd304(t, behavior) })
	}
}

func testStreamEventsDriveRepullAnd304(t *testing.T, behavior string) {
	g := newFakeGateway(t, 1, 10, 30, 60) // 轮询拉长到 30 秒：期间的请求只能由事件触发
	opt := testOptions(g.srv.URL)
	opt.Behavior = behavior
	opt.Stream = true
	opt.Stagger = 0
	nodeID := g.order[0]
	var v1 string
	s, rep := runFor(t, g, opt, 400*time.Millisecond, func() {
		g.waitFor("stream", 3*time.Second, func() bool { return g.nodes[nodeID].streams > 0 && len(g.userINM) == 1 })

		// 1. 用户变了，流推全量新版本；节点不回头拉
		users := append(g.usersCopy(), nodefabric.ProxyUser{ID: 9001, UUID: uuid.NewString()})
		g.setUsers(users)
		v1 = g.pushUsersEvent(nodeID)
		time.Sleep(150 * time.Millisecond)

		// 2. 基准对不上的增量 → 改拉全量，带着流推下来的版本换 304
		g.pushEvent(nodeID, nodefabric.EventSyncUserDelta, nodefabric.SyncUserDeltaPayload{
			FromVersion: `"u1-stale"`, ToVersion: `"u1-next"`})
		g.waitFor("delta re-pull", 2*time.Second, func() bool { return len(g.userINM) == 2 })

		// 3. 配置事件只当信号：回头走一遍签名拉取（current 照 pdnd 走 syncOnce，
		//    装上新版后用户 ETag 已作废，紧接着全量拉一次用户）
		g.bumpGeneration(nodeID)
		before := 0
		g.read(func() { before = g.hits["GET /v1/nodes/effective-config"] })
		g.pushEvent(nodeID, nodefabric.EventSyncConfig, nodefabric.SyncConfigPayload{ETag: `"c2"`})
		g.waitFor("config re-fetch", 2*time.Second, func() bool {
			return g.hits["GET /v1/nodes/effective-config"] == before+1 && g.phases["switched"] == 2
		})

		// 4. legacy：基准对得上的增量就地打上，不发请求。current：装新版时用户版本已
		//    作废（pdnd resetUserMirror），同一条增量对不上基准，改拉全量换 304
		g.pushEvent(nodeID, nodefabric.EventSyncUserDelta, nodefabric.SyncUserDeltaPayload{
			Delta:       nodefabric.UserDelta{Added: []nodefabric.ProxyUser{{ID: 9002, UUID: uuid.NewString()}}},
			FromVersion: v1, ToVersion: `"u1-after"`})
	})

	wantINM, wantCodes, wantMismatches := []string{"", v1}, []int{200, 304}, int64(1)
	if behavior == BehaviorCurrent {
		wantINM, wantCodes, wantMismatches = []string{"", v1, "", v1}, []int{200, 304, 200, 304}, 2
	}
	g.read(func() {
		if !slices.Equal(g.userINM, wantINM) || !slices.Equal(g.userCodes, wantCodes) {
			t.Fatalf("user pulls sent %q got %v, want %q / %v", g.userINM, g.userCodes, wantINM, wantCodes)
		}
	})
	if s.DeltaMismatches != wantMismatches || s.StreamEvents[nodefabric.EventSyncUsers] < 2 || s.StreamEvents[nodefabric.EventSyncConfig] != 1 ||
		s.StreamsPeak != 1 {
		t.Fatalf("summary %+v", s)
	}
	if endpoint(rep, "node:GET /v1/nodes/effective-config").Flags[flagStream] != 1 {
		t.Fatalf("stream-triggered config fetch not flagged: %+v", endpoint(rep, "node:GET /v1/nodes/effective-config").Flags)
	}
	if endpoint(rep, "node:GET /api/v1/server/UniProxy/stream").Codes["200"] == 0 {
		t.Fatal("stream connect not observed")
	}
}

func newTestRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, 0)) }

func (g *fakeGateway) usersCopy() []nodefabric.ProxyUser {
	return append([]nodefabric.ProxyUser(nil), g.users...)
}

func TestTickersFollowBaseConfig(t *testing.T) {
	g := newFakeGateway(t, 2, 10, 1, 1)
	opt := testOptions(g.srv.URL)
	opt.Stagger = 0
	runFor(t, g, opt, 3300*time.Millisecond, nil)
	g.read(func() {
		// 兜底节拍是一小时：多出来的拉取与上报只能来自 base_config 的 1 秒
		if got := g.hits["GET /v1/nodes/effective-config"]; got < 2*3 {
			t.Fatalf("effective-config fetched %d times", got)
		}
		if g.pushes < 2*2 {
			t.Fatalf("pushes %d", g.pushes)
		}
	})
}

func TestStaggerOffsetsAreSpreadWithSubSecondJitter(t *testing.T) {
	offsets := staggerOffsets(200, 10*time.Second, newTestRand(7))
	seen := map[time.Duration]bool{}
	subSecond := 0
	for _, o := range offsets {
		if o < 0 || o >= 10*time.Second {
			t.Fatalf("offset %v outside window", o)
		}
		seen[o] = true
		if o%time.Second != 0 {
			subSecond++
		}
	}
	if len(seen) < 199 || subSecond < 199 {
		t.Fatalf("distinct=%d subSecond=%d", len(seen), subSecond)
	}
	if got := staggerOffsets(3, 0, newTestRand(1)); got[0] != 0 || got[2] != 0 {
		t.Fatalf("zero window should start all at once: %v", got)
	}
	if _, err := canonicalServer("http://panel.example.test"); err == nil {
		t.Fatal("plain http to a non-loopback host must be refused, as pdnd does")
	}
}

func TestMainStrictExit(t *testing.T) {
	g := newFakeGateway(t, 2, 5, 1, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := g.manifest().Save(path); err != nil {
		t.Fatal(err)
	}
	args := func(out string, strict bool) []string {
		a := []string{"-manifest", path, "-node-url", g.srv.URL, "-out", out, "-duration", "1500ms",
			"-stagger", "100ms", "-stream=false", "-status-interval", "300ms", "-progress", "500ms", "-seed", "3"}
		if strict {
			a = append(a, "-strict")
		}
		return a
	}

	if err := Main(args(filepath.Join(dir, "ok"), true)); err != nil {
		t.Fatalf("clean run failed strict: %v", err)
	}
	for _, f := range []string{"nodes.json", "nodes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, "ok", f)); err != nil {
			t.Fatal(err)
		}
	}

	g.read(func() { g.fail500["POST /api/v1/server/UniProxy/push"] = true })
	if err := Main(args(filepath.Join(dir, "lenient"), false)); err != nil {
		t.Fatalf("without -strict a 5xx must not fail the run: %v", err)
	}
	err := Main(args(filepath.Join(dir, "5xx"), true))
	if err == nil || !strings.Contains(err.Error(), "5xx") {
		t.Fatalf("want strict 5xx failure, got %v", err)
	}
	g.read(func() { g.fail500 = map[string]bool{} })

	bad := g.manifest()
	bad.Nodes[1].PrivateKey = g.manifest().Nodes[0].PrivateKey // 拿别人的私钥签名
	if err := bad.Save(path); err != nil {
		t.Fatal(err)
	}
	err = Main(args(filepath.Join(dir, "sig"), true))
	if err == nil || !strings.Contains(err.Error(), "signed requests were rejected") {
		t.Fatalf("want strict signature failure, got %v", err)
	}
}
