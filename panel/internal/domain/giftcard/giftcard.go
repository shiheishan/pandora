// [INPUT]: 依赖 gift_card_templates / gift_card_codes / gift_card_batches 表，依赖 billing 经 Granter 接口注入的发放能力，依赖 platform/audit、platform/db、platform/httpx
// [OUTPUT]: 对外提供 Service、New、Granter、模板用例（SaveTemplate/ListTemplates）、GenerateCodes 与 GenerateOutput、奖励与条件类型
// [POS]: giftcard 的模板与生码核心：生码与批次行同一事务写入，响应只带明文样例；批次视图与一次性导出在 batches.go，读模型在 codes.go，兑换在 redeem.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package giftcard 实现礼品卡 / 卡密（对标 Xboard gift-card）。
//
// 三种卡型：
//
//	general —— 送余额 / 流量 / 延长到期
//	plan    —— 直接兑换一个套餐
//	mystery —— 从奖池里加权随机抽一个
//
// 兑换这件事有三个必须守住的点，它们决定了这个包的写法：
//
//  1. 一码一次。用 SELECT ... FOR UPDATE 锁住码行，再加 gift_card_redemptions
//     上的 UNIQUE(code_id) 兜底 —— 应用层锁万一失效，数据库仍然拦得住。
//  2. 发出去的东西要记账。余额走复式记账，不直接 UPDATE 余额数字。
//  3. 盲盒抽中什么必须落库。不记的话，用户说「我抽到的是 10 元」时
//     没有任何东西能对质。
package giftcard

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type Service struct {
	pool *db.Pool
	log  *slog.Logger
	// grant 由 billing 注入：礼品卡要往余额里记账、要延长订阅，
	// 但那些规则属于计费域。这里只负责「什么条件下发多少」，
	// 「怎么发」交回给已经把账做对的那一方。
	grant Granter
}

// Granter 是礼品卡对计费域的唯一依赖面。
//
// 声明成接口而不是直接引 billing：礼品卡依赖计费，计费不该反过来知道
// 礼品卡的存在。真到了要在下单时抵扣礼品卡的那天，这条边界能省很多事。
type Granter interface {
	// GrantBalance 往用户余额记一笔，返回账本流水号。
	GrantBalance(ctx context.Context, tx pgx.Tx, tenantID, userID string,
		amount int64, currency, memo string) (string, error)
	// GrantTraffic 给用户发一笔流量包余额（字节），codeID 是这张卡密，
	// 一码只能发一笔（D-E-1：礼品卡流量与购买的流量包同一余额、同一规则）。
	GrantTraffic(ctx context.Context, tx pgx.Tx, tenantID, userID, codeID string,
		bytes int64) error
	// ExtendExpiry 把用户当前生效订阅的到期时间往后推。
	ExtendExpiry(ctx context.Context, tx pgx.Tx, tenantID, userID string,
		days int) error
	// ResetQuota 把当前周期的已用流量清零。
	ResetQuota(ctx context.Context, tx pgx.Tx, tenantID, userID string) error
	// GrantPlan 直接给用户开通一个套餐（等同于人工单）。
	GrantPlan(ctx context.Context, tx pgx.Tx, tenantID, userID,
		planID, priceID, reason string) (string, error)
}

func New(pool *db.Pool, log *slog.Logger, grant Granter) *Service {
	return &Service{pool: pool, log: log, grant: grant}
}

//-----------------------------------------------------------------------------
// 奖励与条件的结构
//-----------------------------------------------------------------------------

type Rewards struct {
	// general
	Balance      int64 `json:"balance,omitempty"`       // 余额，最小货币单位
	TrafficBytes int64 `json:"traffic_bytes,omitempty"` // 追加流量，字节
	ExpireDays   int   `json:"expire_days,omitempty"`   // 延长到期天数
	ResetQuota   bool  `json:"reset_quota,omitempty"`   // 清零本周期已用流量

	// plan
	PlanID  string `json:"plan_id,omitempty"`
	PriceID string `json:"price_id,omitempty"`

	// mystery
	Pool []MysteryPrize `json:"pool,omitempty"`
}

// MysteryPrize 是盲盒奖池里的一项。
//
// Weight 是相对权重而非百分比：填 70/20/10 和填 7/2/1 效果一样。
// 用百分比的话，运营改动其中一项就得手工把其余项凑回 100，
// 凑错了就是概率不合法 —— 相对权重没有这个负担。
type MysteryPrize struct {
	Label        string `json:"label"`
	Weight       int    `json:"weight"`
	Balance      int64  `json:"balance,omitempty"`
	TrafficBytes int64  `json:"traffic_bytes,omitempty"`
	ExpireDays   int    `json:"expire_days,omitempty"`
}

