package service

// 订阅线路微缓存（delivery_cache.go）与行转线路（node_source.go 的 deliverableFromRows）的单元测试，不连库。
// 缓存层用计数的 load；服务层用只实现 PlanPools 的假 catalog（套餐一律不绑池，走不到 repo）与假的 PlanVersions。
// 并发用例要用 -race 跑才有意义：
//
//	GOTOOLCHAIN=go1.26.9 go test -race ./internal/module/node/service/ -run 'DeliveryCache|DeliverableNodesCache|DeliverableFromRows|DeliveryKey'

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// countingCatalog 是只实现 PlanPools 的假 Catalog：数调用次数，可注入错误，套餐一律不绑池。
type countingCatalog struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (c *countingCatalog) PlanPools(_ context.Context, _ []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return map[uuid.UUID][]uuid.UUID{}, nil
}

func (c *countingCatalog) PoolUsage(context.Context, []uuid.UUID) (map[uuid.UUID]catalogsvc.PoolUsage, error) {
	return map[uuid.UUID]catalogsvc.PoolUsage{}, nil
}

func (c *countingCatalog) PlansByIDs(context.Context, []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error) {
	return map[uuid.UUID]catalogsvc.Plan{}, nil
}

func (c *countingCatalog) ReleaseArchivedPoolBindings(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return []uuid.UUID{}, nil
}

func (c *countingCatalog) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *countingCatalog) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

type fakeCacheVersions struct {
	planID uuid.UUID
}

func (f *fakeCacheVersions) VersionsByIDs(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Version, error) {
	out := make(map[uuid.UUID]catalogsvc.Version, len(ids))
	for _, id := range ids {
		out[id] = catalogsvc.Version{ID: id, PlanID: f.planID}
	}
	return out, nil
}

// newCacheTestService 构造装了假 catalog 与假 versions 的 Service：repo 为 nil，套餐不绑池时不会读它。
func newCacheTestService(cat Catalog) *Service {
	s := New(nil, nil, nil, nil)
	s.UseCatalog(cat)
	s.UseDelivery(nil, nil, &fakeCacheVersions{planID: uuid.New()})
	return s
}

// cacheFixtureRows 是一组覆盖各协议的候选节点（存储形状的协议配置，虚构值），配置里有嵌套的对象与数组：
// 翻成内核字段名后，这些嵌套值在缓存里被所有命中者共享，并发渲染只能读不能改。前两条同名，渲染时要去重名。
func cacheFixtureRows() []repo.DeliveryNodeRow {
	row := func(name, nodeType string, port int, cfg string) repo.DeliveryNodeRow {
		return repo.DeliveryNodeRow{
			ID: uuid.New(), Name: name, NodeType: nodeType, Host: "node.example.test", Port: port,
			ProtocolConfig: cfg, TrafficRate: decimal.NewFromInt(1), HeartbeatFresh: true,
		}
	}
	return []repo.DeliveryNodeRow{
		row("香港", "vless", 20001, `{"tls":2,"flow":"xtls-rprx-vision","network":"tcp","utls":"chrome","reality_settings":{"public_key":"pk-fixture-1","short_id":["0a1b","2c3d"],"server_name":["a.example.test","b.example.test"],"dest":"a.example.test:443"}}`),
		row("香港", "vmess", 20002, `{"network":"ws","network_settings":{"path":"/ws","headers":{"Host":"cdn.example.test"}}}`),
		row("东京", "trojan", 20003, `{"tls":2,"network":"grpc","network_settings":{"serviceName":"svc"},"reality_settings":{"public_key":"pk-fixture-2","short_id":"0e0f","server_name":"c.example.test"}}`),
		row("新加坡", "shadowsocks", 20004, `{"cipher":"2022-blake3-aes-128-gcm"}`),
		row("首尔", "hysteria2", 20005, `{"obfs":{"type":"salamander","password":"obfs-fixture"},"bandwidth":{"up":100,"down":200},"tls_settings":{"server_name":"hy.example.test"}}`),
		row("台北", "tuic", 20006, `{"congestion_control":"bbr","tls_settings":{"server_name":"tuic.example.test"}}`),
		row("伦敦", "anytls", 20007, `{"cert_path":"/etc/pdnd/anytls.pem","utls":"firefox","tls_settings":{"server_name":"any.example.test"}}`),
		row("巴黎", "naive", 20008, `{"masquerade":{"type":"proxy","url":"https://www.example.test","rewrite_host":true},"tls_settings":{"server_name":"naive.example.test"}}`),
		row("柏林", "shadowtls", 20009, `{"server":"hs.example.test","server_port":443,"password":"stls-fixture","cipher":"aes-128-gcm"}`),
		row("纽约", "mieru", 20010, `{"transport":"TCP"}`),
		row("洛杉矶", "socks", 20011, `{"network":"udp"}`),
		row("悉尼", "http", 20012, ``),
	}
}

