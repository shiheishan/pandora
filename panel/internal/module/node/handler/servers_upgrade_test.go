package handler

// 批量在线升级与服务器 DTO 升级字段的 handler 测试：不连库。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

func newUpgradeEngine(reauth *poolReauthStub, scopes *[]string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: reauth.handler(),
		Idempotency: func(scope string) gin.HandlerFunc {
			if scopes != nil {
				*scopes = append(*scopes, scope)
			}
			return pass
		},
		Switch: func(string) gin.HandlerFunc { return pass },
	}
	New(service.New(nil, nil, nil, nil)).registerInventory(e.Group("/v1"), kit)
	return e
}

func TestUpgradeServersValidation(t *testing.T) {
	e := newUpgradeEngine(&poolReauthStub{}, nil)

	w := doPool(e, http.MethodPost, "/v1/servers/upgrade", `{"server_ids":["nope"]}`)
	poolExpectErr(t, w, errs.ValidationFailed, "server_ids")

	for _, body := range []string{`{"server_ids":[]}`, `{}`} {
		w = doPool(e, http.MethodPost, "/v1/servers/upgrade", body)
		poolExpectErr(t, w, errs.ValidationFailed, "server_ids")
	}

	ids := make([]string, 51)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	w = doPool(e, http.MethodPost, "/v1/servers/upgrade", `{"server_ids":`+mustJSON(t, ids)+`}`)
	poolExpectErr(t, w, errs.ValidationFailed, "server_ids")

	one := uuid.NewString()
	w = doPool(e, http.MethodPost, "/v1/servers/upgrade", `{"server_ids":["`+one+`"],"force":true}`)
	poolExpectErr(t, w, errs.BadRequest, "")

	w = doPool(e, http.MethodPost, "/v1/servers/upgrade", `{"server_ids":["`+uuid.NewString()+`"]}`)
	poolExpectErr(t, w, errs.Unavailable, "")
}

func TestUpgradeServersRouteGuards(t *testing.T) {
	var scopes []string
	e := newUpgradeEngine(&poolReauthStub{deny: true}, &scopes)
	w := doPool(e, http.MethodPost, "/v1/servers/upgrade", `{"server_ids":["`+uuid.NewString()+`"]}`)
	poolExpectErr(t, w, errs.ReauthRequired, "")
	if !containsString(scopes, "server_upgrade") {
		t.Fatalf("幂等 scope 应含 server_upgrade，得到 %v", scopes)
	}
}

func TestServerResponseUpgradeShape(t *testing.T) {
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	can := serverFrom(service.Server{
		Upgrade: service.ServerUpgradeOption{
			Available:     true,
			TargetVersion: "v2026.10.21",
		},
	})
	_, m := poolJSONKeys(t, can)
	if m["binary_sha256"] != nil {
		t.Fatalf("binary_sha256 应为 null，得到 %#v", m["binary_sha256"])
	}
	up, ok := m["upgrade"].(map[string]any)
	if !ok {
		t.Fatal("upgrade 应为对象")
	}
	if up["available"] != true || up["target_version"] != "v2026.10.21" || up["reason"] != nil {
		t.Fatalf("可升级 upgrade 形状不对：%#v", up)
	}
	if m["last_upgrade"] != nil {
		t.Fatalf("last_upgrade 应为 null，得到 %#v", m["last_upgrade"])
	}

	reasonCode := "confirm_timeout"
	blocked := serverFrom(service.Server{
		Upgrade: service.ServerUpgradeOption{
			TargetVersion: "",
			Reason:        service.UpgradeBlockManifestMissing,
		},
		LastUpgrade: &service.ServerUpgradeJob{
			Status:        service.UpgradeRolledBack,
			TargetVersion: "v2026.10.21",
			ReasonCode:    &reasonCode,
			UpdatedAt:     now,
		},
	})
	_, m = poolJSONKeys(t, blocked)
	up = m["upgrade"].(map[string]any)
	if up["target_version"] != nil || up["reason"] != service.UpgradeBlockManifestMissing {
		t.Fatalf("不可升级 upgrade 形状不对：%#v", up)
	}
	last, ok := m["last_upgrade"].(map[string]any)
	if !ok {
		t.Fatal("last_upgrade 应为对象")
	}
	poolExpectKeys(t, poolSortedKeys(last), "reason_code", "status", "target_version", "updated_at")
}

func TestServerUpgradeResponseShape(t *testing.T) {
	empty := serverUpgradeFrom(service.UpgradeBatch{TargetVersion: "v2026.10.21", Results: nil})
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	res, ok := top["results"].([]any)
	if !ok {
		t.Fatalf("results 应为数组，得到 %#v", top["results"])
	}
	if len(res) != 0 {
		t.Fatalf("results 应为空数组，长度 %d", len(res))
	}

	okID := uuid.New()
	missID := uuid.New()
	batch := serverUpgradeFrom(service.UpgradeBatch{
		TargetVersion: "v2026.10.21",
		Results: []service.UpgradeResult{
			{ServerID: okID, OK: true, Reason: ""},
			{ServerID: missID, OK: false, Reason: service.UpgradeBlockNotFound},
		},
	})
	raw, err = json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	items, ok := top["results"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("results 应有 2 条，得到 %#v", top["results"])
	}
	first := items[0].(map[string]any)
	if first["ok"] != true || first["reason"] != nil {
		t.Fatalf("成功条 reason 应为 null：%#v", first)
	}
	second := items[1].(map[string]any)
	if second["ok"] != false || second["reason"] != service.UpgradeBlockNotFound {
		t.Fatalf("失败条 reason 应为 not_found：%#v", second)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
