package service

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

// TrafficCharge 是一笔要记到订阅上的流量：UID 是 subscriptions.node_uid，Bytes 是已按节点倍率折算的计费字节（可以为 0）。
type TrafficCharge struct {
	UID   int64
	Bytes int64
}

// ChargeTraffic 在调用方的事务里（ctx 里有事务就加入，没有就自己开）把一批计费流量记到订阅上，返回 uid → 订阅 id（只含找得到订阅的 uid，
// 包括 Bytes 为 0 的）。
//
// 口径：先扣本期限量配额（剩余含本期送的量，含滚动空窗那一行），不够仍记在配额上；饱和加法；当日用量与配额同一事务。
// 切日与门户 MyUsage 同为 dayOf + siteLocation。
//
// 锁序：本方法不锁订阅行；配额行按 id → 按日用量按订阅 id。
// 同一批按 uid 升序决定怎么分，并发两批（同一组订阅、反序到达）加锁顺序相同，不会交叉死锁；
// 与别的路径（续费、重置先锁订阅再锁配额）若成环，由调用方遇 40P01 / 40001 整笔重试（node 的 IngestTraffic）。
// 不做审计（每分钟一批，留档在 node_traffic_reports）。
func (s *Service) ChargeTraffic(ctx context.Context, charges []TrafficCharge) (map[int64]uuid.UUID, error) {
	merged := make(map[int64]int64, len(charges))
	for _, c := range charges {
		if c.UID <= 0 {
			continue
		}
		merged[c.UID] = satAdd(merged[c.UID], max64(c.Bytes, 0))
	}
	if len(merged) == 0 {
		return map[int64]uuid.UUID{}, nil
	}
	uids := make([]int64, 0, len(merged))
	for uid := range merged {
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })

	var subs map[int64]uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var err error
		subs, err = s.repo.SubscriptionIDsByNodeUIDs(ctx, uids)
		if err != nil {
			return subErr(err)
		}
		items := make([]chargeItem, 0, len(uids))
		for _, uid := range uids {
			if subID, ok := subs[uid]; ok && merged[uid] > 0 {
				items = append(items, chargeItem{SubID: subID, Bytes: merged[uid]})
			}
		}
		if len(items) == 0 {
			return nil
		}

		// 先读站点时区再锁行，缩短持锁时间
		site, err := s.settings.Site(ctx)
		if err != nil {
			return err
		}
		now := s.clock()
		day := dayOf(now, siteLocation(site.Timezone)).Format(time.DateOnly)

		subIDs := make([]uuid.UUID, 0, len(items))
		for _, it := range items {
			subIDs = append(subIDs, it.SubID)
		}
		rows, err := s.repo.LockTrafficQuotas(ctx, sortedUniqueIDs(subIDs), now)
		if err != nil {
			return subErr(err)
		}
		plan := planTrafficCharges(items, rows)
		if err := s.repo.AddQuotaConsumed(ctx, plan.QuotaAdds); err != nil {
			return subErr(err)
		}
		if err := s.repo.AddUsageDaily(ctx, day, plan.Usage); err != nil {
			return subErr(err)
		}
		// 提交后通知，名单秒级摘掉用尽的人；一批只登记一个钩子（每次通知都会让全部会话重算名单）
		s.notifyRosterBatch(ctx, plan.Exhausted)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return subs, nil
}
