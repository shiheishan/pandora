package adminops

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 群发邮件（对标 Xboard user/sendMail）。
//
// 走既有的 notification_deliveries 队列，不在请求里直接连 SMTP：
//   · 几百封信同步发会把 HTTP 请求拖到超时，而前面已经发出去的收不回来
//   · 队列有重试、有投递状态，发失败的能查出来是谁
//   · 用户关掉了「营销通知」的偏好会被自动跳过 —— 直接发 SMTP 绕过这一层，
//     等于把用户的退订当没看见
//
// 代价是发送变成异步的：接口返回的是「排了多少封」，不是「发成功多少封」。
// 这是对的 —— 邮件本来就没有同步的成功。

const bulkMailTemplateCode = "admin.broadcast"

type BulkMailInput struct {
	Filter  BulkFilter
	Subject string
	Body    string
	ActorID string
}

type BulkMailResult struct {
	Queued  int `json:"queued"`
	Skipped int `json:"skipped"` // 关掉了营销通知偏好的人
}

func (s *Service) SendBulkMail(ctx context.Context, tenantID string,
	in BulkMailInput) (*BulkMailResult, error) {

	in.Subject = strings.TrimSpace(in.Subject)
	in.Body = strings.TrimSpace(in.Body)
	if err := in.Filter.validate(); err != nil {
		return nil, err
	}
	if n := utf8.RuneCountInString(in.Subject); n < 1 || n > 200 {
		return nil, httpx.Invalid(map[string]string{"subject": "主题必填，不超过 200 字"})
	}
	if n := utf8.RuneCountInString(in.Body); n < 1 || n > 20000 {
		return nil, httpx.Invalid(map[string]string{"body": "正文必填，不超过 20000 字"})
	}
	if _, err := uuid.Parse(in.ActorID); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "操作人标识不正确")
	}

	out := &BulkMailResult{}
	actor := in.ActorID
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		// 群发用一次性的 dedupe 前缀：同一批次里同一个人只会收到一封，
		// 而下一次群发（不同批次）不受影响。
		batch := uuid.New().String()

		var total int
		var args []any
		where := buildFilterSQL(in.Filter, &args, tenantID)
		if err := tx.QueryRow(ctx,
			"SELECT count(*) FROM users u"+where, args...).Scan(&total); err != nil {
			return err
		}
		if total == 0 {
			return httpx.New(httpx.CodeValidationFailed,
				"这个筛选条件下没有用户，先用预览确认一下")
		}
		if total > 20000 {
			return httpx.New(httpx.CodeValidationFailed,
				"一次最多群发 20000 人，请缩小范围分批发送")
		}

		// 一条 INSERT ... SELECT 把整批排进队列。
		//
		// 偏好检查写在 SQL 里而不是 Go 循环里：几千个用户逐个查偏好
		// 会产生几千次往返，而这整件事本来就该是一次集合运算。
		//
		// category 用 marketing：这类信是运营内容，用户有权关掉。
		// 走 transactional 能绕过偏好，但那是给「你的密码改了」这种
		// 必须送达的通知准备的，拿来发促销是滥用。
		// 参数顺序与下标：buildFilterSQL 已经占掉了 $1..$len(args)，
		// 这四个接在后面。每一个都必须在 SQL 里被引用 ——
		// 传了却不用的参数会让 Postgres 推断不出类型（42P18）。
		args = append(args, batch, in.Subject, in.Body, bulkMailTemplateCode)
		bi := len(args) - 3
		tag, err := tx.Exec(ctx, `
			INSERT INTO notification_deliveries
				(tenant_id, user_id, template_code, channel, dedupe_key,
				 payload, status, max_attempts, next_retry_at)
			SELECT u.tenant_id, u.id, $`+itoa(bi+3)+`::text, 'email',
			       'bulk:' || $`+itoa(bi)+`::text || ':' || u.id::text,
			       jsonb_build_object('subject', $`+itoa(bi+1)+`::text,
			                          'body', $`+itoa(bi+2)+`::text),
			       'queued', 5, now()
			  FROM users u`+where+`
			   AND COALESCE((SELECT np.enabled FROM notification_preferences np
			                  WHERE np.tenant_id = u.tenant_id AND np.user_id = u.id
			                    AND np.category = 'marketing' AND np.channel = 'email'),
			                true)
			ON CONFLICT DO NOTHING`, args...)
		if err != nil {
			return err
		}
		out.Queued = int(tag.RowsAffected())
		out.Skipped = total - out.Queued

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "user.bulk_mail_sent", ResourceType: "notification_delivery",
			AfterDigest: map[string]any{
				"batch": batch, "matched": total,
				"queued": out.Queued, "skipped_by_preference": out.Skipped,
				"subject": in.Subject,
				"filter": map[string]any{
					"status": in.Filter.Status, "group_id": in.Filter.GroupID,
					"query": in.Filter.Query,
				},
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
