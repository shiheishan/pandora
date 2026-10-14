package service

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// categories 是允许的工单分类（封闭枚举：分类用于路由、统计与 SLA，自由文本会让这三样失效）；
// 顺序即门户下拉框的顺序。
var categories = []Category{
	{Code: "general", Name: "一般咨询"},
	{Code: "technical", Name: "连接与技术"},
	{Code: "subscription", Name: "订阅与套餐"},
	{Code: "billing", Name: "账单与支付"},
	{Code: "account", Name: "账号与安全"},
	{Code: "abuse", Name: "举报与投诉"},
}

// Categories 返回工单分类（固定顺序）。
func (s *Service) Categories() []Category {
	out := make([]Category, len(categories))
	copy(out, categories)
	return out
}

func validCategory(code string) bool {
	for _, c := range categories {
		if c.Code == code {
			return true
		}
	}
	return false
}

// 文本长度上限（按字符数）。
const (
	subjectMin  = 4
	subjectMax  = 120
	createMin   = 10
	bodyMax     = 5000
	reasonMax   = 500
	macroTitle  = 20
	defaultCode = "general"
)

// slaHours 按优先级给出「首次响应 / 解决」的小时数；必须保持首次响应 < 解决时限。
var slaHours = map[string][2]int{
	PriorityUrgent: {1, 8},
	PriorityHigh:   {4, 24},
	PriorityNormal: {12, 72},
	PriorityLow:    {24, 168},
}

// slaDue 按优先级算首次响应与解决的截止时间（未知优先级按 normal）。
func slaDue(priority string, from time.Time) (first, resolution time.Time) {
	h, ok := slaHours[priority]
	if !ok {
		h = slaHours[PriorityNormal]
	}
	return from.Add(time.Duration(h[0]) * time.Hour), from.Add(time.Duration(h[1]) * time.Hour)
}

// priorityFor 由分类推导优先级：用户不能自选（谁都会选 urgent），客服可以事后调整。
func priorityFor(category string) string {
	switch category {
	case "billing", "account":
		return PriorityHigh
	case "abuse":
		return PriorityUrgent
	}
	return PriorityNormal
}

// escalatedPriority 是人工升级后的优先级：至少 high，已是 urgent 不降。
func escalatedPriority(p string) string {
	if p == PriorityLow || p == PriorityNormal {
		return PriorityHigh
	}
	return p
}

// newTicketNo 生成工单号：TK + UTC 日期 + 8 位 base32（40 位随机）。
func newTicketNo(now time.Time) (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return fmt.Sprintf("TK%s-%s", now.UTC().Format("20060102"), enc), nil
}

// subjectFromBody 从正文首个非空行生成标题：按字符截到 120，太短的加固定前缀以满足最短 4 字。
func subjectFromBody(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > subjectMax {
			runes = runes[:subjectMax]
		}
		subject := string(runes)
		if utf8.RuneCountInString(subject) < subjectMin {
			subject = "用户咨询：" + subject
		}
		return subject
	}
	return ""
}

