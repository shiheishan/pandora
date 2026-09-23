package notify

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 通知模板的管理端读写。
//
// 模板表和渲染早就有了（Enqueue 按 code 找模板、投递时套变量），
// 缺的只是让管理员改内容的入口 —— 在此之前想改一句措辞得连数据库。
// 对应 Xboard 的 mail/template 那组接口（list/get/save/reset/test）。
//
// 这里刻意不做「新建模板」：模板的 code 是代码里写死的常量，
// 后台建一个 code 没人调用的模板毫无意义，只会让人以为配了就会发。

type TemplateRow struct {
	Code             string    `json:"code"`
	Channel          string    `json:"channel"`
	Locale           string    `json:"locale"`
	Category         string    `json:"category"`
	Status           string    `json:"status"`
	Version          int       `json:"version"`
	Subject          string    `json:"subject"`
	Body             string    `json:"body"`
	AllowedVariables []string  `json:"allowed_variables"`
	IsDefault        bool      `json:"is_default"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// templateDescription 让后台不只显示 code。
// 「subscription.expiring」看得懂，但「什么时候发、发给谁」得说明白，
// 否则管理员不敢改。
var templateDescription = map[string]string{
	"subscription.expiring": "套餐到期前提醒（由定时扫描触发，每个订阅每个提醒窗口只发一次）",
	"quota.warning":         "流量用量预警（用量越过阈值时触发）",
	"order.paid":            "订单支付成功后发给下单用户",
	"ticket.replied":        "工单被管理员回复后通知提单人",
}

func TemplateDescription(code string) string { return templateDescription[code] }

func (s *Service) ListTemplates(ctx context.Context, tenantID string) ([]TemplateRow, error) {
	out := []TemplateRow{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT code, channel, locale, category, status, version,
			       subject, body, allowed_variables, updated_at
			  FROM notification_templates
			 WHERE tenant_id = $1
			 ORDER BY code, channel`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t TemplateRow
			if err := rows.Scan(&t.Code, &t.Channel, &t.Locale, &t.Category, &t.Status,
				&t.Version, &t.Subject, &t.Body, &t.AllowedVariables, &t.UpdatedAt); err != nil {
				return err
			}
			if d, ok := defaultTemplates[t.Code+"|"+t.Channel]; ok {
				t.IsDefault = t.Subject == d.Subject && t.Body == d.Body
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

type SaveTemplateInput struct {
	Code    string
	Channel string
	Subject string
	Body    string
	ActorID string
}

var templateVarRe = regexp.MustCompile(`\{\{\s*([a-z_][a-z0-9_]*)\s*\}\}`)

// SaveTemplate 只允许改主题与正文。
//
// 变量白名单是硬约束：模板里写了 {{balance}} 而渲染时没人往 payload 里塞这个
// 键，用户收到的就是一封带着 "{{balance}}" 字样的邮件。与其等用户截图来问，
// 不如在保存的时候就挡掉。
func (s *Service) SaveTemplate(ctx context.Context, tenantID string,
	in SaveTemplateInput) (*TemplateRow, error) {

	in.Subject = strings.TrimSpace(in.Subject)
	in.Body = strings.TrimSpace(in.Body)
	if tenantID == "" || in.Code == "" || in.Channel == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "tenant, code and channel are required")
	}
	if in.Subject == "" || utf8.RuneCountInString(in.Subject) > 200 {
		return nil, httpx.Invalid(map[string]string{"subject": "主题必填，且不超过 200 字"})
	}
	if in.Body == "" || utf8.RuneCountInString(in.Body) > 20000 {
		return nil, httpx.Invalid(map[string]string{"body": "正文必填，且不超过 20000 字"})
	}

	var out TemplateRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var allowed []string
		var templateID, oldSubject, oldBody string
		var version int
		// 审计的 ResourceID 是 uuid 列，必须取模板自己的主键 ——
		// 模板在业务上按 (code, channel) 定位，但那不是 uuid，直接塞进去会 22P02。
		err := tx.QueryRow(ctx, `
			SELECT id::text, allowed_variables, subject, body, version
			  FROM notification_templates
			 WHERE tenant_id = $1 AND code = $2 AND channel = $3 AND locale = 'zh-CN'
			 FOR UPDATE`, tenantID, in.Code, in.Channel).
			Scan(&templateID, &allowed, &oldSubject, &oldBody, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}

		if bad := unknownVariables(in.Subject+"\n"+in.Body, allowed); len(bad) > 0 {
			return httpx.Invalid(map[string]string{
				"body": "用到了这个模板不提供的变量：" + strings.Join(bad, "、") +
					"。可用变量：" + strings.Join(allowed, "、"),
			})
		}

		err = tx.QueryRow(ctx, `
			UPDATE notification_templates
			   SET subject = $4, body = $5, version = version + 1, updated_at = now()
			 WHERE tenant_id = $1 AND code = $2 AND channel = $3 AND locale = 'zh-CN'
			 RETURNING code, channel, locale, category, status, version,
			           subject, body, allowed_variables, updated_at`,
			tenantID, in.Code, in.Channel, in.Subject, in.Body).
			Scan(&out.Code, &out.Channel, &out.Locale, &out.Category, &out.Status,
				&out.Version, &out.Subject, &out.Body, &out.AllowedVariables, &out.UpdatedAt)
		if err != nil {
			return err
		}
		if d, ok := defaultTemplates[out.Code+"|"+out.Channel]; ok {
			out.IsDefault = out.Subject == d.Subject && out.Body == d.Body
		}

		actor := in.ActorID
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "notification_template.updated", ResourceType: "notification_template",
			ResourceID: &templateID,
			BeforeDigest: map[string]any{
				"code": in.Code, "channel": in.Channel, "version": version,
				"subject": oldSubject, "body": oldBody,
			},
			AfterDigest: map[string]any{
				"code": in.Code, "channel": in.Channel, "version": out.Version,
				"subject": out.Subject, "body": out.Body,
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ResetTemplate 恢复出厂内容。
func (s *Service) ResetTemplate(ctx context.Context, tenantID, code, channel,
	actorID string) (*TemplateRow, error) {

	d, ok := defaultTemplates[code+"|"+channel]
	if !ok {
		return nil, httpx.New(httpx.CodeNotFound, "这个模板没有内置默认内容")
	}
	return s.SaveTemplate(ctx, tenantID, SaveTemplateInput{
		Code: code, Channel: channel, Subject: d.Subject, Body: d.Body, ActorID: actorID})
}

// RenderPreview 用示例值渲染，让管理员保存前能看到成品。
func RenderPreview(subject, body string, allowed []string) (string, string) {
	sample := map[string]string{
		"site": "潘多拉面板", "plan": "旗舰套餐", "days": "3",
		"expires_at": "2026-08-31 23:59", "percent": "85",
		"remaining": "3.2 GB", "order_no": "AO20260804-XXXXXX",
		"subject": "无法连接节点",
	}
	rep := func(s string) string {
		return templateVarRe.ReplaceAllStringFunc(s, func(m string) string {
			name := templateVarRe.FindStringSubmatch(m)[1]
			if v, ok := sample[name]; ok {
				return v
			}
			return m
		})
	}
	return rep(subject), rep(body)
}

func unknownVariables(text string, allowed []string) []string {
	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}
	seen := map[string]bool{}
	var bad []string
	for _, m := range templateVarRe.FindAllStringSubmatch(text, -1) {
		if !ok[m[1]] && !seen[m[1]] {
			seen[m[1]] = true
			bad = append(bad, m[1])
		}
	}
	sort.Strings(bad)
	return bad
}

type defaultTemplate struct{ Subject, Body string }

// defaultTemplates 与 migrations/00023_notification_seed.sql 保持一致。
//
// 之所以在代码里再存一份：种子迁移只在初装时跑一次，改过内容之后
// 没有任何地方还留着原文，"恢复默认"就无从谈起。两处一旦不同步，
// 后果是「恢复默认」恢复出一个从没存在过的版本 —— 所以改动其中一处时
// 务必同时改另一处。
var defaultTemplates = map[string]defaultTemplate{
	"subscription.expiring|inapp": {
		Subject: "套餐即将到期",
		Body:    "你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期。到期后节点会停止服务，记得及时续费。",
	},
	"subscription.expiring|email": {
		Subject: "【{{site}}】你的套餐 {{days}} 天后到期",
		Body: `你好，

你的「{{plan}}」将在 {{days}} 天后到期（{{expires_at}}）。
到期后节点将停止服务，请及时续费以免影响使用。

{{site}}`,
	},
	"quota.warning|inapp": {
		Subject: "流量即将用尽",
		Body:    "你的「{{plan}}」已使用 {{percent}}% 流量（剩余 {{remaining}}）。用尽后将无法连接节点。",
	},
	"quota.warning|email": {
		Subject: "【{{site}}】流量已使用 {{percent}}%",
		Body: `你好，

你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。
流量用尽后将无法连接节点，可在面板购买流量包或升级套餐。

{{site}}`,
	},
	"order.paid|inapp": {
		Subject: "支付成功",
		Body:    "订单 {{order_no}} 已支付成功，「{{plan}}」已开通，有效期至 {{expires_at}}。",
	},
	"ticket.replied|inapp": {
		Subject: "工单有新回复",
		Body:    "你的工单「{{subject}}」有新回复，点击查看。",
	},
}
