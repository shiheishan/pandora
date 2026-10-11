package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

//------------------------------------------------------------------------------
// 运行时资源上限：数据库连接池与口令哈希并发
//------------------------------------------------------------------------------

// DBMaxConnsEnv 是各网关连接池上限的环境变量名。三个网关的 systemd 单元加载
// 同一份 .env，所以与 PprofAddrEnv 一样每个网关一个变量，各自可调。
var DBMaxConnsEnv = map[Domain]string{
	DomainPublic: "AEGIS_PUBLIC_DB_MAX_CONNS",
	DomainAdmin:  "AEGIS_ADMIN_DB_MAX_CONNS",
	DomainNode:   "AEGIS_NODE_DB_MAX_CONNS",
}

// 连接池缺省值的算式。数据基座 compose 给 PostgreSQL 设了 max_connections=60：
//
//	60 = 3（superuser_reserved_connections 缺省，留给迁移与超级用户排障）
//	   + 11（维护余量：adminctl / payctl 各自的池、备份 pg_dump、迁移演练、
//	         psql 排障、健康检查，按同时在场的最坏情况留）
//	   + 1（aegis-public 常驻的 LISTEN，它从 public 的池里借走一条且不归还）
//	   + 3 × 15（三个网关各 15 条）
//
// aegis-node 的 15 条里：纪元监听的 LISTEN 从它的池里借走 1 条且不归还（同 public），节点
// 「变了没」探针用 1 条池外的专用连接（不和请求抢池，nodefabric/epoch_watch.go），所以它的
// 池上限是 14，处理请求与后台循环的是 13 条。纪元监听健康时节点请求几乎不碰库（1000 节点
// 静默实测：每秒约 17 个上报事务 + 7 个在线记录事务，平均在用不到 0.2 条），13 条有余量。
//
// 所以 public 缺省 16（15 + LISTEN），admin 15，node 14（+ 1 条探针专用），合计 46。
// 改了 max_connections 或在同一个库上多开网关实例时，按同一个算式重排这三个变量，
// 合计不要超过 max_connections − 3 − 维护余量。原生安装（系统包 PostgreSQL，
// 缺省 max_connections=100）用同样的缺省值也有余量。
const (
	composeMaxConnections        = 60
	superuserReservedConnections = 3
	maintenanceConnections       = 11
	publicListenConnections      = 1
	// nodeProbeConnections 是 aegis-node 纪元监听探针的池外专用连接。
	nodeProbeConnections = 1
	gatewayCount         = 3

	defaultGatewayDBConns = (composeMaxConnections - superuserReservedConnections -
		maintenanceConnections - publicListenConnections) / gatewayCount

	// 下限 2：public 的 LISTEN 独占一条，至少还要一条处理请求；上限只防手误。
	minDBMaxConns = 2
	maxDBMaxConns = 500
)

// DefaultDBMaxConns 是各网关连接池上限的缺省值，算式见上。
var DefaultDBMaxConns = map[Domain]int32{
	DomainPublic: defaultGatewayDBConns + publicListenConnections,
	DomainAdmin:  defaultGatewayDBConns,
	DomainNode:   defaultGatewayDBConns - nodeProbeConnections,
}

// DBMinConnsEnv 是各网关连接池常驻连接数（pgxpool MinConns）的环境变量名。
var DBMinConnsEnv = map[Domain]string{
	DomainPublic: "AEGIS_PUBLIC_DB_MIN_CONNS",
	DomainAdmin:  "AEGIS_ADMIN_DB_MIN_CONNS",
	DomainNode:   "AEGIS_NODE_DB_MIN_CONNS",
}

// DefaultDBMinConns 是常驻连接数的缺省值。
//
// node 缺省 8：300 个节点每分钟约 70 次请求/节点，池子常年有 8 条以上在用
// （5k-r3 实测已建连接 8→15）。常驻连接少了，空闲回收后的下一波请求要现建连接：
// 重做 SCRAM 认证、新起后端进程、语句缓存全冷，正是尾延迟的来源。public、admin
// 的请求量小一个数量级，保持 1。常驻连接不突破 MaxConns，不改变连接预算。
// 纪元监听上线后节点请求多数不碰库，常驻可能用不了 8 条；降不降、降到几（2 / 4 / 8）
// 留给性能闸门的 A/B 实测（总协调 2026-10-09 定，计划 D 路），没数据之前不动。
var DefaultDBMinConns = map[Domain]int32{
	DomainPublic: 1,
	DomainAdmin:  1,
	DomainNode:   8,
}

