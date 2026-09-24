// [INPUT]: 依赖 gift_card_batches / gift_card_codes / gift_card_templates 三张表（迁移 00045、00069），依赖 platform/audit、platform/db、platform/httpx
// [OUTPUT]: 对外提供 Batch、ListBatchesInput、ListBatches、BatchExport、ExportRow、ExportBatch、MaskCode
// [POS]: giftcard 的批次视图与一次性导出：明文卡码只在生码响应的样例与这里的一次导出里出现，其余接口一律经 MaskCode 掩码；giftcard.go 的 GenerateCodes 在同一事务里写批次行
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package giftcard

// 批次是「一次生成的一批码」。它存在的理由只有一个：记住这批码的明文
// 有没有被导出过。卡密是等价现金，完整明文只应出现两次 —— 生码那一刻的
// 响应样例，和一次性导出的那份文件。之后列表、兑换记录里只给掩码，
// 导出文件丢了就是丢了，这是设计明确接受的运营风险。

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

// maskedTail 是掩码遮住的末尾字符数。码 = 前缀 + 12 位随机段，遮住末尾 8 位
// 即露出「前缀 + 随机段前 4 位」，足够人工核对是哪张卡，又不足以拿去兑换。
const maskedTail = 8

// MaskCode 把明文卡密变成可展示的掩码，例如 GCH2K9Q7MNPRST → GCH2K9••••••••。
func MaskCode(code string) string {
	n := utf8.RuneCountInString(code)
	if n <= maskedTail {
		return strings.Repeat("•", n)
	}
	runes := []rune(code)
	return string(runes[:n-maskedTail]) + strings.Repeat("•", maskedTail)
}

// ErrBatchAlreadyExported：一次性导出的第二次请求。
var ErrBatchAlreadyExported = httpx.New(httpx.CodeConflict,
	"该批次已导出，完整卡码不可再次获取")

type Batch struct {
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

// batchSelect 是批次视图的唯一 SELECT，列表与单个批次共用；
// 调用方在后面接 WHERE / ORDER / LIMIT。
const batchSelect = `
	SELECT b.id::text, b.template_id::text, t.name, b.prefix, b.count,
	       (SELECT count(*)::int FROM gift_card_codes c
	         WHERE c.tenant_id = b.tenant_id AND c.batch_id = b.id AND c.status = 'used'),
	       (SELECT count(*)::int FROM gift_card_codes c
	         WHERE c.tenant_id = b.tenant_id AND c.batch_id = b.id AND c.status = 'disabled'),
	       b.expires_at, cu.email::text, b.created_at, b.exported_at, eu.email::text
	  FROM gift_card_batches b
	  JOIN gift_card_templates t ON t.tenant_id = b.tenant_id AND t.id = b.template_id
	  LEFT JOIN users cu ON cu.tenant_id = b.tenant_id AND cu.id = b.created_by
	  LEFT JOIN users eu ON eu.tenant_id = b.tenant_id AND eu.id = b.exported_by`

func scanBatch(row pgx.Row, b *Batch) error {
	return row.Scan(&b.ID, &b.TemplateID, &b.TemplateName, &b.Prefix, &b.Count,
		&b.Used, &b.Disabled, &b.ExpiresAt, &b.CreatedByEmail, &b.CreatedAt,
		&b.ExportedAt, &b.ExportedByEmail)
}

func loadBatchTx(ctx context.Context, tx pgx.Tx, tenantID, batchID string) (*Batch, error) {
	var b Batch
	err := scanBatch(tx.QueryRow(ctx, batchSelect+`
		 WHERE b.tenant_id = $1 AND b.id = $2::uuid`, tenantID, batchID), &b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

type ListBatchesInput struct {
	TemplateID string
	Limit      int
	Offset     int
}

// ListBatches 按生成时间倒序列出批次。
func (s *Service) ListBatches(ctx context.Context, tenantID string,
	in ListBatchesInput) ([]Batch, int, error) {

	if in.Limit <= 0 || in.Limit > 200 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	if in.TemplateID != "" {
		if _, err := uuid.Parse(in.TemplateID); err != nil {
			return nil, 0, httpx.New(httpx.CodeBadRequest, "标识符格式不正确")
		}
	}
	out := []Batch{}
	var total int
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*)::int FROM gift_card_batches
			 WHERE tenant_id = $1 AND ($2 = '' OR template_id = $2::uuid)`,
			tenantID, in.TemplateID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, batchSelect+`
			 WHERE b.tenant_id = $1 AND ($2 = '' OR b.template_id = $2::uuid)
			 ORDER BY b.created_at DESC, b.id DESC
			 LIMIT $3 OFFSET $4`, tenantID, in.TemplateID, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b Batch
			if err := scanBatch(rows, &b); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, total, err
}

// ExportRow 是导出文件的一行，只在 ExportBatch 的返回值里出现明文。
type ExportRow struct {
	Code         string
	Status       string
	ExpiresAt    *time.Time
	TemplateName string
}

type BatchExport struct {
	Batch Batch
	Rows  []ExportRow
}

// ExportBatch 一次性取出整批明文卡码，并在同一事务里把批次标记为已导出。
//
// 标记与读取同生共死：导出在写审计或打标记时失败，明文就不会离开事务。
// 第二次调用一律 409 —— 同一幂等键的网络重试由网关的幂等层原样重放
// 第一次的响应，不会走到这里。
func (s *Service) ExportBatch(ctx context.Context, tenantID, batchID,
	actorID string) (*BatchExport, error) {

	if _, err := uuid.Parse(batchID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	var out BatchExport
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var exportedAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT exported_at FROM gift_card_batches
			 WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`,
			tenantID, batchID).Scan(&exportedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if exportedAt != nil {
			return ErrBatchAlreadyExported
		}

		tag, err := tx.Exec(ctx, `
			UPDATE gift_card_batches SET exported_at = now(), exported_by = $3::uuid
			 WHERE tenant_id = $1 AND id = $2::uuid AND exported_at IS NULL`,
			tenantID, batchID, actorID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("gift card batch export mark lost")
		}
		batch, err := loadBatchTx(ctx, tx, tenantID, batchID)
		if err != nil {
			return err
		}
		out.Batch = *batch

		rows, err := tx.Query(ctx, `
			SELECT code, status, expires_at FROM gift_card_codes
			 WHERE tenant_id = $1 AND batch_id = $2::uuid
			 ORDER BY created_at, code`, tenantID, batchID)
		if err != nil {
			return err
		}
		for rows.Next() {
			r := ExportRow{TemplateName: batch.TemplateName}
			if err := rows.Scan(&r.Code, &r.Status, &r.ExpiresAt); err != nil {
				rows.Close()
				return err
			}
			out.Rows = append(out.Rows, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// 审计只记数量，不记任何码：审计表不能成为明文的第二个出口。
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "gift_card.batch_exported", ResourceType: "gift_card_batch",
			ResourceID: &batchID,
			AfterDigest: map[string]any{
				"template_id": batch.TemplateID, "codes": len(out.Rows),
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
