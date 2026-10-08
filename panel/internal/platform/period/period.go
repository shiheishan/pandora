// Package period 是计费周期推进的唯一实现，billing 与 subscription 共用。
//
// 从 billing/service.go 原样挪出：subscription 要算「续一期会到哪天」，又不能 import
// billing（billing 的 PG18 测试 import 了 subscription，反过来就成环）。
package period

import "time"

// AddInterval 按计费周期推进时间。
//
// 用 AddDate 而非固定天数：AddDate 处理月末与闰年的规则是
// 「1月31日 + 1月 = 3月3日（平年）」，这与多数支付平台一致。
// SUB-010 要求的月末/闰年测试即针对此行为。
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
		// 一次性商品没有周期，给一个远期哨兵值
		return from.AddDate(100, 0, 0)
	default:
		return from.AddDate(0, count, 0)
	}
}
