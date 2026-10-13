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
	PackTakes []repo.PackTake // 按流量包的锁顺序
	Usage     []repo.UsageAdd // 每份订阅这一批的计费字节（= item.Bytes），按 SubscriptionID 升序
	Exhausted []uuid.UUID     // 因这一批从「有余量」变成「耗尽」的订阅，按出现顺序
}

// splitTrafficCharge 与旧实现逐字同口径：先吃套餐剩余额度 planRoom（nil = 不限量），超出部分从流量包余额 packRemaining 扣，
// 两者都不够的那部分仍记在套餐上（配额变负、下一轮停止下发）。返回 记到套餐配额上的量、从流量包扣的量。
func splitTrafficCharge(billed int64, planRoom *int64, packRemaining int64) (int64, int64) {
	if billed <= 0 {
		return 0, 0
	}
	if planRoom == nil {
		return billed, 0
	}
	overflow := billed - max64(*planRoom, 0)
	if overflow <= 0 {
		return billed, 0
	}
	fromPacks := min64(overflow, max64(packRemaining, 0))
	return billed - fromPacks, fromPacks
}

// max64 与 min64 是 int64 的取大取小。
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
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

// needsPacks 判断这一笔要不要锁流量包：有限量（room != nil）且 bytes >= max(*room, 0)。
func needsPacks(room *int64, bytes int64) bool {
	if room == nil {
		return false
	}
	return bytes >= max64(*room, 0)
}

// planTrafficCharges 按 items 的顺序（调用方已按 uid 升序）决定每笔怎么分，口径同旧 applyTrafficCharges。
// items 中每份订阅只出现一次（uid 与订阅一一对应）；Exhausted 只是让名单秒级摘人，多发一次无害。
func planTrafficCharges(items []chargeItem, rows []repo.TrafficQuotaRow, packs []repo.OpenTrafficPack) chargePlan {
	rooms := quotaRooms(rows)
	// 包的剩余在内存里随扣随减；拷一份，不改调用方的切片
	packBySub := make(map[uuid.UUID][]*repo.OpenTrafficPack)
	for _, p := range packs {
		p := p
		packBySub[p.SubscriptionID] = append(packBySub[p.SubscriptionID], &p)
	}

	planChargeBySub := make(map[uuid.UUID]int64)
	packTakeAmt := make(map[uuid.UUID]int64)
	var packTakeOrder []uuid.UUID
	var usage []repo.UsageAdd
	var exhausted []uuid.UUID

	for _, item := range items {
		if item.Bytes <= 0 {
			continue
		}
		room := rooms[item.SubID]
		packRemaining := int64(0)
		for _, g := range packBySub[item.SubID] {
			packRemaining += g.Remaining
		}
		hadRoom := room != nil && (*room > 0 || packRemaining > 0)

		_, fromPacks := splitTrafficCharge(item.Bytes, room, packRemaining)
		left := fromPacks
		for _, g := range packBySub[item.SubID] {
			if left == 0 {
				break
			}
			take := min64(left, g.Remaining)
			if take == 0 {
				continue
			}
			if packTakeAmt[g.ID] == 0 {
				packTakeOrder = append(packTakeOrder, g.ID)
			}
			packTakeAmt[g.ID] += take
			g.Remaining -= take
			left -= take
		}

		planCharge := item.Bytes - fromPacks
		planChargeBySub[item.SubID] = satAdd(planChargeBySub[item.SubID], planCharge)

		var roomAfter int64
		if room != nil {
			roomAfter = *room - planCharge
			rooms[item.SubID] = &roomAfter
		}
		packAfter := packRemaining - fromPacks
		if hadRoom && room != nil && roomAfter <= 0 && packAfter == 0 {
			exhausted = append(exhausted, item.SubID)
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
	packTakes := make([]repo.PackTake, 0, len(packTakeOrder))
	for _, id := range packTakeOrder {
		packTakes = append(packTakes, repo.PackTake{ID: id, Amount: packTakeAmt[id]})
	}
	usage = sortUsageBySubID(usage)

	return chargePlan{
		QuotaAdds: quotaAdds,
		PackTakes: packTakes,
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
