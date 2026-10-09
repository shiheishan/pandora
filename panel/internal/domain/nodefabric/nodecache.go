package nodefabric

import (
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/platform/cache"
)

// 节点链路的进程内缓存。
//
// 5k 实测（200 节点、每节点 15 秒拉一次用户）：ListNodeUsers 每秒 13 次、纯节点
// 负载占库时 80%。同一个池的 200 个节点拉到的是同一份用户表，两次变更之间它也
// 不变——缓存它，同池节点合成一次查询。签名请求的节点身份同理。
//
// 「改完立刻生效」靠下发纪元（迁移 00101 的 node_delivery_epoch 序列）：订阅、配额
// 用尽与否、流量包、套餐版本、池授权、用户组、系统设置、节点身份与节点状态一提交
// 就推进它。aegis-node 在本来就要跑的查询里顺手读出当前纪元（UniProxy 认证、拉生效
// 配置前的 nonce 认领、推送前读节点），缓存条目记着自己算出时的纪元，纪元前进了
// 就重算。所以无论改动来自后台、门户、定时任务还是直接执行的 SQL，下一次请求都
// 看得到，不用等 TTL，也不依赖 Pub/Sub 送达。
//
// TTL 只兜两类没有纪元的情况：不追踪的输入（在线设备记录），以及读方恰好卡在写方
// 「纪元已推进、数据还没提交可见」那道提交缝里算出的旧结果。订阅到期的时间流逝没有
// 写，用户集条目记着名单里最早的到期时刻（nextExpiry），到点硬过期、同步重算，不走
// 「先回旧值」的宽限。
//
// 只在 aegis-node 里开（EnableNodeCaches）；只缓存成功结果。缓存本身（单飞、上限、TTL、
// 硬到期、pinned、staleGrace、rank）是 platform/cache 的 Cache，这里只配参数。

const (
	// nodeUsersCacheTTL 是用户集缓存的寿命上限。strict 模式下的在线设备变化没有纪元，
	// 最坏延迟 = 这个 TTL + nodeUsersStaleGrace + 节点拉取间隔。订阅到期按条目的
	// nextExpiry 硬过期，不受这一条约束。
	nodeUsersCacheTTL = 5 * time.Second
	// nodeUsersStaleGrace 是用户集过了 TTL 之后还能先回旧值的窗口：先回旧值、后台单飞
	// 重算（stale-while-revalidate），请求不再每 5 秒同步等一次 30 多毫秒的名单查询。
	// 只对「TTL 到了」生效；纪元前进（任何已提交的下发相关改动）照旧同步重算。
	// 超过这个窗口还没人来取的旧值不再回，下一次请求同步重算。
	nodeUsersStaleGrace = 10 * time.Second
	// nodeIdentityCacheTTL 是签名身份缓存的寿命上限，且不超过身份自己的 expires_at。
	// 吊销、重新接入、节点退役或改服务状态都会推进纪元、下一次请求就回库，这个 TTL
	// 只兜「纪元已推进、数据还没提交可见」那道提交缝。
	nodeIdentityCacheTTL = 10 * time.Minute

	nodeUsersCacheMax    = 1024 // 条目按（租户, 池）计
	nodeIdentityCacheMax = 8192 // 条目按（租户, 节点）计
)

// deliveryEpochSQL 读出当前下发纪元，嵌进各处本来就要跑的查询里，不单独多一次往返。
//
// 要加上 is_called：新建的序列 last_value=1、is_called=false，第一次 nextval 返回 1、
// last_value 还是 1，只把 is_called 翻成 true。只读 last_value 会漏掉全库的第一次推进。
// 与 cache.EpochSQL(cache.NodeDeliveryEpoch) 逐字相同（nodecache_test.go 钉住）；写成常量是为了
// 源码契约按名字找到每个读纪元的入口（TestDeliveryEpochIsReadAlongsideEveryCachedInput）。
const deliveryEpochSQL = `(SELECT last_value + is_called::int FROM node_delivery_epoch)`

