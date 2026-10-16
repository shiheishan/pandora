package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/google/uuid"
)

// 本文件是各写路径共用的内部函数：行与对外类型的转换、入参校验、锁行、读版本、写重置日志、签发凭据、状态转换。

// toSubscription 把行转成对外类型。
func toSubscription(m model.Subscription) Subscription {
	return Subscription{
		ID:              m.ID,
		UserID:          m.UserID,
		PlanID:          m.PlanID,
		PlanVersionID:   m.PlanVersionID,
		Status:          m.Status,
		PeriodStart:     m.CurrentPeriodStart,
		PeriodEnd:       m.CurrentPeriodEnd,
		RenewalClosedAt: m.RenewalClosedAt,
		Price: PriceSnapshot{
			PriceID:       m.PriceID,
			Amount:        m.SnapshotAmount,
			Interval:      m.SnapshotInterval,
			IntervalCount: m.SnapshotIntervalCount,
		},
		DeviceLimit:   m.DeviceLimit,
		Label:         m.Label,
		ProxyUUID:     m.ProxyUUID,
		NodeUID:       m.NodeUID,
		SourceOrderID: m.SourceOrderID,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

// toQuota 把配额行转成对外类型。
func toQuota(m model.QuotaBalance) Quota {
	return Quota{
		Metric:      m.Metric,
		Period:      m.Period,
		PeriodStart: m.PeriodStart,
		PeriodEnd:   m.PeriodEnd,
		Limit:       m.LimitValue,
		Bonus:       m.BonusValue,
		Consumed:    m.Consumed,
		Remaining:   m.Remaining,
	}
}

// sortQuotas 按 metric 升序、同一指标内 period_start 倒序排（原地）：
// 前端取第一条 traffic.bytes 显示，门户的 next_reset_at 也取同一条（nextResetAt）。起点相同时按 period 升序，保证稳定。
func sortQuotas(qs []Quota) {
	sort.SliceStable(qs, func(i, j int) bool {
		if qs[i].Metric != qs[j].Metric {
			return qs[i].Metric < qs[j].Metric
		}
		if !qs[i].PeriodStart.Equal(qs[j].PeriodStart) {
			return qs[i].PeriodStart.After(qs[j].PeriodStart)
		}
		return qs[i].Period < qs[j].Period
	})
}

// validatePrice 校验价格快照：金额两位小数且不为负、周期合法、有效天数只属于一次性价且为正。调用方传错是程序错误，回 internal_error。
func validatePrice(p PriceSnapshot) error {
	switch {
	case p.ValidityDays != nil && (p.Interval != "one_time" || *p.ValidityDays < 1 || *p.ValidityDays > 36500):
		return errs.Wrap(errs.Internal, fmt.Errorf("subscription: 价格快照有效天数 %d 不合法", *p.ValidityDays))
	case money.CheckNonNegative(p.Amount) != nil:
		return errs.Wrap(errs.Internal, fmt.Errorf("subscription: 价格快照金额 %s 不合法", p.Amount))
	case !ValidInterval(p.Interval) || p.IntervalCount < 1 || p.IntervalCount > 32767:
		return errs.Wrap(errs.Internal, fmt.Errorf("subscription: 价格快照周期 %q×%d 不合法", p.Interval, p.IntervalCount))
	}
	return nil
}

// validateActor 校验操作者：user、admin 必须带 ID，其余不带。
func validateActor(a Actor) error {
	switch a.Kind {
	case ActorUser, ActorAdmin:
		if a.ID == nil || *a.ID == uuid.Nil {
			return errs.Wrap(errs.Internal, fmt.Errorf("subscription: 操作者 %s 缺 ID", a.Kind))
		}
	case ActorSystem, ActorPayment, ActorScheduler:
		if a.ID != nil {
			return errs.Wrap(errs.Internal, fmt.Errorf("subscription: 操作者 %s 不带 ID", a.Kind))
		}
	default:
		return errs.Wrap(errs.Internal, fmt.Errorf("subscription: 未知操作者 %q", a.Kind))
	}
	return nil
}

// cleanText 去首尾空白后校验字数（按 rune）与 NUL；不合格回 422，fields[field] = msg。
func cleanText(s string, minRunes, maxRunes int, field, msg string) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) || n < minRunes || n > maxRunes {
		return "", errs.Invalid(map[string]string{field: msg})
	}
	return s, nil
}

