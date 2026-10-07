package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 后台给订阅加流量（用户 2026-10-07 定：发一笔不过期的流量包）。
//
// 与礼品卡送流量同一套：traffic_pack_grants 里一笔余额，挂在订阅所属的用户身上、
// 不过期、用完为止；扣量顺序不变——每周期先扣套餐额度，再按先到先扣扣流量包
// （nodefabric 的 applyTrafficCharges）。来源记 admin（迁移 00129 放宽了 CHECK），
// source_id 是这次发放自己的 uuid，谁、为什么发的在审计里。
//
// 门槛与加时长相同：billing.adjustment.write → 近期重认证 → 独立的幂等 scope
// （router_users.go）。发流量包不是天然幂等的（每执行一次多一笔），所以审计与
// 幂等记录和发放写在同一个事务里，提交了却没记下幂等结果的话重放会再发一笔。

// SubscriptionTrafficGrantIdempotencyScope 是后台加流量包的幂等 scope，不与别的接口共用。
const SubscriptionTrafficGrantIdempotencyScope = "subscription_admin_traffic_grant"

const (
	// 一次最多发 10 TiB：再多基本是手滑多打了零
	maxAdminTrafficGrantBytes int64 = 10 << 40
	adminTrafficReasonMin           = 5
	adminTrafficReasonMax           = 500
)

type AdminTrafficGrantInput struct {
	SubscriptionID string
	ActorID        string
	Bytes          int64
	// Reason 写进审计：凭空给用户加流量必须留下理由
	Reason string
	Claim  middleware.IdempotencyClaim
}

// AdminTrafficGrantOutput 是加流量包的响应，也是幂等记录里回放的那一份。
type AdminTrafficGrantOutput struct {
	SubscriptionID string `json:"subscription_id"`
	UserID         string `json:"user_id"`
	UserEmail      string `json:"user_email"`
	GrantID        string `json:"grant_id"`
	GrantedBytes   int64  `json:"granted_bytes"`
	// RemainingBytesTotal 是发放之后这个用户全部流量包的剩余合计
	RemainingBytesTotal int64 `json:"remaining_bytes_total"`

	prepared httpx.PreparedResponse
}

func (o *AdminTrafficGrantOutput) PreparedResponse() httpx.PreparedResponse { return o.prepared }

// validateAdminTrafficGrant 校验字节数与原因，返回去掉首尾空白的原因。
func validateAdminTrafficGrant(bytes int64, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	fields := map[string]string{}
	if bytes < 1 || bytes > maxAdminTrafficGrantBytes {
		fields["bytes"] = "流量要大于 0，一次最多 10240 GB"
	}
	if n := utf8.RuneCountInString(reason); n < adminTrafficReasonMin || n > adminTrafficReasonMax {
		fields["reason"] = fmt.Sprintf("请写清加流量的原因，%d 到 %d 个字。这条会进审计",
			adminTrafficReasonMin, adminTrafficReasonMax)
	}
	if len(fields) > 0 {
		return "", httpx.Invalid(fields)
	}
	return reason, nil
}

// GrantTrafficPackAsAdmin 由管理员给指定订阅的用户发一笔不过期的流量包。
func (s *Service) GrantTrafficPackAsAdmin(ctx context.Context, tenantID string,
	in AdminTrafficGrantInput) (*AdminTrafficGrantOutput, error) {

	if err := middleware.ValidateIdempotencyClaim(
		in.Claim, tenantID, in.ActorID, SubscriptionTrafficGrantIdempotencyScope,
	); err != nil {
		return nil, fmt.Errorf("grant traffic pack: %w", err)
	}
	reason, err := validateAdminTrafficGrant(in.Bytes, in.Reason)
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(in.SubscriptionID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	sourceID, err := uuid.NewV7()
	if err != nil {
		return nil, httpx.Internal(err)
	}

	var out AdminTrafficGrantOutput
	actor := in.ActorID
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		// 只为找到订阅归谁；KEY SHARE 挡住并发删订阅，不挡续费、扣量对订阅行的更新
		err := tx.QueryRow(ctx, `
			SELECT s.user_id::text, u.email::text
			  FROM subscriptions s
			  JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
			 WHERE s.tenant_id = $1 AND s.id = $2::uuid
			 FOR KEY SHARE OF s`, tenantID, in.SubscriptionID).Scan(&out.UserID, &out.UserEmail)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}

		grantID, err := GrantTrafficPackTx(ctx, tx, tenantID, out.UserID, "admin", sourceID.String(), in.Bytes)
		if err != nil {
			return err
		}
		out.SubscriptionID = in.SubscriptionID
		out.GrantID = grantID
		out.GrantedBytes = in.Bytes
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(granted_bytes - consumed_bytes), 0)::bigint
			  FROM traffic_pack_grants WHERE tenant_id = $1 AND user_id = $2::uuid`,
			tenantID, out.UserID).Scan(&out.RemainingBytesTotal); err != nil {
			return err
		}

		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "subscription.traffic_granted", ResourceType: "subscription",
			ResourceID: &out.SubscriptionID,
			AfterDigest: map[string]any{
				"user_id": out.UserID, "grant_id": grantID, "granted_bytes": in.Bytes,
				"remaining_bytes_total": out.RemainingBytesTotal, "reason": reason,
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		}); err != nil {
			return err
		}

		prepared, err := httpx.PrepareJSON(http.StatusOK, out)
		if err != nil {
			return err
		}
		out.prepared = prepared
		return middleware.CompleteSuccessJSONInTx(ctx, tx, in.Claim, prepared)
	})
	if err != nil {
		return nil, err
	}
	// 流量用尽被摘下的用户拿到余额后重新进节点名单：提交后再通知节点
	s.notifyUsersChanged(ctx, tenantID)
	return &out, nil
}
