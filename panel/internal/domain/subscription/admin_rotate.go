package subscription

import (
	"context"

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
	// Token 是新的订阅令牌明文。只在这一次返回 —— 库里只存哈希与信封密文。
	Token string
	// UserEmail 方便界面直接显示「已为 xxx 换好」。
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

	// 先查出订阅归谁 —— Rotate 内部要用它做所有权校验，这里由管理员代为
	// 提供，而不是绕过那道校验。绕过去的话，一个拼错的 subID 就会静默地
	// 换掉别人的链接。
	var ownerID, email string
	if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT s.user_id::text, u.email::text
			  FROM subscriptions s JOIN users u ON u.id = s.user_id
			 WHERE s.tenant_id = $1 AND s.id = $2::uuid`,
			tenantID, in.SubscriptionID).Scan(&ownerID, &email)
	}); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}

	token, err := s.Rotate(ctx, tenantID, ownerID, in.SubscriptionID)
	if err != nil {
		if err == ErrNotFound {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}

	// 审计单独一笔事务：Rotate 已经提交了，这里失败不该把换发回滚掉，
	// 但要如实报出来 —— 没有审计的凭据变更等于没人负责。
	actor := in.ActorID
	sub := in.SubscriptionID
	if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
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
		}); err != nil {
		return nil, httpx.New(httpx.CodeInternal,
			"订阅链接已换发，但审计写入失败，请联系运维核对："+err.Error())
	}

	return &AdminRotateOutput{Token: token, UserEmail: email}, nil
}
