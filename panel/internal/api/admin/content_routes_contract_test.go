package admin

import (
	"os"
	"strings"
	"testing"
)

func TestContentPageRouteContracts(t *testing.T) {
	source, err := routerSource()
	if err != nil {
		t.Fatal(err)
	}
	router := string(source)
	for _, want := range []string{
		`Get("/content-pages", h.listContentPages)`,
		`Get("/content-pages/{id}", h.getContentPage)`,
		`middleware.Idempotency(d.Pool, "content_page_version_create", d.Log)`,
		`).Post("/content-pages", h.publishContentVersion)`,
		`middleware.Idempotency(d.Pool, "content_page_archive", d.Log)`,
		`).Post("/content-pages/{id}/archive", h.archiveContentPage)`,
	} {
		if !strings.Contains(router, want) {
			t.Fatalf("content route contract missing %q", want)
		}
	}
	if strings.Count(router, `middleware.RequirePermission("ops.content.write", d.Log)`) != 4 {
		t.Fatal("every content route must require ops.content.write")
	}
	start := strings.Index(router, `middleware.Idempotency(d.Pool, "content_page_version_create"`)
	if start < 0 || !strings.Contains(router[start-180:start], "middleware.RequireRecentReauth") {
		t.Fatal("version creation must require recent reauthentication before idempotency")
	}
	start = strings.Index(router, `middleware.Idempotency(d.Pool, "content_page_archive"`)
	if start < 0 || !strings.Contains(router[start-180:start], "middleware.RequireRecentReauth") {
		t.Fatal("archive must require recent reauthentication before idempotency")
	}
}

func TestContentWritesCarryTransactionalAudit(t *testing.T) {
	source, err := os.ReadFile("../../domain/content/service.go")
	if err != nil {
		t.Fatal(err)
	}
	service := string(source)
	for _, want := range []string{
		`Action: "content.version_created"`,
		`Action: "content.archived"`,
		`return audit.Write(ctx, tx, tenantID`,
		`if latest != in.ExpectedLatestVersion`,
		`"superseded": superseded`,
		`RETURNING id::text,version`,
		`json:"latest_version,omitempty"`,
		`json:"is_latest_in_audience,omitempty"`,
	} {
		if !strings.Contains(service, want) {
			t.Fatalf("transactional content audit contract missing %q", want)
		}
	}
}