// groupFixtureRows 是两条候选线路：一条在不限组的池，一条在只限 group 的池。
func groupFixtureRows(group uuid.UUID) []repo.DeliveryNodeRow {
	return []repo.DeliveryNodeRow{
		{ID: uuid.New(), Name: "不限组", NodeType: "vless", Host: "open.example.test", Port: 443,
			ProtocolConfig: `{"network":"tcp"}`, TrafficRate: decimal.NewFromInt(1), HeartbeatFresh: true},
		{ID: uuid.New(), Name: "限 A 组", NodeType: "vless", Host: "limited.example.test", Port: 443,
			ProtocolConfig: `{"network":"tcp"}`, TrafficRate: decimal.NewFromInt(1), HeartbeatFresh: true,
			PoolGroupIDs: []uuid.UUID{group}},
	}
}

func TestDeliveryKey(t *testing.T) {
	v, g, other := uuid.New(), uuid.New(), uuid.New()
	gCopy := g
	if deliveryKey(v, &g) != deliveryKey(v, &gCopy) {
		t.Fatal("同一个组（不同指针）应是同一个键")
	}
	keys := map[string]bool{
		deliveryKey(v, nil):         true,
		deliveryKey(v, &g):          true,
		deliveryKey(v, &other):      true,
		deliveryKey(uuid.New(), &g): true,
	}
	if len(keys) != 4 {
		t.Fatalf("默认组、不同组、不同版本应是不同的键: %v", keys)
	}
}

// TestDeliverableNodesCacheKey：同一个（套餐版本, 用户组）在 TTL 内只现算一次，订阅 id、用户 id 不参与键；
// 换版本、换组、默认组各算各的。
func TestDeliverableNodesCacheKey(t *testing.T) {
	ctx := context.Background()
	cat := &countingCatalog{}
	s := newCacheTestService(cat)
	v, gA, gB := uuid.New(), uuid.New(), uuid.New()

	query := func(version uuid.UUID, group *uuid.UUID) {
		t.Helper()
		nodes, err := s.DeliverableNodes(ctx, subsvc.NodeQuery{
			SubscriptionID: uuid.New(), UserID: uuid.New(), PlanVersionID: version, UserGroupID: group,
		})
		if err != nil {
			t.Fatal(err)
		}
		if nodes == nil || len(nodes) != 0 {
			t.Fatalf("版本没绑池应是非 nil 空切片: %#v", nodes)
		}
	}
	expect := func(label string, want int) {
		t.Helper()
		if got := cat.count(); got != want {
			t.Fatalf("%s: 读版本绑池 %d 次，期望 %d", label, got, want)
		}
	}

	query(v, &gA)
	expect("第一次", 1)
	gACopy := gA
	query(v, &gACopy)
	expect("同版本同组（换了订阅、用户、指针）", 1)
	query(v, &gB)
	expect("换组", 2)
	query(v, nil)
	expect("默认组", 3)
	query(v, nil)
	query(v, &gB)
	query(v, &gA)
	expect("三个键都已缓存", 3)
	query(uuid.New(), &gA)
	expect("换版本", 4)
}