// normalizeCreate 去空白并补默认值后校验建单输入；不合法回 422（字段级）。
func normalizeCreate(in *CreateInput) error {
	in.Subject = strings.TrimSpace(in.Subject)
	in.Category = strings.TrimSpace(in.Category)
	in.Body = strings.TrimSpace(in.Body)
	if in.Subject == "" {
		in.Subject = subjectFromBody(in.Body)
	}
	if in.Category == "" {
		in.Category = defaultCode
	}
	fields := map[string]string{}
	if n := utf8.RuneCountInString(in.Subject); n < subjectMin || n > subjectMax || hasNUL(in.Subject) {
		fields["subject"] = "标题需在 4–120 字之间"
	}
	if n := utf8.RuneCountInString(in.Body); n < createMin || n > bodyMax || hasNUL(in.Body) {
		fields["body"] = "内容需在 10–5000 字之间"
	}
	if !validCategory(in.Category) {
		fields["category"] = "无效的分类"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// normalizeBody 去空白后校验回复正文（1–5000 字、不含 NUL）。
func normalizeBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if n := utf8.RuneCountInString(body); n < 1 || n > bodyMax || hasNUL(body) {
		return "", errs.Invalid(map[string]string{"body": "内容需在 1–5000 字之间"})
	}
	return body, nil
}

// normalizeReason 去空白后校验撤回说明、改状态原因（可空、≤500 字、不含 NUL）；field 是出错时的字段名。
func normalizeReason(field, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > reasonMax || hasNUL(reason) {
		return "", errs.Invalid(map[string]string{field: "不能超过 500 字"})
	}
	return reason, nil
}

// normalizeMacro 去空白后校验快捷回复（标题 1–20 字、正文 1–5000 字、都不含 NUL）。
func normalizeMacro(in MacroInput) (MacroInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	fields := map[string]string{}
	if n := utf8.RuneCountInString(in.Title); n < 1 || n > macroTitle || hasNUL(in.Title) {
		fields["title"] = "标题必须为 1 到 20 个字"
	}
	if n := utf8.RuneCountInString(in.Body); n < 1 || n > bodyMax || hasNUL(in.Body) {
		fields["body"] = "内容必须为 1 到 5000 个字"
	}
	if len(fields) > 0 {
		return in, errs.Invalid(fields)
	}
	return in, nil
}

// hasNUL 报告文本里有没有 NUL（PG 的 text 不收，放进库会变成 500）。
func hasNUL(s string) bool { return strings.IndexByte(s, 0) >= 0 }

// transitions 是工单状态的转换表。用户侧动作另有限制（关闭后不能回复、不能再关闭或撤回），在各方法里判断；
// 客服可以从任何状态重新打开（回复、改状态），所以 closed 也能转出。
var transitions = map[string][]string{
	StatusOpen:         {StatusPendingUser, StatusPendingAgent, StatusEscalated, StatusResolved, StatusClosed},
	StatusPendingUser:  {StatusPendingUser, StatusPendingAgent, StatusEscalated, StatusResolved, StatusClosed},
	StatusPendingAgent: {StatusPendingUser, StatusPendingAgent, StatusEscalated, StatusResolved, StatusClosed},
	StatusEscalated:    {StatusPendingUser, StatusPendingAgent, StatusEscalated, StatusResolved, StatusClosed},
	StatusResolved:     {StatusPendingUser, StatusPendingAgent, StatusEscalated, StatusResolved, StatusClosed},
	StatusClosed:       {StatusPendingUser, StatusPendingAgent, StatusEscalated, StatusResolved, StatusClosed},
}

// CanTransition 报告工单能否从 from 转到 to。
func CanTransition(from, to string) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// agentSettable 是客服能直接设置的状态。pending_user 只由客服回复驱动：手工设成「等待用户」却没有回复，用户会一直等。
var agentSettable = map[string]bool{
	StatusResolved: true, StatusClosed: true, StatusEscalated: true, StatusPendingAgent: true,
}

// agentStatusCols 算客服把工单从 t 当前状态改成 to 时要写的列：
//   - resolved：resolved_at 保留已有、没有则 now；closed：保留 resolved_at；其它清空
//   - closed：closed_at 保留已有、没有则 now；其它清空
//   - closed_reason：转出 closed 时清空；原本就是 closed 时保留原因（没有则 agent_closed）；否则 agent_closed
//   - closed_note：closed → closed 时保留，其它清空
//   - escalated：escalated_at 保留已有、没有则 now，优先级至少 high；其它状态不动 escalated_at
func agentStatusCols(from, to string, resolvedAt, closedAt, escalatedAt *time.Time, closedReason, closedNote *string,
	priority string, now time.Time) map[string]any {
	cols := map[string]any{"status": to}
	switch to {
	case StatusResolved:
		cols["resolved_at"] = keepOr(resolvedAt, now)
	case StatusClosed:
		cols["resolved_at"] = resolvedAt
	default:
		cols["resolved_at"] = nil
	}
	if to == StatusClosed {
		cols["closed_at"] = keepOr(closedAt, now)
		reason := ClosedByAgent
		if from == StatusClosed && closedReason != nil {
			reason = *closedReason
		}
		cols["closed_reason"] = reason
		if from == StatusClosed {
			cols["closed_note"] = closedNote
		} else {
			cols["closed_note"] = nil
		}
	} else {
		cols["closed_at"] = nil
		cols["closed_reason"] = nil
		cols["closed_note"] = nil
	}
	if to == StatusEscalated {
		cols["escalated_at"] = keepOr(escalatedAt, now)
		cols["priority"] = escalatedPriority(priority)
	}
	return cols
}

// keepOr 返回已有时间，没有时返回 now。
func keepOr(t *time.Time, now time.Time) time.Time {
	if t != nil {
		return *t
	}
	return now
}
