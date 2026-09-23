package giftcard

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 兑换。整个流程在一个 Serializable 事务里：
//
//   锁码行 → 校验状态与有效期 → 校验领取条件 → 校验每人次数与冷却
//   → 结算奖励（余额走账本 / 流量与到期改订阅）→ 写兑换流水 → 标记码已用 → 审计
//
// 顺序不能调：先发奖励再标记码，中间崩了就是「发了但码还能再兑」；
// 反过来先标记再发，崩了就是「码废了但什么都没拿到」。放在同一个事务里
// 两者才同生共死。

// ErrCodeUnusable 对所有「这个码不能用」的情况回同一句话。
//
// 区分「码不存在」和「码已被用」听起来更友好，实际上是把卡密库的状态
// 一点点泄漏给爆破的人：他能靠错误文案区分出哪些码真实存在。
var ErrCodeUnusable = httpx.New(httpx.CodeValidationFailed, "卡密无效、已被使用或已过期")

type RedeemResult struct {
	TemplateName string   `json:"template_name"`
	Type         string   `json:"type"`
	PrizeLabel   string   `json:"prize_label,omitempty"` // 盲盒抽中的奖品名
	Balance      int64    `json:"balance,omitempty"`
	TrafficBytes int64    `json:"traffic_bytes,omitempty"`
	ExpireDays   int      `json:"expire_days,omitempty"`
	QuotaReset   bool     `json:"quota_reset,omitempty"`
	PlanGranted  string   `json:"plan_granted,omitempty"`
	Summary      []string `json:"summary"` // 给用户看的人话
}