// 口令哈希（Argon2id，每次 19 MiB）的全局并发上限与排队超时。
//
// 并发缺省 2，等于 2 核机器的核数：登录潮（预热时 200 个登录、10k 稳态的登录峰）里两个
// 哈希并行，吞吐翻倍，p50 排队时间折半。这个值要求 aegis-public 没有 CPU 硬配额：原先单元文件
// 是 CPUQuota=60%，两个 Argon2 同时算约 30ms 就耗光额度，整个进程被节流停摆，所以当时只能
// 压到 1（5k-r3 预热那一分钟节流 14 秒），而 1 个名额让 200 个登录串行排队，预热 p50 是
// 100 × 23ms 量级。现在单元文件只设 CPUWeight（争抢时按权重让位，空闲时不封顶），闸门放到 2。
// 内存峰值多 19 MiB，仍在 MemoryMax=256M 之内。aegis-admin 仍带 CPUQuota=80%，但后台登录
// 极少并发，两个哈希同时算不会常发生；要压回 1 在 .env 里设 AEGIS_PASSWORD_HASH_CONCURRENCY=1。
// 排队缺省 5 秒：远小于 nginx 认证入口 20 秒的读超时与网关 25 秒的请求超时，
// 排不上就回 503 让客户端稍后重试，而不是在队里耗到上游超时。排队不占数据库
// 连接，也不占内存，只是一个等待中的 goroutine。
const (
	PasswordHashConcurrencyEnv  = "AEGIS_PASSWORD_HASH_CONCURRENCY"
	PasswordHashQueueTimeoutEnv = "AEGIS_PASSWORD_HASH_QUEUE_TIMEOUT"

	DefaultPasswordHashConcurrency  = 2
	DefaultPasswordHashQueueTimeout = 5 * time.Second

	maxPasswordHashConcurrency  = 16
	maxPasswordHashQueueTimeout = 15 * time.Second
)

// 节点拉用户名单的节拍（下发给节点的 pull_interval，整秒）。缺省 15 秒：第三方
// UniProxy 节点端不连事件流，到期、配额用尽只能靠轮询收口；名单已按池缓存，
// 拉一次命中缓存不碰库，这个频率不再是库的负担。范围与 nodefabric.SetNodePullInterval 一致。
const (
	NodePullIntervalEnv     = "AEGIS_NODE_PULL_INTERVAL"
	DefaultNodePullInterval = 15 * time.Second

	minNodePullIntervalSeconds = 5
	maxNodePullIntervalSeconds = 300
)

// Runtime 是进程级的资源上限，缺省即用默认值，设了就严格校验。
type Runtime struct {
	// DBMaxConns 是各网关连接池上限，三个域都有值。
	DBMaxConns map[Domain]int32
	// DBMinConns 是各网关连接池常驻连接数，三个域都有值，不超过对应的 DBMaxConns。
	DBMinConns map[Domain]int32
	// DBPlanCacheMode 是各网关连接的 plan_cache_mode，空串 = 不设（PG 缺省 auto），见 plan_cache.go。
	DBPlanCacheMode map[Domain]string
	// PasswordHashConcurrency 是同时进行的 Argon2 计算上限。
	PasswordHashConcurrency int
	// PasswordHashQueueTimeout 是等一个哈希名额的最长时间，超时回 503。
	PasswordHashQueueTimeout time.Duration
	// NodePullInterval 是下发给节点的拉用户节拍，只有 aegis-node 用。
	NodePullInterval time.Duration
}

func loadRuntime() (Runtime, error) {
	r := Runtime{DBMaxConns: map[Domain]int32{}, DBMinConns: map[Domain]int32{}}
	for _, d := range []Domain{DomainPublic, DomainAdmin, DomainNode} {
		name := DBMaxConnsEnv[d]
		n, err := boundedEnvInt(name, int(DefaultDBMaxConns[d]), minDBMaxConns, maxDBMaxConns)
		if err != nil {
			return Runtime{}, err
		}
		r.DBMaxConns[d] = int32(n)
		// 常驻数缺省值随上限收：只调小了上限的部署不必再去调常驻数
		minDefault := min(int(DefaultDBMinConns[d]), n)
		m, err := boundedEnvInt(DBMinConnsEnv[d], minDefault, 0, n)
		if err != nil {
			return Runtime{}, fmt.Errorf("%w（不能超过 %s）", err, name)
		}
		r.DBMinConns[d] = int32(m)
	}
	modes, err := loadPlanCacheModes()
	if err != nil {
		return Runtime{}, err
	}
	r.DBPlanCacheMode = modes
	n, err := boundedEnvInt(PasswordHashConcurrencyEnv, DefaultPasswordHashConcurrency, 1, maxPasswordHashConcurrency)
	if err != nil {
		return Runtime{}, err
	}
	r.PasswordHashConcurrency = n
	wait, err := strictPositiveEnvDuration(PasswordHashQueueTimeoutEnv, DefaultPasswordHashQueueTimeout)
	if err != nil {
		return Runtime{}, err
	}
	if wait > maxPasswordHashQueueTimeout {
		return Runtime{}, fmt.Errorf("%s 不能超过 %s（要短于 nginx 认证入口 20 秒的读超时）",
			PasswordHashQueueTimeoutEnv, maxPasswordHashQueueTimeout)
	}
	r.PasswordHashQueueTimeout = wait
	secs, err := boundedEnvInt(NodePullIntervalEnv, int(DefaultNodePullInterval/time.Second),
		minNodePullIntervalSeconds, maxNodePullIntervalSeconds)
	if err != nil {
		return Runtime{}, err
	}
	r.NodePullInterval = time.Duration(secs) * time.Second
	return r, nil
}

// boundedEnvInt 读一个闭区间内的整数；未设置用 def。
func boundedEnvInt(k string, def, lo, hi int) (int, error) {
	v := strings.TrimSpace(env(k, ""))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s 不是合法的整数: %w", k, err)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%s 必须在 %d 到 %d 之间，当前为 %d", k, lo, hi, n)
	}
	return n, nil
}
