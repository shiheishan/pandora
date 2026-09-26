// [INPUT]: 依赖 router_source_test.go 的 routerSource，依赖 platform/sourcetest 按名取公告保存、撤回与 notify 定时发布的源码，依赖 platform/httpx 的错误码
// [OUTPUT]: 对外提供 TestAnnouncementRouteContracts、TestAnnouncementWritesCarryAtomicAuditAndCAS、TestAnnouncementLifecycleCannotBypassWithdrawal、TestParseAnnounceTimeRequiresTimezoneAndNormalizesUTC、TestNormalizeAnnouncePlanIDsRejectsInvalidAndCanonicalizes
// [POS]: api/admin 公告的路由门槛、写入审计与乐观锁、状态机与入参规范化
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestAnnouncementRouteContracts(t *testing.T) {
	source, err := routerSource()
	if err != nil {
		t.Fatal(err)
	}
	router := string(source)
	for _, want := range []string{
		`Get("/announcements", h.listAnnouncements)`,
		`middleware.Idempotency(d.Pool, "announcement_save", d.Log)`,
		`Post("/announcements", h.saveAnnouncement)`,
		`Post("/announcements/{id}", h.saveAnnouncement)`,
		`middleware.Idempotency(d.Pool, "announcement_withdraw", d.Log)`,
		`Post("/announcements/{id}/withdraw", h.withdrawAnnouncement)`,
	} {
		if !strings.Contains(router, want) {
			t.Fatalf("announcement route contract missing %q", want)
		}
	}
	assertAnnouncementRouteGuards(t, router, `Get("/announcements", h.listAnnouncements)`,
		[]string{`RequirePermission("ops.announcement.write"`})
	for _, route := range []string{
		`Post("/announcements", h.saveAnnouncement)`,
		`Post("/announcements/{id}", h.saveAnnouncement)`,
	} {
		assertAnnouncementRouteGuards(t, router, route, []string{
			`RequirePermission("ops.announcement.write"`, "RequireRecentReauth",
			`Idempotency(d.Pool, "announcement_save"`,
		})
	}
	assertAnnouncementRouteGuards(t, router,
		`Post("/announcements/{id}/withdraw", h.withdrawAnnouncement)`, []string{
			`RequirePermission("ops.announcement.write"`, "RequireRecentReauth",
			`Idempotency(d.Pool, "announcement_withdraw"`,
		})
}

func assertAnnouncementRouteGuards(t *testing.T, router, route string, guards []string) {
	t.Helper()
	index := strings.Index(router, route)
	if index < 0 {
		t.Fatalf("announcement route missing %q", route)
	}
	window := router[max(0, index-420):index]
	for _, guard := range guards {
		if !strings.Contains(window, guard) {
			t.Fatalf("route %q missing guard %q", route, guard)
		}
	}
}

func TestAnnouncementWritesCarryAtomicAuditAndCAS(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	body := pkg.Decls("handlers.saveAnnouncement", "handlers.withdrawAnnouncement")
	for _, want := range []string{
		`Action: "announcement.saved"`,
		`Action: "announcement.withdrawn"`,
		`return audit.Write(r.Context(), tx, tenantID`,
		`FOR UPDATE`,
		`version=version+1`,
		`newVersion != req.ExpectedVersion`,
		`validateAnnouncementTransition(currentStatus, status)`,
		`announcementAuditSnapshot(currentContent`,
		`validateAnnouncementPlansContext`,
		`httpx.NotFoundOrForbidden()`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("announcement mutation contract missing %q", want)
		}
	}
	if all := pkg.Source(); strings.Contains(all, "withdrawn_at=NULL") || strings.Contains(all, "withdrawn_by=NULL") {
		t.Fatal("announcement update must not erase withdrawal evidence")
	}

	notifyBody := sourcetest.Load(t, "../../domain/notify").Decl("Service.PublishDueAnnouncements")
	for _, want := range []string{
		`RETURNING id::text, version`,
		`Action: "announcement.published"`,
		`return err`,
	} {
		if !strings.Contains(notifyBody, want) {
			t.Fatalf("scheduled publication audit contract missing %q", want)
		}
	}
}

func TestAnnouncementLifecycleCannotBypassWithdrawal(t *testing.T) {
	for name, tc := range map[string]struct {
		current string
		next    string
		ok      bool
	}{
		"draft-to-published":     {"draft", "published", true},
		"scheduled-to-draft":     {"scheduled", "draft", true},
		"published-edit":         {"published", "published", true},
		"published-to-draft":     {"published", "draft", false},
		"published-to-scheduled": {"published", "scheduled", false},
		"withdrawn-to-published": {"withdrawn", "published", false},
		"withdrawn-to-draft":     {"withdrawn", "draft", false},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateAnnouncementTransition(tc.current, tc.next)
			if (err == nil) != tc.ok {
				t.Fatalf("transition %s -> %s err=%v, ok=%v", tc.current, tc.next, err, tc.ok)
			}
		})
	}
}

func TestParseAnnounceTimeRequiresTimezoneAndNormalizesUTC(t *testing.T) {
	got, err := parseAnnounceTime("2026-08-02T15:30:00+08:00")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 2, 7, 30, 0, 0, time.UTC)
	if got == nil || !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("parseAnnounceTime() = %v, want %v in UTC", got, want)
	}

	_, err = parseAnnounceTime("2026-08-02T15:30")
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) || apiErr.Code != httpx.CodeValidationFailed {
		t.Fatalf("timezone-free datetime error = %v, want validation_failed", err)
	}

	empty, err := parseAnnounceTime("  ")
	if err != nil || empty != nil {
		t.Fatalf("blank datetime = (%v, %v), want (nil, nil)", empty, err)
	}
}

func TestNormalizeAnnouncePlanIDsRejectsInvalidAndCanonicalizes(t *testing.T) {
	const upper = "550E8400-E29B-41D4-A716-446655440000"
	const second = "00000000-0000-4000-8000-000000000001"
	got, err := normalizeAnnouncePlanIDs([]string{" " + upper + " ", second, upper})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{second, strings.ToLower(upper)}
	if !slices.Equal(got, want) {
		t.Fatalf("normalizeAnnouncePlanIDs() = %v, want %v", got, want)
	}

	_, err = normalizeAnnouncePlanIDs([]string{"not-a-uuid"})
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) || apiErr.Code != httpx.CodeValidationFailed {
		t.Fatalf("invalid plan ID error = %v, want validation_failed", err)
	}
}
