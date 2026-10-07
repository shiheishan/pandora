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
//	   + 3 × 15（三个网关各 15 条处理请求与后台循环）
//
// 所以 public 缺省 16（15 + LISTEN），admin、node 各 15，三者合计 46。
// 改了 max_connections 或在同一个库上多开网关实例时，按同一个算式重排这三个变量，
// 合计不要超过 max_connections − 3 − 维护余量。原生安装（系统包 PostgreSQL，
// 缺省 max_connections=100）用同样的缺省值也有余量。
const (
	composeMaxConnections        = 60
	superuserReservedConnections = 3
	maintenanceConnections       = 11
	publicListenConnections      = 1
	gatewayCount                 = 3

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
	DomainNode:   defaultGatewayDBConns,
}

// 口令哈希（Argon2id，每次 19 MiB）的全局并发上限与排队超时。
//
// 并发缺省 2：public 网关的 CPUQuota 是 60%，两个并发已经能吃满这点 CPU，
// 再多只会让每个哈希都变慢、同时把内存翻倍（4 个就是 76 MiB 常驻工作集）。
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

// Runtime 是进程级的资源上限，缺省即用默认值，设了就严格校验。
type Runtime struct {
	// DBMaxConns 是各网关连接池上限，三个域都有值。
	DBMaxConns map[Domain]int32
	// PasswordHashConcurrency 是同时进行的 Argon2 计算上限。
	PasswordHashConcurrency int
	// PasswordHashQueueTimeout 是等一个哈希名额的最长时间，超时回 503。
	PasswordHashQueueTimeout time.Duration
}

func loadRuntime() (Runtime, error) {
	r := Runtime{DBMaxConns: map[Domain]int32{}}
	for _, d := range []Domain{DomainPublic, DomainAdmin, DomainNode} {
		name := DBMaxConnsEnv[d]
		n, err := boundedEnvInt(name, int(DefaultDBMaxConns[d]), minDBMaxConns, maxDBMaxConns)
		if err != nil {
			return Runtime{}, err
		}
		r.DBMaxConns[d] = int32(n)
	}
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
