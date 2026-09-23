package public

import (
	"os"
	"strings"
	"testing"
)

func TestContentDeliveryIsAuthenticatedAndNonCacheable(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	router := string(raw)
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
	handler, err := os.ReadFile("content.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(handler), `w.Header().Set("Cache-Control", "no-store")`) != 2 {
		t.Fatal("content list and detail must both prevent browser/shared cache storage")
	}
	service, err := os.ReadFile("../../domain/content/service.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(service), `AND cp.visibility='authenticated'`) ||
		strings.Contains(string(service), `cp.visibility IN ('public','authenticated')`) {
		t.Fatal("authenticated content route must not retain a fake anonymous-public visibility branch")
	}
	for _, forbidden := range []string{`json:"id"`, `json:"target_plan_ids"`, `json:"review_due_at"`, `json:"status"`} {
		if strings.Contains(string(handler), forbidden) {
			t.Fatalf("public content DTO exposes internal field %q", forbidden)
		}
	}
}
