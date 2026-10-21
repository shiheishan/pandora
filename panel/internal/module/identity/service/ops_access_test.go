package service

import (
	"slices"
	"testing"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/google/uuid"
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

func TestAccessTarget(t *testing.T) {
	uid := uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	other := uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	cases := []struct {
		name string
		a    auditsvc.AccessEvent
		want *uuid.UUID
	}{
		{
			name: "匿名登录失败对象 user",
			a: auditsvc.AccessEvent{
				Action:       actionLoginFailed,
				ResourceType: "user",
				ResourceID:   &uid,
			},
			want: &uid,
		},
		{
			name: "有 ActorID 的同动作",
			a: auditsvc.AccessEvent{
				ActorID:      &uid,
				Action:       actionLoginFailed,
				ResourceType: "user",
				ResourceID:   &uid,
			},
		},
		{
			name: "匿名 refresh_reuse 对象 user",
			a: auditsvc.AccessEvent{
				Action:       "session.refresh_reuse",
				ResourceType: "user",
				ResourceID:   &uid,
			},
		},
		{
			name: "匿名登录失败对象 session",
			a: auditsvc.AccessEvent{
				Action:       actionLoginFailed,
				ResourceType: "session",
				ResourceID:   &uid,
			},
		},
		{
			name: "匿名登录失败无 ResourceID",
			a: auditsvc.AccessEvent{
				Action:       actionLoginFailed,
				ResourceType: "user",
			},
		},
	}
	for _, tc := range cases {
		got := accessTarget(tc.a)
		if tc.want == nil {
			if got != nil {
				t.Fatalf("%s: got %v", tc.name, got)
			}
			continue
		}
		if got == nil || *got != *tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
		if got == tc.a.ResourceID {
			t.Fatalf("%s: 返回指针不应与入参 ResourceID 相同", tc.name)
		}
		*got = other
		if *tc.a.ResourceID != uid {
			t.Fatalf("%s: 改返回值不应影响入参", tc.name)
		}
	}
}

func TestHigherRisk(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want string
	}{
		{name: "empty_low", a: "", b: "low", want: "low"},
		{name: "mid_low", a: "mid", b: "low", want: "mid"},
		{name: "low_high", a: "low", b: "high", want: "high"},
		{name: "empty_empty", a: "", b: "", want: ""},
		{name: "high_mid", a: "high", b: "mid", want: "high"},
	}
	for _, tc := range cases {
		if got := higherRisk(tc.a, tc.b); got != tc.want {
			t.Fatalf("%s: higherRisk(%q,%q)=%q want %q", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}
