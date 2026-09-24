// [INPUT]: 依赖 traffic_reset_logs / quota_balances 表与 platform/audit、platform/db、platform/httpx
// [OUTPUT]: 对外提供 LogTrafficReset、ListTrafficResets、TrafficResetStats、ManualResetTraffic 及其类型
// [POS]: billing 的流量重置：只清套餐配额的已用量并留痕；流量包余额挂用户，重置不碰（D-E-1）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 流量重置的记录与手动触发（对标 Xboard traffic-reset）。
//
// 重置以前发生得静悄悄，四条路都直接改 consumed 不留痕：
// 续费、周期滚动、礼品卡、管理员手动。用户问「我的流量怎么变了」
// 时谁也答不上来。现在每一次都落一条日志。

// LogTrafficReset 记录一次重置。
//
// 接收调用方的 tx —— 日志必须和重置本身同生共死。
// 分开写的话会出现「流量清了但没记录」或者反过来，
// 而这张表存在的全部意义就是它和事实一致。
func LogTrafficReset(ctx context.Context, tx pgx.Tx, tenantID, subscriptionID,
	userID, metric, reason string, consumedBefore int64, actorID *string, note string) error {

	_, err := tx.Exec(ctx, `
		INSERT INTO traffic_reset_logs
			(tenant_id, subscription_id, user_id, metric, reason,
			 consumed_before, consumed_after, actor_id, note)
		VALUES ($1,$2::uuid,$3::uuid,$4,$5,$6,0,$7::uuid,NULLIF($8,''))`,
		tenantID, subscriptionID, userID, metric, reason,
		consumedBefore, actorID, note)
	return err
}

type ResetLog struct {
	ID             string    `json:"id"`
	UserEmail      string    `json:"user_email"`
	PlanName       string    `json:"plan_name,omitempty"`
	Metric         string    `json:"metric"`
	Reason         string    `json:"reason"`
	ConsumedBefore int64     `json:"consumed_before"`
	ActorEmail     string    `json:"actor_email,omitempty"`
	Note           string    `json:"note,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type ListResetLogsInput struct {
	UserID string
	Reason string
	Limit  int
	Offset int
}

func (s *Service) ListTrafficResets(ctx context.Context, tenantID string,
	in ListResetLogsInput) ([]ResetLog, int64, error) {

	if in.Limit <= 0 || in.Limit > 200 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	switch in.Reason {
	case "", "renewal", "cycle_roll", "manual", "gift_card":
	default:
		return nil, 0, httpx.New(httpx.CodeBadRequest, "不支持的重置原因")
	}
	if in.UserID != "" {
		if _, err := uuid.Parse(in.UserID); err != nil {
			return nil, 0, httpx.New(httpx.CodeBadRequest, "用户标识格式不正确")
		}
	}

	out := []ResetLog{}
	var total int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM traffic_reset_logs
			 WHERE tenant_id=$1
			   AND ($2='' OR user_id=$2::uuid)
			   AND ($3='' OR reason=$3)`,
			tenantID, in.UserID, in.Reason).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT l.id::text, coalesce(u.email::text,''), coalesce(p.name,''),
			       l.metric, l.reason, l.consumed_before,
			       coalesce(a.email::text,''), coalesce(l.note,''), l.created_at
			  FROM traffic_reset_logs l
			  LEFT JOIN users u ON u.tenant_id=l.tenant_id AND u.id=l.user_id
			  LEFT JOIN users a ON a.tenant_id=l.tenant_id AND a.id=l.actor_id
			  LEFT JOIN subscriptions s ON s.tenant_id=l.tenant_id AND s.id=l.subscription_id
			  LEFT JOIN plans p ON p.tenant_id=s.tenant_id AND p.id=s.plan_id
			 WHERE l.tenant_id=$1
			   AND ($2='' OR l.user_id=$2::uuid)
			   AND ($3='' OR l.reason=$3)
			 ORDER BY l.created_at DESC
			 LIMIT $4 OFFSET $5`,
			tenantID, in.UserID, in.Reason, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ResetLog
			if err := rows.Scan(&r.ID, &r.UserEmail, &r.PlanName, &r.Metric,
				&r.Reason, &r.ConsumedBefore, &r.ActorEmail, &r.Note,
				&r.CreatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, total, err
}

type ResetStats struct {
	Last30Days  int            `json:"last_30_days"`
	ByReason    map[string]int `json:"by_reason"`
	FreedBytes  int64          `json:"freed_bytes"` // 30 天内被清掉的已用量合计
	ManualCount int            `json:"manual_count"`
}

func (s *Service) TrafficResetStats(ctx context.Context, tenantID string) (*ResetStats, error) {
	st := &ResetStats{ByReason: map[string]int{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT reason, count(*), coalesce(sum(consumed_before),0)
			  FROM traffic_reset_logs
			 WHERE tenant_id=$1 AND created_at > now() - interval '30 days'
			 GROUP BY reason`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var reason string
			var n int
			var freed int64
			if err := rows.Scan(&reason, &n, &freed); err != nil {
				return err
			}
			st.ByReason[reason] = n
			st.Last30Days += n
			st.FreedBytes += freed
			if reason == "manual" {
				st.ManualCount = n
			}
		}
		return rows.Err()
	})
	return st, err
}

