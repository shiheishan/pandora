package config

import (
	"fmt"
	"strings"
)

//------------------------------------------------------------------------------
// 数据库连接的 plan_cache_mode：每网关一个可选开关，缺省不设
//------------------------------------------------------------------------------

// DBPlanCacheModeEnv 是各网关连接 plan_cache_mode 的环境变量名，写法与 DBMaxConnsEnv 一致：
// 三个网关加载同一份 .env，各自一个变量。
//
// 为什么要有这个入口：pgx 缺省按「缓存预备语句」执行，PG 对预备语句前 5 次用 custom plan（带实参
// 现规划），之后可能转 generic plan。节点上报的热路径是固定形状的几条 SQL、每分钟上千次，
// 规划时间在 PG 执行时间之外占了一块 CPU（节点后端约 124 秒 CPU，其中执行约 61 秒；
// 原因是规划还是协议，要先开 track_planning 量，见性能总方案 N2 第 7 项）。若量出来是 custom plan
// 反复规划，给 node 网关设 force_generic_plan 就能把规划做一次；反过来 generic plan 选错
// 计划时设 force_custom_plan。
//
// 缺省为空 = 不往连接上带这个参数，行为与以前完全一致。这里只做开关，没有量测结论之前不要
// 设：generic plan 在带倾斜数据的参数上可能变慢，必须经 bench-eval 判分。
//
// 实现是把 plan_cache_mode 作为运行参数拼到该网关的连接串上（pgx 把不认识的键当作启动参数
// 交给服务端），所以不改连接池，也不动 db 包；网关用 Config.DatabaseURLFor 取连接串。
var DBPlanCacheModeEnv = map[Domain]string{
	DomainPublic: "AEGIS_PUBLIC_DB_PLAN_CACHE_MODE",
	DomainAdmin:  "AEGIS_ADMIN_DB_PLAN_CACHE_MODE",
	DomainNode:   "AEGIS_NODE_DB_PLAN_CACHE_MODE",
}

// validPlanCacheModes 是 PG 接受的取值（auto 即服务端缺省，显式写出来用于回滚时覆盖别处的设置）。
var validPlanCacheModes = []string{"auto", "force_custom_plan", "force_generic_plan"}

func loadPlanCacheModes() (map[Domain]string, error) {
	modes := map[Domain]string{}
	for d, name := range DBPlanCacheModeEnv {
		v := strings.TrimSpace(env(name, ""))
		if v == "" {
			modes[d] = ""
			continue
		}
		ok := false
		for _, m := range validPlanCacheModes {
			ok = ok || v == m
		}
		if !ok {
			return nil, fmt.Errorf("%s 必须是 %s 之一，当前为 %q", name, strings.Join(validPlanCacheModes, " / "), v)
		}
		modes[d] = v
	}
	return modes, nil
}

// checkPlanCacheModes 拒绝「连接串里已经写了 plan_cache_mode、环境变量又设了一个」：两处并存时
// 以哪个为准取决于驱动对重复键的处理，不留歧义。
func checkPlanCacheModes(databaseURL string, modes map[Domain]string) error {
	if !strings.Contains(databaseURL, "plan_cache_mode") {
		return nil
	}
	for d, m := range modes {
		if m != "" {
			return fmt.Errorf("AEGIS_DATABASE_URL 里已经带了 plan_cache_mode，不能再设 %s；只留一处", DBPlanCacheModeEnv[d])
		}
	}
	return nil
}

// DatabaseURLFor 返回某个网关用的连接串：该网关设了 plan_cache_mode 就把它拼成运行参数，
// 否则原样返回 DatabaseURL。支持 URL 形式（postgres://…）与键值形式（host=… dbname=…）。
func (c *Config) DatabaseURLFor(d Domain) string {
	mode := c.DBPlanCacheMode[d]
	if mode == "" {
		return c.DatabaseURL
	}
	u := c.DatabaseURL
	if strings.HasPrefix(u, "postgres://") || strings.HasPrefix(u, "postgresql://") {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		return u + sep + "plan_cache_mode=" + mode
	}
	return u + " plan_cache_mode=" + mode
}