type Conditions struct {
	NewUserOnly   bool     `json:"new_user_only,omitempty"`    // 仅从未付费的新用户
	PaidUserOnly  bool     `json:"paid_user_only,omitempty"`   // 仅付过费的用户
	AllowedPlanID []string `json:"allowed_plan_ids,omitempty"` // 仅持有这些套餐的用户
	RequireInvite bool     `json:"require_invite,omitempty"`   // 必须是被邀请注册的
}

type Limits struct {
	MaxUsePerUser int `json:"max_use_per_user,omitempty"` // 同一模板每人最多兑几次，0 = 不限
	CooldownHours int `json:"cooldown_hours,omitempty"`   // 两次兑换之间的冷却
}

type Template struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Type        string     `json:"type"`
	Status      string     `json:"status"`
	Rewards     Rewards    `json:"rewards"`
	Conditions  Conditions `json:"conditions"`
	Limits      Limits     `json:"limits"`
	ThemeColor  string     `json:"theme_color"`
	CodeTotal   int        `json:"code_total"`
	CodeUsed    int        `json:"code_used"`
	CreatedAt   time.Time  `json:"created_at"`
}

//-----------------------------------------------------------------------------
// 模板校验
//-----------------------------------------------------------------------------

// validateRewards 把「这张卡到底送什么」这件事在保存时就问清楚。
//
// 一张什么都不送的卡能建出来的话，运营会一直等着用户来兑，
// 而用户兑完什么都没收到 —— 这种问题在生产上极难查。
func validateRewards(cardType string, r Rewards) error {
	switch cardType {
	case "general":
		if r.Balance <= 0 && r.TrafficBytes <= 0 && r.ExpireDays <= 0 && !r.ResetQuota {
			return errors.New("通用卡至少要送一样东西：余额、流量、延长到期或重置流量")
		}
		if r.Balance < 0 || r.TrafficBytes < 0 || r.ExpireDays < 0 {
			return errors.New("奖励数值不能为负")
		}
		if r.ExpireDays > 3650 {
			return errors.New("延长天数不能超过 3650 天")
		}
	case "plan":
		if r.PlanID == "" {
			return errors.New("套餐卡必须指定套餐")
		}
		if _, err := uuid.Parse(r.PlanID); err != nil {
			return errors.New("套餐标识格式不正确")
		}
	case "mystery":
		if len(r.Pool) < 2 {
			return errors.New("盲盒至少要有 2 个奖品，否则它就是一张普通卡")
		}
		if len(r.Pool) > 50 {
			return errors.New("盲盒奖品不能超过 50 个")
		}
		total := 0
		for i, p := range r.Pool {
			if strings.TrimSpace(p.Label) == "" {
				return fmt.Errorf("第 %d 个奖品缺少名称 —— 用户中奖后要看到它", i+1)
			}
			if p.Weight <= 0 {
				return fmt.Errorf("第 %d 个奖品的权重必须大于 0（权重为 0 等于永远抽不到，"+
					"不如直接删掉）", i+1)
			}
			if p.Balance <= 0 && p.TrafficBytes <= 0 && p.ExpireDays <= 0 {
				return fmt.Errorf("第 %d 个奖品什么都不送", i+1)
			}
			total += p.Weight
		}
		if total <= 0 {
			return errors.New("奖池权重合计必须大于 0")
		}
	default:
		return errors.New("不支持的卡型")
	}
	return nil
}

//-----------------------------------------------------------------------------
// 模板 CRUD
//-----------------------------------------------------------------------------

type SaveTemplateInput struct {
	ID          string
	Name        string
	Description string
	Type        string
	Status      string
	Rewards     Rewards
	Conditions  Conditions
	Limits      Limits
	ThemeColor  string
	ActorID     string
}

