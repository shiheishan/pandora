package nodefabric

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// healthyWatch 给服务装一个已经健康的监听（不连库），返回它。
func healthyWatch(t *testing.T, svc *Service) *epochWatch {
	t.Helper()
	w := newEpochWatch(nil)
	w.connect()
	w.observe(w.nextProbe())
	svc.caches.watch = w
	if !svc.watchStamp().ok() {
		t.Fatal("watch not healthy")
	}
	return w
}

// 监听健康、缓存身份的戳覆盖请求时的戳：验签不读纪元、不回库（Service 没有连接池，
// 回库就会 panic）；之后来了一条 'd' 通知，同一份缓存身份不再算新鲜。
func TestVerifySignatureTrustsWatchedIdentityUntilDeliveryNotification(t *testing.T) {
	svc := NewService(nil, nil)
	svc.EnableNodeCaches()
	w := healthyWatch(t, svc)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{NodeID: "n1", PublicKey: pub, epoch: 3, watch: svc.watchStamp(), expiresAt: time.Now().Add(time.Hour)}
	_, _ = svc.caches.identity.get(context.Background(), identityCacheKey("t1", "n1"), "", always[Identity], nil,
		func(context.Context) (Identity, error) { return id, nil })
	payload := []byte("payload")
	check, err := svc.VerifyNodeRequestSignature(context.Background(), "t1", "n1", payload, ed25519.Sign(priv, payload))
	if err != nil {
		t.Fatal(err)
	}
	if check.NeedsEpoch() || !check.fresh {
		t.Fatalf("watched fresh identity still asks for an epoch read: %+v", check)
	}
	// 新鲜的身份复核是空操作（不比纪元）
	if err := svc.ConfirmNodeIdentity(context.Background(), "t1", "n1", check, 99, payload, nil); err != nil {
		t.Fatalf("fresh identity re-checked: %v", err)
	}
	w.observe("d")
	if id.watch.deliveryCovers(svc.watchStamp()) {
		t.Fatal("identity loaded before a delivery notification still counts as fresh")
	}
	w.observe("c")
	// 'c' 不影响身份，但上一条 'd' 已经让它过期：取身份必须回库（这里没有库，会 panic）
	defer func() {
		if recover() == nil {
			t.Fatal("stale identity was served without a database read")
		}
	}()
	_, _ = svc.VerifyNodeRequestSignature(context.Background(), "t1", "n1", payload, ed25519.Sign(priv, payload))
}

// 监听健康时，loose 模式的名单过了 TTL 也不重算；strict 模式照旧按 TTL；来了 'd' 就重算。
func TestWatchedUserSetSkipsTTLOnlyInLooseMode(t *testing.T) {
	clock := newFakeClock()
	svc := NewService(nil, nil)
	svc.caches = newNodeCaches(clock.Now)
	w := newEpochWatch(clock.Now)
	w.connect()
	w.observe(w.nextProbe())
	svc.caches.watch = w
	pool := "pool-1"
	users := []ProxyUser{{ID: 1, UUID: "u-1"}}
	seed := func(strict bool) {
		set := nodeUserSet{users: users, version: UserSetVersion(users), epoch: 4, watch: svc.watchStamp(), strict: strict}
		svc.caches.users.drop(usersCacheKey("t1", pool))
		_, _ = svc.caches.users.get(context.Background(), usersCacheKey("t1", pool), "", always[nodeUserSet], nil,
			func(context.Context) (nodeUserSet, error) { return set, nil })
	}
	node := func() *ServingNode {
		return &ServingNode{ID: "n1", PoolID: &pool, deliveryEpoch: 4, epochKnown: true, watch: svc.watchStamp()}
	}
	seed(false)
	clock.Advance(nodeUsersCacheTTL + nodeUsersStaleGrace + time.Second)
	w.observe(w.nextProbe()) // 探针回声跟上时钟
	if got, _, err := svc.NodeUserSet(context.Background(), "t1", node()); err != nil || len(got) != 1 {
		t.Fatalf("watched loose set was not served past its TTL: %v %v", got, err)
	}
	mustReload := func(why string) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s: set served without a reload", why)
			}
		}()
		_, _, _ = svc.NodeUserSet(context.Background(), "t1", node())
	}
	seed(true)
	clock.Advance(nodeUsersCacheTTL + nodeUsersStaleGrace + time.Second)
	w.observe(w.nextProbe())
	mustReload("strict set past TTL")
	seed(false)
	w.observe("d")
	mustReload("set loaded before a delivery notification")
}

func sampleView(t *testing.T) *nodeConfigView {
	t.Helper()
	rid, gen := "0190a5e2-8f00-7000-8000-000000000001", int64(7)
	key := "k1"
	pool := "pool-1"
	return &nodeConfigView{
		watch:     watchStamp{session: 1},
		serving:   ServingNode{ID: "n1", NodeType: "vless", Status: "active", ServingStatus: "active", PoolID: &pool, epochKnown: true},
		tokenHash: crypto.HashToken("tok"), uniOK: true,
		effOK: true, cfgGeneration: gen, desiredReleaseID: &rid, desiredGeneration: &gen, releaseID: &rid, keyID: &key,
		status: "active", desiredConfigVersion: 3,
	}
}

