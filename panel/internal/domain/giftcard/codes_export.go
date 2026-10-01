// [INPUT]: 依赖 codes.go 的 CodeFilter 与 codeFilterCond（与列表同一筛选口径），依赖 gift_card_codes / gift_card_templates / users 表，依赖 platform/audit、platform/db、platform/httpx
// [OUTPUT]: 对外提供 CodesExportMax、CodeReportRow、ExportCodes
// [POS]: giftcard 的掩码报表导出（运营对账用）：按卡码列表的筛选整批取出，掩码在 SQL 里算好，明文列的值从不离开数据库；与 batches.go 的一次性明文导出是两条互不相干的出口
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package giftcard

// 按筛选导出只给运营对账：哪些码兑了、谁兑的、什么时候兑的。完整卡码
// 只在生码那一刻与批次的一次性导出里出现（00069），这里绝不能成为第三个
// 出口 —— 所以掩码不在 Go 里算，而是在 SQL 里由 maskedCodeSQL 算好再取出，
// 扫描进来的只有掩码。maskedCodeSQL 与 MaskCode 必须逐字一致，PG18 测试
// 对每一行核对；codes_export_test.go 守住导出 SQL 除掩码表达式外不碰 code 列。

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// CodesExportMax 是一次导出的行数上限，与审计导出同一量级。超过就要求缩小
// 筛选范围，而不是悄悄截断 —— 截断的对账表比没有更糟。
const CodesExportMax = 50000

// maskedCodeSQL 是 MaskCode 的 SQL 版：去掉末尾 maskedTail 位，换成同样多个 •。
// 码长受表上 CHECK 约束在 8–32 之间，least 只为与 MaskCode 的短码分支同形。
const maskedCodeSQL = `left(c.code, -8) || repeat('•', least(char_length(c.code), 8))`

// CodeReportRow 是掩码报表的一行，字段全部来自表里真实存在的列。
type CodeReportRow struct {
	CodeMasked   string
	Status       string
	TemplateName string
	BatchID      *string
	ExpiresAt    *time.Time
	CreatedAt    time.Time
	UsedEmail    string
	UsedAt       *time.Time
}

const codesExportSelect = `
	SELECT ` + maskedCodeSQL + `, c.status, t.name, c.batch_id::text, c.expires_at,
	       c.created_at, coalesce(u.email::text, ''), c.used_at
	  FROM gift_card_codes c
	  JOIN gift_card_templates t ON t.tenant_id = c.tenant_id AND t.id = c.template_id
	  LEFT JOIN users u ON u.tenant_id = c.tenant_id AND u.id = c.used_by`

// ExportCodes 按 CodeFilter 取出全部匹配的卡码掩码报表，并写一条审计。
//
// 先数再取：超过 CodesExportMax 回 422，整批读完、审计写进同一事务之后才
// 交给调用方输出，查询失败时还能回正常的错误响应，而不是半截 CSV。
func (s *Service) ExportCodes(ctx context.Context, tenantID, actorID string,
	f CodeFilter) ([]CodeReportRow, error) {

	if err := f.check(); err != nil {
		return nil, err
	}
	out := []CodeReportRow{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var total int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM gift_card_codes c`+codeFilterCond,
			f.args(tenantID)...).Scan(&total); err != nil {
			return err
		}
		if total > CodesExportMax {
			return httpx.New(httpx.CodeValidationFailed,
				"超过 50000 行，请缩小筛选范围（按模板、批次或状态）")
		}
		rows, err := tx.Query(ctx, codesExportSelect+codeFilterCond+`
			 ORDER BY c.created_at DESC, c.id`, f.args(tenantID)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r CodeReportRow
			if err := rows.Scan(&r.CodeMasked, &r.Status, &r.TemplateName, &r.BatchID,
				&r.ExpiresAt, &r.CreatedAt, &r.UsedEmail, &r.UsedAt); err != nil {
				rows.Close()
				return err
			}
			out = append(out, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// 审计记筛选条件与行数，不记任何码（掩码也不记：没有用处）。
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "gift_card.codes_report_exported", ResourceType: "gift_card_code",
			AfterDigest: map[string]any{
				"template_id": f.TemplateID, "status": f.Status, "batch_id": f.BatchID,
				"rows": len(out),
			},
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
