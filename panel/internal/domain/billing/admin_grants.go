package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 后台给订阅发东西（规划第 5 条）。现在只有加时长；后台加流量的形式待拍板
// （发不过期的流量包，还是加到本周期配额行），定了再放进这个文件。

// SubscriptionExtendIdempotencyScope 是后台加时长的幂等 scope，不与别的接口共用：
// 同一个键在两边会被当成重放。
const SubscriptionExtendIdempotencyScope = "subscription_admin_extend"

const (
	// 一次最多加十年：再多基本是手滑多打了零
	maxAdminExtendDays   = 3650
	adminExtendReasonMin = 5
	adminExtendReasonMax = 500
)

type AdminExtendInput struct {
	SubscriptionID string
	ActorID        string
	Days           int
	// Reason 写进审计与订阅事件：动用户的到期时间必须留下理由。
	Reason string
	Claim  middleware.IdempotencyClaim
}

// AdminExtendOutput 是加时长的响应，也是幂等记录里回放的那一份。
type AdminExtendOutput struct {
	SubscriptionID string    `json:"subscription_id"`
	UserEmail      string    `json:"user_email"`
	Days           int       `json:"days"`
	PreviousEnd    time.Time `json:"previous_end"`
	PeriodEnd      time.Time `json:"period_end"`

	prepared httpx.PreparedResponse
}

func (o *AdminExtendOutput) PreparedResponse() httpx.PreparedResponse {
	return o.prepared
}

// validateAdminExtend 校验天数与原因，返回去掉首尾空白的原因。
func validateAdminExtend(days int, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	fields := map[string]string{}
	if days < 1 || days > maxAdminExtendDays {
		fields["days"] = fmt.Sprintf("天数是 1 到 %d 之间的整数", maxAdminExtendDays)
	}
	// 按字数卡而不是字节：「补偿」两个汉字是 6 字节，说明不了任何事
	if n := utf8.RuneCountInString(reason); n < adminExtendReasonMin || n > adminExtendReasonMax {
		fields["reason"] = fmt.Sprintf("请写清加时长的原因，%d 到 %d 个字。这条会进审计",
			adminExtendReasonMin, adminExtendReasonMax)
	}
	if len(fields) > 0 {
		return "", httpx.Invalid(fields)
	}
	return reason, nil
}

// ExtendSubscriptionAsAdmin 由管理员给指定订阅加时长。
//
// 与礼品卡延期走同一个 extendSubscriptionTx：订阅周期末、本周期 cycle 配额行、
// active 凭据一起往后推，写 extended 订阅事件。审计与幂等记录和业务写在同一个
// 事务里：加时长不是天然幂等的（每执行一次多 N 天），提交了却没记下幂等结果的话，
// 重放会再加一次。
func (s *Service) ExtendSubscriptionAsAdmin(ctx context.Context, tenantID string,
	in AdminExtendInput) (*AdminExtendOutput, error) {

	if err := middleware.ValidateIdempotencyClaim(
		in.Claim, tenantID, in.ActorID, SubscriptionExtendIdempotencyScope,
	); err != nil {
		return nil, fmt.Errorf("extend subscription: %w", err)
	}
	reason, err := validateAdminExtend(in.Days, in.Reason)
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(in.SubscriptionID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}

	var out AdminExtendOutput
	actor := in.ActorID
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		var userID string
		err := tx.QueryRow(ctx, `
			SELECT s.user_id::text, u.email::text
			  FROM subscriptions s
			  JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
			 WHERE s.tenant_id = $1 AND s.id = $2::uuid
			 FOR UPDATE OF s`, tenantID, in.SubscriptionID).Scan(&userID, &out.UserEmail)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}

		change, err := extendSubscriptionTx(ctx, tx, subscriptionExtension{
			TenantID: tenantID, SubscriptionID: in.SubscriptionID, Days: in.Days,
			ActorKind: "admin", ActorID: &actor, Source: "admin", Reason: reason,
		})
		if err != nil {
			return err
		}
		out.SubscriptionID = in.SubscriptionID
		out.Days = in.Days
		out.PreviousEnd = change.PreviousEnd
		out.PeriodEnd = change.PeriodEnd

		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "subscription.extended", ResourceType: "subscription",
			ResourceID:   &out.SubscriptionID,
			BeforeDigest: map[string]any{"period_end": change.PreviousEnd},
			AfterDigest: map[string]any{
				"period_end": change.PeriodEnd, "days": in.Days,
				"reason": reason, "user_id": userID,
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
	// 已走过到期日的订阅延期后重新进节点名单：提交后再通知节点
	s.notifyUsersChanged(ctx, tenantID)
	return &out, nil
}
