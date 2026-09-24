// [INPUT]: 依赖 platform/pg18test 打开 content 域的一次性库，依赖 service.go 的 PublishVersion 与 feedback.go 的 SubmitFeedback
// [OUTPUT]: 对外提供 TestContentFeedbackPG18
// [POS]: domain/content 的 PG18 测试：反馈按（文章、版本、用户）覆盖写，看不到的文章 404，不存在的版本 422
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package content

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestContentFeedbackPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CONTENT", DatabasePrefix: "pandora_content_",
		MarkerTable: "pandora_content_test_marker", CommentTag: "pandora-content-pg18",
	})
	const (
		tenant  = "81000000-0000-4000-8000-000000000001"
		actor   = "81000000-0000-4000-8000-000000000011"
		user    = "81000000-0000-4000-8000-000000000012"
		plan    = "81000000-0000-4000-8000-000000000031"
		product = "81000000-0000-4000-8000-000000000021"
	)
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','feedback-pg18','Feedback','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + actor + `','` + tenant + `','ops@feedback.invalid','Ops','active'),
			('` + user + `','` + tenant + `','user@feedback.invalid','User','active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed feedback fixture: %v", err)
		}
	}
	svc := New(app)
	publish := func(slug string, expected int, planIDs []string) {
		t.Helper()
		if _, err := svc.PublishVersion(ctx, tenant, actor, "feedback-"+slug+string(rune('0'+expected)), PublishInput{
			Slug: slug, Kind: "kb_article", Title: "连接失败怎么办", Body: "## 先检查\n确认订阅没有过期，再重新导入。",
			Status: "published", TargetPlanIDs: planIDs, ExpectedLatestVersion: expected,
		}); err != nil {
			t.Fatalf("publish %s after v%d: %v", slug, expected, err)
		}
	}
	publish("connect-failed", 0, nil)
	publish("connect-failed", 1, nil) // v1 被 v2 取代、自动归档，但它发布过
	isCode := func(err error, code httpx.Code, field string) bool {
		var httpErr *httpx.Error
		return errors.As(err, &httpErr) && httpErr.Code == code && (field == "" || httpErr.Fields[field] != "")
	}
	helpfulOf := func(version int) (n int, helpful bool) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT count(*), coalesce(bool_or(helpful), false) FROM content_page_feedback
			WHERE tenant_id=$1 AND page_slug='connect-failed' AND page_version=$2 AND user_id=$3`,
			tenant, version, user).Scan(&n, &helpful); err != nil {
			t.Fatal(err)
		}
		return
	}

	if err := svc.SubmitFeedback(ctx, tenant, user, "connect-failed", 2, true, VisibleFilter{}); err != nil {
		t.Fatalf("submit helpful: %v", err)
	}
	// 同一个人对同一版改主意：覆盖，不累计
	if err := svc.SubmitFeedback(ctx, tenant, user, "Connect-Failed", 2, false, VisibleFilter{}); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if n, helpful := helpfulOf(2); n != 1 || helpful {
		t.Fatalf("v2 feedback rows=%d helpful=%v, want 1/false", n, helpful)
	}
	// 读到的是刚被取代的旧版：照收
	if err := svc.SubmitFeedback(ctx, tenant, user, "connect-failed", 1, true, VisibleFilter{}); err != nil {
		t.Fatalf("submit on superseded version: %v", err)
	}
	if n, helpful := helpfulOf(1); n != 1 || !helpful {
		t.Fatalf("v1 feedback rows=%d helpful=%v, want 1/true", n, helpful)
	}
	for _, v := range []int{0, 9} {
		if err := svc.SubmitFeedback(ctx, tenant, user, "connect-failed", v, true, VisibleFilter{}); !isCode(err, httpx.CodeValidationFailed, "version") {
			t.Fatalf("version %d: %v, want 422 fields.version", v, err)
		}
	}

	// 只对某个套餐可见的文章：用户没有该套餐，与详情一样 404，什么都不写
	if _, err := admin.Exec(ctx, `INSERT INTO products(id,tenant_id,code,name,status) VALUES($1,$2,'fb','FB','active')`, product, tenant); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($1,$2,$3,'fb','FB','draft')`, plan, tenant, product); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	publish("vip-only", 0, []string{plan})
	for _, slug := range []string{"vip-only", "missing-page", "Bad Slug"} {
		if err := svc.SubmitFeedback(ctx, tenant, user, slug, 1, true, VisibleFilter{}); !isCode(err, httpx.CodeNotFound, "") {
			t.Fatalf("invisible %q: %v, want 404", slug, err)
		}
	}
	var total int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM content_page_feedback WHERE tenant_id=$1`, tenant).Scan(&total); err != nil || total != 2 {
		t.Fatalf("feedback rows=%d err=%v, want 2", total, err)
	}
}
