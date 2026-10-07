package subscription

// 订阅拉取用的两份进程内缓存。
//
// 规矩（与面板其它缓存相同）：只在进程内、条目有上限、有 TTL、可被事件失效。
// 缓存的只有「人人相同」的东西——租户的路径前缀、某个套餐版本对某个用户组
// 可下发的节点；凭据认证、用量、限流一律现查，轮换或吊销链接立刻生效。

import (
	"context"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/realtime"
)

const (
	// prefixCacheTTL：前缀只在迁移 00018 里生成，代码从不改；运维手工改库后
	// 最多这么久生效。
	prefixCacheTTL = time.Minute

	// nodeCacheTTL：节点变更信号丢了（Valkey 抖动）时的兜底时效。心跳「新鲜」
	// 的判定（10 分钟窗口）与刚上线节点首次心跳后的出现，最多因此晚这么久。
	nodeCacheTTL = 20 * time.Second
	// nodeCacheMaxEntries：键是（租户, 套餐版本, 用户组），正常只有几十个；
	// 满了先清过期的，仍满就整表清空，宁可多查几次也不无界增长。
	nodeCacheMaxEntries = 256
)

//------------------------------------------------------------------------------
// 路径前缀
//------------------------------------------------------------------------------

type prefixEntry struct {
	prefix  string
	expires time.Time
}

type prefixCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]prefixEntry
}

func newPrefixCache(ttl time.Duration) *prefixCache {
	return &prefixCache{ttl: ttl, now: time.Now, entries: map[string]prefixEntry{}}
}

// get 取未过期的前缀；空串（租户没配前缀）同样缓存。缓存为 nil 时一律未命中。
func (c *prefixCache) get(tenant string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[tenant]
	if !ok || !c.now().Before(e.expires) {
		return "", false
	}
	return e.prefix, true
}

func (c *prefixCache) put(tenant, prefix string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// 产品只有一个租户；这里的上限只防异常输入把表撑大
	if len(c.entries) >= nodeCacheMaxEntries {
		clear(c.entries)
	}
	c.entries[tenant] = prefixEntry{prefix: prefix, expires: c.now().Add(c.ttl)}
}

//------------------------------------------------------------------------------
// 可下发节点
//------------------------------------------------------------------------------

// nodeCacheKey：资格查询（listEligibleNodesTx）的答案只随这三样变。
type nodeCacheKey struct {
	tenant      string
	planVersion string
	userGroup   string
}

type nodeEntry struct {
	nodes   []Node
	expires time.Time
}

// nodeFlight 是一次正在进行的现查；同一个键的并发未命中等它，不各查一遍。
type nodeFlight struct {
	done  chan struct{}
	nodes []Node
	err   error
}

// nodeCache 缓存的 []Node 由所有命中者共享，只读：Render 先复制切片再改名，
// 协议配置 map 只读不写（render_test.go 的 TestRenderLeavesCachedNodesUntouched 锁着）。
type nodeCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	now     func() time.Time
	entries map[nodeCacheKey]nodeEntry
	flights map[nodeCacheKey]*nodeFlight
	// gen 是每个租户的失效代数：现查开始前记下，写回时代数变了就丢弃结果，
	// 免得一份在失效信号之前查出来的旧答案在信号之后被写回去
	gen map[string]uint64

	hub      *realtime.Hub
	watching map[string]bool
}

func newNodeCache(ttl time.Duration, max int) *nodeCache {
	return &nodeCache{ttl: ttl, max: max, now: time.Now,
		entries:  map[nodeCacheKey]nodeEntry{},
		flights:  map[nodeCacheKey]*nodeFlight{},
		gen:      map[string]uint64{},
		watching: map[string]bool{},
	}
}

// AttachRealtime 让节点缓存跟着节点变更信号失效。
//
// 后台改节点（含路由、换池）后发 node.config.changed，套餐换绑节点池、池的用户组
// 名单、用户换组、付款履约后发 node.users.changed，都在租户级频道
// realtime.ChannelNodeAll 上；收到任何一条就清掉该租户的全部条目。只有做订阅
// 拉取的进程（public 网关）需要调它；没调时缓存只靠 TTL 过期。
// 门户节点预览的缓存（previews）同样挂上。
func (s *Service) AttachRealtime(hub *realtime.Hub) {
	if hub == nil {
		return
	}
	for _, c := range []*nodeCache{s.nodes, s.previews} {
		if c == nil {
			continue
		}
		c.mu.Lock()
		c.hub = hub
		c.mu.Unlock()
	}
}

// load 取一个键的节点：命中直接返回；未命中由第一个请求现查并写回，同键的并发
// 请求等它的结果。现查失败时，等待者各自再查一次，不把别人的错误（例如对方的
// 请求被取消）当成自己的。缓存为 nil 时直接现查。
func (c *nodeCache) load(ctx context.Context, key nodeCacheKey,
	fill func(context.Context) ([]Node, error)) ([]Node, error) {
	if c == nil {
		return fill(ctx)
	}
	c.mu.Lock()
	c.watchLocked(key.tenant)
	if e, ok := c.entries[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.nodes, nil
	}
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			if f.err == nil {
				return f.nodes, nil
			}
			return fill(ctx)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &nodeFlight{done: make(chan struct{})}
	c.flights[key] = f
	gen := c.gen[key.tenant]
	c.mu.Unlock()

	nodes, err := fill(ctx)

	c.mu.Lock()
	delete(c.flights, key)
	if err == nil && c.gen[key.tenant] == gen {
		c.storeLocked(key, nodes)
	}
	f.nodes, f.err = nodes, err
	close(f.done)
	c.mu.Unlock()
	return nodes, err
}

func (c *nodeCache) storeLocked(key nodeCacheKey, nodes []Node) {
	now := c.now()
	if len(c.entries) >= c.max {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.max {
			clear(c.entries)
		}
	}
	c.entries[key] = nodeEntry{nodes: nodes, expires: now.Add(c.ttl)}
}

// invalidate 清掉一个租户的全部条目，并让此刻还在进行的现查结果作废。
func (c *nodeCache) invalidate(tenant string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen[tenant]++
	for k := range c.entries {
		if k.tenant == tenant {
			delete(c.entries, k)
		}
	}
}

// watchLocked 在第一次见到某个租户时订阅它的节点频道（调用方持有 c.mu）。
// 订阅随进程存活；hub 不在时什么也不做。
func (c *nodeCache) watchLocked(tenant string) {
	if c.hub == nil || c.watching[tenant] {
		return
	}
	c.watching[tenant] = true
	events, _ := c.hub.Subscribe([]string{realtime.ChannelNodeAll(tenant)})
	go func() {
		for range events {
			c.invalidate(tenant)
		}
	}()
}