// 视图上的 204 判定与 EffectiveConfigUnchangedAt 的 EXISTS 逐条对应。
func TestNodeConfigViewEffectiveUnchangedMatchesExistsConditions(t *testing.T) {
	v := sampleView(t)
	rid := *v.releaseID
	if !v.effectiveUnchanged(rid, 7, "k1") {
		t.Fatal("current release reported changed")
	}
	other := "0190a5e2-8f00-7000-8000-000000000002"
	for name, mutate := range map[string]func(*nodeConfigView){
		"not deliverable":       func(v *nodeConfigView) { v.effOK = false },
		"new source generation": func(v *nodeConfigView) { v.cfgGeneration = 8 },
		"desired elsewhere":     func(v *nodeConfigView) { v.desiredReleaseID = &other },
		"desired unset":         func(v *nodeConfigView) { v.desiredGeneration = nil },
		"no release row":        func(v *nodeConfigView) { v.releaseID = nil },
		"signed by old key":     func(v *nodeConfigView) { k := "k0"; v.keyID = &k },
	} {
		c := *sampleView(t)
		mutate(&c)
		if c.effectiveUnchanged(rid, 7, "k1") {
			t.Errorf("%s: still reported unchanged", name)
		}
	}
	if v.effectiveUnchanged(rid, 6, "k1") || v.effectiveUnchanged(other, 7, "k1") {
		t.Fatal("a different applied release reported unchanged")
	}
}

// 视图只在令牌对、门槛过、控制节点生命周期允许时给出节点；其余交回查库路径。
func TestNodeConfigViewServingNodeDefersFailuresToDatabase(t *testing.T) {
	want := watchStamp{session: 1, delivery: 2}
	v := sampleView(t)
	n, ok := v.servingNode("tok", "VLESS", want)
	if !ok || n.ID != "n1" || n.watch != want || !n.epochKnown || n.DeclaredType != "" {
		t.Fatalf("valid token rejected or node malformed: %+v ok=%v", n, ok)
	}
	n.Outbounds = append(n.Outbounds, NodeOutbound{})
	if len(v.serving.Outbounds) != 0 {
		t.Fatal("servingNode handed out the shared view instead of a copy")
	}
	if n, _ := v.servingNode("tok", "trojan", want); n.DeclaredType != "trojan" {
		t.Fatalf("protocol drift not reported: %+v", n)
	}
	for name, c := range map[string]struct {
		mutate func(*nodeConfigView)
		token  string
	}{
		"wrong token":    {func(*nodeConfigView) {}, "nope"},
		"empty token":    {func(*nodeConfigView) {}, ""},
		"gate failed":    {func(v *nodeConfigView) { v.uniOK = false }, "tok"},
		"no token set":   {func(v *nodeConfigView) { v.tokenHash = nil }, "tok"},
		"control parked": {func(v *nodeConfigView) { v.isControl = true; v.serving.Status = "standby" }, "tok"},
	} {
		view := *sampleView(t)
		c.mutate(&view)
		if _, ok := view.servingNode(c.token, "", want); ok {
			t.Errorf("%s: view answered instead of deferring to the database", name)
		}
	}
	out := v.heartbeatOutput()
	if out.NodeStatus != "active" || out.DesiredConfigVersion != 3 || out.DesiredReleaseID != *v.releaseID ||
		out.DesiredGeneration != 7 || out.IntervalSeconds != 30 {
		t.Fatalf("heartbeat reply from the view: %+v", out)
	}
}

// 共享正文与 httpx.OK 逐字节相同；gzip 解开也一样；同一版本只编码一次。
func TestUserSetBodyMatchesHTTPXAndGzipRoundTrips(t *testing.T) {
	users := []ProxyUser{{ID: 1, UUID: "u-1", SpeedLimit: 10, DeviceLimit: 2}, {ID: 2, UUID: "u-2"}}
	rec := httptest.NewRecorder()
	httpx.OK(rec, UserSetWire{Users: users})
	r := UserSetResponse{Version: "v", users: users, body: &userSetBody{}}
	p, err := r.Prepared()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.BodyBytes(), rec.Body.Bytes()) || p.ContentType() != rec.Header().Get("Content-Type") {
		t.Fatalf("prepared body differs from httpx.OK:\n%s\n%s", p.BodyBytes(), rec.Body.Bytes())
	}
	var decoded struct {
		Users []ProxyUser `json:"users"`
	}
	if err := json.Unmarshal(p.BodyBytes(), &decoded); err != nil || len(decoded.Users) != 2 {
		t.Fatalf("body is not the UniProxy shape: %v %+v", err, decoded)
	}
	gz, err := r.Gzipped()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(plain, rec.Body.Bytes()) {
		t.Fatalf("gzip body does not decode to the JSON body: %v", err)
	}
	again, _ := r.Gzipped()
	if &again[0] != &gz[0] {
		t.Fatal("gzip body re-encoded for the same version")
	}
}
