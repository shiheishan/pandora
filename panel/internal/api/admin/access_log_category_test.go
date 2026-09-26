// [INPUT]: 依赖 access_log.go 的 accessCategoryRules / categoryFromAction / auditCategoryFilter
// [OUTPUT]: 对外提供访问日志分类的单元测试
// [POS]: api/admin 的缺陷 14 守卫：展示归类与 SQL 筛选出自同一张规则表，二者对任意 action 给出同一个答案
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"strings"
	"testing"
)

// sqlWouldMatch 复述 accessLogList 里那段 SQL：命中 include 之一（include 为空视为命中），
// 且不命中 exclude 之一。
func sqlWouldMatch(action string, include, exclude []string) bool {
	hit := len(include) == 0
	for _, p := range include {
		if strings.HasPrefix(action, p) {
			hit = true
		}
	}
	for _, p := range exclude {
		if strings.HasPrefix(action, p) {
			return false
		}
	}
	return hit
}

func TestAccessLogCategoryFilterAgreesWithDisplay(t *testing.T) {
	actions := []string{
		"user.login", "user.login.failed", "user.registered", "user.password.changed", "user.reset",
		"order.created", "order.manual_created", "payment.captured", "payment_provider.toggle",
		"ticket.close", "node.update", "server.status", "plan.publish", "adminctl.bootstrap",
		"appearance.theme.save", "session.revoked_by_user", "",
	}
	categories := []string{"other"}
	for _, rule := range accessCategoryRules {
		categories = append(categories, rule.category)
	}
	for _, cat := range categories {
		include, exclude, ok := auditCategoryFilter(cat)
		if !ok {
			t.Fatalf("category %q unknown to the filter", cat)
		}
		for _, a := range actions {
			if got, want := sqlWouldMatch(a, include, exclude), categoryFromAction(a) == cat; got != want {
				t.Errorf("category=%s action=%q: SQL filter says %v, display says %v", cat, a, got, want)
			}
		}
	}
}

func TestAccessLogCategoryEdges(t *testing.T) {
	// 后台改支付渠道是管理动作，不是用户支付
	if got := categoryFromAction("payment_provider.toggle"); got != "admin" {
		t.Fatalf("payment_provider.* = %s, want admin", got)
	}
	if got := categoryFromAction("payment.captured"); got != "payment" {
		t.Fatalf("payment.* = %s, want payment", got)
	}
	// 空分类不过滤；不认识的分类要报错而不是退化成不过滤
	if inc, exc, ok := auditCategoryFilter(""); !ok || inc != nil || exc != nil {
		t.Fatal("empty category must mean no filter")
	}
	if _, _, ok := auditCategoryFilter("bogus"); ok {
		t.Fatal("unknown category must be rejected")
	}
}