func (s *Service) Redeem(ctx context.Context, tenantID, userID, code string) (*RedeemResult, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if tenantID == "" || userID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "tenant and user are required")
	}
	if _, err := uuid.Parse(userID); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "user identifier is invalid")
	}
	if len(code) < 8 || len(code) > 32 {
		return nil, ErrCodeUnusable
	}

	var out RedeemResult
	expired := false
	err := s.pool.InTxSerializable(ctx,
		db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {

			// 1) 锁住码行。FOR UPDATE 让并发兑换同一个码的请求排队，
			//    后到的那个会看到 status 已经变成 used。
			var codeID, templateID, status string
			var expiresAt *time.Time
			err := tx.QueryRow(ctx, `
				SELECT id::text, template_id::text, status, expires_at
				  FROM gift_card_codes
				 WHERE tenant_id=$1 AND code=$2
				 FOR UPDATE`, tenantID, code).
				Scan(&codeID, &templateID, &status, &expiresAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrCodeUnusable
			}
			if err != nil {
				return err
			}
			if status != "unused" {
				return ErrCodeUnusable
			}
			if expiresAt != nil && expiresAt.Before(time.Now()) {
				// 顺手把状态改成 expired：不改的话每次兑换都要重新算一遍时间，
				// 管理员在后台也看不出哪些码已经作废。
				if _, err := tx.Exec(ctx, `
					UPDATE gift_card_codes SET status='expired', updated_at=now()
					 WHERE tenant_id=$1 AND id=$2::uuid AND status='unused'`,
					tenantID, codeID); err != nil {
					return err
				}
				expired = true
				return nil // 提交 expired 状态，事务外再返回不可用错误
			}

			// 2) 读模板
			var t Template
			if err := s.loadTemplate(ctx, tx, tenantID, templateID, &t); err != nil {
				return err
			}
			if t.Status != "active" {
				return httpx.New(httpx.CodeValidationFailed, "这个礼品卡活动已经停用了")
			}

			// 3) 领取条件
			if err := s.checkConditions(ctx, tx, tenantID, userID, t.Conditions); err != nil {
				return err
			}

			// 4) 每人次数与冷却
			if err := s.checkLimits(ctx, tx, tenantID, userID, templateID, t.Limits); err != nil {
				return err
			}

			// 5) 结算奖励
			granted, err := s.applyRewards(ctx, tx, tenantID, userID, t, &out)
			if err != nil {
				return err
			}
			out.TemplateName = t.Name
			out.Type = t.Type

			grantedJSON, err := json.Marshal(granted)
			if err != nil {
				return err
			}

			// 6) 兑换流水。UNIQUE(code_id) 是最后一道防线 ——
			//    上面的行锁万一因为某种原因没生效，这里也会撞唯一约束。
			var ledgerTxn any
			if granted.LedgerTxnID != "" {
				ledgerTxn = granted.LedgerTxnID
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO gift_card_redemptions
					(tenant_id,code_id,template_id,user_id,granted,ledger_txn_id)
				VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5::jsonb,$6::uuid)`,
				tenantID, codeID, templateID, userID, string(grantedJSON), ledgerTxn); err != nil {
				if db.IsUniqueViolation(err) {
					return ErrCodeUnusable
				}
				return err
			}

			// 7) 标记码已用
			tag, err := tx.Exec(ctx, `
				UPDATE gift_card_codes
				   SET status='used', used_by=$3::uuid, used_at=now(), updated_at=now()
				 WHERE tenant_id=$1 AND id=$2::uuid AND status='unused'`,
				tenantID, codeID, userID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("gift card code transition lost")
			}

			if err := audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "user", ActorID: &userID,
				Action: "gift_card.redeemed", ResourceType: "gift_card_code",
				ResourceID: &codeID,
				AfterDigest: map[string]any{
					"template": t.Name, "type": t.Type, "granted": granted,
				},
				APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
			}); err != nil {
				return err
			}
			return plugin.EmitGiftCardRedeemed(ctx, tx, tenantID, codeID, userID,
				t.Name, t.Type, granted)
		})
	if err != nil {
		// Serializable 隔离下，并发兑换同一个码会让后到的事务收到 40001。
		// 那不是系统故障，是「有人比你先兑走了」—— 回 500「服务暂时不可用」
		// 会让用户以为面板坏了，然后反复重试。
		if db.IsSerializationFailure(err) {
			return nil, ErrCodeUnusable
		}
		return nil, err
	}
	if expired {
		return nil, ErrCodeUnusable
	}
	return &out, nil
}

type grantedRecord struct {
	PrizeLabel   string `json:"prize_label,omitempty"`
	Balance      int64  `json:"balance,omitempty"`
	TrafficBytes int64  `json:"traffic_bytes,omitempty"`
	ExpireDays   int    `json:"expire_days,omitempty"`
	QuotaReset   bool   `json:"quota_reset,omitempty"`
	PlanID       string `json:"plan_id,omitempty"`
	OrderID      string `json:"order_id,omitempty"`
	LedgerTxnID  string `json:"ledger_txn_id,omitempty"`
}

func (s *Service) applyRewards(ctx context.Context, tx pgx.Tx, tenantID, userID string,
	t Template, out *RedeemResult) (grantedRecord, error) {

	var g grantedRecord
	r := t.Rewards

	if t.Type == "mystery" {
		prize, err := drawPrize(r.Pool)
		if err != nil {
			return g, err
		}
		g.PrizeLabel = prize.Label
		out.PrizeLabel = prize.Label
		r = Rewards{
			Balance:      prize.Balance,
			TrafficBytes: prize.TrafficBytes,
			ExpireDays:   prize.ExpireDays,
		}
	}

	if t.Type == "plan" {
		orderID, err := s.grant.GrantPlan(ctx, tx, tenantID, userID,
			r.PlanID, r.PriceID, "礼品卡兑换："+t.Name)
		if err != nil {
			return g, err
		}
		g.PlanID = r.PlanID
		g.OrderID = orderID
		out.PlanGranted = t.Name
		out.Summary = append(out.Summary, "已为你开通「"+t.Name+"」")
		return g, nil
	}

	if r.Balance > 0 {
		txnID, err := s.grant.GrantBalance(ctx, tx, tenantID, userID, r.Balance,
			"CNY", "礼品卡兑换："+t.Name)
		if err != nil {
			return g, err
		}
		g.Balance = r.Balance
		g.LedgerTxnID = txnID
		out.Balance = r.Balance
		out.Summary = append(out.Summary, "余额 +"+formatMoney(r.Balance))
	}
	if r.TrafficBytes > 0 {
		if err := s.grant.GrantTraffic(ctx, tx, tenantID, userID, r.TrafficBytes); err != nil {
			return g, err
		}
		g.TrafficBytes = r.TrafficBytes
		out.TrafficBytes = r.TrafficBytes
		out.Summary = append(out.Summary, "流量 +"+formatBytes(r.TrafficBytes))
	}
	if r.ExpireDays > 0 {
		if err := s.grant.ExtendExpiry(ctx, tx, tenantID, userID, r.ExpireDays); err != nil {
			return g, err
		}
		g.ExpireDays = r.ExpireDays
		out.ExpireDays = r.ExpireDays
		out.Summary = append(out.Summary, "到期时间延长 "+itoa(r.ExpireDays)+" 天")
	}
	if r.ResetQuota {
		if err := s.grant.ResetQuota(ctx, tx, tenantID, userID); err != nil {
			return g, err
		}
		g.QuotaReset = true
		out.QuotaReset = true
		out.Summary = append(out.Summary, "本周期已用流量已清零")
	}
	if len(out.Summary) == 0 {
		// 到这里还什么都没发，说明模板校验漏了什么。宁可报错也不能
		// 让用户收到一张「兑换成功」却什么都没到账的回执。
		return g, errors.New("gift card granted nothing")
	}
	return g, nil
}

// drawPrize 按权重抽奖。
//
// 用 crypto/rand 而不是 math/rand：奖池是有实际价值的，
// 可预测的随机数意味着有人能算出什么时候去兑能中大奖。
func drawPrize(pool []MysteryPrize) (MysteryPrize, error) {
	total := 0
	for _, p := range pool {
		total += p.Weight
	}
	if total <= 0 {
		return MysteryPrize{}, errors.New("mystery pool has no positive weight")
	}

	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return MysteryPrize{}, err
	}
	// 取模会让靠前的区间略微偏高（模偏差）。奖池权重合计通常是几十到几百，
	// 相对 2^64 而言偏差在 1e-17 量级，远小于运营调权重的精度，可以接受。
	n := int(binary.BigEndian.Uint64(buf[:]) % uint64(total))

	acc := 0
	for _, p := range pool {
		acc += p.Weight
		if n < acc {
			return p, nil
		}
	}
	return pool[len(pool)-1], nil
}

func (s *Service) checkConditions(ctx context.Context, tx pgx.Tx,
	tenantID, userID string, c Conditions) error {

	if c.NewUserOnly || c.PaidUserOnly {
		var paidCount int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM orders
			 WHERE tenant_id=$1 AND user_id=$2::uuid
			   AND status IN ('paid','fulfilled') AND payable_amount > 0`,
			tenantID, userID).Scan(&paidCount); err != nil {
			return err
		}
		if c.NewUserOnly && paidCount > 0 {
			return httpx.New(httpx.CodeValidationFailed, "这张卡只能新用户使用")
		}
		if c.PaidUserOnly && paidCount == 0 {
			return httpx.New(httpx.CodeValidationFailed, "这张卡需要有过付费记录才能使用")
		}
	}

	if len(c.AllowedPlanID) > 0 {
		var ok bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM subscriptions
				 WHERE tenant_id=$1 AND user_id=$2::uuid AND status='active'
				   AND plan_id::text = ANY($3))`,
			tenantID, userID, c.AllowedPlanID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return httpx.New(httpx.CodeValidationFailed, "你当前的套餐不在这张卡的适用范围内")
		}
	}

	if c.RequireInvite {
		var ok bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM referrals
			                WHERE tenant_id=$1 AND referee_user_id=$2::uuid)`,
			tenantID, userID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return httpx.New(httpx.CodeValidationFailed, "这张卡只对通过邀请注册的用户开放")
		}
	}
	return nil
}

