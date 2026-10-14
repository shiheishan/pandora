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

// scanBatch 是每轮扫描最多写的通知件数。
const scanBatch = 1000

// paidLookback 是支付成功通知第一次（还没有游标时）回看的履约时间：通知上线前就履约的老订单不补发。
const paidLookback = 2 * time.Hour

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
	Plugin *scanPlugin  // 要同时发的插件事件，nil 表示不发
	Admin  *adminNotice // 同一事务里一起写的管理员群组提醒，nil 表示不写
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

// daysLeft 返回到期时刻还剩多少整天（按 24 小时向上取整，至少 1）。
func daysLeft(end, now time.Time) int {
	d := int(end.Sub(now).Hours() / 24)
	if end.Sub(now) > time.Duration(d)*24*time.Hour {
		d++
	}
	if d < 1 {
		return 1
	}
	return d
}

// daysSince 返回自某时刻起已过多少整天（按 24 小时向下取整，至少 1）。
func daysSince(end, now time.Time) int {
	d := int(now.Sub(end).Hours() / 24)
	if d < 1 {
		return 1
	}
	return d
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
					"days":       fmt.Sprint(daysLeft(c.PeriodEnd, now)),
					"expires_at": endAt,
				},
				Key: fmt.Sprintf("expiring:%s:%dd:%d", c.SubscriptionID, win.label, c.PeriodEnd.Unix()),
				Plugin: &scanPlugin{
					Event: "subscription.expiring",
					Payload: map[string]any{
						"event":           "subscription.expiring",
						"subscription_id": c.SubscriptionID.String(),
						"user_id":         c.UserID.String(),
						"expires_at":      endAt,
						"days_left":       win.label,
					},
				},
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
				vars["days"] = fmt.Sprint(daysSince(c.PeriodEnd, now))
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
			Plugin: &scanPlugin{
				Event: "traffic.exhausted",
				Payload: map[string]any{
					"event":           "traffic.exhausted",
					"subscription_id": q.SubscriptionID.String(),
					"user_id":         q.UserID.String(),
					"consumed_bytes":  q.Consumed,
					"total_bytes":     q.Total,
					"percent":         pct,
				},
			},
		})
	}

	if err := s.resolvePlanNames(ctx, items); err != nil {
		return nil, err
	}
	return items, nil
}

// resolvePlanNames 把 Vars["plan"] 里的套餐 id 换成套餐名（一次批量读）；解析不了或已下架的显示「套餐」。
func (s *Service) resolvePlanNames(ctx context.Context, items []scanItem) error {
	planIDs := make([]uuid.UUID, 0)
	seen := map[uuid.UUID]bool{}
	for _, it := range items {
		id, err := uuid.Parse(it.Vars["plan"])
		if err == nil && !seen[id] {
			seen[id] = true
			planIDs = append(planIDs, id)
		}
		if it.Admin != nil && it.Admin.Vars != nil {
			aid, err := uuid.Parse(it.Admin.Vars["plan"])
			if err == nil && !seen[aid] {
				seen[aid] = true
				planIDs = append(planIDs, aid)
			}
		}
	}
	plans, err := s.plans.PlansByIDs(ctx, planIDs)
	if err != nil {
		return err
	}
	for i := range items {
		id, err := uuid.Parse(items[i].Vars["plan"])
		if err != nil {
			items[i].Vars["plan"] = "套餐"
		} else {
			items[i].Vars["plan"] = planName(plans, id)
		}
		if items[i].Admin != nil && items[i].Admin.Vars != nil {
			aid, err := uuid.Parse(items[i].Admin.Vars["plan"])
			if err != nil {
				items[i].Admin.Vars["plan"] = "套餐"
			} else {
				items[i].Admin.Vars["plan"] = planName(plans, aid)
			}
		}
	}
	return nil
}

