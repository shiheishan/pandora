package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"go.uber.org/zap"
)

// adminNotice 是一条发到管理员群组的提醒（随业务写入，派发时才发）。
type adminNotice struct {
	Code     string // TemplateAdmin*
	Vars     map[string]string
	Key      string     // 去重键
	AboutID  uuid.UUID  // 事件涉及的用户（notification_events.user_id）
	TicketID *uuid.UUID // 工单提醒带上，派发成功后记映射
}

// AdminTicketInput 是 support 经适配器传来的一条工单活动。Kind 为 "opened"（新建）或 "user_replied"（用户回复）。
type AdminTicketInput struct {
	Kind      string
	UserID    uuid.UUID
	TicketID  uuid.UUID
	MessageID uuid.UUID
	TicketNo  string
	Subject   string
	Body      string
}

// adminTicketBodyMax 是工单提醒里正文最多带的字数（Telegram 单条上限 4096）。
const adminTicketBodyMax = 1500

// maskEmail 给邮箱打码：保留 @ 前两个字符（不足三个字符全打码）与域名；不是邮箱时返回 "***"（与 commission.MaskEmail 同一规则）。
func maskEmail(e string) string {
	for i := 0; i < len(e); i++ {
		if e[i] == '@' {
			if i <= 2 {
				return "***" + e[i:]
			}
			return e[:2] + "***" + e[i:]
		}
	}
	return "***"
}

// adminChat 判断管理员群组提醒能不能发：Telegram 可发（telegramReady）且设了管理员群组 chat id；返回群 chat id。
func (s *Service) adminChat(ctx context.Context) (int64, bool, error) {
	cfg, ready, err := s.telegramReady(ctx)
	if err != nil {
		return 0, false, err
	}
	if !ready || cfg.AdminChatID == nil || *cfg.AdminChatID == 0 {
		return 0, false, nil
	}
	return *cfg.AdminChatID, true, nil
}

