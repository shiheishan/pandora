package service

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"time"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// TaskScan 是到期与流量预警的定时扫描任务（约每 5 分钟）。
const TaskScan = "notify:scan"

// ScanTimeout 是 notify:scan 的任务超时。
const ScanTimeout = 2 * time.Minute

// 到期提醒窗口：剩余天数落在 (lower, upper] 天。
var expiryWindows = []struct {
	label int
	lower int
	upper int
}{
	{label: 7, lower: 3, upper: 7},
	{label: 3, lower: 1, upper: 3},
	{label: 1, lower: 0, upper: 1},
}

// 过期后的通知：到期当时一条，过期第 1、7 天召回。
var expiredNotices = []struct {
	code  string
	label int
	lower time.Duration
	upper time.Duration
}{
	{code: TemplateSubscriptionExpired, label: 0, lower: 0, upper: 24 * time.Hour},
	{code: TemplateSubscriptionRecall, label: 1, lower: 24 * time.Hour, upper: 7 * 24 * time.Hour},
	{code: TemplateSubscriptionRecall, label: 7, lower: 7 * 24 * time.Hour, upper: 30 * 24 * time.Hour},
}

// 流量预警档位（%）。
var quotaThresholds = []int{80, 95}

type scanItem struct {
	UserID uuid.UUID
	Code   string
	Vars   map[string]string
	Key    string
}

// quotaTier 返回 consumed/total 已跨过的最高一档；都没跨或 total<=0 返回 0。
func quotaTier(consumed, total int64) int {
	if total <= 0 {
		return 0
	}
	best := 0
	for _, t := range quotaThresholds {
		if quotaAtOrAbove(consumed, total, t) && t > best {
			best = t
		}
	}
	return best
}