// loadSub 读一份订阅（可加锁）；不存在或 owner 非 nil 且不是他的，回 ErrNotFound。
func (s *Service) loadSub(ctx context.Context, id uuid.UUID, owner *uuid.UUID, lock repo.Lock) (model.Subscription, error) {
	if id == uuid.Nil {
		return model.Subscription{}, ErrNotFound
	}
	m, ok, err := s.repo.SubscriptionByID(ctx, id, lock)
	if err != nil {
		return model.Subscription{}, subErr(err)
	}
	if !ok || (owner != nil && m.UserID != *owner) {
		return model.Subscription{}, ErrNotFound
	}
	return m, nil
}

// lockOwned 在 ctx 的事务里锁住一份订阅（FOR NO KEY UPDATE）。
func (s *Service) lockOwned(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (model.Subscription, error) {
	return s.loadSub(ctx, id, owner, repo.ForNoKeyUpdate)
}

// errAccountDeleted 是后台改已注销账号的订阅（与 identity 同码同文案）。
var errAccountDeleted = errs.Newf(errs.Conflict, "账号已注销")

// rejectDeletedOwner 后台改订阅前拒绝已注销账号：先不加锁读出属主（属主不会变），再经 identity 的 IsDeleted
// 给用户行加共享锁并持有到提交。这一步排在锁订阅行之前，锁序与注销（用户行 → 订单 → 订阅）一致，不会互等。
func (s *Service) rejectDeletedOwner(ctx context.Context, subID uuid.UUID) error {
	m, err := s.loadSub(ctx, subID, nil, repo.NoLock)
	if err != nil {
		return err
	}
	deleted, err := s.users.IsDeleted(ctx, m.UserID)
	if err != nil {
		return err
	}
	if deleted {
		return errAccountDeleted
	}
	return nil
}

// lockForAdmin 是后台改订阅的锁行入口：先拒已注销账号（rejectDeletedOwner），再锁订阅行。
func (s *Service) lockForAdmin(ctx context.Context, subID uuid.UUID) (model.Subscription, error) {
	if err := s.rejectDeletedOwner(ctx, subID); err != nil {
		return model.Subscription{}, err
	}
	return s.lockOwned(ctx, subID, nil)
}

// versionFor 取开通、换套餐用的版本：必须存在、属于 planID、已发布。
func (s *Service) versionFor(ctx context.Context, planID, versionID uuid.UUID) (catalogsvc.Version, error) {
	vs, err := s.catalog.VersionsByIDs(ctx, []uuid.UUID{versionID})
	if err != nil {
		return catalogsvc.Version{}, err
	}
	v, ok := vs[versionID]
	if !ok || v.PlanID != planID || v.Status != catalogsvc.VersionPublished {
		return catalogsvc.Version{}, ErrVersionUnavailable
	}
	return v, nil
}

// planOf 读一个套餐（开通、续费、叠加、换套餐、送流量判断类型用）；读不到回 ErrVersionUnavailable。
func (s *Service) planOf(ctx context.Context, planID uuid.UUID) (catalogsvc.Plan, error) {
	plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{planID})
	if err != nil {
		return catalogsvc.Plan{}, err
	}
	p, ok := plans[planID]
	if !ok {
		return catalogsvc.Plan{}, ErrVersionUnavailable
	}
	return p, nil
}

// trafficPeriod 是套餐类型对应的流量配额周期：订阅套餐 cycle（每周期一份、滚动清零），一次性套餐 total（不清零）。
// 流量落在哪一行一律按套餐类型判断，不信版本里配额定义的 period（开发库里可能有 cycle 的一次性版本）。
func trafficPeriod(kind string) string {
	if kind == catalogsvc.KindOneTime {
		return PeriodTotal
	}
	return PeriodCycle
}

// withKind 返回 v 的副本：traffic.bytes 配额定义的周期按套餐类型改正（trafficPeriod）。开通、换套餐、续费恢复、叠加建配额前都过一遍。
func withKind(v catalogsvc.Version, kind string) catalogsvc.Version {
	qs := make([]catalogsvc.Quota, len(v.Quotas))
	copy(qs, v.Quotas)
	for i := range qs {
		if qs[i].Metric == MetricTraffic {
			qs[i].Period = trafficPeriod(kind)
		}
	}
	v.Quotas = qs
	return v
}

