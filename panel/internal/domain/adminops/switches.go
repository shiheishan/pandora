// [INPUT]: 依赖 platform/db、audit、httpx
// [OUTPUT]: 对外提供 SwitchRow、Service 的 ListSwitches、SetSwitch
// [POS]: adminops 的降级开关读写：从 service.go 拆出。数据库拒绝切换时按约束名给中文原因（switchRefusal），PG 原句只进日志（R116）；开关门本身在 middleware/switches.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//==============================================================================
// 系统
//==============================================================================

type SwitchRow struct {
	Code      string  `json:"code"`
	Enabled   bool    `json:"enabled"`
	Essential bool    `json:"essential"`
	Reason    *string `json:"reason"`
}

func (s *Service) ListSwitches(ctx context.Context, tenantID string) ([]SwitchRow, error) {
	out := []SwitchRow{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT code, enabled, essential, reason FROM feature_switches
			  WHERE tenant_id = $1 ORDER BY essential DESC, code`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r SwitchRow
			if err := rows.Scan(&r.Code, &r.Enabled, &r.Essential, &r.Reason); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// switchRefusal 把数据库拒绝切换开关的原因翻成中文（R116）。PG 的原句是英文
// （new row ... violates check constraint ...），只进日志，不给页面。
func switchRefusal(err error) string {
	switch db.ConstraintName(err) {
	case "feature_switches_essential_stays_on":
		return "核心开关不能关闭"
	case "feature_switches_disable_needs_reason":
		return "关闭开关必须填写原因"
	}
	if db.IsInsufficientPrivilege(err) {
		return "当前账号没有修改这个开关的权限"
	}
	return "数据库约束拒绝了这次修改"
}

// SetSwitch 切换降级开关（NFR-008）。
// essential 的三项由数据库触发器挡住，这里不重复判断，
// 让唯一的真相来源留在约束里 —— 但要把数据库的报错翻译成人话。
func (s *Service) SetSwitch(ctx context.Context, tenantID, actorID, code string, enabled bool, reason string) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var before bool
		if err := tx.QueryRow(ctx,
			`SELECT enabled FROM feature_switches WHERE tenant_id = $1 AND code = $2 FOR UPDATE`,
			tenantID, code).Scan(&before); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE feature_switches SET enabled = $3, reason = $4
			 WHERE tenant_id = $1 AND code = $2`,
			tenantID, code, enabled, nullIfEmpty(reason)); err != nil {
			if db.IsCheckViolation(err) || db.IsInsufficientPrivilege(err) {
				return httpx.New(httpx.CodeConflict,
					fmt.Sprintf("开关 %s 不允许该操作：%s", code, switchRefusal(err))).WithInternal(err)
			}
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "feature_switch.toggle", ResourceType: "feature_switch",
			APIDomain: "admin", Outcome: "success",
			BeforeDigest: map[string]any{"enabled": before},
			AfterDigest:  map[string]any{"enabled": enabled, "reason": reason},
			RequestID:    httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
			return err
		}
		return httpx.Internal(err)
	}
	return nil
}

func nullIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
