// [INPUT]: 依赖 gift_card_codes / gift_card_redemptions / gift_card_templates 表与 batches.go 的 MaskCode，依赖 platform/audit、platform/db、platform/httpx
// [OUTPUT]: 对外提供 Code、ListCodes、ToggleCode、Stats、Usage、ListUsages、PreviewCode
// [POS]: giftcard 的卡码读模型与单码操作：后台列表与兑换记录只回掩码，门户预览按码查模板；批次与导出在 batches.go，兑换在 redeem.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package giftcard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// Code 是卡码列表的一行。只给掩码：明文只在生码样例与一次性导出里出现（batches.go）。
type Code struct {
	ID         string     `json:"id"`
	CodeMasked string     `json:"code_masked"`
	Status     string     `json:"status"`
	BatchID    *string    `json:"batch_id,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	UsedEmail  string     `json:"used_email,omitempty"`
	UsedAt     *time.Time `json:"used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	TemplateID string     `json:"template_id"`
}

type ListCodesInput struct {
	TemplateID string
	Status     string
	BatchID    string
	Limit      int
	Offset     int
}

func (s *Service) ListCodes(ctx context.Context, tenantID string,
	in ListCodesInput) ([]Code, int64, error) {

	if in.Limit <= 0 || in.Limit > 5000 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	// 状态走白名单：这个值来自查询串，放任 % 进去会变成模糊匹配。
	switch in.Status {
	case "", "unused", "used", "disabled", "expired":
	default:
		return nil, 0, httpx.New(httpx.CodeBadRequest, "不支持的卡密状态")
	}
	for _, id := range []string{in.TemplateID, in.BatchID} {
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return nil, 0, httpx.New(httpx.CodeBadRequest, "标识符格式不正确")
			}
		}
	}

	out := []Code{}
	var total int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM gift_card_codes
			 WHERE tenant_id=$1
			   AND ($2='' OR template_id=$2::uuid)
			   AND ($3='' OR status=$3)
			   AND ($4='' OR batch_id=$4::uuid)`,
			tenantID, in.TemplateID, in.Status, in.BatchID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT c.id::text,c.code,c.status,c.batch_id::text,c.expires_at,
			       coalesce(u.email::text,''),c.used_at,c.created_at,c.template_id::text
			  FROM gift_card_codes c
			  LEFT JOIN users u ON u.tenant_id=c.tenant_id AND u.id=c.used_by
			 WHERE c.tenant_id=$1
			   AND ($2='' OR c.template_id=$2::uuid)
			   AND ($3='' OR c.status=$3)
			   AND ($4='' OR c.batch_id=$4::uuid)
			 ORDER BY c.created_at DESC, c.code
			 LIMIT $5 OFFSET $6`,
			tenantID, in.TemplateID, in.Status, in.BatchID, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Code
			var plain string
			if err := rows.Scan(&c.ID, &plain, &c.Status, &c.BatchID, &c.ExpiresAt,
				&c.UsedEmail, &c.UsedAt, &c.CreatedAt, &c.TemplateID); err != nil {
				return err
			}
			c.CodeMasked = MaskCode(plain)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, total, err
}

// ToggleCode 停用或恢复一个未使用的卡密。
//
// 已兑换的码不能改：那等于事后否认一次已经发生的发放。
// 要收回已发出的权益，得走对应的调账或工单，不是把码状态改回去。
func (s *Service) ToggleCode(ctx context.Context, tenantID, codeID string,
	disabled bool, actorID string) error {

	if _, err := uuid.Parse(codeID); err != nil {
		return httpx.NotFoundOrForbidden()
	}
	from, to := "disabled", "unused"
	if disabled {
		from, to = "unused", "disabled"
	}
	actor := actorID
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		var status, code string
		err := tx.QueryRow(ctx, `
			SELECT status, code FROM gift_card_codes
			 WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`,
			tenantID, codeID).Scan(&status, &code)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if status == "used" {
			return httpx.New(httpx.CodeConflict,
				"这个码已经被兑换了，不能停用。要收回权益请走调账或工单")
		}
		if status == "expired" {
			return httpx.New(httpx.CodeConflict, "这个码已经过期了")
		}
		if status != from {
			return httpx.New(httpx.CodeConflict, "这个码当前的状态不需要该操作")
		}
		tag, err := tx.Exec(ctx, `
			UPDATE gift_card_codes SET status=$3, updated_at=now()
			 WHERE tenant_id=$1 AND id=$2::uuid AND status=$4`,
			tenantID, codeID, to, from)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("gift card code toggle lost")
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "gift_card.code_toggled", ResourceType: "gift_card_code",
			ResourceID:   &codeID,
			BeforeDigest: map[string]any{"status": from},
			AfterDigest:  map[string]any{"status": to, "code": MaskCode(code)},
			APIDomain:    "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
}