func (s *Service) checkLimits(ctx context.Context, tx pgx.Tx,
	tenantID, userID, templateID string, l Limits) error {

	if l.MaxUsePerUser > 0 {
		var used int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM gift_card_redemptions
			 WHERE tenant_id=$1 AND user_id=$2::uuid AND template_id=$3::uuid`,
			tenantID, userID, templateID).Scan(&used); err != nil {
			return err
		}
		if used >= l.MaxUsePerUser {
			return httpx.New(httpx.CodeValidationFailed,
				"这个活动你已经兑换过了，每人限 "+itoa(l.MaxUsePerUser)+" 次")
		}
	}

	if l.CooldownHours > 0 {
		var last *time.Time
		if err := tx.QueryRow(ctx, `
			SELECT max(redeemed_at) FROM gift_card_redemptions
			 WHERE tenant_id=$1 AND user_id=$2::uuid AND template_id=$3::uuid`,
			tenantID, userID, templateID).Scan(&last); err != nil {
			return err
		}
		if last != nil {
			next := last.Add(time.Duration(l.CooldownHours) * time.Hour)
			if next.After(time.Now()) {
				return httpx.New(httpx.CodeValidationFailed,
					"兑换太频繁了，请在 "+next.Format("2006-01-02 15:04")+" 之后再试")
			}
		}
	}
	return nil
}

//-----------------------------------------------------------------------------
// 用户端查询
//-----------------------------------------------------------------------------

type MyRedemption struct {
	TemplateName string    `json:"template_name"`
	Type         string    `json:"type"`
	PrizeLabel   string    `json:"prize_label,omitempty"`
	Balance      int64     `json:"balance,omitempty"`
	TrafficBytes int64     `json:"traffic_bytes,omitempty"`
	ExpireDays   int       `json:"expire_days,omitempty"`
	RedeemedAt   time.Time `json:"redeemed_at"`
}

func (s *Service) MyRedemptions(ctx context.Context, tenantID, userID string) ([]MyRedemption, error) {
	out := []MyRedemption{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.name, t.type, r.granted, r.redeemed_at
			  FROM gift_card_redemptions r
			  JOIN gift_card_templates t
			    ON t.tenant_id=r.tenant_id AND t.id=r.template_id
			 WHERE r.tenant_id=$1 AND r.user_id=$2::uuid
			 ORDER BY r.redeemed_at DESC LIMIT 100`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m MyRedemption
			var raw []byte
			if err := rows.Scan(&m.TemplateName, &m.Type, &raw, &m.RedeemedAt); err != nil {
				return err
			}
			var g grantedRecord
			_ = json.Unmarshal(raw, &g)
			m.PrizeLabel = g.PrizeLabel
			m.Balance = g.Balance
			m.TrafficBytes = g.TrafficBytes
			m.ExpireDays = g.ExpireDays
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func formatMoney(minor int64) string {
	return "¥" + itoa(int(minor/100)) + "." +
		string([]byte{byte('0' + (minor%100)/10), byte('0' + minor%10)})
}

func formatBytes(b int64) string {
	const gb = 1 << 30
	const mb = 1 << 20
	if b >= gb {
		return itoa(int(b/gb)) + " GB"
	}
	return itoa(int(b/mb)) + " MB"
}
