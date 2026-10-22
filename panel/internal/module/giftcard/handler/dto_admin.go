package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/giftcard/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// ---------------------------------------------------------------------------
// 模板
// ---------------------------------------------------------------------------

type templateDTO struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Type       string        `json:"type"`
	Status     string        `json:"status"`
	Rewards    rewardsDTO    `json:"rewards"`
	Conditions conditionsDTO `json:"conditions"`
	Limits     limitsDTO     `json:"limits"`
	CodeTotal  int64         `json:"code_total"`
	CodeUsed   int64         `json:"code_used"`
	// PriceArchived 套餐卡的价格档已下架（后台标「价格已下架」，编辑时要重选价格）
	PriceArchived bool      `json:"price_archived"`
	CreatedAt     time.Time `json:"created_at"`
}

func templateFrom(t service.Template, c *cents) templateDTO {
	return templateDTO{
		ID:            t.ID.String(),
		Name:          t.Name,
		Type:          t.Type,
		Status:        t.Status,
		Rewards:       rewardsFrom(t.Rewards, c),
		Conditions:    conditionsFrom(t.Conditions),
		Limits:        limitsFrom(t.Limits),
		CodeTotal:     t.CodeTotal,
		CodeUsed:      t.CodeUsed,
		PriceArchived: t.PriceArchived,
		CreatedAt:     t.CreatedAt,
	}
}

type templatesResponse struct {
	Templates []templateDTO `json:"templates"`
}

type templateResponse struct {
	Template templateDTO `json:"template"`
}

// templateRequest 是保存模板的请求体：id 空串为新建，金额是分。
type templateRequest struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Type       string        `json:"type"`
	Status     string        `json:"status"`
	Rewards    rewardsDTO    `json:"rewards"`
	Conditions conditionsDTO `json:"conditions"`
	Limits     limitsDTO     `json:"limits"`
}

// input 把请求转成 service 的输入；id 类字段格式不对回 422。
func (r templateRequest) input() (service.TemplateInput, error) {
	id, err := optUUID("id", r.ID)
	if err != nil {
		return service.TemplateInput{}, err
	}
	planID, err := optUUID("rewards", r.Rewards.PlanID)
	if err != nil {
		return service.TemplateInput{}, err
	}
	priceID, err := optUUID("rewards", r.Rewards.PriceID)
	if err != nil {
		return service.TemplateInput{}, err
	}
	pool := make([]service.Prize, 0, len(r.Rewards.Pool))
	for _, p := range r.Rewards.Pool {
		pool = append(pool, service.Prize{
			Label: p.Label, Weight: p.Weight, Balance: money.FromCents(p.Balance),
			TrafficBytes: p.TrafficBytes, ExpireDays: p.ExpireDays,
		})
	}
	return service.TemplateInput{
		ID:     id,
		Name:   r.Name,
		Type:   r.Type,
		Status: r.Status,
		Rewards: service.Rewards{
			Balance:      money.FromCents(r.Rewards.Balance),
			TrafficBytes: r.Rewards.TrafficBytes,
			ExpireDays:   r.Rewards.ExpireDays,
			ResetQuota:   r.Rewards.ResetQuota,
			PlanID:       planID,
			PriceID:      priceID,
			Pool:         pool,
		},
		Conditions: service.Conditions{
			NewUserOnly:  r.Conditions.NewUserOnly,
			PaidUserOnly: r.Conditions.PaidUserOnly,
		},
		Limits: service.Limits{MaxUsePerUser: r.Limits.MaxUsePerUser},
	}, nil
}

// ---------------------------------------------------------------------------
// 批次与生码
// ---------------------------------------------------------------------------

type batchDTO struct {
	ID              string     `json:"id"`
	TemplateID      string     `json:"template_id"`
	TemplateName    string     `json:"template_name"`
	Prefix          string     `json:"prefix"`
	Count           int        `json:"count"`
	Used            int        `json:"used"`
	Disabled        int        `json:"disabled"`
	ExpiresAt       *time.Time `json:"expires_at"`
	CreatedByEmail  *string    `json:"created_by_email"`
	CreatedAt       time.Time  `json:"created_at"`
	ExportedAt      *time.Time `json:"exported_at"`
	ExportedByEmail *string    `json:"exported_by_email"`
}

func batchFrom(b service.Batch) batchDTO {
	return batchDTO{
		ID:              b.ID.String(),
		TemplateID:      b.TemplateID.String(),
		TemplateName:    b.TemplateName,
		Prefix:          b.Prefix,
		Count:           b.Count,
		Used:            b.Used,
		Disabled:        b.Disabled,
		ExpiresAt:       b.ExpiresAt,
		CreatedByEmail:  b.CreatedByEmail,
		CreatedAt:       b.CreatedAt,
		ExportedAt:      b.ExportedAt,
		ExportedByEmail: b.ExportedByEmail,
	}
}

type batchesResponse struct {
	Items []batchDTO `json:"items"`
	Total int64      `json:"total"`
}

