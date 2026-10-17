package service

import (
	"bytes"
	"math"
	"sort"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/repo"
)

// maxInt64 用于饱和加法。
const maxInt64 = math.MaxInt64

// satAdd 返回 a+b，超过 math.MaxInt64 时封顶（a、b 都 >= 0）。
func satAdd(a, b int64) int64 {
	if a > maxInt64-b {
		return maxInt64
	}
	return a + b
}

// chargeItem 是一份订阅这一批的计费字节（> 0）。
type chargeItem struct {
	SubID uuid.UUID
	Bytes int64
}

// chargePlan 是一批扣量在内存里算好的写回。
type chargePlan struct {
	QuotaAdds []repo.QuotaAdd // 按配额行 id 升序（与锁的顺序相同）
	Usage     []repo.UsageAdd // 每份订阅这一批的计费字节（= item.Bytes），按 SubscriptionID 升序
	Exhausted []uuid.UUID     // 因这一批从「有余量」变成「耗尽」的订阅，按出现顺序
}

// max64 是 int64 的取大。
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// quotaRooms 按订阅取剩余额度：有配额行的订阅都在 map 里；值为该订阅所有限量行里最小的 Room，没有限量行为 nil（不限量）。
func quotaRooms(rows []repo.TrafficQuotaRow) map[uuid.UUID]*int64 {
	out := make(map[uuid.UUID]*int64)
	for _, row := range rows {
		cur, seen := out[row.SubscriptionID]
		if !seen {
			out[row.SubscriptionID] = nil
		}
		if row.Room == nil {
			continue
		}
		if cur == nil || *row.Room < *cur {
			v := *row.Room
			out[row.SubscriptionID] = &v
		}
	}
	return out
}

// planTrafficCharges 按 items 的顺序（调用方已按 uid 升序）决定每笔怎么分。
// items 中每份订阅只出现一次（uid 与订阅一一对应）；Exhausted 只是让名单秒级摘人，多发一次无害。
func planTrafficCharges(items []chargeItem, rows []repo.TrafficQuotaRow) chargePlan {
	rooms := quotaRooms(rows)

	planChargeBySub := make(map[uuid.UUID]int64)
	var usage []repo.UsageAdd
	var exhausted []uuid.UUID

	for _, item := range items {
		if item.Bytes <= 0 {
			continue
		}
		room := rooms[item.SubID]
		hadRoom := room != nil && *room > 0

		planCharge := item.Bytes
		planChargeBySub[item.SubID] = satAdd(planChargeBySub[item.SubID], planCharge)

		if room != nil {
			roomAfter := *room - planCharge
			rooms[item.SubID] = &roomAfter
			if hadRoom && roomAfter <= 0 {
				exhausted = append(exhausted, item.SubID)
			}
		}
		usage = append(usage, repo.UsageAdd{SubscriptionID: item.SubID, Bytes: item.Bytes})
	}

	var quotaAdds []repo.QuotaAdd
	for _, row := range rows {
		amt := planChargeBySub[row.SubscriptionID]
		if amt > 0 {
			quotaAdds = append(quotaAdds, repo.QuotaAdd{ID: row.ID, Amount: amt})
		}
	}
	usage = sortUsageBySubID(usage)

	return chargePlan{
		QuotaAdds: quotaAdds,
		Usage:     usage,
		Exhausted: exhausted,
	}
}

// sortUsageBySubID 按订阅 id 字节序排序（按日用量 upsert 的加锁顺序）。
func sortUsageBySubID(in []repo.UsageAdd) []repo.UsageAdd {
	sort.Slice(in, func(i, j int) bool { return bytes.Compare(in[i].SubscriptionID[:], in[j].SubscriptionID[:]) < 0 })
	return in
}

// sortedUniqueIDs 去重并按 uuid 字节序排序（锁行的顺序）。
func sortedUniqueIDs(in []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(in))
	out := make([]uuid.UUID, 0, len(in))
	for _, id := range in {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}
