// [INPUT]: 依赖同包 service.go 的 GetVisible 可见性判定与 slugPattern，依赖 content_page_feedback 表（00079），依赖 platform 的 db/httpx
// [OUTPUT]: 对外提供 Service.SubmitFeedback
// [POS]: domain/content 的门户「这篇文章有帮助吗」写入口，按（文章、版本、用户）覆盖写；后台统计尚未定形状，暂无读路径
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package content

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// SubmitFeedback 记下用户对某篇文章某个版本「有没有帮助」的判断。
//
// 可见性与 GET 详情同一套判定（同一个 filter）：看不到的文章不能反馈，
// 回 404 而不是 422，免得借反馈接口探测文章是否存在。版本单独校验——
// 用户读的可能是刚被新版取代的旧版，只要那一版真的发布过就收。
// 同一用户重复提交是覆盖，天然幂等，不需要幂等键。
func (s *Service) SubmitFeedback(ctx context.Context, tenantID, userID, slug string, version int, helpful bool, filter VisibleFilter) error {
	if _, err := s.GetVisible(ctx, tenantID, userID, slug, filter); err != nil {
		return err
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	versionInvalid := httpx.Invalid(map[string]string{"version": "该版本不存在"})
	if version <= 0 {
		return versionInvalid
	}
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		var published bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM content_pages
			   WHERE tenant_id = $1 AND slug = $2 AND version = $3
			     AND visibility = 'authenticated' AND published_at IS NOT NULL)`,
			tenantID, slug, version).Scan(&published); err != nil {
			return err
		}
		if !published {
			return versionInvalid
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO content_page_feedback (tenant_id, page_slug, page_version, user_id, helpful)
			VALUES ($1, $2, $3, $4::uuid, $5)
			ON CONFLICT (tenant_id, page_slug, page_version, user_id)
			DO UPDATE SET helpful = EXCLUDED.helpful`,
			tenantID, slug, version, userID, helpful)
		return err
	})
}
