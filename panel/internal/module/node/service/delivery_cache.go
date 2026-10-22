package service

// 订阅线路的进程内微缓存：同一个（套餐版本, 用户组）的可下发线路在 deliveryCacheTTL 内只算一次。
//
// 能缓存的前提：DeliverableNodes 的结果只取决于套餐版本绑的池、池里节点与服务器的状态、订阅主人的用户组和当前时刻
// （心跳新鲜窗口），与订阅 id、用户 id 无关，同一个池里每个人拿到的列表完全相同。压测里订阅拉取约每秒 50 次就饱和，
// 节点列表那条 SQL 占 PG 执行时间一半，而它每次都在重算同一份结果。
//
// 失效：本进程推进下发纪元时（改节点、改池、改名单相关的数据，epoch.go 的 bumpErr）整份清空，后台改了线路，下一次拉取就是新的；
// 别的变化（心跳失联、别的进程里的写）最多晚 deliveryCacheTTL 出现在订阅里。不订阅 Redis。它不是安全边界：谁能连哪个节点由节点上的用户名单决定，名单走 gRPC 纪元推送
// （session*.go），不经过这里；订阅生效、主人账号 active、凭据有效由拉取路径每次现查（subscription 的 pull.go），也不经过这里。
//
// 缓存里的切片与 render.Node.Config（protocol.KernelConfig 的结果，嵌套的 map、切片是共享引用）被所有命中者同时读：
// 调用方只读，不改切片、不改 Config（render.RenderRequest 只读，有 render/shared_test.go 守着）。
// 不要在事务里调：事务里读到的未提交数据会经缓存给到别的请求（拉取与门户预览都不在事务里调）。

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"github.com/aegispanel/aegis/internal/module/subscription/render"
)

const (
	// deliveryCacheTTL 是一份线路列表在进程里留多久：线路变化最多晚这么久出现在订阅里。
	deliveryCacheTTL = 5 * time.Second
	// deliveryCacheSize 是最多留多少个（套餐版本, 用户组）键，超出按最近最少使用淘汰。
	// 实际组合远少于它，只防异常输入把内存撑大。
	deliveryCacheSize = 1024
	// deliveryLoadTimeout 是一次现算（两次读库）的上限：现算不跟随调用者的取消（见 get），要自己设上限。
	deliveryLoadTimeout = 5 * time.Second
)

// deliveryKey 是缓存键：套餐版本 + 用户组；默认组（用户组为 nil）是单独的键。
func deliveryKey(planVersionID uuid.UUID, userGroupID *uuid.UUID) string {
	if userGroupID == nil {
		return planVersionID.String() + "/default"
	}
	return planVersionID.String() + "/" + userGroupID.String()
}

// deliveryCache 是按键的有界 TTL 缓存，加同键并发未命中的合并。并发安全。
//
// expirable.NewLRU 会起一个清理过期项的 goroutine，库（v2.0.7）没有关掉它的方法，随进程退出；所以第一次用到时才建
// （worker 与命令行子命令不拉订阅，就不起这个 goroutine）。
type deliveryCache struct {
	size int
	ttl  time.Duration
	mu   sync.Mutex
	// lru 第一次用到时由 store 建；purge 只读它，没建过就不建
	lru    atomic.Pointer[expirable.LRU[string, []render.Node]]
	flight singleflight.Group
}

// newDeliveryCache 建一个最多 size 个键、每项留 ttl 的缓存。ttl 必须大于 0（库把 0 当作永不过期）。
func newDeliveryCache(size int, ttl time.Duration) *deliveryCache {
	return &deliveryCache{size: size, ttl: ttl}
}

func (c *deliveryCache) store() *expirable.LRU[string, []render.Node] {
	if l := c.lru.Load(); l != nil {
		return l
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if l := c.lru.Load(); l != nil {
		return l
	}
	l := expirable.NewLRU[string, []render.Node](c.size, nil, c.ttl)
	c.lru.Store(l)
	return l
}

// purge 清空全部键；还没建过就什么也不做（不为清空去起清理 goroutine）。
func (c *deliveryCache) purge() {
	if l := c.lru.Load(); l != nil {
		l.Purge()
	}
}

// get 取 key 的线路：命中直接返回；没命中时同一个 key 的并发调用只有一个执行 load，其余等它的结果。
// load 成功才放进缓存；出错不缓存，下一次调用重算。
//
// load 拿到的 ctx 不跟随调用者的取消（保留 ctx 里的值，期限是 deliveryLoadTimeout）：否则第一个调用者断开时，
// 同时在等的请求全部跟着失败（与 settings 开关缓存同一做法）。
func (c *deliveryCache) get(ctx context.Context, key string, load func(context.Context) ([]render.Node, error)) ([]render.Node, error) {
	lru := c.store()
	if nodes, ok := lru.Get(key); ok {
		return nodes, nil
	}
	v, err, _ := c.flight.Do(key, func() (any, error) {
		// 两次查之间可能刚有一轮现算写进了缓存：再查一次，不重复读库
		if nodes, ok := lru.Get(key); ok {
			return nodes, nil
		}
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryLoadTimeout)
		defer cancel()
		nodes, err := load(lctx)
		if err != nil {
			return nil, err
		}
		lru.Add(key, nodes)
		return nodes, nil
	})
	if err != nil {
		return nil, err
	}
	nodes, _ := v.([]render.Node)
	return nodes, nil
}
