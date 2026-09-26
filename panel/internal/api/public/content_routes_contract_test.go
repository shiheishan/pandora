// [INPUT]: 依赖 platform/sourcetest 按名取 NewRouter、内容下发各声明与 domain/content 的可见性查询
// [OUTPUT]: 对外提供 TestContentDeliveryIsAuthenticatedAndNonCacheable
// [POS]: api/public 内容下发：在登录分组内、不可缓存、DTO 不露内部字段、没有伪匿名公开分支
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestContentDeliveryIsAuthenticatedAndNonCacheable(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	router := pkg.Decl("NewRouter")
	auth := strings.Index(router, "r.Use(middleware.RequireAuth(d.Log))")
	list := strings.Index(router, `r.Get("/content/pages", h.listContentPages)`)
	webhook := strings.Index(router, `r.Get("/webhooks/payments/{provider}", h.paymentWebhook)`)
	if auth < 0 || list < 0 || webhook < 0 || !(auth < list && list < webhook) {
		t.Fatal("content delivery routes must remain in the authenticated group")
	}
	for _, want := range []string{
		`r.Get("/content/pages", h.listContentPages)`,
		`r.Get("/content/pages/{slug}", h.getContentPage)`,
	} {
		if strings.Count(router, want) != 1 {
			t.Fatalf("content delivery registration count invalid for %q", want)
		}
	}
	if strings.Count(pkg.Decls("handlers.listContentPages", "handlers.getContentPage"),
		`w.Header().Set("Cache-Control", "no-store")`) != 2 {
		t.Fatal("content list and detail must both prevent browser/shared cache storage")
	}
	service := sourcetest.Load(t, "../../domain/content")
	if !strings.Contains(service.Decl("Service.visible"), `AND cp.visibility='authenticated'`) ||
		strings.Contains(service.Source(), `cp.visibility IN ('public','authenticated')`) {
		t.Fatal("authenticated content route must not retain a fake anonymous-public visibility branch")
	}
	// 内容下发的全部声明（DTO、过滤、列表、详情、反馈）
	delivery := pkg.Decls("contentPageResponse", "deliveryPage", "contentFilter",
		"handlers.listContentPages", "handlers.getContentPage", "contentFeedbackRequest", "handlers.submitContentFeedback")
	for _, forbidden := range []string{`json:"id"`, `json:"target_plan_ids"`, `json:"review_due_at"`, `json:"status"`} {
		if strings.Contains(delivery, forbidden) {
			t.Fatalf("public content DTO exposes internal field %q", forbidden)
		}
	}
}
