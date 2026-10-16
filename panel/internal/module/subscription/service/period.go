package service

import (
	"math/big"
	"time"
)

// 周期计算只在 Go 里做，不进 SQL。

// ValidInterval 判断计费周期是否合法。
func ValidInterval(interval string) bool {
	switch interval {
	case "day", "week", "month", "quarter", "year", "one_time":
		return true
	}
	return false
}

// AddInterval 按计费周期推进时间。用 AddDate：1 月 31 日 + 1 月 = 3 月 3 日（平年），与多数支付平台一致。
// count<=0 按 1；one_time 加 100 年作远期哨兵；未知周期按月。
func AddInterval(from time.Time, interval string, count int) time.Time {
	if count <= 0 {
		count = 1
	}
	switch interval {
	case "day":
		return from.AddDate(0, 0, count)
	case "week":
		return from.AddDate(0, 0, 7*count)
	case "month":
		return from.AddDate(0, count, 0)
	case "quarter":
		return from.AddDate(0, 3*count, 0)
	case "year":
		return from.AddDate(count, 0, 0)
	case "one_time":
		return from.AddDate(100, 0, 0)
	default:
		return from.AddDate(0, count, 0)
	}
}

// permanentHorizon 是判断「永久」的门槛：期末离开通超过 50 年就当作远期哨兵（AddInterval 的 one_time 加 100 年）。
const permanentHorizon = 50

// IsPermanent 判断期末是不是一次性套餐「永久」的远期哨兵（晚于现在 + 50 年；哨兵是开通时 + 100 年，
// 留出的余量让判断与时钟无关）。订阅上不另存「永久」，显示与叠加都靠它判断。
func IsPermanent(end time.Time) bool {
	return end.After(time.Now().AddDate(permanentHorizon, 0, 0))
}

// PeriodEndFor 是从 start 开一整期的期末：一次性价有天数时 start + N 天，没有天数（永久）时落远期哨兵；
// 订阅价按计费周期推进。结果截到微秒（与库里 timestamptz 同精度）。
func PeriodEndFor(start time.Time, p PriceSnapshot) time.Time {
	if p.Interval == "one_time" && p.ValidityDays != nil && *p.ValidityDays > 0 {
		return start.AddDate(0, 0, *p.ValidityDays).Truncate(time.Microsecond)
	}
	return AddInterval(start, p.Interval, p.IntervalCount).Truncate(time.Microsecond)
}

// StackEnd 是一次性套餐叠加后的期末：原来已永久或新价永久 → 永久哨兵（从 now 起）；
// 否则 max(原期末, now) + N 天。
func StackEnd(oldEnd, now time.Time, p PriceSnapshot) time.Time {
	if IsPermanent(oldEnd) || p.ValidityDays == nil || *p.ValidityDays <= 0 {
		if IsPermanent(oldEnd) {
			return oldEnd
		}
		return AddInterval(now, "one_time", 1).Truncate(time.Microsecond)
	}
	base := oldEnd
	if now.After(base) {
		base = now
	}
	return base.AddDate(0, 0, *p.ValidityDays).Truncate(time.Microsecond)
}

// CycleLength 是从 from 起一个计费周期的长度。
func CycleLength(from time.Time, interval string, count int) time.Duration {
	return AddInterval(from, interval, count).Sub(from)
}

// QuotaPeriodEnd 算配额行的周期结束：total 无结束（nil），day、month 按自然长度，cycle 跟订阅的计费周期。
func QuotaPeriodEnd(period string, start, cycleEnd time.Time) *time.Time {
	var end time.Time
	switch period {
	case PeriodTotal:
		return nil
	case PeriodDay:
		end = start.AddDate(0, 0, 1)
	case PeriodMonth:
		end = start.AddDate(0, 1, 0)
	default:
		end = cycleEnd
	}
	return &end
}

// RenewalBase 算续费从哪里接：提前续费接旧到期；到期前下单、回调晚到也接旧到期；否则从付款时刻重开（restart）。
func RenewalBase(oldEnd, paidAt time.Time, orderCreatedAt *time.Time) (base time.Time, restart bool) {
	if oldEnd.After(paidAt) {
		return oldEnd, false
	}
	if orderCreatedAt != nil && orderCreatedAt.Before(oldEnd) {
		return oldEnd, false
	}
	return paidAt, true
}

// ExtendBase 算延期基准：没过期接旧到期；已过期（lapsed）从 now 起。
func ExtendBase(oldEnd, now time.Time) (base time.Time, lapsed bool) {
	lapsed = !oldEnd.After(now)
	if lapsed {
		return now, true
	}
	return oldEnd, false
}

// ProratedQuota 按天数折算一个周期的额度：limit × days×24h ÷ cycle，向下取整，整数运算不经浮点；
// 参数非正或结果超出 int64 返回 0。
func ProratedQuota(limit int64, days int, cycle time.Duration) int64 {
	if limit <= 0 || days <= 0 || cycle <= 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(limit), new(big.Int).Mul(big.NewInt(int64(days)), big.NewInt(int64(24*time.Hour))))
	n.Quo(n, big.NewInt(int64(cycle)))
	if !n.IsInt64() {
		return 0
	}
	return n.Int64()
}

// NextCycleEnd 算 cycle 配额下一期的结束：再推一期会越过订阅到期时，把零头并进这一期、直接到订阅到期。
func NextCycleEnd(start, subEnd time.Time, interval string, count int) time.Time {
	next := AddInterval(start, interval, count)
	if AddInterval(next, interval, count).After(subEnd) {
		return subEnd
	}
	return next
}

// RenewUntil 算门户「续一期到哪天」：没到期从到期接，否则从 now 起。
func RenewUntil(periodEnd, now time.Time, interval string, count int) time.Time {
	base := now
	if periodEnd.After(now) {
		base = periodEnd
	}
	return AddInterval(base.UTC(), interval, count).Truncate(time.Microsecond)
}
