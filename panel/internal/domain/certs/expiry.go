package certs

import "time"

// ExpiryLevel 是证书的到期等级：none（还没签出）、ok、warning（黄）、critical（红）、expired。
//
// 阈值按证书寿命缩放（设计稿 §1.4）：固定的 14 天 / 3 天对短寿命证书没有意义，CA 也在把寿命
// 缩到 64 天、45 天。
//   - 黄：剩余 < min(14 天, 寿命 × 25%)
//   - 红：剩余 < min(3 天, 寿命 × 10%)
//
// 颜色一律用面板时钟算（节点时钟可能偏）。
type ExpiryLevel string

const (
	ExpiryNone     ExpiryLevel = "none"
	ExpiryOK       ExpiryLevel = "ok"
	ExpiryWarning  ExpiryLevel = "warning"
	ExpiryCritical ExpiryLevel = "critical"
	ExpiryExpired  ExpiryLevel = "expired"
)

// expiryLevel 按寿命缩放的阈值算等级；notBefore / notAfter 为 nil 表示还没有证书。
func expiryLevel(notBefore, notAfter *time.Time, now time.Time) ExpiryLevel {
	if notBefore == nil || notAfter == nil {
		return ExpiryNone
	}
	remaining := notAfter.Sub(now)
	if remaining <= 0 {
		return ExpiryExpired
	}
	life := notAfter.Sub(*notBefore)
	if remaining < min(3*24*time.Hour, life/10) {
		return ExpiryCritical
	}
	if remaining < min(14*24*time.Hour, life/4) {
		return ExpiryWarning
	}
	return ExpiryOK
}

// alertRank 把等级映射成 certificates.alerted_level（0 无、1 黄、2 红；过期算红）。
func alertRank(l ExpiryLevel) int {
	switch l {
	case ExpiryWarning:
		return 1
	case ExpiryCritical, ExpiryExpired:
		return 2
	}
	return 0
}

// defaultRenewAfter 是没有 ARI 时的续期时间：剩余寿命 1/3 时续（90 天证书剩 30 天）。
func defaultRenewAfter(notBefore, notAfter time.Time) time.Time {
	life := notAfter.Sub(notBefore)
	return notAfter.Add(-life / 3)
}

// failureBackoff 是第 n 次连续失败后的退避：1h → 2h → 4h …，最长 24h。
func failureBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := time.Hour
	for i := 1; i < n && d < 24*time.Hour; i++ {
		d *= 2
	}
	return min(d, 24*time.Hour)
}
