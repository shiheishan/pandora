package service

// 配额周期滚动（subscription:roll_quota，每 10 分钟）：把周期已过的配额行滚到下一期，已用量清零、写流量重置日志。
//
//	day / month  自然周期，独立于订阅周期（年付套餐按月给流量、月付套餐按日限量）；新周期从旧周期末起。
//	cycle        跟订阅的计费周期：提前续费不动本期 cycle 行（本期剩余照常用到原到期日），到点由这里滚进新周期，
//	             上限按套餐当前版本写（救回折算加进来的只属于上一期），日志记 renewal；滚进新周期时本期送的量（bonus_value）清零，
//	             订阅套餐在 cycle 行滚动时把 plan_version_id 切到套餐当前版本（一次性套餐是 total 行不滚动、不受影响）。
//	过长的旧行   周期末被错写成订阅周期末的 day / month 行，从 period_start 按自然周期数到最近一个已过的边界，一步追上。
//	落后多期     任务停过时也一步追上、只写一条重置日志，不逐期补发。
//
// 已过期或走过到期日的订阅不滚：那种情况该停服，不是发新一轮流量。分批、每批一个短事务；锁按 id、SKIP LOCKED，
// 与节点扣量同序，正在被扣量锁着的行这批跳过。清零前的用量取 UPDATE 的 RETURNING OLD.consumed（含刚提交的并发扣量）。
// 第一遍之后还有到期的行（被锁跳过）就隔 rollQuotaRetryDelay 再补一遍，仍锁着的留给下一轮。
// 周期起止只在 Go 里算（period.go），SQL 只筛到期行、加锁、批量写。滚动清零会让超额用户重新可用，提交后通知名单。

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// 配额滚动的分批参数：每批一个短事务、至多 rollQuotaBatch 行，一遍至多 rollQuotaMaxBatches 批；被锁跳过的隔 rollQuotaRetryDelay 补一遍。
const (
	rollQuotaBatch      = 500
	rollQuotaMaxBatches = 100
	rollQuotaRetryDelay = 500 * time.Millisecond
)

// RollStats 是一轮配额滚动的结果。
type RollStats struct {
	// Rolled 是滚动的 day / month 行数，Cycle 是滚动的 cycle 行数
	Rolled int
	Cycle  int
	// Switched 是滚进新周期时切到套餐当前版本的订阅数
	Switched int
	// Passes 是跑了几遍（1 或 2）
	Passes int
}

// RollQuotas 把周期已过的配额滚到下一期（见文件头）。
func (s *Service) RollQuotas(ctx context.Context) (RollStats, error) {
	var st RollStats
	if err := s.rollPass(ctx, &st); err != nil {
		return st, err
	}
	remain, err := s.repo.QuotaRollsRemain(ctx, s.clock())
	if err != nil {
		return st, subErr(err)
	}
	if !remain {
		return st, nil
	}
	select {
	case <-ctx.Done():
		return st, ctx.Err()
	case <-time.After(rollQuotaRetryDelay):
	}
	return st, s.rollPass(ctx, &st)
}

// rollPass 滚一遍：分批，直到一批不满、一批一行没滚（全被跳过，防空转）或批数用完。
func (s *Service) rollPass(ctx context.Context, st *RollStats) error {
	st.Passes++
	for b := 0; b < rollQuotaMaxBatches; b++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		selected, applied, err := s.rollBatch(ctx, st)
		if err != nil {
			return fmt.Errorf("配额滚动: %w", err)
		}
		if selected < rollQuotaBatch || applied == 0 {
			return nil
		}
	}
	return nil
}

