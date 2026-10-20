package service

// 数据巡检（report:integrity，每 30 分钟）：逐项跑库里约束拦不住、出现就说明有 bug 或半截状态的检查，
// 结果存进本模块的 integrity_checks，后台总览待办读它，变坏时经 notify 往管理员群组发告警。
//
// 检查本身是各属主 service 的只读方法（SQL 在属主 repo），report 不读别人的表；两项跨模块的
// （收款与订单、佣金与订单）由属主各给出「记录 → 订单」那一半，这里再经 order 的 UnsettledAmong 核对订单状态。
// 每项检查的口径、怎么确认、怎么修见 panel/deploy/RUNBOOK.md「数据巡检告警」。
//
// 告警规则（每项单独一个短事务：锁行 → 决定 → 发告警 → 写行，告警的去重键与投递随同一事务提交）：
//   - 违例数 > 0 且大于这一段上次告警时的数（alerted_count）就发一条：从 0 变成非 0 发，变多再发，数不变或变少不发；
//   - 回到 0 不发，清掉 failing_since 与 alerted_count，下次再出现是新的一段；
//   - 没配管理员群组（notify 回 false）不发、也不记 alerted_count，配好后下一轮照发。
// 某项检查失败（超时、库错）只记日志并跳过这一项（表里保留上一次的结果），其余照跑，最后把错误合起来返回。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/module/report/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/integrity"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// 检查代号：integrity_checks.code、告警去重键、运维手册小节与前端文案都用它，改名等于换一项新检查。
const (
	CheckLedgerDrift           = "ledger_drift"
	CheckLedgerHolds           = "ledger_hold_mismatch"
	CheckOrderPaidUnfulfilled  = "order_paid_unfulfilled"
	CheckOrderReservations     = "order_reservation_mismatch"
	CheckPaymentUnsettled      = "payment_unsettled"
	CheckCommissionUnpaidOrder = "commission_unpaid_order"
	CheckSubscriptionExpire    = "subscription_expire_overdue"
	CheckSubscriptionClose     = "subscription_close_overdue"
	CheckGiftcardRedemptions   = "giftcard_redemption_mismatch"
)

// IntegrityWindow 是两项跨模块检查（收款、佣金条目 → 订单）回看的时长：只核对这段时间里新产生的记录，
// 让每轮要核对的订单 id 有界。一条违例在窗口里至少被巡检 300 多轮，从 0 变成非 0 时一定告警；滑出窗口后计数会自己回落。
const IntegrityWindow = 7 * 24 * time.Hour

// unsettledChunk 是一次交给 order 核对的订单 id 上限（每批一条按主键的 SQL）。
const unsettledChunk = 5000

// integrityCheck 是一项检查：代号、中文名（告警里用）与怎么跑。
type integrityCheck struct {
	Code string
	Name string
	Run  func(ctx context.Context, now time.Time) (integrity.Result, error)
}

// integrityChecks 是全部检查，按后台显示顺序排（钱相关的在前）。
func (s *Service) integrityChecks() []integrityCheck {
	d := s.deps
	return []integrityCheck{
		{CheckLedgerDrift, "账本漂移", func(ctx context.Context, _ time.Time) (integrity.Result, error) {
			return d.Ledger.CheckDrift(ctx)
		}},
		{CheckLedgerHolds, "余额冻结对不上", func(ctx context.Context, _ time.Time) (integrity.Result, error) {
			return d.Ledger.CheckHolds(ctx)
		}},
		{CheckPaymentUnsettled, "收款了订单却没付", func(ctx context.Context, now time.Time) (integrity.Result, error) {
			refs, err := d.Payments.SettledPaymentsSince(ctx, now.Add(-IntegrityWindow))
			if err != nil {
				return integrity.Result{}, err
			}
			return s.unsettledRefs(ctx, refs)
		}},
		{CheckOrderPaidUnfulfilled, "已付订单没落地", func(ctx context.Context, _ time.Time) (integrity.Result, error) {
			return d.Orders.CheckPaidUnfulfilled(ctx)
		}},
		{CheckOrderReservations, "订单预留没释放", func(ctx context.Context, _ time.Time) (integrity.Result, error) {
			return d.Orders.CheckReservations(ctx)
		}},
		{CheckCommissionUnpaidOrder, "佣金挂在没付的订单上", func(ctx context.Context, now time.Time) (integrity.Result, error) {
			refs, err := d.Commissions.EntriesSince(ctx, now.Add(-IntegrityWindow))
			if err != nil {
				return integrity.Result{}, err
			}
			return s.unsettledRefs(ctx, refs)
		}},
		{CheckGiftcardRedemptions, "礼品卡兑换对不上", func(ctx context.Context, _ time.Time) (integrity.Result, error) {
			return d.Giftcards.CheckRedemptions(ctx)
		}},
		{CheckSubscriptionExpire, "订阅到期没停", func(ctx context.Context, now time.Time) (integrity.Result, error) {
			return d.Subscriptions.CheckExpireOverdue(ctx, now)
		}},
		{CheckSubscriptionClose, "过期订阅没关续费窗口", func(ctx context.Context, now time.Time) (integrity.Result, error) {
			return d.Subscriptions.CheckCloseOverdue(ctx, now)
		}},
	}
}

