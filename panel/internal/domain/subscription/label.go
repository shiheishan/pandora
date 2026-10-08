package subscription

// 订阅备注名与 App 里的配置名（购买模型统一 2.8，2026-10-07）。
//
// 一个人可以有好几份订阅（自己的手机、妈妈的 iPad……），App 里要分得清：
//   - 备注名 subscriptions.label 由用户自己起，同一个人的几份不能重名（唯一索引
//     subscriptions_user_label_unique，按 lower(label)），字符规则只在 purchase.NormalizeLabel 一处；
//   - 配置名只经 ProfileName 拼出：「站点名 · 备注名」，没起名时「站点名 · 套餐名」。
//     门户订阅列表的 client_name 与订阅下载的 Content-Disposition 用的是同一个函数，
//     页面上写的「App 里显示为：…」与客户端真正显示的一致。
//
// 改名不推进节点下发纪元（00135 把订阅表的纪元触发器拆开，排除 label 与 updated_at）：
// 节点名单与名字无关，改一次名不该让所有节点池重算一遍。

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// labelUniqueIndex 是同一用户备注名不重名的唯一索引（迁移 00136）。
const labelUniqueIndex = "subscriptions_user_label_unique"

// profileNameSeparator 是站点名与备注名（或套餐名）之间的分隔。
const profileNameSeparator = " · "

// ProfileName 是订阅在 App 里显示的配置名，全站唯一来源：「站点名 · 备注名」，
// 没起名时「站点名 · 套餐名」，套餐名也取不到时只有站点名。
func ProfileName(site, label, planName string) string {
	site = strings.TrimSpace(site)
	if site == "" {
		site = appearance.DefaultSiteName
	}
	name := strings.TrimSpace(label)
	if name == "" {
		name = strings.TrimSpace(planName)
	}
	if name == "" {
		return site
	}
	return site + profileNameSeparator + name
}

// LabelResult 是改名之后这份订阅的名字：Label 为 nil 表示没起名。
type LabelResult struct {
	Label      *string `json:"label"`
	ClientName string  `json:"client_name"`
}

// SetLabel 给本人的一份订阅起名、改名或清除名字（label 为 nil 或空白）。
//
// 校验经 purchase.NormalizeLabel（422，字段 label）；不是本人的、不存在的回 ErrNotFound；
// 与本人另一份重名回 409，文案点出那一份。一条 UPDATE 完成：所有权写在 WHERE 里，
// 同时按主键取回套餐名拼配置名。订阅不论状态都能改名（门户列表里也有已过期的份）。
func (s *Service) SetLabel(ctx context.Context, tenantID, userID, subID string, label *string) (LabelResult, error) {
	id, err := uuid.Parse(subID)
	if err != nil {
		return LabelResult{}, ErrNotFound
	}
	subID = id.String()
	var normalized string
	if label != nil {
		if normalized, err = purchase.NormalizeLabel(*label); err != nil {
			return LabelResult{}, httpx.Invalid(map[string]string{"label": err.Error()})
		}
	}

	var planName string
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			UPDATE subscriptions s SET label = NULLIF($4::text, '')
			  FROM plans pl
			 WHERE s.tenant_id = $1 AND s.id = $2::uuid AND s.user_id = $3::uuid
			   AND pl.id = s.plan_id
			RETURNING pl.name`, tenantID, subID, userID, normalized).Scan(&planName)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return LabelResult{}, ErrNotFound
	}
	if db.IsUniqueViolation(err) && db.ConstraintName(err) == labelUniqueIndex {
		return LabelResult{}, s.labelTaken(ctx, tenantID, userID, subID, normalized)
	}
	if err != nil {
		return LabelResult{}, err
	}

	out := LabelResult{ClientName: ProfileName(s.SiteName(ctx, tenantID), normalized, planName)}
	if normalized != "" {
		out.Label = &normalized
	}
	return out, nil
}

// labelTaken 是撞了同名的 409：另开一个只读事务查出占着这个名字的那一份，文案里点出它
// （「妈妈的 iPad · 基础版」）。只在冲突时多一次往返；查不到（那一份刚被改掉）也照样 409。
func (s *Service) labelTaken(ctx context.Context, tenantID, userID, subID, label string) error {
	msg := "这个名字已经用在你的另一份套餐上了，换一个吧"
	var otherLabel, otherPlan string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT s.label, pl.name
			  FROM subscriptions s
			  JOIN plans pl ON pl.id = s.plan_id
			 WHERE s.tenant_id = $1 AND s.user_id = $2::uuid AND s.id <> $3::uuid
			   AND lower(s.label) = lower($4::text)
			 LIMIT 1`, tenantID, userID, subID, label).Scan(&otherLabel, &otherPlan)
	})
	if err == nil {
		msg = "这个名字已经用在「" + otherLabel + profileNameSeparator + otherPlan + "」上了，换一个吧"
	}
	return &httpx.Error{Code: httpx.CodeConflict, Message: msg, Fields: map[string]string{"label": msg}}
}