// writeAdminNotice 在 ctx 的事务里给管理员群组排一条提醒：先插去重键（撞了返回 false, nil，什么都不写）；
// 再按 lookupDef(n.Code, ChannelTelegram) 的白名单过滤变量（去 NUL），写一条 user_id 为空、收件地址为群 chat id 的 queued Telegram 投递
// （payload 带 TicketID），提交后 tx.AfterCommit(ctx, TaskDispatch, s.kick)。返回是否新写。
func (s *Service) writeAdminNotice(ctx context.Context, chatID int64, n adminNotice) (bool, error) {
	if n.Key == "" || len(n.Key) > 200 {
		return false, errs.Wrap(errs.Internal, fmt.Errorf("notify: 去重键无效"))
	}
	def, ok := lookupDef(n.Code, ChannelTelegram)
	if !ok {
		return false, errs.Wrap(errs.Internal, fmt.Errorf("notify: 未登记的通知模板 %q", n.Code))
	}

	inserted, err := s.store.InsertEvent(ctx, &model.Event{
		DedupeKey:    n.Key,
		UserID:       n.AboutID,
		TemplateCode: n.Code,
	})
	if err != nil {
		return false, err
	}
	if !inserted {
		return false, nil
	}

	payloadVars := make(map[string]string)
	for _, k := range def.Allowed {
		if v, ok := n.Vars[k]; ok && !strings.ContainsRune(v, '\x00') {
			payloadVars[k] = v
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return false, errs.Wrap(errs.Internal, err)
	}
	to := strconv.FormatInt(chatID, 10)
	payload := deliveryPayload{
		To:   to,
		Vars: payloadVars,
	}
	if n.TicketID != nil {
		payload.TicketID = n.TicketID.String()
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return false, errs.Wrap(errs.Internal, err)
	}
	blob, err := s.sealer.Seal(raw, deliveryAAD(id))
	if err != nil {
		return false, errs.Wrap(errs.Internal, err)
	}

	if err := s.store.InsertDelivery(ctx, &model.Delivery{
		ID:            id,
		UserID:        nil,
		TemplateCode:  n.Code,
		Channel:       ChannelTelegram,
		RecipientHash: s.recipientHash(to),
		PayloadEnc:    blob,
		Status:        model.StatusQueued,
		MaxAttempts:   maxAttempts,
	}); err != nil {
		return false, err
	}
	tx.AfterCommit(ctx, TaskDispatch, s.kick)
	return true, nil
}

// truncateTicketBody 截断工单正文，超出 max 字符追加「…」。
func truncateTicketBody(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// NotifyAdminsTicket 是 support 建单 / 用户回复时调的（在调用方事务里，s.tx.Do 加入）：
// 后台没开工单提醒（TelegramExtras.AdminTicketNotify）或 adminChat 不可发时什么都不写、返回 nil；
// 否则批量读这个用户的邮箱（s.dir.Emails）、订阅摘要（s.summaries）、余额（s.balances），拼变量，writeAdminNotice。
// 这几项读失败只记 Warn、对应变量写「—」；读设置与写去重键、投递失败才返回错误。
// 变量：ticket_no、subject、body（截到 adminTicketBodyMax 字，超出加「…」）、email（maskEmail）、
//
//	plan / traffic / expires_at：取 UserSubscriptions.Current 且 State == StateLive 的那份——plan 为 PlanName，
//	traffic 为第一条 MetricTraffic 配额的 Remaining（含本期赠送；humanBytes；Limit 为 nil 写「不限量」；没有流量配额行写「—」），
//	expires_at 为 paidExpiry(PeriodEnd, 站点时区)（永久写「永久」）；没有生效订阅时 plan 写「无生效订阅」，traffic、expires_at 写「—」；
//	balance：余额.StringFixed(2) + " 元"。
//
// 模板：Kind 为 "opened" 用 TemplateAdminTicketOpened，否则 TemplateAdminTicketReplied。去重键 "admin-ticket:" + MessageID。
func (s *Service) NotifyAdminsTicket(ctx context.Context, in AdminTicketInput) error {
	ex, err := s.settings.TelegramExtras(ctx)
	if err != nil {
		return err
	}
	if !ex.AdminTicketNotify {
		return nil
	}
	chatID, ok, err := s.adminChat(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if s.dir == nil {
		return errs.Wrap(errs.Internal, fmt.Errorf("notify: Directory 未装配"))
	}
	if s.summaries == nil {
		return errs.Wrap(errs.Internal, fmt.Errorf("notify: SubscriptionSummaries 未装配"))
	}
	if s.balances == nil {
		return errs.Wrap(errs.Internal, fmt.Errorf("notify: Balances 未装配"))
	}

	// 下面这些只是给客服看的上下文，读不到就记 Warn 写「—」，不让旁路提醒拖垮建单 / 回复；
	// 只有写去重键与投递失败才返回错误（随调用方事务回滚）。
	lg := plog.From(ctx)
	emailStr := "—"
	if emails, err := s.dir.Emails(ctx, []uuid.UUID{in.UserID}); err != nil {
		lg.Warn("notify: 工单提醒读邮箱失败", zap.Error(err))
	} else {
		emailStr = maskEmail(emails[in.UserID])
	}

	planStr, trafficStr, expiresAtStr := "—", "—", "—"
	userSubs, err := s.summaries.SummariesByUsers(ctx, []uuid.UUID{in.UserID})
	if err != nil {
		lg.Warn("notify: 工单提醒读订阅摘要失败", zap.Error(err))
	} else {
		planStr = "无生效订阅"
		if us := userSubs[in.UserID]; us.Current != nil && us.Current.State == subsvc.StateLive {
			cur := us.Current
			planStr = cur.PlanName
			trafficStr = ticketTraffic(cur)
			if loc, err := scanSiteLocation(ctx, s); err != nil {
				lg.Warn("notify: 工单提醒读站点时区失败", zap.Error(err))
			} else {
				expiresAtStr = paidExpiry(cur.PeriodEnd, loc)
			}
		}
	}

	balanceStr := "—"
	if balances, err := s.balances.UserBalances(ctx, []uuid.UUID{in.UserID}); err != nil {
		lg.Warn("notify: 工单提醒读余额失败", zap.Error(err))
	} else {
		bal := balances[in.UserID]
		balanceStr = bal.StringFixed(2) + " 元"
	}

	code := TemplateAdminTicketReplied
	if in.Kind == "opened" {
		code = TemplateAdminTicketOpened
	}

	tid := in.TicketID
	notice := adminNotice{
		Code: code,
		Vars: map[string]string{
			"ticket_no":  in.TicketNo,
			"subject":    in.Subject,
			"body":       truncateTicketBody(in.Body, adminTicketBodyMax),
			"email":      emailStr,
			"plan":       planStr,
			"traffic":    trafficStr,
			"expires_at": expiresAtStr,
			"balance":    balanceStr,
		},
		Key:      "admin-ticket:" + in.MessageID.String(),
		AboutID:  in.UserID,
		TicketID: &tid,
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		_, err := s.writeAdminNotice(ctx, chatID, notice)
		return err
	})
}

// ticketTraffic 是工单提醒里的剩余流量：第一条流量配额的剩余（含本期赠送）；Limit 为 nil 写「不限量」，没有流量配额写「—」。
func ticketTraffic(cur *subsvc.Summary) string {
	for _, q := range cur.Quotas {
		if q.Metric != subsvc.MetricTraffic {
			continue
		}
		if q.Limit == nil {
			return "不限量"
		}
		var rem int64
		if q.Remaining != nil {
			rem = *q.Remaining
		} else {
			rem = *q.Limit - q.Consumed
		}
		return humanBytes(rem)
	}
	return "—"
}

// paidAmount 是收款提醒里的金额：订单总额两位小数加「元」。
func paidAmount(p ordersvc.PaidNotice) string {
	return p.Total.StringFixed(2) + " 元"
}
