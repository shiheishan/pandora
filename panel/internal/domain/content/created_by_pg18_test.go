// [INPUT]: 依赖 platform/pg18test 打开 content 域的一次性库，依赖 service.go 的 PublishVersion / ListAdmin
// [OUTPUT]: 对外提供 TestContentAdminListCreatedByPG18
// [POS]: domain/content 的 PG18 测试（契约后台-08）：后台列表每个版本带作者 created_by 与 created_by_name（显示名，缺省邮箱）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package content

import (
	"testing"

	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestContentAdminListCreatedByPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CONTENT", DatabasePrefix: "pandora_content_",
		MarkerTable: "pandora_content_test_marker", CommentTag: "pandora-content-pg18",
	})
	const (
		tenant = "81000000-0000-4000-8000-000000000101"
		named  = "81000000-0000-4000-8000-000000000111"
		plain  = "81000000-0000-4000-8000-000000000112"
	)
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','content-author','Content Author','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		   ('` + named + `','` + tenant + `','editor@content.invalid','编辑小王','active'),
		   ('` + plain + `','` + tenant + `','plain@content.invalid',NULL,'active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	svc := New(app)
	for i, actor := range []string{named, plain} {
		if _, err := svc.PublishVersion(ctx, tenant, actor, "author-"+string(rune('0'+i)), PublishInput{
			Slug: "author-page", Kind: "kb_article", Title: "作者测试", Body: "正文内容足够长的一段说明。",
			Status: "draft", ExpectedLatestVersion: i,
		}); err != nil {
			t.Fatalf("publish v%d: %v", i+1, err)
		}
	}
	pages, err := svc.ListAdmin(ctx, tenant, named, ListFilter{})
	if err != nil || len(pages) != 2 {
		t.Fatalf("list pages=%d err=%v", len(pages), err)
	}
	// 按 version 倒序：v2 是 plain（没有显示名，回退邮箱），v1 是 named
	if pages[0].CreatedBy == nil || *pages[0].CreatedBy != plain || pages[0].CreatedByName == nil || *pages[0].CreatedByName != "plain@content.invalid" ||
		pages[1].CreatedBy == nil || *pages[1].CreatedBy != named || pages[1].CreatedByName == nil || *pages[1].CreatedByName != "编辑小王" {
		t.Fatalf("authors: v%d=%v/%v v%d=%v/%v", pages[0].Version, pages[0].CreatedBy, pages[0].CreatedByName,
			pages[1].Version, pages[1].CreatedBy, pages[1].CreatedByName)
	}
}