// TestDeliverableNodesCacheSkipsErrors：现算出错不缓存，下一次重算；成功之后才缓存。
func TestDeliverableNodesCacheSkipsErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("catalog 不可用")
	cat := &countingCatalog{err: boom}
	s := newCacheTestService(cat)
	q := subsvc.NodeQuery{PlanVersionID: uuid.New()}

	if _, err := s.DeliverableNodes(ctx, q); !errors.Is(err, boom) {
		t.Fatalf("第一次应把错误交回: %v", err)
	}
	if _, err := s.DeliverableNodes(ctx, q); !errors.Is(err, boom) || cat.count() != 2 {
		t.Fatalf("出错不缓存，第二次应重算: err=%v calls=%d", err, cat.count())
	}
	cat.setErr(nil)
	if _, err := s.DeliverableNodes(ctx, q); err != nil || cat.count() != 3 {
		t.Fatalf("恢复后重算: err=%v calls=%d", err, cat.count())
	}
	if _, err := s.DeliverableNodes(ctx, q); err != nil || cat.count() != 3 {
		t.Fatalf("成功后应缓存: err=%v calls=%d", err, cat.count())
	}
}

// TestDeliverableNodesCachePurgedOnEpochBump：推进下发纪元（后台改了节点、池等）后，下一次取线路重算，不等 TTL。
func TestDeliverableNodesCachePurgedOnEpochBump(t *testing.T) {
	ctx := context.Background()
	cat := &countingCatalog{}
	s := newCacheTestService(cat)
	q := subsvc.NodeQuery{PlanVersionID: uuid.New()}
	// 还没建缓存时清空不出错，之后照常建
	s.SubscriptionsChanged(ctx, nil)
	for i := 0; i < 2; i++ {
		if _, err := s.DeliverableNodes(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if cat.count() != 1 {
		t.Fatalf("纪元没动时应命中缓存: calls=%d", cat.count())
	}
	s.SubscriptionsChanged(ctx, nil)
	if _, err := s.DeliverableNodes(ctx, q); err != nil || cat.count() != 2 {
		t.Fatalf("推进纪元后应重算: err=%v calls=%d", err, cat.count())
	}
}

// TestDeliveryCacheGroupsDoNotMix：有组与默认组的线路各存各的键，先缓存的有组结果不会被默认组拿到，反之亦然。
func TestDeliveryCacheGroupsDoNotMix(t *testing.T) {
	ctx := context.Background()
	c := newDeliveryCache(deliveryCacheSize, deliveryCacheTTL)
	v, gA, gB := uuid.New(), uuid.New(), uuid.New()
	rows := groupFixtureRows(gA)
	loads := 0
	get := func(group *uuid.UUID) []string {
		t.Helper()
		nodes, err := c.get(ctx, deliveryKey(v, group), func(context.Context) ([]render.Node, error) {
			loads++
			return deliverableFromRows(rows, group, time.Now(), time.UTC, testPSKDeriver())
		})
		if err != nil {
			t.Fatal(err)
		}
		return lineNames(nodes)
	}
	for round := 1; round <= 2; round++ {
		if got := get(&gA); !slices.Equal(got, []string{"不限组", "限 A 组"}) {
			t.Fatalf("第 %d 轮 A 组: %v", round, got)
		}
		if got := get(nil); !slices.Equal(got, []string{"不限组"}) {
			t.Fatalf("第 %d 轮默认组: %v", round, got)
		}
		if got := get(&gB); !slices.Equal(got, []string{"不限组"}) {
			t.Fatalf("第 %d 轮 B 组: %v", round, got)
		}
	}
	if loads != 3 {
		t.Fatalf("现算 %d 次，期望 3 次（每个键一次）", loads)
	}
}

// TestDeliveryCacheConcurrentRender：同一个键 50 个并发未命中只现算一次（singleflight），拿到的同一份线路
// 再各自按全部格式渲染；渲染不改缓存里的切片与 Config。要用 -race 跑。
func TestDeliveryCacheConcurrentRender(t *testing.T) {
	c := newDeliveryCache(deliveryCacheSize, deliveryCacheTTL)
	rows := cacheFixtureRows()
	var loads atomic.Int32
	load := func(context.Context) ([]render.Node, error) {
		loads.Add(1)
		// 让其余调用者在这次现算进行中到达，合并到同一次
		time.Sleep(20 * time.Millisecond)
		return deliverableFromRows(rows, nil, time.Now(), time.UTC, testPSKDeriver())
	}
	key := deliveryKey(uuid.New(), nil)
	formats := []render.Format{render.FormatClash, render.FormatClashPremium, render.FormatSingbox, render.FormatURI}

	const callers = 50
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			nodes, err := c.get(context.Background(), key, load)
			if err != nil {
				errs <- err
				return
			}
			if len(nodes) != len(rows) {
				errs <- fmt.Errorf("线路 %d 条，期望 %d", len(nodes), len(rows))
				return
			}
			proxyUUID := uuid.NewString()
			for _, f := range formats {
				if body, _, _ := render.RenderRequest(render.Request{Format: f, Nodes: nodes, ProxyUUID: proxyUUID, UA: "sing-box 1.13.0"}); len(body) == 0 {
					errs <- fmt.Errorf("%s 渲染为空", f)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := loads.Load(); n != 1 {
		t.Fatalf("现算 %d 次，期望 1 次（同键并发未命中应合并）", n)
	}
	// 渲染没改缓存里的东西：与重新算的一份逐项相同
	want, err := deliverableFromRows(rows, nil, time.Now(), time.UTC, testPSKDeriver())
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.get(context.Background(), key, func(context.Context) ([]render.Node, error) {
		return nil, errors.New("缓存里已有，不应重算")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("并发渲染改动了缓存里的线路或协议配置")
	}
}

// TestDeliveryCacheExpires：过了 TTL 重算（测试用很短的 TTL，不睡 5 秒）。
func TestDeliveryCacheExpires(t *testing.T) {
	ctx := context.Background()
	c := newDeliveryCache(deliveryCacheSize, 100*time.Millisecond)
	loads := 0
	load := func(context.Context) ([]render.Node, error) {
		loads++
		return []render.Node{{Name: fmt.Sprintf("第 %d 次", loads)}}, nil
	}
	for i := 0; i < 2; i++ {
		nodes, err := c.get(ctx, "k", load)
		if err != nil || len(nodes) != 1 || nodes[0].Name != "第 1 次" || loads != 1 {
			t.Fatalf("TTL 内第 %d 次取: %v %v loads=%d", i+1, nodes, err, loads)
		}
	}
	time.Sleep(250 * time.Millisecond)
	nodes, err := c.get(ctx, "k", load)
	if err != nil || len(nodes) != 1 || nodes[0].Name != "第 2 次" || loads != 2 {
		t.Fatalf("过期后应重算: %v %v loads=%d", nodes, err, loads)
	}
}

// TestDeliveryCacheLoadDetachesFromCaller：现算拿到的 ctx 保留调用者 ctx 里的值、不跟随调用者的取消，并且有期限。
func TestDeliveryCacheLoadDetachesFromCaller(t *testing.T) {
	type ctxKey struct{}
	c := newDeliveryCache(deliveryCacheSize, deliveryCacheTTL)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "req-1"))
	cancel()
	nodes, err := c.get(ctx, "k", func(lctx context.Context) ([]render.Node, error) {
		if err := lctx.Err(); err != nil {
			return nil, err
		}
		if _, ok := lctx.Deadline(); !ok {
			return nil, errors.New("现算的 ctx 应有期限")
		}
		if lctx.Value(ctxKey{}) != "req-1" {
			return nil, errors.New("现算的 ctx 应保留调用者 ctx 里的值")
		}
		return []render.Node{{Name: "a"}}, nil
	})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("调用者已取消也应拿到结果: %v %v", nodes, err)
	}
}

// TestDeliverableFromRowsBadConfig：协议配置不是 JSON 对象时整份报错（不缓存由 get 保证）；空输入是非 nil 空切片。
func TestDeliverableFromRowsBadConfig(t *testing.T) {
	rows := groupFixtureRows(uuid.New())
	rows[0].ProtocolConfig = `[1,2]`
	if _, err := deliverableFromRows(rows, nil, time.Now(), time.UTC, testPSKDeriver()); err == nil {
		t.Fatal("协议配置不是 JSON 对象应报错")
	}
	nodes, err := deliverableFromRows(nil, nil, time.Now(), time.UTC, testPSKDeriver())
	if err != nil || nodes == nil || len(nodes) != 0 {
		t.Fatalf("空输入应是非 nil 空切片: %#v %v", nodes, err)
	}
}
