// [INPUT]: 依赖 security_guards_test.go 的 adminRouteChain / serveGuarded / guardPrincipal，依赖 risk.go 的 clusterRisk、audit_log.go 的 auditExportRange 与 csvSafe
// [OUTPUT]: 对外提供第 ④ 步新接口的路由守卫反向测试与纯函数单测
// [POS]: api/admin 的第 ④ 步不连库测试：缺任一权限 404、未重认证 403，风险分级、导出日期区间与 CSV 公式防护
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/geoip"
)

func TestStepFourAdminRouteGuards(t *testing.T) {
	for _, tc := range []struct {
		method, pattern string
		permissions     []string // 全部都要，缺任何一个都得 404
		reauth          bool
	}{
		{http.MethodGet, "/v1/audit/export", []string{"security.audit.read", "ops.export"}, true},
		{http.MethodPost, "/v1/ip-clusters/{key}/review", []string{"security.risk.review"}, false},
		{http.MethodPost, "/v1/ip-clusters/{key}/disable-accounts", []string{"security.risk.review", "iam.user.write"}, true},
		{http.MethodGet, "/v1/ticket-macros", []string{"ops.ticket.read"}, false},
		{http.MethodPost, "/v1/ticket-macros", []string{"ops.ticket.write"}, false},
		{http.MethodPost, "/v1/ticket-macros/{id}", []string{"ops.ticket.write"}, false},
		{http.MethodDelete, "/v1/ticket-macros/{id}", []string{"ops.ticket.write"}, false},
		{http.MethodGet, "/v1/nodes/{id}/identity", []string{"node.read"}, false},
	} {
		t.Run(tc.method+" "+tc.pattern, func(t *testing.T) {
			h := adminRouteChain(t, tc.method, tc.pattern)
			for i := range tc.permissions {
				partial := append(append([]string{}, tc.permissions[:i]...), tc.permissions[i+1:]...)
				if w := serveGuarded(h, tc.method, guardPrincipal(true, partial...)); w.Code != http.StatusNotFound {
					t.Fatalf("missing %s: status=%d want 404", tc.permissions[i], w.Code)
				}
			}
			if tc.reauth {
				w := serveGuarded(h, tc.method, guardPrincipal(false, tc.permissions...))
				if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "重新验证身份") {
					t.Fatalf("permission without recent reauth: status=%d body=%s", w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestClusterRisk(t *testing.T) {
	for _, tc := range []struct {
		accounts int
		kind     geoip.NetworkKind
		want     string
	}{
		{2, geoip.KindResidential, "low"},
		{3, geoip.KindMobile, "mid"},
		{4, geoip.KindUnknown, "mid"},
		{5, geoip.KindEducation, "high"},
		{2, geoip.KindDatacenter, "high"},
	} {
		if got := clusterRisk(tc.accounts, tc.kind); got != tc.want {
			t.Errorf("clusterRisk(%d,%q)=%s want %s", tc.accounts, tc.kind, got, tc.want)
		}
	}
}

func TestAuditExportRange(t *testing.T) {
	from, to, err := auditExportRange(url.Values{"from": {"2026-09-01"}, "to": {"2026-09-01"}})
	if err != nil || from.Format("2006-01-02") != "2026-09-01" || to.Format("2006-01-02") != "2026-09-02" {
		t.Fatalf("same-day range: from=%v to=%v err=%v", from, to, err)
	}
	if from, to, err := auditExportRange(url.Values{}); err != nil || from != nil || to != nil {
		t.Fatalf("empty range: %v %v %v", from, to, err)
	}
	for _, q := range []url.Values{
		{"from": {"2026/09/01"}}, {"to": {"yesterday"}}, {"from": {"2026-09-03"}, "to": {"2026-09-01"}},
	} {
		if _, _, err := auditExportRange(q); err == nil {
			t.Errorf("%v accepted", q)
		}
	}
}

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "ops@example.com": "ops@example.com", "=HYPERLINK()": "'=HYPERLINK()",
		"+1": "'+1", "-2": "'-2", "@SUM": "'@SUM", "\tx": "'\tx",
	} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q)=%q want %q", in, got, want)
		}
	}
}