// quotaDef 在版本的配额定义里找 (metric, period)；没有返回 false。
func quotaDef(v catalogsvc.Version, metric, period string) (catalogsvc.Quota, bool) {
	for _, q := range v.Quotas {
		if q.Metric == metric && q.Period == period {
			return q, true
		}
	}
	return catalogsvc.Quota{}, false
}

// transition 带旧状态条件更新订阅：先查转换表，影响 0 行回 ErrStateChanged。cols 里放要改的列（含 status）。
func (s *Service) transition(ctx context.Context, sub model.Subscription, to string, cols map[string]any) error {
	if !CanTransition(sub.Status, to) {
		return errs.Wrap(errs.Internal, fmt.Errorf("subscription: 不允许的状态转换 %s → %s", sub.Status, to))
	}
	cols["status"] = to
	ok, err := s.repo.UpdateSubscription(ctx, sub.ID, sub.Status, cols)
	if err != nil {
		return subErr(err)
	}
	if !ok {
		return ErrStateChanged
	}
	s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, sub.ID, sub.UserID)
	return nil
}

// logResets 为一组被清零的配额行写重置日志（consumed_before 取清零前的值）。
func (s *Service) logResets(ctx context.Context, sub model.Subscription, before []model.QuotaBalance, reason string, actorID *uuid.UUID, note string) error {
	if len(before) == 0 {
		return nil
	}
	var notePtr *string
	if note != "" {
		n := note
		notePtr = &n
	}
	rows := make([]model.ResetLog, 0, len(before))
	for _, q := range before {
		rows = append(rows, model.ResetLog{
			SubscriptionID: sub.ID,
			UserID:         sub.UserID,
			Metric:         q.Metric,
			Reason:         reason,
			ConsumedBefore: q.Consumed,
			ConsumedAfter:  0,
			ActorID:        actorID,
			Note:           notePtr,
		})
	}
	return subErr(s.repo.InsertResetLogs(ctx, rows))
}

// tokenBytes 是订阅链接令牌的熵（字节）。
const tokenBytes = 32

// CredentialAAD 是封存订阅链接令牌用的附加数据：用途前缀加订阅 id。封存器的密钥被多处共用，
// 前缀把订阅凭据与别的密文隔开；拉取时解封用同一个函数。
func CredentialAAD(subID uuid.UUID) []byte { return []byte("sub-credential:" + subID.String()) }

// issueCredential 给订阅签发一条订阅链接凭据：库里存令牌的 SHA-256 与封存后的原文（AAD 见 CredentialAAD）。
// 令牌原文不返回、不进日志；轮换见 rotate.go，渲染在拉取（pull.go）。
func (s *Service) issueCredential(ctx context.Context, sub model.Subscription) error {
	if s.sealer == nil {
		return errs.Wrap(errs.Internal, errors.New("subscription: 没有注入封存器"))
	}
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return errs.Wrap(errs.Internal, fmt.Errorf("subscription: 生成令牌: %w", err))
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	sealed, err := s.sealer.Seal([]byte(token), CredentialAAD(sub.ID))
	if err != nil {
		return errs.Wrap(errs.Internal, fmt.Errorf("subscription: 封存令牌: %w", err))
	}
	c := model.Credential{
		SubscriptionID: sub.ID,
		UserID:         sub.UserID,
		TokenHash:      sum[:],
		TokenPrefix:    token[:8],
		TokenEncrypted: sealed,
		Status:         "active",
	}
	if err := s.repo.InsertCredential(ctx, &c); err != nil {
		return subErr(err)
	}
	// c.ID 由库生成，GORM 插入时回填
	s.rtChanged(ctx, tableCredentials, realtime.OpInsert, c.ID, sub.UserID)
	return nil
}

// errMissing 是调用方漏传或传错必填入参的内部错误（程序错误，回 internal_error）。
func errMissing(what string) error {
	return fmt.Errorf("subscription: 入参 %s 缺失或不合法", what)
}