func quotaAtOrAbove(consumed, total int64, percent int) bool {
	if percent <= 0 || total <= 0 {
		return false
	}
	// 用量可能远超额度：两边任一乘 100 会溢出就走大数
	if total > math.MaxInt64/100 || consumed > math.MaxInt64/100 || consumed < -math.MaxInt64/100 {
		c := big.NewInt(consumed)
		c.Mul(c, big.NewInt(100))
		t := big.NewInt(total)
		t.Mul(t, big.NewInt(int64(percent)))
		return c.Cmp(t) >= 0
	}
	return consumed*100 >= total*int64(percent)
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// noticeTime 按站点时区把时刻格式化为 2006-01-02 15:04。
func noticeTime(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2006-01-02 15:04")
}

func scanSiteLocation(ctx context.Context, s *Service) (*time.Location, error) {
	site, err := s.settings.Site(ctx)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(site.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return loc, nil
}

func planName(plans map[uuid.UUID]catalogsvc.Plan, planID uuid.UUID) string {
	if p, ok := plans[planID]; ok && p.Name != "" {
		return p.Name
	}
	return "套餐"
}

func (s *Service) collectScanItems(ctx context.Context, now time.Time, loc *time.Location) ([]scanItem, error) {
	var items []scanItem

	for _, win := range expiryWindows {
		after := now.AddDate(0, 0, win.lower)
		until := now.AddDate(0, 0, win.upper)
		cands, err := s.subs.ExpiringForNotify(ctx, after, until)
		if err != nil {
			return nil, err
		}
		for _, c := range cands {
			endAt := noticeTime(c.PeriodEnd, loc)
			items = append(items, scanItem{
				UserID: c.UserID,
				Code:   TemplateSubscriptionExpiring,
				Vars: map[string]string{
					"plan":       c.PlanID.String(),
					"days":       fmt.Sprint(win.label),
					"expires_at": endAt,
				},
				Key: fmt.Sprintf("expiring:%s:%dd:%d", c.SubscriptionID, win.label, c.PeriodEnd.Unix()),
			})
		}
	}

	for _, win := range expiredNotices {
		endedAfter := now.Add(-win.upper)
		endedUntil := now.Add(-win.lower)
		onlyLive := win.label > 0
		cands, err := s.subs.ExpiredForNotify(ctx, endedAfter, endedUntil, onlyLive)
		if err != nil {
			return nil, err
		}
		for _, c := range cands {
			expAt := noticeTime(c.PeriodEnd, loc)
			vars := map[string]string{
				"plan":       c.PlanID.String(),
				"expired_at": expAt,
			}
			key := fmt.Sprintf("expired:%s:%d", c.SubscriptionID, c.PeriodEnd.Unix())
			if win.label > 0 {
				vars["days"] = fmt.Sprint(win.label)
				key = fmt.Sprintf("recall:%s:%dd:%d", c.SubscriptionID, win.label, c.PeriodEnd.Unix())
			}
			items = append(items, scanItem{
				UserID: c.UserID,
				Code:   win.code,
				Vars:   vars,
				Key:    key,
			})
		}
	}

	quotaRows, err := s.subs.QuotaUsageForNotify(ctx, quotaThresholds[0])
	if err != nil {
		return nil, err
	}
	for _, q := range quotaRows {
		pct := quotaTier(q.Consumed, q.Total)
		if pct == 0 {
			continue
		}
		remain := q.Total - q.Consumed
		if remain < 0 {
			remain = 0
		}
		items = append(items, scanItem{
			UserID: q.UserID,
			Code:   TemplateQuotaWarning,
			Vars: map[string]string{
				"plan":      q.PlanID.String(),
				"percent":   fmt.Sprint(pct),
				"remaining": humanBytes(remain),
			},
			Key: fmt.Sprintf("quota:%s:%d:%d", q.SubscriptionID, pct, q.PeriodStart.Unix()),
		})
	}

	planIDs := make([]uuid.UUID, 0)
	seen := map[uuid.UUID]bool{}
	for _, it := range items {
		id, err := uuid.Parse(it.Vars["plan"])
		if err != nil {
			continue
		}
		if !seen[id] {
			seen[id] = true
			planIDs = append(planIDs, id)
		}
	}
	plans, err := s.plans.PlansByIDs(ctx, planIDs)
	if err != nil {
		return nil, err
	}
	for i := range items {
		id, err := uuid.Parse(items[i].Vars["plan"])
		if err != nil {
			items[i].Vars["plan"] = "套餐"
			continue
		}
		items[i].Vars["plan"] = planName(plans, id)
	}
	return items, nil
}

// Scan 扫到期、过期召回与流量预警，给尚未去重的用户写通知；返回新写件数。
func (s *Service) Scan(ctx context.Context) (int, error) {
	if s.subs == nil {
		return 0, errs.Wrap(errs.Internal, fmt.Errorf("notify: Subscriptions 未装配"))
	}
	if s.plans == nil {
		return 0, errs.Wrap(errs.Internal, fmt.Errorf("notify: Plans 未装配"))
	}
	if s.dir == nil {
		return 0, errs.Wrap(errs.Internal, fmt.Errorf("notify: Directory 未装配"))
	}

	now := s.now()
	loc, err := scanSiteLocation(ctx, s)
	if err != nil {
		return 0, err
	}

	items, err := s.collectScanItems(ctx, now, loc)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}

	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.Key
	}
	existing, err := s.store.ExistingEventKeys(ctx, keys)
	if err != nil {
		return 0, err
	}
	var pending []scanItem
	for _, it := range items {
		if !existing[it.Key] {
			pending = append(pending, it)
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}

	env, err := s.loadNoticeEnv(ctx)
	if err != nil {
		return 0, err
	}

	userSet := map[uuid.UUID]bool{}
	for _, it := range pending {
		userSet[it.UserID] = true
	}
	userIDs := make([]uuid.UUID, 0, len(userSet))
	for id := range userSet {
		userIDs = append(userIDs, id)
	}
	contacts, err := s.loadContacts(ctx, userIDs)
	if err != nil {
		return 0, err
	}

	log := plog.From(ctx)
	written := 0
	for _, it := range pending {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		c := contacts[it.UserID]
		err := s.tx.Do(ctx, func(ctx context.Context) error {
			ok, err := s.writeNotice(ctx, env, it.UserID, c, it.Code, it.Vars, it.Key)
			if err != nil {
				return err
			}
			if ok {
				written++
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return written, ctx.Err()
			}
			log.Warn("notify: 扫描写入失败", zap.String("key", it.Key), zap.Error(err))
		}
	}
	if written > 0 {
		log.Info("notify: 扫描写入完成", zap.Int("count", written))
	}
	return written, nil
}
