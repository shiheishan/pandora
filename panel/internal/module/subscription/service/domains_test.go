package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNormalizeBaseURL(t *testing.T) {
	ok := []struct {
		in, want string
	}{
		{"https://Sub.Example.TEST/", "https://sub.example.test"},
		{"https://a.example.test:8443", "https://a.example.test:8443"},
		{"  https://a.example.test/  ", "https://a.example.test"},
	}
	for _, c := range ok {
		got, err := NormalizeBaseURL(c.in)
		if err != nil || got != c.want {
			t.Fatalf("%q: got %q err %v", c.in, got, err)
		}
	}

	errWant := []struct {
		in   string
		want string
	}{
		{"http://a.example.test", "必须以 https:// 开头"},
		{"https://", "必须包含域名"},
		{"https://user@a.example.test", "不能包含用户名或密码"},
		{"https://a.example.test/x", "只能写到域名（可带端口），不能带路径、参数"},
		{"https://a.example.test?q=1", "只能写到域名（可带端口），不能带路径、参数"},
		{"https://a.example.test#f", "只能写到域名（可带端口），不能带路径、参数"},
		{"https://" + strings.Repeat("a", 250) + ".test", "长度不能超过 255"},
		{"https://a_b.example.test", "域名只能含小写字母、数字、点和短横线（中文域名请写成 xn-- 开头的形式）"},
		{"https://a.example.test:0", "端口必须在 1–65535 之间"},
		{"https://a.example.test:70000", "端口必须在 1–65535 之间"},
		{"https://a.example.test:abc", "格式不正确"},
	}
	for _, c := range errWant {
		_, err := NormalizeBaseURL(c.in)
		if err == nil || err.Error() != c.want {
			t.Fatalf("%q: err %v want %q", c.in, err, c.want)
		}
	}
}

func assertInvalidGroups(t *testing.T, err error, wantMsg string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed {
		t.Fatalf("want errs.Invalid: %v", err)
	}
	if e.Fields["groups"] != wantMsg {
		t.Fatalf("groups %q want %q", e.Fields["groups"], wantMsg)
	}
}

