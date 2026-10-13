package render

import "testing"

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