// Scan 先写支付成功（按游标，读失败只记日志），再扫到期、过期召回与流量预警，给尚未去重的用户写通知；返回新写件数。
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
	if s.orders == nil {
		return 0, errs.Wrap(errs.Internal, fmt.Errorf("notify: Orders 未装配"))
	}

	now := s.now()
	loc, err := scanSiteLocation(ctx, s)
	if err != nil {
		return 0, err
	}
	log := plog.From(ctx)

	// 支付成功是交易类通知：先写、单独计额度，不被别的积压挤掉，也不被别的读取失败拖住
	written, err := s.scanPaid(ctx, now, loc)
	if err != nil {
		if ctx.Err() != nil {
			return written, ctx.Err()
		}
		log.Warn("notify: 支付成功通知扫描失败，下一轮从游标处接着写", zap.Error(err))
	}

	items, err := s.collectScanItems(ctx, now, loc)
	if err != nil {
		return written, err
	}
	n, _, err := s.writeScanItems(ctx, items, 0)
	written += n
	if written > 0 {
		log.Info("notify: 扫描写入完成", zap.Int("count", written))
	}
	return written, err
}

// writeScanItems 按顺序给尚未去重的条目写通知，最多写 scanBatch 件（余下的下一轮接着写，已写的靠去重键跳过）：
// 首次上线或长时间停机后待发件可能很多，一轮全写会超任务时限，联系人查询的参数也会超过 PG 单条语句的上限。
// 返回新写件数，以及从头算起连续处理完（写成或已去重）的条目数，给按游标推进的调用方用。
func (s *Service) writeScanItems(ctx context.Context, items []scanItem, adminChatID int64) (written, done int, err error) {
	if len(items) == 0 {
		return 0, 0, nil
	}
	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.Key
	}
	existing, err := s.store.ExistingEventKeys(ctx, keys)
	if err != nil {
		return 0, 0, err
	}
	// 截到前 scanBatch 件待写：limit 是 items 里第一件放不下的下标
	limit, pending := len(items), 0
	userSet := map[uuid.UUID]bool{}
	for i, it := range items {
		if existing[it.Key] {
			continue
		}
		if pending == scanBatch {
			limit = i
			plog.From(ctx).Info("notify: 待发通知超过单轮上限，余下的下一轮写", zap.Int("batch", scanBatch))
			break
		}
		pending++
		userSet[it.UserID] = true
	}
	if pending == 0 {
		return 0, len(items), nil
	}

	env, err := s.loadNoticeEnv(ctx)
	if err != nil {
		return 0, 0, err
	}
	userIDs := make([]uuid.UUID, 0, len(userSet))
	for id := range userSet {
		userIDs = append(userIDs, id)
	}
	contacts, err := s.loadContacts(ctx, userIDs)
	if err != nil {
		return 0, 0, err
	}

	log := plog.From(ctx)
	done = -1
	// unfinished 是从头算起第一件没处理完的下标：之前有写失败的就是它，否则是 i
	unfinished := func(i int) int {
		if done >= 0 {
			return done
		}
		return i
	}
	for i, it := range items[:limit] {
		if err := ctx.Err(); err != nil {
			return written, unfinished(i), err
		}
		if existing[it.Key] {
			continue
		}
		c := contacts[it.UserID]
		err := s.tx.Do(ctx, func(ctx context.Context) error {
			ok, err := s.writeNotice(ctx, env, it.UserID, c, it.Code, it.Vars, it.Key)
			if err != nil {
				return err
			}
			if ok && adminChatID != 0 && it.Admin != nil {
				if _, err := s.writeAdminNotice(ctx, adminChatID, *it.Admin); err != nil {
					return err
				}
			}
			if err := s.emitScanPlugin(ctx, it); err != nil {
				return err
			}
			if ok {
				written++
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return written, unfinished(i), ctx.Err()
			}
			log.Warn("notify: 扫描写入失败", zap.String("key", it.Key), zap.Error(err))
			if done < 0 {
				done = i
			}
		}
	}
	if done < 0 {
		done = limit
	}
	return written, done, nil
}
