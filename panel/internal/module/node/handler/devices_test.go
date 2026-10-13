package handler

// 设备数限制接口的 handler 测试：不连库，只测路由、登录、重认证、绑定与校验、端口未装配、DTO 形状。

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

func newDevicesEngine(loggedIn bool, reauth *poolReauthStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	auth := pass
	if !loggedIn {
		auth = func(c *gin.Context) { httpx.Fail(c, errs.New(errs.Unauthorized)) }
	}
	if reauth == nil {
		reauth = &poolReauthStub{}
	}
	kit := route.Kit{
		RequireAuth:  auth,
		RecentReauth: reauth.handler(),
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	New(service.New(nil, nil, nil, nil)).registerDevices(e.Group("/v1"), kit)
	return e
}

func TestDevicesRoutesRequireLogin(t *testing.T) {
	e := newDevicesEngine(false, nil)
	w := doPool(e, http.MethodGet, "/v1/devices", "")
	poolExpectErr(t, w, errs.Unauthorized, "")
	w = doPool(e, http.MethodPost, "/v1/settings/device-limit", `{"mode":"loose"}`)
	poolExpectErr(t, w, errs.Unauthorized, "")
}

func TestDevicePolicyRequiresReauth(t *testing.T) {
	stub := &poolReauthStub{deny: true}
	e := newDevicesEngine(true, stub)
	w := doPool(e, http.MethodPost, "/v1/settings/device-limit", `{"mode":"strict"}`)
	poolExpectErr(t, w, errs.ReauthRequired, "")
	if stub.calls != 1 {
		t.Fatalf("重认证调用 %d 次", stub.calls)
	}
}

func TestDevicePolicyValidation(t *testing.T) {
	e := newDevicesEngine(true, nil)
	cases := []struct {
		body  string
		code  errs.Code
		field string
	}{
		{`{"mode":"x"}`, errs.ValidationFailed, "mode"},
		{`{"mode":"strict","grace":6}`, errs.ValidationFailed, "grace"},
		{`{"mode":"loose","window_minutes":7}`, errs.ValidationFailed, "window_minutes"},
		{`{"mode":"loose","extra":1}`, errs.BadRequest, ""},
	}
	for _, tc := range cases {
		w := doPool(e, http.MethodPost, "/v1/settings/device-limit", tc.body)
		poolExpectErr(t, w, tc.code, tc.field)
	}
}

func TestDevicePolicyValidPassesToService(t *testing.T) {
	e := newDevicesEngine(true, nil)
	w := doPool(e, http.MethodPost, "/v1/settings/device-limit",
		`{"mode":"strict","grace":2,"window_minutes":30}`)
	poolExpectErr(t, w, errs.Unavailable, "")
}

func TestDevicesListUnavailableWithoutPorts(t *testing.T) {
	e := newDevicesEngine(true, nil)
	w := doPool(e, http.MethodGet, "/v1/devices", "")
	poolExpectErr(t, w, errs.Unavailable, "")
}

func TestDevicesResponseShape(t *testing.T) {
	subID := uuid.MustParse("0199a3b4-0000-7000-8000-000000000001")
	userID := uuid.MustParse("0199a3b4-0000-7000-8000-000000000002")
	last := time.Date(2026, 10, 13, 8, 0, 0, 0, time.FixedZone("x", 8*3600))
	resp := devicesResponseFrom(service.DeviceOverview{
		Devices: []service.OnlineDevice{{
			SubscriptionID: subID,
			UserID:         userID,
			Email:          "a@b.c",
			Plan:           "pro",
			Limit:          3,
			Online:         2,
			Nodes:          1,
			Overridden:     true,
			Exceeded:       false,
			LastSeenAt:     last,
		}},
		Mode:          "strict",
		Grace:         1,
		WindowMinutes: 10,
	})
	keys, m := poolJSONKeys(t, resp)
	poolExpectKeys(t, keys, "devices", "grace", "mode", "window_minutes")
	devs, ok := m["devices"].([]any)
	if !ok || len(devs) != 1 {
		t.Fatalf("devices %v", m["devices"])
	}
	row := devs[0].(map[string]any)
	poolExpectKeys(t, poolSortedKeys(row),
		"email", "exceeded", "last_seen_at", "limit", "nodes", "online",
		"overridden", "plan", "subscription_id", "user_id")
	if row["last_seen_at"] != "2026-10-13T00:00:00Z" {
		t.Fatalf("last_seen_at %v", row["last_seen_at"])
	}
	if m["window_minutes"] != float64(10) {
		t.Fatalf("window_minutes %v", m["window_minutes"])
	}
}

func TestDevicesResponseEmpty(t *testing.T) {
	resp := devicesResponseFrom(service.DeviceOverview{
		Mode: "loose", Grace: 1, WindowMinutes: 5,
	})
	_, m := poolJSONKeys(t, resp)
	devs, ok := m["devices"].([]any)
	if !ok {
		t.Fatalf("devices %v", m["devices"])
	}
	if len(devs) != 0 {
		t.Fatalf("devices len %d", len(devs))
	}

	zero := devicesResponseFrom(service.DeviceOverview{
		Devices: []service.OnlineDevice{{SubscriptionID: uuid.New(), UserID: uuid.New()}},
	})
	_, zm := poolJSONKeys(t, zero)
	zdevs := zm["devices"].([]any)
	if zdevs[0].(map[string]any)["last_seen_at"] != nil {
		t.Fatalf("last_seen_at %v", zdevs[0])
	}
}

func TestDevicePolicyOKShape(t *testing.T) {
	raw, err := json.Marshal(okResponse{OK: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("got %s", raw)
	}
}