func TestNormalizeDomainGroups(t *testing.T) {
	many := make([]DomainGroupInput, 21)
	for i := range many {
		many[i] = DomainGroupInput{Name: fmt.Sprintf("g%d", i), Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 1}}}
	}
	_, err := normalizeDomainGroups(many)
	assertInvalidGroups(t, err, "订阅域名组最多 20 个")

	_, err = normalizeDomainGroups([]DomainGroupInput{{Name: "  ", Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 1}}}})
	assertInvalidGroups(t, err, "第 1 组：名称不能为空")

	longName := strings.Repeat("字", 41)
	_, err = normalizeDomainGroups([]DomainGroupInput{{Name: longName, Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 1}}}})
	assertInvalidGroups(t, err, "第 1 组：名称最多 40 个字")

	_, err = normalizeDomainGroups([]DomainGroupInput{
		{Name: "同名", Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 1}}},
		{Name: "同名", Domains: []Domain{{BaseURL: "https://b.example.test", Weight: 1}}},
	})
	assertInvalidGroups(t, err, "第 2 组：名称和第 1 组重复")

	_, err = normalizeDomainGroups([]DomainGroupInput{
		{Name: "a", IsDefault: true, Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 1}}},
		{Name: "b", IsDefault: true, Domains: []Domain{{BaseURL: "https://b.example.test", Weight: 1}}},
	})
	assertInvalidGroups(t, err, "只能有一个缺省组")

	_, err = normalizeDomainGroups([]DomainGroupInput{{Name: "空", Domains: []Domain{}}})
	assertInvalidGroups(t, err, "第 1 组：至少要有一个域名")

	dom11 := make([]Domain, 11)
	for i := range dom11 {
		dom11[i] = Domain{BaseURL: fmt.Sprintf("https://d%d.example.test", i), Weight: 1}
	}
	_, err = normalizeDomainGroups([]DomainGroupInput{{Name: "多", Domains: dom11}})
	assertInvalidGroups(t, err, "第 1 组：域名最多 10 个")

	_, err = normalizeDomainGroups([]DomainGroupInput{{Name: "坏", Domains: []Domain{{BaseURL: "http://bad.example.test", Weight: 1}}}})
	assertInvalidGroups(t, err, "第 1 组第 1 个域名：必须以 https:// 开头")

	_, err = normalizeDomainGroups([]DomainGroupInput{{Name: "重", Domains: []Domain{
		{BaseURL: "https://dup.example.test", Weight: 1},
		{BaseURL: "https://dup.example.test/", Weight: 1},
	}}})
	assertInvalidGroups(t, err, "第 1 组：https://dup.example.test 重复了")

	_, err = normalizeDomainGroups([]DomainGroupInput{{Name: "w", Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 101}}}})
	assertInvalidGroups(t, err, "第 1 组第 1 个域名：权重必须是 1–100 的整数")

	_, err = normalizeDomainGroups([]DomainGroupInput{{Name: "w2", Domains: []Domain{{BaseURL: "https://a.example.test", Weight: -1}}}})
	assertInvalidGroups(t, err, "第 1 组第 1 个域名：权重必须是 1–100 的整数")

	ug := uuid.New()
	_, err = normalizeDomainGroups([]DomainGroupInput{
		{Name: "g1", Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 1}}, UserGroupIDs: []uuid.UUID{ug}},
		{Name: "g2", Domains: []Domain{{BaseURL: "https://b.example.test", Weight: 1}}, UserGroupIDs: []uuid.UUID{ug}},
	})
	assertInvalidGroups(t, err, "同一个用户组只能分到一个域名组")

	ug1, ug2 := uuid.New(), uuid.New()
	if ug1.String() > ug2.String() { // 结果按字符串升序，固定 ug1 在前
		ug1, ug2 = ug2, ug1
	}
	out, err := normalizeDomainGroups([]DomainGroupInput{{
		Name:         "  名  ",
		IsDefault:    true,
		Domains:      []Domain{{BaseURL: "https://SUB-A.example.test/", Weight: 0}},
		UserGroupIDs: []uuid.UUID{uuid.Nil, ug2, ug1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Name != "名" || out[0].Domains[0].BaseURL != "https://sub-a.example.test" || out[0].Domains[0].Weight != 1 {
		t.Fatalf("规范化 %+v", out[0])
	}
	if len(out[0].UserGroupIDs) != 2 || out[0].UserGroupIDs[0] != ug1 || out[0].UserGroupIDs[1] != ug2 {
		t.Fatalf("UserGroupIDs %v", out[0].UserGroupIDs)
	}
}

func TestDomainGroupsFromRows(t *testing.T) {
	g1, g2 := uuid.New(), uuid.New()
	ug := uuid.New()
	rows := repo.DomainConfigRows{
		Groups: []repo.DomainGroupRow{
			{ID: g1, Name: "甲", IsDefault: true, Position: 0},
			{ID: g2, Name: "乙", IsDefault: false, Position: 1},
		},
		Domains: []repo.DomainRow{
			{GroupID: g1, BaseURL: "https://a.example.test", Weight: 1, Position: 0},
			{GroupID: g2, BaseURL: "https://b.example.test", Weight: 2, Position: 0},
		},
		Assigns: []repo.DomainAssignRow{{UserGroupID: ug, GroupID: g2}},
	}
	groups := domainGroupsFromRows(rows)
	if len(groups) != 2 || groups[0].Domains[0].BaseURL != "https://a.example.test" {
		t.Fatalf("组1 %+v", groups[0])
	}
	if groups[1].UserGroupIDs[0] != ug || groups[1].Domains[0].Weight != 2 {
		t.Fatalf("组2 %+v", groups[1])
	}
	empty := domainGroupsFromRows(repo.DomainConfigRows{
		Groups: []repo.DomainGroupRow{{ID: g1, Name: "空", Position: 0}},
	})
	if empty[0].Domains == nil || empty[0].UserGroupIDs == nil {
		t.Fatal("空组切片应为非 nil")
	}
}

func TestSameDomainConfig(t *testing.T) {
	ug := uuid.New()
	old := []DomainGroup{{
		Name: "n", IsDefault: true,
		Domains:      []Domain{{BaseURL: "https://a.example.test", Weight: 1}},
		UserGroupIDs: []uuid.UUID{ug},
	}}
	in := []DomainGroupInput{{
		Name: "n", IsDefault: true,
		Domains:      []Domain{{BaseURL: "https://a.example.test", Weight: 1}},
		UserGroupIDs: []uuid.UUID{ug},
	}}
	if !sameDomainConfig(old, in) {
		t.Fatal("相同应为真")
	}
	if sameDomainConfig(old, []DomainGroupInput{{Name: "x", IsDefault: true, Domains: in[0].Domains, UserGroupIDs: in[0].UserGroupIDs}}) {
		t.Fatal("改名应为假")
	}
	if sameDomainConfig(old, []DomainGroupInput{{Name: "n", IsDefault: false, Domains: in[0].Domains, UserGroupIDs: in[0].UserGroupIDs}}) {
		t.Fatal("改缺省应为假")
	}
	if sameDomainConfig(old, []DomainGroupInput{{Name: "n", IsDefault: true, Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 2}}, UserGroupIDs: in[0].UserGroupIDs}}) {
		t.Fatal("改权重应为假")
	}
	if sameDomainConfig(old, []DomainGroupInput{{Name: "n", IsDefault: true, Domains: []Domain{{BaseURL: "https://b.example.test", Weight: 1}, {BaseURL: "https://a.example.test", Weight: 1}}, UserGroupIDs: in[0].UserGroupIDs}}) {
		t.Fatal("换域名顺序应为假")
	}
	if sameDomainConfig(old, []DomainGroupInput{{Name: "n", IsDefault: true, Domains: in[0].Domains, UserGroupIDs: []uuid.UUID{uuid.New()}}}) {
		t.Fatal("改用户组应为假")
	}
}

func TestDomainSet(t *testing.T) {
	ugHit, ugMiss := uuid.New(), uuid.New()
	def := DomainGroup{ID: uuid.New(), Name: "缺省", IsDefault: true, Domains: []Domain{{BaseURL: "https://d.example.test", Weight: 1}}}
	vip := DomainGroup{ID: uuid.New(), Name: "专线", Domains: []Domain{{BaseURL: "https://v.example.test", Weight: 1}}, UserGroupIDs: []uuid.UUID{ugHit}}
	ds := newDomainSet([]DomainGroup{def, vip})

	if g := ds.groupFor(&ugHit); g == nil || g.Name != "专线" {
		t.Fatalf("命中用户组 %+v", g)
	}
	if g := ds.groupFor(&ugMiss); g == nil || g.Name != "缺省" {
		t.Fatalf("没命中走缺省 %+v", g)
	}
	if g := ds.groupFor(nil); g == nil || g.Name != "缺省" {
		t.Fatalf("nil 用户组走缺省 %+v", g)
	}

	onlyDef := newDomainSet([]DomainGroup{def})
	if onlyDef.needsUserGroup() {
		t.Fatal("只有缺省组 needsUserGroup 应为假")
	}

	onlyVIP := newDomainSet([]DomainGroup{vip})
	if g := onlyVIP.groupFor(&ugMiss); g != nil {
		t.Fatal("没缺省且未命中应 nil")
	}

	dsSub := newDomainSet([]DomainGroup{{IsDefault: true, Domains: []Domain{{BaseURL: "https://sub.example.test", Weight: 1}}}})
	for _, host := range []string{"SUB.example.test", "sub.example.test:443", "sub.example.test."} {
		base, ok := dsSub.hostBaseURL(host)
		if !ok || base != "https://sub.example.test" {
			t.Fatalf("host %q => %q ok=%v", host, base, ok)
		}
	}
	with8443 := newDomainSet([]DomainGroup{{IsDefault: true, Domains: []Domain{{BaseURL: "https://sub.example.test:8443", Weight: 1}}}})
	if base, ok := with8443.hostBaseURL("sub.example.test:8443"); !ok || base != "https://sub.example.test:8443" {
		t.Fatalf("8443 登记: %q %v", base, ok)
	}
	if _, ok := dsSub.hostBaseURL("sub.example.test:8443"); ok {
		t.Fatal("未登记 8443 应假")
	}
	if _, ok := dsSub.hostBaseURL(""); ok {
		t.Fatal("空串应假")
	}
	if _, ok := dsSub.hostBaseURL("unknown.example.test"); ok {
		t.Fatal("未登记应假")
	}
}

func TestPickWeighted(t *testing.T) {
	if pickWeighted(nil, nil) != "" {
		t.Fatal("空回 \"\"")
	}
	one := []Domain{{BaseURL: "https://only.example.test", Weight: 1}}
	if pickWeighted(one, nil) != "https://only.example.test" {
		t.Fatal("一个直接回")
	}
	doms := []Domain{{BaseURL: "https://a.example.test", Weight: 1}, {BaseURL: "https://b.example.test", Weight: 3}}
	bCount := 0
	const n = 4000
	for i := 0; i < n; i++ {
		u := pickWeighted(doms, nil)
		if u != "https://a.example.test" && u != "https://b.example.test" {
			t.Fatalf("结果不在候选 %q", u)
		}
		if u == "https://b.example.test" {
			bCount++
		}
	}
	ratio := float64(bCount) / float64(n)
	if ratio < 0.70 || ratio > 0.80 {
		t.Fatalf("b 占比 %.3f 不在 0.70–0.80", ratio)
	}
	aCount := 0
	scores := map[string]float64{"https://a.example.test": 3, "https://b.example.test": 1}
	for i := 0; i < n; i++ {
		if pickWeighted(doms, scores) == "https://a.example.test" {
			aCount++
		}
	}
	ratioA := float64(aCount) / float64(n)
	if ratioA < 0.42 || ratioA > 0.58 {
		t.Fatalf("有 scores 后 a 占比 %.3f 不在 0.42–0.58", ratioA)
	}
}

func TestStableBaseURL(t *testing.T) {
	if stableBaseURL(nil) != "" {
		t.Fatal("nil 回 \"\"")
	}
	g := &DomainGroup{Domains: []Domain{
		{BaseURL: "https://a.example.test", Weight: 1},
		{BaseURL: "https://b.example.test", Weight: 5},
		{BaseURL: "https://c.example.test", Weight: 5},
	}}
	if stableBaseURL(g) != "https://b.example.test" {
		t.Fatal("最高权重")
	}
	g2 := &DomainGroup{Domains: []Domain{
		{BaseURL: "https://x.example.test", Weight: 3},
		{BaseURL: "https://y.example.test", Weight: 3},
	}}
	if stableBaseURL(g2) != "https://x.example.test" {
		t.Fatal("同权重取前面的")
	}
}
