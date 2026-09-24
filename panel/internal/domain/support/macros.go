// [INPUT]: 依赖 ticket_macros 表（00078），依赖 platform 的 db/audit/httpx
// [OUTPUT]: 对外提供 Macro、MacroInput、Service.ListMacros / SaveMacro / DeleteMacro
// [POS]: domain/support 的快捷回复配置：租户共享的客服话术，与工单生命周期（service.go）无耦合，只被后台工单页读写
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

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

// Macro 是一条快捷回复。点标签只把 body 填进回复框，发不发由客服决定，
// 所以这里不做变量替换——替换出错的话术被一键发出去，比手敲还糟。
type Macro struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	SortOrder int       `json:"sort_order"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MacroInput struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	SortOrder int    `json:"sort_order"`
}

// 与 00078 的 CHECK 同值：先在这里拦下，给前端字段级的 422，
// 而不是让约束报错变成一个说不清哪里错的 500。
const (
	macroTitleMax = 20
	macroBodyMax  = 5000
)

func normalizeMacroInput(in MacroInput) (MacroInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	fields := map[string]string{}
	if n := utf8.RuneCountInString(in.Title); n < 1 || n > macroTitleMax {
		fields["title"] = "标题必须为 1 到 20 个字"
	}
	if n := utf8.RuneCountInString(in.Body); n < 1 || n > macroBodyMax {
		fields["body"] = "内容必须为 1 到 5000 个字"
	}
	if len(fields) > 0 {
		return in, httpx.Invalid(fields)
	}
	return in, nil
}

func (s *Service) ListMacros(ctx context.Context, tenantID string) ([]Macro, error) {
	out := []Macro{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, title, body, sort_order, updated_at
			  FROM ticket_macros
			 WHERE tenant_id = $1
			 ORDER BY sort_order, created_at, id`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Macro
			if err := rows.Scan(&m.ID, &m.Title, &m.Body, &m.SortOrder, &m.UpdatedAt); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// SaveMacro 新建（id 为空）或覆盖一条快捷回复，返回它的 id。
func (s *Service) SaveMacro(ctx context.Context, tenantID, actorID, id string, in MacroInput) (string, error) {
	if id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return "", httpx.NotFoundOrForbidden()
		}
	}
	in, err := normalizeMacroInput(in)
	if err != nil {
		return "", err
	}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		if id == "" {
			if err := tx.QueryRow(ctx, `
				INSERT INTO ticket_macros (tenant_id, title, body, sort_order, created_by)
				VALUES ($1, $2, $3, $4, $5::uuid) RETURNING id::text`,
				tenantID, in.Title, in.Body, in.SortOrder, actorID).Scan(&id); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `
				UPDATE ticket_macros SET title = $3, body = $4, sort_order = $5
				 WHERE tenant_id = $1 AND id = $2::uuid`,
				tenantID, id, in.Title, in.Body, in.SortOrder)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return httpx.NotFoundOrForbidden()
			}
		}
		// 摘要只记标题：话术正文可能长达几千字，审计要的是「谁改了哪一条」
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "ticket_macro.saved", ResourceType: "ticket_macro", ResourceID: &id,
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"title": in.Title, "sort_order": in.SortOrder},
		})
	})
	if err != nil {
		return "", macroError(err)
	}
	return id, nil
}

func (s *Service) DeleteMacro(ctx context.Context, tenantID, actorID, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return httpx.NotFoundOrForbidden()
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var title string
		if err := tx.QueryRow(ctx, `
			DELETE FROM ticket_macros WHERE tenant_id = $1 AND id = $2::uuid
			RETURNING title`, tenantID, id).Scan(&title); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "ticket_macro.deleted", ResourceType: "ticket_macro", ResourceID: &id,
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"title": title},
		})
	})
	return macroError(err)
}

func macroError(err error) error {
	if err == nil {
		return nil
	}
	if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
		return err
	}
	return httpx.Internal(err)
}
