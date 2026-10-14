package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 回复文案。
const (
	tgDefaultStart  = "请发送面板上显示的 8 位绑定码。" // /start 没配欢迎文案时（与现在的文案一致）
	tgNotBound      = "还没有绑定账号：请在门户「账号」页获取 8 位绑定码，发给我即可绑定。"
	tgSubDisabled   = "这个命令没有开启。"
	tgNoLiveSub     = "你还没有生效中的订阅。"
	tgUnbound       = "已解除绑定，以后不会再在这里收到通知。"
	tgTryLater      = "暂时没办法处理，请稍后再试。"
	tgTicketReplied = "已回复工单。"
	tgSubHeader     = "你的订阅链接（相当于账号密码，请勿转发给他人）："
)

// tgAdminReplyEvent 是 TG 回复工单去重键（tg-admin-reply:<chat_id>:<message_id>）在 notification_events 里的标签。
const tgAdminReplyEvent = "admin.tg_reply"

// parseBotCommand 拆出命令与参数：以 / 开头时 cmd 是第一个词去掉 / 与 @bot 后缀、转小写，arg 是其余部分去空白；不是命令时 cmd 为空串、arg 为原文去空白。
func parseBotCommand(text string) (cmd, arg string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return "", trimmed
	}
	parts := strings.SplitN(trimmed, " ", 2)
	c := strings.TrimPrefix(parts[0], "/")
	if idx := strings.Index(c, "@"); idx >= 0 {
		c = c[:idx]
	}
	cmd = strings.ToLower(c)
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return cmd, arg
}

// startText 是 /start 的回复：后台配了欢迎文案用它，否则 tgDefaultStart。
func startText(ex settingssvc.TelegramExtras) string {
	msg := strings.TrimSpace(ex.StartMessage)
	if msg != "" {
		return msg
	}
	return tgDefaultStart
}

// helpText 是认不得的命令的回复：列出可用命令（/traffic、/unbind，开了 /sub 时加上），并提示可以直接发绑定码。
func helpText(ex settingssvc.TelegramExtras) string {
	var sb strings.Builder
	sb.WriteString("可用命令：\n")
	sb.WriteString("/traffic - 查看订阅与用量\n")
	if ex.SubCommand {
		sb.WriteString("/sub - 获取订阅链接\n")
	}
	sb.WriteString("/unbind - 解除绑定\n\n")
	sb.WriteString("也可以直接向我发送 8 位绑定码绑定账号。")
	return sb.String()
}

