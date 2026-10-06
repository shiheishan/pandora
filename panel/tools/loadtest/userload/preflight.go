// [INPUT]: 依赖 userload.go 的 usersConfig（各类速率与面板限流档位），依赖 traffic.go 的 actor（固定来源 IP）
// [OUTPUT]: 对外提供 包内的 panelLimits、preflight（按配置速率预估每个限流维度的峰值并给出告警）、netKey（与 middleware.ByIPPrefix 同口径的网段键）
// [POS]: tools/loadtest/userload 的开跑前自检：压测要测的是面板而不是限流，速率会撞上哪一道闸门在开跑前就说清楚，结果里的 429 才分得清是预期还是意外
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package userload

import (
	"fmt"
	"strings"
	"time"
)

// panelLimits 是被测面板的限流档位。默认值取面板代码里的缺省：
// AEGIS_RL_IP_PER_MIN=120、AEGIS_RL_ACCOUNT_PER_MIN=300、AEGIS_RL_AUTH_PER_MIN=10（platform/config），
// 订阅凭据每小时 60 次（迁移 00007 的 rate_limit_per_hour 缺省），后台每 IP 每分钟 240 次（api/admin/router.go 写死）。
// 被测环境改过环境变量时，用同名 flag 告诉压测工具。
type panelLimits struct {
	ipPerMin      int // pub_ip：public 网关每来源 IP 每分钟（订阅分发也在内）
	accountPerMin int // acct：public 需登录组每账号每分钟
	authPerMin    int // auth_route：登录每路由每 IP 每分钟；auth_net 为它 ×6 每 /24 每 10 分钟
	subPerHour    int // 订阅凭据每小时成功拉取次数
	adminIPPerMin int // adm_ip：后台每 IP 每分钟
}

// netKey 与 middleware.ByIPPrefix 同口径：IPv4 取 /24，IPv6 取前三组（/48）。
func netKey(ip string) string {
	if strings.Contains(ip, ":") {
		parts := strings.Split(ip, ":")
		if len(parts) >= 3 {
			return strings.Join(parts[:3], ":") + "::/48"
		}
		return ip
	}
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		return strings.Join(parts[:3], ".") + ".0/24"
	}
	return ip
}

type peak struct {
	key   string
	value float64
}

func (p *peak) see(key string, v float64) {
	if v > p.value {
		p.key, p.value = key, v
	}
}

// preflight 按目标速率估算每个限流维度上的峰值（均匀分摊的期望值，不含抖动），
// 返回全部说明行与其中的告警行。估算假设开环调度把每类流量均匀摊给对应的用户。
func preflight(cfg usersConfig, users, pool []*actor) (lines, warns []string) {
	lim := cfg.limits
	n, p := float64(len(users)), float64(len(pool))
	inPool := make(map[*actor]bool, len(pool))
	for _, a := range pool {
		inPool[a] = true
	}
	perUserSub := cfg.subRate / n // 次/秒
	perPoolPortal, perPoolLogin := 0.0, 0.0
	if p > 0 {
		perPoolPortal, perPoolLogin = cfg.portalRate/p, cfg.loginRate/p
	}

	ipRate := map[string]float64{}
	netRate := map[string]float64{}
	ipPool := map[string]int{}
	netPool := map[string]int{}
	for _, a := range users {
		r := perUserSub
		if inPool[a] {
			r += perPoolPortal + perPoolLogin
			ipPool[a.u.RealIP]++
			netPool[netKey(a.u.RealIP)]++
		}
		ipRate[a.u.RealIP] += r
		netRate[netKey(a.u.RealIP)] += r
	}
	var ipPeak, netPeak, ipLogin, netLogin peak
	for k, v := range ipRate {
		ipPeak.see(k, v*60)
	}
	for k, v := range netRate {
		netPeak.see(k, v*60)
	}
	for k, c := range ipPool {
		ipLogin.see(k, float64(c))
	}
	for k, c := range netPool {
		// 预热每人登录一次，再加运行期 10 分钟内的重新登录
		netLogin.see(k, float64(c)*(1+perPoolLogin*600))
	}

	add := func(warn bool, format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if warn {
			line = "WARN " + line
			warns = append(warns, line)
		} else {
			line = "ok   " + line
		}
		lines = append(lines, line)
	}

	add(ipPeak.value > float64(lim.ipPerMin),
		"pub_ip   每 IP 每分钟峰值 %.1f / 上限 %d（订阅+门户+登录，IP 相同的用户合并计）", ipPeak.value, lim.ipPerMin)
	add(netPeak.value > float64(lim.ipPerMin*8),
		"pub_net  每 /24 每分钟峰值 %.1f / 上限 %d（%s）", netPeak.value, lim.ipPerMin*8, netPeak.key)
	add(perPoolPortal*60 > float64(lim.accountPerMin),
		"acct     每账号每分钟 %.2f / 上限 %d", perPoolPortal*60, lim.accountPerMin)

	window := min(cfg.duration, time.Hour).Seconds()
	add(perUserSub*window > float64(lim.subPerHour),
		"sub      每凭据每小时成功拉取 %.1f / 上限 %d（%.0f 秒内每人 %.2f 次）",
		perUserSub*3600, lim.subPerHour, window, perUserSub*window)
	add(ipLogin.value > float64(lim.authPerMin),
		"auth     同一 IP 上的活跃用户 %.0f 个（预热时每人登录一次）/ 每 IP 每分钟上限 %d", ipLogin.value, lim.authPerMin)
	add(netLogin.value > float64(lim.authPerMin*6),
		"auth_net 每 /24 每 10 分钟登录峰值 %.1f / 上限 %d（%s；预热按 %.1f 次/秒约 %.0f 秒登完）",
		netLogin.value, lim.authPerMin*6, netLogin.key, cfg.warmupLoginRate, p/max(cfg.warmupLoginRate, 0.001))
	if len(cfg.adminIPs) > 0 && cfg.adminRate > 0 {
		per := cfg.adminRate / float64(len(cfg.adminIPs)) * 60
		add(per > float64(lim.adminIPPerMin),
			"adm_ip   每后台 IP 每分钟 %.1f / 上限 %d（%d 个后台 IP）", per, lim.adminIPPerMin, len(cfg.adminIPs))
	}
	return lines, warns
}
