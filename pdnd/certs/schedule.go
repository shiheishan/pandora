package certs

import (
	"math/rand/v2"
	"net"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

const (
	ProfileShortlived = "shortlived"
	firstRetry        = 15 * time.Minute   // 失败后第一次重试
	maxRetry          = 4 * time.Hour      // 退避上限
	portBusyRetry     = 5 * time.Minute    // 80 端口被占：不消耗 CA 额度，按这个间隔重试
	ariPollInterval   = 6 * time.Hour      // 每张证书查 ARI 的间隔
	unusedKeep        = 7 * 24 * time.Hour // 不再需要的证书保留多久再删
)

// jitter 给间隔加 ±10% 抖动；测试里可替换成恒等函数。
var jitter = func(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	// ±10%：在 [0.9, 1.1) 倍之间均匀取
	frac := 0.9 + rand.Float64()*0.2
	return time.Duration(float64(d) * frac)
}

// profileFor：IP 标识符 → ProfileShortlived；域名 → ""（用 CA 缺省档）。
func profileFor(id string) string {
	id = core.NormalizeCertIdentifier(id)
	if id == "" {
		return ""
	}
	if net.ParseIP(id) != nil {
		return ProfileShortlived
	}
	return ""
}

// fallbackRenewAt 是拿不到 ARI 建议时的续期时刻：shortlived 档在有效期过半时，其他档在过了 2/3 时。
func fallbackRenewAt(notBefore, notAfter time.Time, profile string) time.Time {
	life := notAfter.Sub(notBefore)
	if life <= 0 {
		return notBefore
	}
	if profile == ProfileShortlived {
		return notBefore.Add(life / 2)
	}
	return notBefore.Add(life * 2 / 3)
}

// retryDelay 是第 failures 次（从 1 起）失败后的等待：
//
//	code == ReasonRateLimited 且 retryAfter > 0：max(retryAfter, firstRetry)，不加抖动；
//	code == ReasonChallengePortBusy：jitter(portBusyRetry)；
//	其他：jitter(min(firstRetry << (failures-1), maxRetry))（failures < 1 按 1；移位注意溢出，超过上限直接取上限）。
func retryDelay(code string, failures int, retryAfter time.Duration) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if code == ReasonRateLimited && retryAfter > 0 {
		d := retryAfter
		if d < firstRetry {
			d = firstRetry
		}
		return d
	}
	if code == ReasonChallengePortBusy {
		return jitter(portBusyRetry)
	}
	// 逐次翻倍到上限为止，不用移位，免得 failures 很大时溢出
	d := firstRetry
	for i := 1; i < failures && d < maxRetry; i++ {
		d *= 2
	}
	if d > maxRetry {
		d = maxRetry
	}
	return jitter(d)
}