// rollBatch 在一个事务里锁一批到期行、算新周期、批量写回并记重置日志；统计只在提交成功后累加。
func (s *Service) rollBatch(ctx context.Context, st *RollStats) (selected, applied int, err error) {
	var dayMonth, cycle, switched int
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		dayMonth, cycle, switched = 0, 0, 0
		now := s.clock()
		cands, err := s.repo.LockDueQuotaRolls(ctx, now, rollQuotaBatch)
		if err != nil {
			return subErr(err)
		}
		selected = len(cands)
		if selected == 0 {
			return nil
		}
		versions, targets, err := s.rollTargets(ctx, cands)
		if err != nil {
			return err
		}
		rolls := make([]repo.QuotaRoll, 0, len(cands))
		byID := make(map[uuid.UUID]repo.QuotaRollCandidate, len(cands))
		for _, c := range cands {
			roll, ok := planRoll(c, now, versions, targets)
			if !ok {
				continue
			}
			rolls = append(rolls, roll)
			byID[c.ID] = c
		}
		rolled, err := s.repo.ApplyQuotaRolls(ctx, rolls, now)
		if err != nil {
			return subErr(err)
		}
		applied = len(rolled)
		logs := make([]model.ResetLog, 0, len(rolled))
		subIDs := make([]uuid.UUID, 0, len(rolled))
		cycleSwitch := map[uuid.UUID]repo.QuotaRollCandidate{}
		for _, r := range rolled {
			c := byID[r.ID]
			reason := ResetCycleRoll
			if c.Period == PeriodCycle {
				reason = ResetRenewal
				cycle++
				tv := targets[c.SubscriptionID]
				if tv != uuid.Nil && tv != c.PlanVersionID {
					if _, ok := cycleSwitch[c.SubscriptionID]; !ok {
						cycleSwitch[c.SubscriptionID] = c
					}
				}
			} else {
				dayMonth++
			}
			logs = append(logs, model.ResetLog{
				SubscriptionID: r.SubscriptionID,
				UserID:         c.UserID,
				Metric:         r.Metric,
				Reason:         reason,
				ConsumedBefore: r.ConsumedBefore,
				ConsumedAfter:  0,
			})
			subIDs = append(subIDs, r.SubscriptionID)
		}
		if err := s.repo.InsertResetLogs(ctx, logs); err != nil {
			return subErr(err)
		}
		sw := make([]repo.VersionSwitch, 0, len(cycleSwitch))
		for subID, c := range cycleSwitch {
			sw = append(sw, repo.VersionSwitch{
				SubscriptionID: subID,
				From:           c.PlanVersionID,
				To:             targets[subID],
			})
		}
		switchedIDs, err := s.repo.SwitchPlanVersions(ctx, sw, now)
		if err != nil {
			return subErr(err)
		}
		switched = len(switchedIDs)
		for _, id := range switchedIDs {
			c := cycleSwitch[id]
			s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, id, c.UserID)
		}
		// notifyRoster 按订阅去重（AfterCommit 的键），同一份的几行只通知一次
		s.notifyRoster(ctx, subIDs...)
		return nil
	})
	if err == nil {
		st.Rolled += dayMonth
		st.Cycle += cycle
		st.Switched += switched
	}
	return selected, applied, err
}

// rollTargets 读 cycle 行滚动要用的目标版本与版本定义；没有 cycle 行不读 catalog。
func (s *Service) rollTargets(ctx context.Context, cands []repo.QuotaRollCandidate) (map[uuid.UUID]catalogsvc.Version, map[uuid.UUID]uuid.UUID, error) {
	targets := make(map[uuid.UUID]uuid.UUID)
	planSeen := map[uuid.UUID]bool{}
	var planIDs []uuid.UUID
	hasCycle := false
	for _, c := range cands {
		if c.Period != PeriodCycle {
			continue
		}
		hasCycle = true
		targets[c.SubscriptionID] = c.PlanVersionID
		if !planSeen[c.PlanID] {
			planSeen[c.PlanID] = true
			planIDs = append(planIDs, c.PlanID)
		}
	}
	if !hasCycle {
		return map[uuid.UUID]catalogsvc.Version{}, map[uuid.UUID]uuid.UUID{}, nil
	}
	plans, err := s.catalog.PlansByIDs(ctx, planIDs)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range cands {
		if c.Period != PeriodCycle {
			continue
		}
		p, ok := plans[c.PlanID]
		if !ok || p.Kind != catalogsvc.KindSubscription {
			continue
		}
		if p.CurrentVersionID != nil {
			targets[c.SubscriptionID] = *p.CurrentVersionID
		}
	}
	verSeen := map[uuid.UUID]bool{}
	var versionIDs []uuid.UUID
	for _, vID := range targets {
		if !verSeen[vID] {
			verSeen[vID] = true
			versionIDs = append(versionIDs, vID)
		}
	}
	versions, err := s.catalog.VersionsByIDs(ctx, versionIDs)
	if err != nil {
		return nil, nil, err
	}
	out := make(map[uuid.UUID]catalogsvc.Version, len(versions))
	for id, v := range versions {
		p, ok := plans[v.PlanID]
		if !ok {
			out[id] = v
			continue
		}
		out[id] = withKind(v, p.Kind)
	}
	return out, targets, nil
}