// formatTrafficReply 把生效中的订阅逐条列出：
//
//	「套餐名」（有备注名时接「 · 备注」）
//	流量：已用 X / 共 Y，剩余 Z   （取第一条 Metric == subsvc.MetricTraffic 的配额；Limit 为 nil 写「流量：已用 X（不限量）」；没有流量配额行就不写这一行）
//	流量包余量：N               （PackRemainingBytes > 0 时）
//	到期：<noticeTime(PeriodEnd, loc)>
//
// 多份之间空一行；subs 为空返回 tgNoLiveSub。字节数用 humanBytes。
func formatTrafficReply(subs []subsvc.Summary, loc *time.Location) string {
	if len(subs) == 0 {
		return tgNoLiveSub
	}
	var blocks []string
	for _, sub := range subs {
		var lines []string

		title := "「" + sub.PlanName + "」"
		if sub.Label != nil && strings.TrimSpace(*sub.Label) != "" {
			title += " · " + strings.TrimSpace(*sub.Label)
		}
		lines = append(lines, title)

		// 流量配额
		for _, q := range sub.Quotas {
			if q.Metric == subsvc.MetricTraffic {
				if q.Limit == nil {
					lines = append(lines, "流量：已用 "+humanBytes(q.Consumed)+"（不限量）")
				} else {
					rem := int64(0)
					if q.Remaining != nil {
						rem = *q.Remaining
					}
					lines = append(lines, "流量：已用 "+humanBytes(q.Consumed)+" / 共 "+humanBytes(*q.Limit)+"，剩余 "+humanBytes(rem))
				}
				break
			}
		}

		if sub.PackRemainingBytes > 0 {
			lines = append(lines, "流量包余量："+humanBytes(sub.PackRemainingBytes))
		}

		lines = append(lines, "到期："+noticeTime(sub.PeriodEnd, loc))
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

// handlePrivateMessage 处理私聊：
//
//	start：arg 为空 → startText；arg 非空 → 走现有绑定（s.handleBindMessage(ctx, "/start "+arg, chatID, username)）
//	traffic → s.trafficReply；sub → s.subReply；unbind → s.unbindReply
//	不是命令（cmd 为空）→ 现有绑定流程 s.handleBindMessage(ctx, text, chatID, username)
//	其他命令 → helpText
func (s *Service) handlePrivateMessage(ctx context.Context, ex settingssvc.TelegramExtras, chatID int64, username, text string) string {
	cmd, arg := parseBotCommand(text)
	switch cmd {
	case "start":
		if arg == "" {
			return startText(ex)
		}
		return s.handleBindMessage(ctx, "/start "+arg, chatID, username)
	case "traffic":
		return s.trafficReply(ctx, chatID)
	case "sub":
		return s.subReply(ctx, ex, chatID)
	case "unbind":
		return s.unbindReply(ctx, chatID)
	case "":
		return s.handleBindMessage(ctx, text, chatID, username)
	default:
		return helpText(ex)
	}
}

// trafficReply：BindingByChatID(chatID) 没有 → tgNotBound；有 → SummariesByUsers 取这个用户，只留 State == subsvc.StateLive 的，
// 按 scanSiteLocation 的时区 formatTrafficReply。读失败记 Warn 回 tgTryLater。
func (s *Service) trafficReply(ctx context.Context, chatID int64) string {
	if s.summaries == nil {
		return tgTryLater
	}
	b, ok, err := s.store.BindingByChatID(ctx, chatID)
	if err != nil {
		plog.From(ctx).Warn("notify: 查询 Telegram 绑定失败", zap.Error(err))
		return tgTryLater
	}
	if !ok {
		return tgNotBound
	}

	summariesMap, err := s.summaries.SummariesByUsers(ctx, []uuid.UUID{b.UserID})
	if err != nil {
		plog.From(ctx).Warn("notify: 读取用户订阅摘要失败", zap.Error(err))
		return tgTryLater
	}

	us := summariesMap[b.UserID]
	var live []subsvc.Summary
	for _, sub := range us.Subscriptions {
		if sub.State == subsvc.StateLive {
			live = append(live, sub)
		}
	}

	loc, err := scanSiteLocation(ctx, s)
	if err != nil {
		plog.From(ctx).Warn("notify: 读取站点时区失败", zap.Error(err))
		return tgTryLater
	}

	return formatTrafficReply(live, loc)
}

// subReply：!ex.SubCommand → tgSubDisabled；没绑定 → tgNotBound；ActiveLinks 为空 → tgNoLiveSub；
// 否则 tgSubHeader + "\n\n" + 链接用空行连接。读失败回 tgTryLater。
func (s *Service) subReply(ctx context.Context, ex settingssvc.TelegramExtras, chatID int64) string {
	if !ex.SubCommand {
		return tgSubDisabled
	}
	if s.links == nil {
		return tgTryLater
	}
	b, ok, err := s.store.BindingByChatID(ctx, chatID)
	if err != nil {
		plog.From(ctx).Warn("notify: 查询 Telegram 绑定失败", zap.Error(err))
		return tgTryLater
	}
	if !ok {
		return tgNotBound
	}

	links, err := s.links.ActiveLinks(ctx, b.UserID)
	if err != nil {
		plog.From(ctx).Warn("notify: 读取用户有效订阅链接失败", zap.Error(err))
		return tgTryLater
	}
	if len(links) == 0 {
		return tgNoLiveSub
	}

	return tgSubHeader + "\n\n" + strings.Join(links, "\n\n")
}

// unbindReply：没绑定 → tgNotBound；有 → s.unbindTelegram(ctx, b.UserID, &b.UserID, "telegram")，成功 tgUnbound，失败记 Warn 回 tgTryLater。
func (s *Service) unbindReply(ctx context.Context, chatID int64) string {
	b, ok, err := s.store.BindingByChatID(ctx, chatID)
	if err != nil {
		plog.From(ctx).Warn("notify: 查询 Telegram 绑定失败", zap.Error(err))
		return tgTryLater
	}
	if !ok {
		return tgNotBound
	}

	if err := s.unbindTelegram(ctx, b.UserID, &b.UserID, "telegram"); err != nil {
		plog.From(ctx).Warn("notify: 解除 Telegram 绑定失败", zap.Error(err))
		return tgTryLater
	}
	return tgUnbound
}

// handleJoinRequest：!ex.JoinReview、ex.UserGroupChatID 为 nil、req.Chat/From 为 nil、或 req.Chat.ID != *ex.UserGroupChatID → 不处理（留给群管理员人工审）。
// 否则 BindingByChatID(req.From.ID)：绑定了且 LivePlanIDs 非空 → ApproveChatJoinRequest，否则 DeclineChatJoinRequest。
// 读库出错只记 Warn、不批也不拒；调 Telegram 失败记 Warn。日志不带 chat id、TG 用户 id。
func (s *Service) handleJoinRequest(ctx context.Context, cfg settingssvc.Telegram, ex settingssvc.TelegramExtras, req *tgChatJoinRequest) {
	if !ex.JoinReview || ex.UserGroupChatID == nil || req == nil || req.Chat == nil || req.From == nil {
		return
	}
	if req.Chat.ID != *ex.UserGroupChatID {
		return
	}
	if s.subs == nil {
		return
	}

	b, ok, err := s.store.BindingByChatID(ctx, req.From.ID)
	if err != nil {
		plog.From(ctx).Warn("notify: 查询 Telegram 绑定失败", zap.Error(err))
		return
	}

	approve := false
	if ok {
		planIDs, err := s.subs.LivePlanIDs(ctx, b.UserID)
		if err != nil {
			plog.From(ctx).Warn("notify: 查询用户生效订阅失败", zap.Error(err))
			return
		}
		if len(planIDs) > 0 {
			approve = true
		}
	}

	if approve {
		if err := s.tg.ApproveChatJoinRequest(ctx, cfg.BotToken, req.Chat.ID, req.From.ID); err != nil {
			plog.From(ctx).Warn("notify: Telegram 批准入群失败", zap.Error(err))
		}
	} else {
		if err := s.tg.DeclineChatJoinRequest(ctx, cfg.BotToken, req.Chat.ID, req.From.ID); err != nil {
			plog.From(ctx).Warn("notify: Telegram 拒绝入群失败", zap.Error(err))
		}
	}
}

// handleAdminReply：依次核对，任何一步不过就直接返回（不回消息、不记内容）：
//
//	cfg.AdminChatID 非 nil 且等于 msg.Chat.ID；msg.From 非 nil 且不是 bot；正文去空白后非空；
//	BindingByChatID(msg.From.ID) 有绑定；s.admins.IsActiveAdmin(绑定的 user_id) 为真；
//	TicketForMessage(msg.Chat.ID, msg.ReplyToMessage.MessageID) 找得到工单。
//
// 然后在一个事务里先插去重键 tg-admin-reply:<chat_id>:<message_id>（撞键 = Telegram 重推，直接返回），
// 再 s.replier.ReplyAsAdmin(ctx, adminID, ticketID, 正文)：成功往群里回 tgTicketReplied；
// 失败时 errs.As(err) 不为 nil 且 Code 不是 errs.Internal → 回「回复没有成功：」+ e.Message；否则记 Warn 回「回复没有成功，请到后台重试。」。
// 读库出错只记 Warn、不回消息。
func (s *Service) handleAdminReply(ctx context.Context, cfg settingssvc.Telegram, msg *tgWebhookMessage) {
	if cfg.AdminChatID == nil || msg == nil || msg.Chat == nil || msg.Chat.ID != *cfg.AdminChatID {
		return
	}
	if msg.From == nil || msg.From.IsBot {
		return
	}
	body := strings.TrimSpace(msg.Text)
	if body == "" {
		return
	}
	if msg.ReplyToMessage == nil || msg.MessageID <= 0 {
		return
	}
	if s.admins == nil || s.replier == nil {
		return
	}

	b, ok, err := s.store.BindingByChatID(ctx, msg.From.ID)
	if err != nil {
		plog.From(ctx).Warn("notify: 查询管理员 Telegram 绑定失败", zap.Error(err))
		return
	}
	if !ok {
		return
	}

	isAdmin, err := s.admins.IsActiveAdmin(ctx, b.UserID)
	if err != nil {
		plog.From(ctx).Warn("notify: 校验管理员身份失败", zap.Error(err))
		return
	}
	if !isAdmin {
		return
	}

	ticketID, ok, err := s.store.TicketForMessage(ctx, msg.Chat.ID, msg.ReplyToMessage.MessageID)
	if err != nil {
		plog.From(ctx).Warn("notify: 查询工单消息映射失败", zap.Error(err))
		return
	}
	if !ok {
		return
	}

	// Telegram 没拿到 2xx 会用同一条消息重推：先在同一事务里插去重键，撞键说明这条已经回过，直接当成功、不再回确认；
	// 回复失败时去重键随事务回滚，下次重推还能重试。support 的 tx.Do 在已有事务里直接加入。
	dup := false
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		ins, err := s.store.InsertEvent(ctx, &model.Event{
			DedupeKey:    fmt.Sprintf("tg-admin-reply:%d:%d", msg.Chat.ID, msg.MessageID),
			UserID:       b.UserID,
			TemplateCode: tgAdminReplyEvent,
		})
		if err != nil {
			return err
		}
		if !ins {
			dup = true
			return nil
		}
		return s.replier.ReplyAsAdmin(ctx, b.UserID, ticketID, body)
	})
	if err == nil && dup {
		return
	}
	if err == nil {
		if cfg.BotToken != "" {
			if _, sendErr := s.tg.SendMessage(ctx, cfg.BotToken, msg.Chat.ID, tgTicketReplied); sendErr != nil {
				plog.From(ctx).Warn("notify: 回复工单确认消息失败", zap.Error(sendErr))
			}
		}
		return
	}

	e := errs.As(err)
	var replyText string
	if e != nil && e.Code != errs.Internal {
		replyText = "回复没有成功：" + e.Message
	} else {
		plog.From(ctx).Warn("notify: 以管理员身份回复工单失败", zap.Error(err))
		replyText = "回复没有成功，请到后台重试。"
	}

	if cfg.BotToken != "" {
		if _, sendErr := s.tg.SendMessage(ctx, cfg.BotToken, msg.Chat.ID, replyText); sendErr != nil {
			plog.From(ctx).Warn("notify: 回复工单错误提示失败", zap.Error(sendErr))
		}
	}
}
