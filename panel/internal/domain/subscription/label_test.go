package subscription

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 配置名：有备注名用备注名，没有用套餐名，都没有只剩站点名；站点名空时用默认站点名。
func TestProfileName(t *testing.T) {
	for _, tc := range []struct{ site, label, plan, want string }{
		{"潘多拉", "妈妈的 iPad", "基础版", "潘多拉 · 妈妈的 iPad"},
		{"潘多拉", "", "基础版", "潘多拉 · 基础版"},
		{"潘多拉", "  ", " 基础版 ", "潘多拉 · 基础版"},
		{"潘多拉", "", "", "潘多拉"},
		{"", "Home", "Basic", "Pandora · Home"},
	} {
		if got := ProfileName(tc.site, tc.label, tc.plan); got != tc.want {
			t.Errorf("ProfileName(%q, %q, %q) = %q, want %q", tc.site, tc.label, tc.plan, got, tc.want)
		}
	}
	// 进 Content-Disposition 时 ASCII 回退名不留双空格，UTF-8 原名完整
	got := ContentDisposition(ProfileName("Pandora", "Mom iPad", "Basic"))
	want := `attachment; filename="Pandora Mom iPad"; filename*=UTF-8''Pandora%20%C2%B7%20Mom%20iPad`
	if got != want {
		t.Fatalf("ContentDisposition(profile)\n got  %s\n want %s", got, want)
	}
}

// 门户订阅列表的 client_name 与订阅下载的文件名只经 ProfileName 拼；改名的 UPDATE
// 把所有权写在 WHERE 里，撞唯一索引按索引名翻成 409，名字规则只经 purchase.NormalizeLabel。
func TestLabelAndProfileNameHaveOneSource(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	if !strings.Contains(pkg.Decl("Service.MySubscriptions"), "ProfileName(site, label, v.PlanName)") {
		t.Fatal("client_name must come from ProfileName")
	}
	set := pkg.Decl("Service.SetLabel")
	for _, needle := range []string{"purchase.NormalizeLabel(", "s.user_id = $3::uuid", "db.ConstraintName(err) == labelUniqueIndex"} {
		if !strings.Contains(set, needle) {
			t.Errorf("SetLabel missing %q", needle)
		}
	}
	if !strings.Contains(pullAuthSQL, "COALESCE(s.label, '')") || !strings.Contains(pullAuthSQL, "LEFT JOIN plans pl ON pl.id = s.plan_id") {
		t.Fatal("pull must read the label and the plan name for the profile name")
	}
	if !strings.Contains(pullAuthSQL, "g.subscription_id = s.id") || strings.Contains(pullAuthSQL, "g.user_id = s.user_id") {
		t.Fatal("pull must count only the packs attached to this subscription")
	}
}
