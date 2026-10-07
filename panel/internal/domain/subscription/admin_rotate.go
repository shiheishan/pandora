package subscription

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 管理员替用户换一条订阅链接。
//
// 用户自己有 POST /v1/me/subscriptions/{id}/rotate，但它带所有权校验
// （owner != userID 就当不存在），管理员用不了。而订阅链接泄露恰恰是
// 用户自己最搞不定的事：他把链接发给朋友、或者截图发到群里，回头发现
// 流量被别人用光，来找客服——客服这边没有任何按钮。
//
// 和管理员重置密码是同一类需求：用户搞不定时，客服要能兜底。
//
// 换完之后旧链接立刻失效，用户的客户端要重新导入。这个后果必须在界面上
// 说清楚，否则客服会以为只是「刷新一下」。
//
// 新令牌明文不交给管理员（保留规则 2：后台看不到用户的订阅地址）。
// 用户到门户重新复制即可；明文在这里就丢弃，不经过处理器，
// 这样任何一个 admin 响应都不可能把它带出去。

type AdminRotateInput struct {
	SubscriptionID string
	ActorID        string
	// Reason 会写进审计。动别人的订阅凭据必须留下理由。
	Reason    string
	APIDomain string
	IP        string
	UserAgent string
}

type AdminRotateOutput struct {
	// UserEmail 方便界面直接显示「已为 xxx 换好」。
	// 刻意没有令牌字段，见文件头注释。
	UserEmail string
}

// AdminRotate 由管理员为指定订阅换发新的订阅链接。
func (s *Service) AdminRotate(ctx context.Context, tenantID string,
	in AdminRotateInput) (*AdminRotateOutput, error) {

	if tenantID == "" || in.SubscriptionID == "" || in.ActorID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "缺少租户、管理员或订阅")
	}
	if _, err := uuid.Parse(in.SubscriptionID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}

	// 先查出订阅归谁 —— rotateInTx 内部要用它做所有权校验，这里由管理员代为
	// 提供，而不是绕过那道校验。绕过去的话，一个拼错的 subID 就会静默地
	// 换掉别人的链接。
	//
	// 换发与审计在同一个事务里（审计台账 2.3 第 4 条）：原来审计单独一笔事务，换发已经
	// 提交、审计却可能写不进去，留下一次没人负责的凭据变更。
	var email string
	actor := in.ActorID
	sub := in.SubscriptionID
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		var ownerID string
		if err := tx.QueryRow(ctx, `
			SELECT s.user_id::text, u.email::text
			  FROM subscriptions s JOIN users u ON u.id = s.user_id
			 WHERE s.tenant_id = $1 AND s.id = $2::uuid`,
			tenantID, sub).Scan(&ownerID, &email); err != nil {
			return httpx.NotFoundOrForbidden()
		}
		if _, err := s.rotateInTx(ctx, tx, tenantID, ownerID, sub, false); err != nil {
			if errors.Is(err, ErrNotFound) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind:    "admin",
			ActorID:      &actor,
			Action:       "subscription.link_rotated_by_admin",
			ResourceType: "subscription",
			ResourceID:   &sub,
			APIDomain:    in.APIDomain,
			Outcome:      "success",
			RequestID:    httpx.RequestIDFrom(ctx),
			SourceIP:     in.IP,
			UserAgent:    in.UserAgent,
			AfterDigest: map[string]any{
				"target_email":  email,
				"reason":        in.Reason,
				"old_revoked":   true,
				"user_must_ref": "客户端需重新导入订阅",
			},
		})
	})
	if err != nil {
		return nil, err
	}

	return &AdminRotateOutput{UserEmail: email}, nil
}