// planRoll 算一条到期行的新周期；不该滚（没走过任何自然边界、未知周期）返回 false。
func planRoll(c repo.QuotaRollCandidate, now time.Time, versions map[uuid.UUID]catalogsvc.Version, targets map[uuid.UUID]uuid.UUID) (repo.QuotaRoll, bool) {
	switch c.Period {
	case PeriodDay, PeriodMonth:
		start, ok := naturalRollStart(c.Period, c.PeriodStart, c.PeriodEnd, now)
		if !ok {
			return repo.QuotaRoll{}, false
		}
		end := QuotaPeriodEnd(c.Period, start, time.Time{})
		return repo.QuotaRoll{ID: c.ID, PeriodStart: start, PeriodEnd: *end}, true
	case PeriodCycle:
		start, end := cycleRollWindow(c.PeriodEnd, c.SubPeriodEnd, now, c.SnapshotInterval, c.SnapshotIntervalCount)
		roll := repo.QuotaRoll{ID: c.ID, PeriodStart: start, PeriodEnd: end}
		targetVID := targets[c.SubscriptionID]
		if targetVID == uuid.Nil {
			targetVID = c.PlanVersionID
		}
		// 版本里没有这一行的定义（读不到版本、或一次性套餐遗留的 cycle 行）就不动上限
		if v, ok := versions[targetVID]; ok {
			if def, ok := quotaDef(v, c.Metric, PeriodCycle); ok {
				roll.SetLimit, roll.Limit = true, def.Limit
			}
		}
		return roll, true
	}
	return repo.QuotaRoll{}, false
}

// overlongPeriod 判断 day / month 行的周期末是否被错写得过长（日多出 1 小时以上、月多出 4 天以上，容差吸收月末差异）。
func overlongPeriod(period string, start, end time.Time) bool {
	start, end = start.UTC(), end.UTC()
	switch period {
	case PeriodDay:
		return end.After(start.Add(25 * time.Hour))
	case PeriodMonth:
		return end.After(start.AddDate(0, 1, 4))
	}
	return false
}

// naturalRollStart 算 day / month 行的新周期起点：从锚点（正常行是旧 period_end，过长的旧行是 period_start）
// 按自然周期数到最近一个不晚于 now 的边界。没有走过任何边界（不该滚）返回 false。
func naturalRollStart(period string, start, end, now time.Time) (time.Time, bool) {
	start, end, now = start.UTC(), end.UTC(), now.UTC()
	overlong := overlongPeriod(period, start, end)
	anchor := end
	if overlong {
		anchor = start
	}
	if anchor.After(now) {
		return time.Time{}, false
	}
	var k int
	var step func(k int) time.Time
	switch period {
	case PeriodDay:
		// 每次都从锚点加 k，不逐期累加，免得月末漂移
		step = func(k int) time.Time { return anchor.AddDate(0, 0, k) }
		k = int(now.Sub(anchor) / (24 * time.Hour))
	case PeriodMonth:
		step = func(k int) time.Time { return anchor.AddDate(0, k, 0) }
		k = (now.Year()-anchor.Year())*12 + int(now.Month()) - int(anchor.Month())
	default:
		return time.Time{}, false
	}
	for k > 0 && step(k).After(now) {
		k--
	}
	if overlong && k == 0 {
		return time.Time{}, false
	}
	return step(k), true
}

// cycleRollWindow 算 cycle 行的新周期：起点是旧 period_end，终点按订阅的计费周期（NextCycleEnd，零头并进最后一期）；
// 新周期仍不晚于 now（任务停过）就接着往后推，直到越过 now 或到订阅到期。
func cycleRollWindow(oldEnd, subEnd, now time.Time, interval string, count int) (start, end time.Time) {
	subEnd = subEnd.UTC()
	start = oldEnd.UTC()
	end = NextCycleEnd(start, subEnd, interval, count)
	for !end.After(now) && end.Before(subEnd) {
		start = end
		end = NextCycleEnd(start, subEnd, interval, count)
	}
	return start, end
}
