// [INPUT]: 依赖 fakegw_test.go 的 fakePanel / testManifest
// [OUTPUT]: burst 子命令的单测：请求形状（X-Real-IP 与可选的 CF-Connecting-IP、Content-Type、无幂等键、Bearer）、reauth_required 后重认证并重放、来回切换回到原状、burst.json 内容、拒绝动别的分组
// [POS]: tools/loadtest/userload 的 burst 测试，打 httptest 假后台；users 的测试在 userload_test.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package userload

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func burstCfg(f *fakePanel, out string) burstConfig {
	return burstConfig{
		adminURL: f.adm.URL, admin: credentials{fakeAdmin, fakeAdminPW}, adminIP: fakeAdminIP,
		op: opBoth, groupCode: "loadtest-burst", count: 2, interval: 10 * time.Millisecond,
		timeout: 5 * time.Second, out: out, ipHeaders: []string{"X-Real-IP", "CF-Connecting-IP"},
	}
}

func TestBurstTogglesBackAndReauths(t *testing.T) {
	m := testManifest(3)
	f := newFakePanel(t, m)
	f.set(func() { f.planMax = ptr(3); f.reauthOnce = true })
	out := t.TempDir()

	bf, err := runBurst(context.Background(), burstCfg(f, out), m, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	violations, groupID, override, creates := f.violations, f.groupID, f.override, f.groupCreates
	f.mu.Unlock()
	if len(violations) > 0 {
		t.Fatalf("X-Real-IP: %v", violations)
	}
	// 跑偶数次回到原状；分组只建一次
	if groupID != nil || override != nil {
		t.Fatalf("not toggled back: group %v override %v", groupID, override)
	}
	if creates != 1 || bf.Group == nil || !bf.Group.Created {
		t.Fatalf("group creates %d, %+v", creates, bf.Group)
	}

	// 请求形状：JSON 体、固定 IP、不带幂等键（前端对这两个接口不带），Bearer 在重认证后换新
	writes := append(f.requests("admin", "/v1/users/"+m.Users[0].ID+"/group"), f.requests("admin", "/v1/subscriptions/")...)
	if len(writes) != 5 { // 换组 2 次 + 第一次被 reauth_required 拦下的 1 次，设备数 2 次
		t.Fatalf("%d writes", len(writes))
	}
	for _, w := range writes {
		if w.method != "POST" || w.ip != fakeAdminIP || w.cf != fakeAdminIP || !strings.HasPrefix(w.ctype, "application/json") ||
			w.idem != "" || w.ua != browserUA {
			t.Errorf("request shape %+v", w)
		}
	}
	groupWrites := f.requests("admin", "/v1/users/"+m.Users[0].ID+"/group")
	if groupWrites[0].auth != "Bearer adm-1" || groupWrites[1].auth != "Bearer adm-2" {
		t.Fatalf("reauth replay: %q then %q", groupWrites[0].auth, groupWrites[1].auth)
	}
	if groupWrites[0].body["group_id"] != groupWrites[1].body["group_id"] || groupWrites[1].body["group_id"] != bf.Group.ID {
		t.Fatalf("replay body changed: %v / %v", groupWrites[0].body, groupWrites[1].body)
	}
	if len(f.requests("admin", "/v1/auth/reauth")) != 1 {
		t.Fatal("expected exactly one reauth")
	}
	// 设备数：套餐 3 → 覆盖 4 → 恢复 null；第一单是 active 的那条订阅
	limits := f.requests("admin", "/v1/subscriptions/")
	if !strings.Contains(limits[0].path, "sub-cur") || limits[0].body["limit"] != float64(4) || limits[1].body["limit"] != nil {
		t.Fatalf("device-limit writes %+v / %+v", limits[0], limits[1])
	}

	// burst.json
	raw, err := os.ReadFile(filepath.Join(out, "burst.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file burstFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Triggers) != 2 || file.UserID != m.Users[0].ID || file.AdminIP != fakeAdminIP {
		t.Fatalf("burst.json %s", raw)
	}
	t1 := file.Triggers[0]
	if !t1.OK || t1.TriggerUnix <= 0 || len(t1.Writes) != 2 || !t1.Writes[1].Reauthed ||
		t1.Writes[1].Field != "users.user_group_id" || t1.Writes[1].To != bf.Group.ID ||
		*t1.Writes[0].EffectiveFrom != 3 || *t1.Writes[0].EffectiveTo != 4 {
		t.Fatalf("trigger 1: %+v", t1)
	}
	if t2 := file.Triggers[1]; t2.Writes[1].To != nil || t2.Writes[1].From != bf.Group.ID || t2.TriggerAt.Before(t1.DoneAt) {
		t.Fatalf("trigger 2: %+v", t2)
	}
	for _, name := range []string{"burst-http.json", "burst-http.txt"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Error(err)
		}
	}
	if strings.Contains(string(raw), fakeAdmin) || strings.Contains(string(raw), fakeAdminPW) {
		t.Fatal("burst.json carries admin credentials")
	}
}

func TestBurstRefusesUserInAnotherGroup(t *testing.T) {
	m := testManifest(1)
	f := newFakePanel(t, m)
	other := "someone-elses-group"
	f.set(func() { f.groupID = &other })
	cfg := burstCfg(f, t.TempDir())
	cfg.op, cfg.count = opGroup, 1
	_, err := runBurst(context.Background(), cfg, m, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "another group") {
		t.Fatalf("got %v", err)
	}
	if len(f.requests("admin", "/v1/users/"+m.Users[0].ID+"/group")) != 0 {
		t.Fatal("wrote the group anyway")
	}
}

func TestNextDeviceLimitAlwaysChangesTheEffectiveValue(t *testing.T) {
	for _, c := range []struct {
		override, planMax *int
		want              *int
	}{
		{nil, nil, ptr(1000)},
		{nil, ptr(0), ptr(1000)},
		{nil, ptr(3), ptr(4)},
		{nil, ptr(1000), ptr(999)},
		{ptr(7), ptr(3), nil},
	} {
		got := nextDeviceLimit(c.override, c.planMax)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("nextDeviceLimit(%v,%v) = %v", c.override, c.planMax, got)
		}
		if c.override == nil && effectiveLimit(got, c.planMax) == effectiveLimit(nil, c.planMax) {
			t.Errorf("effective value unchanged for planMax %v", c.planMax)
		}
	}
}