// unsettledRefs 把「挂在订单上的记录」交给 order 核对：订单不是 paid / fulfilled 的那些记录算违例，样例是记录 id（新的在前）。
func (s *Service) unsettledRefs(ctx context.Context, refs []integrity.OrderRef) (integrity.Result, error) {
	seen := make(map[uuid.UUID]bool, len(refs))
	ids := make([]uuid.UUID, 0, len(refs))
	for _, r := range refs {
		if !seen[r.OrderID] {
			seen[r.OrderID] = true
			ids = append(ids, r.OrderID)
		}
	}
	bad := make(map[uuid.UUID]bool)
	for start := 0; start < len(ids); start += unsettledChunk {
		got, err := s.deps.Orders.UnsettledAmong(ctx, ids[start:min(start+unsettledChunk, len(ids))])
		if err != nil {
			return integrity.Result{}, err
		}
		for _, id := range got {
			bad[id] = true
		}
	}
	var hit []uuid.UUID
	for _, r := range refs {
		if bad[r.OrderID] {
			hit = append(hit, r.ID)
		}
	}
	// uuidv7 的文本序即时间序：倒序取新的在前
	slices.SortFunc(hit, func(a, b uuid.UUID) int { return bytes.Compare(b[:], a[:]) })
	out := integrity.Result{Count: int64(len(hit)), Sample: make([]string, 0, integrity.SampleLimit)}
	for _, id := range hit[:min(len(hit), integrity.SampleLimit)] {
		out.Sample = append(out.Sample, id.String())
	}
	return out, nil
}

// RunIntegrity 跑一轮数据巡检（report:integrity 调）：逐项检查、写 integrity_checks、按规则告警（见文件头）。
func (s *Service) RunIntegrity(ctx context.Context) error {
	now := s.now()
	lg := plog.From(ctx)
	var failed []error
	for _, c := range s.integrityChecks() {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failed, err)...)
		}
		res, err := c.Run(ctx, now)
		if err != nil {
			lg.Warn("数据巡检：检查失败，保留上一次的结果", zap.String("check", c.Code), zap.Error(err))
			failed = append(failed, fmt.Errorf("%s: %w", c.Code, err))
			continue
		}
		if err := s.tx.Do(ctx, func(ctx context.Context) error { return s.recordIntegrity(ctx, c, res, now) }); err != nil {
			lg.Warn("数据巡检：写结果或告警失败", zap.String("check", c.Code), zap.Error(err))
			failed = append(failed, fmt.Errorf("%s: %w", c.Code, err))
			continue
		}
		if res.Count > 0 {
			lg.Warn("数据巡检：发现违例", zap.String("check", c.Code), zap.Int64("count", res.Count), zap.Strings("sample", res.Sample))
		}
	}
	return errors.Join(failed...)
}

// recordIntegrity 在事务里锁住这一项的上一次结果，按规则决定发不发告警，再写这一次的结果。
func (s *Service) recordIntegrity(ctx context.Context, c integrityCheck, res integrity.Result, now time.Time) error {
	prev, found, err := s.repo.LockIntegrityCheck(ctx, c.Code)
	if err != nil {
		return err
	}
	sample := res.Sample
	if sample == nil {
		sample = []string{}
	}
	if len(sample) > integrity.SampleLimit {
		sample = sample[:integrity.SampleLimit]
	}
	raw, err := json.Marshal(sample)
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	next := model.IntegrityCheck{Code: c.Code, Count: res.Count, Sample: string(raw), CheckedAt: now}
	if res.Count > 0 {
		// 这一段违例的起点：上一次也非 0 就沿用，否则从这一次算
		since := now
		var alerted int64
		if found && prev.Count > 0 && prev.FailingSince != nil {
			since = *prev.FailingSince
			alerted = prev.AlertedCount
		}
		next.FailingSince = &since
		next.AlertedCount = alerted
		if res.Count > alerted {
			sent, err := s.deps.Notices.NotifyAdminsIntegrity(ctx, notifysvc.IntegrityAlert{
				Code: c.Code, Name: c.Name, Count: res.Count, Since: since,
			})
			if err != nil {
				return err
			}
			if sent {
				next.AlertedCount = res.Count
			}
		}
	}
	return s.repo.SaveIntegrityCheck(ctx, next)
}

// IntegrityItem 是后台总览待办里的一项巡检结果。
type IntegrityItem struct {
	Code      string
	Count     int64
	CheckedAt time.Time
}

// integrityItems 读表里的巡检结果，只给现在还登记着的检查、按 integrityChecks 的顺序；没跑过的检查不给。
func (s *Service) integrityItems(ctx context.Context) ([]IntegrityItem, error) {
	rows, err := s.repo.IntegrityChecks(ctx)
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]model.IntegrityCheck, len(rows))
	for _, r := range rows {
		byCode[r.Code] = r
	}
	out := make([]IntegrityItem, 0, len(rows))
	for _, c := range s.integrityChecks() {
		if r, ok := byCode[c.Code]; ok {
			out = append(out, IntegrityItem{Code: r.Code, Count: r.Count, CheckedAt: r.CheckedAt})
		}
	}
	return out, nil
}