func (s *Service) SaveTemplate(ctx context.Context, tenantID string,
	in SaveTemplateInput) (*Template, error) {

	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 120 {
		return nil, httpx.Invalid(map[string]string{"name": "名称必填，不超过 120 字"})
	}
	if in.Status == "" {
		in.Status = "active"
	}
	switch in.Status {
	case "active", "paused", "archived":
	default:
		return nil, httpx.Invalid(map[string]string{"status": "状态只能是 active / paused / archived"})
	}
	if err := validateRewards(in.Type, in.Rewards); err != nil {
		return nil, httpx.Invalid(map[string]string{"rewards": err.Error()})
	}
	if in.Limits.MaxUsePerUser < 0 || in.Limits.CooldownHours < 0 {
		return nil, httpx.Invalid(map[string]string{"limits": "限制值不能为负"})
	}
	if in.Conditions.NewUserOnly && in.Conditions.PaidUserOnly {
		return nil, httpx.Invalid(map[string]string{
			"conditions": "「仅新用户」和「仅付费用户」互斥，同时勾选等于谁都不能兑"})
	}

	rewardsJSON, _ := json.Marshal(in.Rewards)
	condJSON, _ := json.Marshal(in.Conditions)
	limitsJSON, _ := json.Marshal(in.Limits)

	var out Template
	actor := in.ActorID
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		var id string
		var action string
		if in.ID == "" {
			action = "gift_card_template.created"
			err := tx.QueryRow(ctx, `
				INSERT INTO gift_card_templates
					(tenant_id,name,description,type,status,rewards,conditions,limits,
					 theme_color,created_by)
				VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9,$10::uuid)
				RETURNING id::text`,
				tenantID, in.Name, in.Description, in.Type, in.Status,
				string(rewardsJSON), string(condJSON), string(limitsJSON),
				in.ThemeColor, actor).Scan(&id)
			if err != nil {
				if db.IsUniqueViolation(err) {
					return httpx.Invalid(map[string]string{"name": "已经有同名的礼品卡了"})
				}
				return err
			}
		} else {
			action = "gift_card_template.updated"
			// 卡型不允许改：已经发出去的码是按旧卡型生成的，
			// 改型之后那些码兑出来的东西和用户当初看到的说明对不上。
			var oldType string
			err := tx.QueryRow(ctx, `
				SELECT type FROM gift_card_templates
				 WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`,
				tenantID, in.ID).Scan(&oldType)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			if err != nil {
				return err
			}
			if oldType != in.Type {
				return httpx.New(httpx.CodeConflict,
					"不能修改卡型：已经发出去的码是按原卡型生成的")
			}
			if _, err := tx.Exec(ctx, `
				UPDATE gift_card_templates
				   SET name=$3,description=$4,status=$5,rewards=$6::jsonb,
				       conditions=$7::jsonb,limits=$8::jsonb,theme_color=$9,updated_at=now()
				 WHERE tenant_id=$1 AND id=$2::uuid`,
				tenantID, in.ID, in.Name, in.Description, in.Status,
				string(rewardsJSON), string(condJSON), string(limitsJSON),
				in.ThemeColor); err != nil {
				if db.IsUniqueViolation(err) {
					return httpx.Invalid(map[string]string{"name": "已经有同名的礼品卡了"})
				}
				return err
			}
			id = in.ID
		}

		if err := s.loadTemplate(ctx, tx, tenantID, id, &out); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: action, ResourceType: "gift_card_template", ResourceID: &id,
			AfterDigest: map[string]any{
				"name": in.Name, "type": in.Type, "status": in.Status,
				"rewards": in.Rewards,
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Service) loadTemplate(ctx context.Context, tx pgx.Tx, tenantID, id string,
	out *Template) error {

	var rewardsRaw, condRaw, limitsRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT t.id::text,t.name,t.description,t.type,t.status,
		       t.rewards,t.conditions,t.limits,t.theme_color,t.created_at,
		       (SELECT count(*) FROM gift_card_codes c
		         WHERE c.tenant_id=t.tenant_id AND c.template_id=t.id),
		       (SELECT count(*) FROM gift_card_codes c
		         WHERE c.tenant_id=t.tenant_id AND c.template_id=t.id AND c.status='used')
		  FROM gift_card_templates t
		 WHERE t.tenant_id=$1 AND t.id=$2::uuid`, tenantID, id).
		Scan(&out.ID, &out.Name, &out.Description, &out.Type, &out.Status,
			&rewardsRaw, &condRaw, &limitsRaw, &out.ThemeColor, &out.CreatedAt,
			&out.CodeTotal, &out.CodeUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return err
	}
	_ = json.Unmarshal(rewardsRaw, &out.Rewards)
	_ = json.Unmarshal(condRaw, &out.Conditions)
	_ = json.Unmarshal(limitsRaw, &out.Limits)
	return nil
}

func (s *Service) ListTemplates(ctx context.Context, tenantID string) ([]Template, error) {
	out := []Template{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id::text,t.name,t.description,t.type,t.status,
			       t.rewards,t.conditions,t.limits,t.theme_color,t.created_at,
			       (SELECT count(*) FROM gift_card_codes c
			         WHERE c.tenant_id=t.tenant_id AND c.template_id=t.id),
			       (SELECT count(*) FROM gift_card_codes c
			         WHERE c.tenant_id=t.tenant_id AND c.template_id=t.id AND c.status='used')
			  FROM gift_card_templates t
			 WHERE t.tenant_id=$1 AND t.status <> 'archived'
			 ORDER BY t.created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Template
			var rewardsRaw, condRaw, limitsRaw []byte
			if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Type, &t.Status,
				&rewardsRaw, &condRaw, &limitsRaw, &t.ThemeColor, &t.CreatedAt,
				&t.CodeTotal, &t.CodeUsed); err != nil {
				return err
			}
			_ = json.Unmarshal(rewardsRaw, &t.Rewards)
			_ = json.Unmarshal(condRaw, &t.Conditions)
			_ = json.Unmarshal(limitsRaw, &t.Limits)
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

//-----------------------------------------------------------------------------
// 批量生码
//-----------------------------------------------------------------------------

type GenerateInput struct {
	TemplateID string
	Count      int
	Prefix     string
	ExpiresAt  *time.Time
	ActorID    string
}

// GenerateOutput 是生码结果。明文只回 Sample（前几张），完整明文只能走
// 一次性导出 ExportBatch；Batch 是批次视图，供界面直接插进批次列表。
type GenerateOutput struct {
	BatchID string   `json:"batch_id"`
	Count   int      `json:"count"`
	Sample  []string `json:"sample"`
	Batch   Batch    `json:"batch"`
}

// generateSampleSize 是生码响应里给出的明文样例张数，够核对格式与前缀。
const generateSampleSize = 4

func (s *Service) GenerateCodes(ctx context.Context, tenantID string,
	in GenerateInput) (*GenerateOutput, error) {

	in.Prefix = strings.ToUpper(strings.TrimSpace(in.Prefix))
	if in.Count < 1 || in.Count > 5000 {
		return nil, httpx.Invalid(map[string]string{
			"count": "一次生成 1 到 5000 个。要更多就分批 —— 单次几万个会把事务拖很久"})
	}
	if in.Prefix != "" && !isSafePrefix(in.Prefix) {
		return nil, httpx.Invalid(map[string]string{
			"prefix": "前缀只能用大写字母和数字，最多 8 位"})
	}
	if _, err := uuid.Parse(in.TemplateID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	if in.ExpiresAt != nil && in.ExpiresAt.Before(time.Now()) {
		return nil, httpx.Invalid(map[string]string{
			"expires_at": "有效期不能设在过去"})
	}

	batchID := uuid.New().String()
	sample := make([]string, 0, generateSampleSize)
	actor := in.ActorID
	var batch *Batch

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT status FROM gift_card_templates
			 WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, in.TemplateID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if status == "archived" {
			return httpx.New(httpx.CodeConflict, "已归档的礼品卡不能再生成新码")
		}

		// 批次行先于码写入：码上的 batch_id 外键指向它。
		if _, err := tx.Exec(ctx, `
			INSERT INTO gift_card_batches
				(id,tenant_id,template_id,prefix,count,expires_at,created_by)
			VALUES ($1::uuid,$2,$3::uuid,$4,$5,$6,$7::uuid)`,
			batchID, tenantID, in.TemplateID, in.Prefix, in.Count,
			in.ExpiresAt, actor); err != nil {
			return err
		}

		for inserted := 0; inserted < in.Count; {
			code, err := newCode(in.Prefix)
			if err != nil {
				return err
			}
			// 撞码就换一个重试。码空间是 31^12，撞的概率极低，
			// 但批量几千个时「极低」不等于「不会」。
			tag, err := tx.Exec(ctx, `
				INSERT INTO gift_card_codes
					(tenant_id,template_id,code,batch_id,expires_at)
				VALUES ($1,$2::uuid,$3,$4::uuid,$5)
				ON CONFLICT (tenant_id,code) DO NOTHING`,
				tenantID, in.TemplateID, code, batchID, in.ExpiresAt)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				inserted++
				if len(sample) < generateSampleSize {
					sample = append(sample, code)
				}
			}
		}

		loaded, err := loadBatchTx(ctx, tx, tenantID, batchID)
		if err != nil {
			return err
		}
		batch = loaded
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "gift_card.codes_generated", ResourceType: "gift_card_template",
			ResourceID: &in.TemplateID,
			AfterDigest: map[string]any{
				"batch_id": batchID, "count": in.Count, "prefix": in.Prefix,
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, err
	}
	return &GenerateOutput{BatchID: batchID, Count: in.Count, Sample: sample, Batch: *batch}, nil
}

func isSafePrefix(p string) bool {
	if len(p) > 8 {
		return false
	}
	for _, c := range p {
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// newCode 生成卡密。
//
// 字母表刻意去掉了 I、L、O、0、1 —— 卡密经常要人工抄写或电话报读，
// 这几个字符在多数字体里几乎不可分辨，混淆一次就是一张废卡加一个工单。
func newCode(prefix string) (string, error) {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	n := 12
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return prefix + string(out), nil
}