// nodeUserSet 是一个池当前该放行的用户、它的版本（UniProxy ETag 同源）、算出它时
// 的下发纪元，以及名单里最早的订阅到期时刻（零值表示没有会到期的订阅）。users 被
// 多个请求共享，只读。version 只在缓存路径上算好，直查路径为空。
type nodeUserSet struct {
	users      []ProxyUser
	version    string
	epoch      int64
	nextExpiry time.Time
	// watch 是加载前的纪元监听戳（零值：加载时监听不健康）。戳覆盖请求时的戳就不必
	// 再比纪元，loose 模式下也不必按 TTL 重算（epoch_watch.go）。
	watch watchStamp
	// strict 是加载时的设备判定模式：strict 名单依赖在线设备记录，没有纪元，照旧按 TTL 重算。
	strict bool
	// body 是这一版名单的 UniProxy 响应正文（JSON 与 gzip 各编码一次，同池节点共享）。
	// 只在缓存路径上有，直查路径为 nil。
	body *userSetBody
}

// nextExpiryFloor 是 nextExpiry 离现在的最小距离。库与本进程的时钟有偏差：本进程
// 先到了到期时刻、库里 now() 还没到，重算出的名单仍含这个人、nextExpiry 不变；
// 不设下限的话，偏差窗口里每个请求都同步重算一次。
const nextExpiryFloor = time.Second

// earlierExpiry 取两者中较早的到期时刻；at 为空（不会到期）时原样返回 cur。
func earlierExpiry(cur time.Time, at *time.Time) time.Time {
	if at == nil || (!cur.IsZero() && !at.Before(cur)) {
		return cur
	}
	return *at
}

// clampNextExpiry 把已经过去或太近的到期时刻推到 now + nextExpiryFloor。
func clampNextExpiry(at, now time.Time) time.Time {
	if at.IsZero() {
		return at
	}
	if floor := now.Add(nextExpiryFloor); at.Before(floor) {
		return floor
	}
	return at
}

// nodeCaches 是 aegis-node 进程的用户集缓存、签名身份缓存与节点配置视图缓存。
type nodeCaches struct {
	users    *cache.Cache[nodeUserSet]
	identity *cache.Cache[Identity]
	// config 只在纪元监听健康时用（nodeConfigView，config_delivery_view.go）。
	config *cache.Cache[*nodeConfigView]
	// watch 是纪元监听（StartEpochWatch 起；没起时为 nil，戳恒为零值）。
	watch *cache.Watch
	// alive 是在线上报的刷新备忘（uniproxy_alive_memo.go）。
	alive *aliveMemo
}

func newNodeCaches(now func() time.Time) *nodeCaches {
	users := cache.New(cache.Options[nodeUserSet]{TTL: nodeUsersCacheTTL, Max: nodeUsersCacheMax, Now: now,
		StaleGrace: nodeUsersStaleGrace,
		Rank:       func(set nodeUserSet) int64 { return set.epoch },
		Expiry:     func(set nodeUserSet) time.Time { return set.nextExpiry }})
	identity := cache.New(cache.Options[Identity]{TTL: nodeIdentityCacheTTL, Max: nodeIdentityCacheMax, Now: now,
		Expiry: func(id Identity) time.Time { return id.expiresAt },
		Rank:   func(id Identity) int64 { return id.epoch }})
	config := cache.New(cache.Options[*nodeConfigView]{TTL: nodeConfigCacheTTL, Max: nodeIdentityCacheMax, Now: now})
	return &nodeCaches{users: users, identity: identity, config: config, alive: newAliveMemo()}
}

// EnableNodeCaches 打开节点链路缓存。装配时调用一次（aegis-node）；其他进程不开，
// 照旧直查库。
func (s *Service) EnableNodeCaches() {
	if s.caches == nil {
		s.caches = newNodeCaches(nil)
		s.served = &servedSets{}
	}
}

func usersCacheKey(tenantID, poolID string) string { return tenantID + "\x00" + poolID }

func identityCacheKey(tenantID, nodeID string) string { return tenantID + "\x00" + nodeID }

func epochFlight(epoch int64) string { return strconv.FormatInt(epoch, 10) }
