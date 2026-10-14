package service

import (
	"slices"
	"testing"
)

// 审计动作到展示分类的映射：按规则顺序取第一个命中的前缀，都不中归 other。
func TestCategoryFromAction(t *testing.T) {
	cases := map[string]string{
		"user.login":             "login",
		"user.registered":        "register",
		"user.password_changed":  "reset_password",
		"user.reset_requested":   "reset_password",
		"order.paid":             "order",
		"admin.admin_granted":    "admin",
		"admin.admin_revoked":    "admin",
		"admin.password_reset":   "admin",
		"node.enroll":            "admin",
		"payment_provider.saved": "admin",
		"payment.succeeded":      "payment",
		"ticket.reply":           "ticket",
		"adminctl.admin_granted": "other",
		"feature_switch.toggle":  "other",
	}
	for action, want := range cases {
		if got := categoryFromAction(action); got != want {
			t.Errorf("%s 应归 %s，得到 %s", action, want, got)
		}
	}
}

// 分类翻成查询前缀：命中的分类 include 自己的前缀、exclude 排在它前面的；other 排除全部；未知分类 ok=false。
func TestAuditCategoryFilter(t *testing.T) {
	inc, exc, ok := auditCategoryFilter("admin")
	if !ok || !slices.Equal(inc, []string{"node.", "route_group.", "admin.", "server.", "plan.", "payment_provider."}) {
		t.Fatalf("admin 的 include 不对：%v %v", inc, ok)
	}
	if !slices.Contains(exc, "order.") || slices.Contains(exc, "payment") {
		t.Fatalf("admin 应排除排在它前面的前缀、不排除后面的：%v", exc)
	}
	// payment 排在 admin 后面，所以要排除 payment_provider.（否则支付渠道的配置变更会被算进支付）
	if _, exc, _ := auditCategoryFilter("payment"); !slices.Contains(exc, "payment_provider.") {
		t.Fatalf("payment 应排除 payment_provider.：%v", exc)
	}
	inc, exc, ok = auditCategoryFilter("other")
	if !ok || inc != nil || !slices.Contains(exc, "ticket.") || !slices.Contains(exc, "user.login") {
		t.Fatalf("other 应不设 include、排除全部前缀：%v %v %v", inc, exc, ok)
	}
	if inc, exc, ok := auditCategoryFilter(""); !ok || inc != nil || exc != nil {
		t.Fatalf("不筛分类应全空：%v %v %v", inc, exc, ok)
	}
	if _, _, ok := auditCategoryFilter("nope"); ok {
		t.Fatal("未知分类应 ok=false")
	}
}