// generateRequest 是生码请求体；expires_at 是 RFC3339，空串表示不过期。
type generateRequest struct {
	Count     int    `json:"count"`
	Prefix    string `json:"prefix"`
	ExpiresAt string `json:"expires_at"`
}

func (r generateRequest) expires() (*time.Time, error) {
	raw := strings.TrimSpace(r.ExpiresAt)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, errs.Invalid(map[string]string{"expires_at": "时间格式不正确"})
	}
	return &t, nil
}

type generatedResponse struct {
	BatchID string   `json:"batch_id"`
	Count   int      `json:"count"`
	Sample  []string `json:"sample"`
	Batch   batchDTO `json:"batch"`
}

func generatedFrom(g service.Generated) generatedResponse {
	sample := g.Sample
	if sample == nil {
		sample = []string{}
	}
	return generatedResponse{BatchID: g.BatchID.String(), Count: g.Count, Sample: sample, Batch: batchFrom(g.Batch)}
}

// generatedReply 是生码的幂等重放响应：sample 恒为空数组 —— 明文卡密不进幂等表，
// 重放的人只能看到批次，要明文走一次性导出。
func generatedReply(g service.Generated) (idemsvc.Response, error) {
	out := generatedFrom(g)
	out.Sample = []string{}
	return idemsvc.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// 卡密、统计、使用明细
// ---------------------------------------------------------------------------

type codeDTO struct {
	ID         string     `json:"id"`
	CodeMasked string     `json:"code_masked"`
	Status     string     `json:"status"`
	BatchID    string     `json:"batch_id,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	UsedEmail  string     `json:"used_email,omitempty"`
	UsedAt     *time.Time `json:"used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	TemplateID string     `json:"template_id"`
}

type codesResponse struct {
	Codes []codeDTO `json:"codes"`
	Total int64     `json:"total"`
}

func codesFrom(rows []service.Code, total int64) codesResponse {
	out := codesResponse{Codes: make([]codeDTO, 0, len(rows)), Total: total}
	for _, r := range rows {
		v := codeDTO{
			ID: r.ID.String(), CodeMasked: r.CodeMasked, Status: r.Status, ExpiresAt: r.ExpiresAt,
			UsedEmail: r.UsedEmail, UsedAt: r.UsedAt, CreatedAt: r.CreatedAt, TemplateID: r.TemplateID.String(),
		}
		if r.BatchID != uuid.Nil {
			v.BatchID = r.BatchID.String()
		}
		out.Codes = append(out.Codes, v)
	}
	return out
}

type toggleRequest struct {
	Disabled bool `json:"disabled"`
}

type toggleResponse struct {
	Disabled bool `json:"disabled"`
}

type statsResponse struct {
	Templates     int64 `json:"templates"`
	CodesTotal    int64 `json:"codes_total"`
	CodesUsed     int64 `json:"codes_used"`
	CodesUnused   int64 `json:"codes_unused"`
	BalanceOut    int64 `json:"balance_out"`
	TrafficOut    int64 `json:"traffic_out"`
	BalanceIssued int64 `json:"balance_issued"`
}

func statsFrom(s service.Stats) (statsResponse, error) {
	var c cents
	out := statsResponse{
		Templates: s.Templates, CodesTotal: s.CodesTotal, CodesUsed: s.CodesUsed, CodesUnused: s.CodesUnused,
		BalanceOut: c.of(s.BalanceOut), TrafficOut: s.TrafficOut, BalanceIssued: c.of(s.BalanceIssued),
	}
	return out, c.err
}

type grantedDTO struct {
	Balance      int64 `json:"balance,omitempty"`
	TrafficBytes int64 `json:"traffic_bytes,omitempty"`
	ExpireDays   int   `json:"expire_days,omitempty"`
	ResetQuota   bool  `json:"reset_quota,omitempty"`
}

type usageDTO struct {
	TemplateName string     `json:"template_name"`
	CodeMasked   string     `json:"code_masked"`
	UserEmail    string     `json:"user_email"`
	Granted      grantedDTO `json:"granted"`
	PrizeLabel   string     `json:"prize_label,omitempty"`
	RedeemedAt   time.Time  `json:"redeemed_at"`
}

type usagesResponse struct {
	Usages []usageDTO `json:"usages"`
}

func usagesFrom(rows []service.Usage) (usagesResponse, error) {
	var c cents
	out := usagesResponse{Usages: make([]usageDTO, 0, len(rows))}
	for _, r := range rows {
		out.Usages = append(out.Usages, usageDTO{
			TemplateName: r.TemplateName, CodeMasked: r.CodeMasked, UserEmail: r.UserEmail,
			Granted: grantedDTO{
				Balance: c.of(r.Granted.Balance), TrafficBytes: r.Granted.TrafficBytes,
				ExpireDays: r.Granted.ExpireDays, ResetQuota: r.Granted.QuotaReset,
			},
			PrizeLabel: r.PrizeLabel, RedeemedAt: r.RedeemedAt,
		})
	}
	return out, c.err
}
