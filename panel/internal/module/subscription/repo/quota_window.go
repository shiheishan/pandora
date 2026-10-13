package repo

import "time"

// TrafficQuotaGrace 是「周期已结束、还没滚动」的流量配额行仍算数的时长。
// 配额滚动每 10 分钟一次（被锁跳过时顺延），一天足够覆盖。
// 名单资格、拉取展示与扣量都按同一个窗口取「本期的配额行」，口径只在这里。
const TrafficQuotaGrace = 24 * time.Hour

// trafficQuotaWindowCond 是「本期流量配额行」的条件，别名 qb，两个参数依次为 now、now - TrafficQuotaGrace。
const trafficQuotaWindowCond = `qb.metric = 'traffic.bytes' AND qb.period_start <= ? AND (qb.period_end IS NULL OR qb.period_end > ?)`

// trafficQuotaWindow 返回 trafficQuotaWindowCond 的两个参数。
func trafficQuotaWindow(now time.Time) (time.Time, time.Time) {
	return now, now.Add(-TrafficQuotaGrace)
}