type ManualResetInput struct {
	UserID  string
	ActorID string
	Note    string
}

// ManualResetTraffic 把用户当前订阅的已用流量清零。
//
// 这是个会直接改变用户可用额度的动作，所以要理由、要审计、要落日志。
// 只清 consumed，不动 limit_value —— 那是「给了多少」，重置改的是「用了多少」。
// 流量包余额挂在用户身上（traffic_pack_grants），重置不碰它（D-E-1）。
func (s *Service) ManualResetTraffic(ctx context.Context, tenantID string,
	in ManualResetInput) (int64, error) {

	in.Note = strings.TrimSpace(in.Note)
	if _, err := uuid.Parse(in.UserID); err != nil {
		return 0, httpx.NotFoundOrForbidden()
	}
	if _, err := uuid.Parse(in.ActorID); err != nil {
		return 0, httpx.New(httpx.CodeBadRequest, "操作人标识不正确")
	}
	if n := utf8.RuneCountInString(in.Note); n < 5 || n > 500 {
		return 0, httpx.Invalid(map[string]string{
			"note": "请写清重置原因，5 到 500 个字"})
	}

	var freed int64
	actor := in.ActorID
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		var subID string
		err := tx.QueryRow(ctx, `
			SELECT id::text FROM subscriptions
			 WHERE tenant_id=$1 AND user_id=$2::uuid AND status='active'
			 ORDER BY current_period_end DESC NULLS LAST
			 LIMIT 1 FOR UPDATE`, tenantID, in.UserID).Scan(&subID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeValidationFailed, "这个用户没有生效中的订阅")
		}
		if err != nil {
			return err
		}

		// 先读出清零前的用量：日志要靠它回答「当时用了多少」，
		// UPDATE 之后就再也读不到了。
		if err := tx.QueryRow(ctx, `
			SELECT consumed FROM quota_balances
			 WHERE tenant_id=$1 AND subscription_id=$2::uuid AND metric='traffic.bytes'
			 FOR UPDATE`, tenantID, subID).Scan(&freed); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeValidationFailed, "这个订阅没有流量配额")
			}
			return err
		}

		// remaining 是生成列，跟着 consumed 自己变，不能也不必手写
		tag, err := tx.Exec(ctx, `
			UPDATE quota_balances
			   SET consumed = 0, notified_thresholds = '{}',
			       overage_applied_at = NULL, updated_at = now()
			 WHERE tenant_id=$1 AND subscription_id=$2::uuid AND metric='traffic.bytes'`,
			tenantID, subID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("traffic reset transition lost")
		}

		if err := LogTrafficReset(ctx, tx, tenantID, subID, in.UserID,
			"traffic.bytes", "manual", freed, &actor, in.Note); err != nil {
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "traffic.manual_reset", ResourceType: "subscription",
			ResourceID:   &subID,
			BeforeDigest: map[string]any{"consumed": freed},
			AfterDigest: map[string]any{
				"consumed": 0, "user_id": in.UserID, "note": in.Note,
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	return freed, err
}