type Stats struct {
	Templates   int   `json:"templates"`
	CodesTotal  int   `json:"codes_total"`
	CodesUsed   int   `json:"codes_used"`
	CodesUnused int   `json:"codes_unused"`
	BalanceOut  int64 `json:"balance_out"` // 已发出的余额合计（最小货币单位）
	TrafficOut  int64 `json:"traffic_out"` // 已发出的流量合计（字节）
}

func (s *Service) Stats(ctx context.Context, tenantID string) (*Stats, error) {
	var st Stats
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM gift_card_templates
			         WHERE tenant_id=$1 AND status<>'archived'),
			       (SELECT count(*) FROM gift_card_codes WHERE tenant_id=$1),
			       (SELECT count(*) FROM gift_card_codes
			         WHERE tenant_id=$1 AND status='used'),
			       (SELECT count(*) FROM gift_card_codes
			         WHERE tenant_id=$1 AND status='unused')`,
			tenantID).Scan(&st.Templates, &st.CodesTotal, &st.CodesUsed,
			&st.CodesUnused); err != nil {
			return err
		}
		// 发出去多少真金白银，是这一页最该被看见的数字。
		return tx.QueryRow(ctx, `
			SELECT coalesce(sum((granted->>'balance')::bigint), 0),
			       coalesce(sum((granted->>'traffic_bytes')::bigint), 0)
			  FROM gift_card_redemptions WHERE tenant_id=$1`,
			tenantID).Scan(&st.BalanceOut, &st.TrafficOut)
	})
	return &st, err
}

type Usage struct {
	TemplateName string    `json:"template_name"`
	CodeMasked   string    `json:"code_masked"`
	UserEmail    string    `json:"user_email"`
	Granted      Rewards   `json:"granted"`
	PrizeLabel   string    `json:"prize_label,omitempty"`
	RedeemedAt   time.Time `json:"redeemed_at"`
}

func (s *Service) ListUsages(ctx context.Context, tenantID, templateID string) ([]Usage, error) {
	if templateID != "" {
		if _, err := uuid.Parse(templateID); err != nil {
			return nil, httpx.New(httpx.CodeBadRequest, "标识符格式不正确")
		}
	}
	out := []Usage{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.name, c.code, coalesce(u.email::text,''), r.granted, r.redeemed_at
			  FROM gift_card_redemptions r
			  JOIN gift_card_templates t ON t.tenant_id=r.tenant_id AND t.id=r.template_id
			  JOIN gift_card_codes c ON c.tenant_id=r.tenant_id AND c.id=r.code_id
			  LEFT JOIN users u ON u.tenant_id=r.tenant_id AND u.id=r.user_id
			 WHERE r.tenant_id=$1 AND ($2='' OR r.template_id=$2::uuid)
			 ORDER BY r.redeemed_at DESC LIMIT 200`, tenantID, templateID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u Usage
			var raw []byte
			var plain string
			if err := rows.Scan(&u.TemplateName, &plain, &u.UserEmail, &raw,
				&u.RedeemedAt); err != nil {
				return err
			}
			u.CodeMasked = MaskCode(plain)
			var g grantedRecord
			_ = json.Unmarshal(raw, &g)
			u.PrizeLabel = g.PrizeLabel
			u.Granted = Rewards{Balance: g.Balance, TrafficBytes: g.TrafficBytes,
				ExpireDays: g.ExpireDays, ResetQuota: g.QuotaReset}
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}

// PreviewCode 让用户在兑换前看清这张卡送什么。
//
// 刻意不暴露卡是否存在：不存在、已用、已停用都回同一句话，
// 否则这个接口就成了免费的卡密探测器。
func (s *Service) PreviewCode(ctx context.Context, tenantID, code string) (*Template, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) < 8 || len(code) > 32 {
		return nil, ErrCodeUnusable
	}
	var out Template
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var templateID, status string
		var expiresAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT template_id::text, status, expires_at FROM gift_card_codes
			 WHERE tenant_id=$1 AND code=$2`, tenantID, code).
			Scan(&templateID, &status, &expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCodeUnusable
		}
		if err != nil {
			return err
		}
		if status != "unused" || (expiresAt != nil && expiresAt.Before(time.Now())) {
			return ErrCodeUnusable
		}
		if err := s.loadTemplate(ctx, tx, tenantID, templateID, &out); err != nil {
			return err
		}
		if out.Status != "active" {
			return ErrCodeUnusable
		}
		// 盲盒的奖池不回给用户：把权重摆出来等于公示中奖概率，
		// 而奖池里各项的金额也会让人算出期望值再决定要不要兑。
		// 只保留奖品名称，够用户知道能抽到什么。
		if out.Type == "mystery" {
			labels := make([]MysteryPrize, 0, len(out.Rewards.Pool))
			for _, p := range out.Rewards.Pool {
				labels = append(labels, MysteryPrize{Label: p.Label})
			}
			out.Rewards = Rewards{Pool: labels}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
