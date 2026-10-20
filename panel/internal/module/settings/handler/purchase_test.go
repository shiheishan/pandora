package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

func (f *fakeSettings) PurchaseLimits(ctx context.Context) (service.PurchaseLimits, error) {
	return service.PurchaseLimits{}, nil
}

func (f *fakeSettings) SetPurchaseLimits(ctx context.Context, v service.PurchaseLimits) error {
	return nil
}

type fakePurchase struct {
	fakeSettings
	limits service.PurchaseLimits
}

func (f *fakePurchase) PurchaseLimits(ctx context.Context) (service.PurchaseLimits, error) {
	return f.limits, nil
}

func (f *fakePurchase) SetPurchaseLimits(ctx context.Context, v service.PurchaseLimits) error {
	f.limits = v
	return nil
}

func TestGetPurchaseLimitsRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakePurchase{limits: service.PurchaseLimits{MaxUnpaidOrders: 1, MaxSubscriptions: 2}}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))

	rec := do(e, http.MethodGet, "/v1/settings/purchase", "", nil)
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthorized" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}
}

func TestSetPurchaseLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakePurchase{}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), testKit(h))
	hdrs := map[string]string{
		"X-Test-Auth":   "1",
		"X-Test-Reauth": "1",
	}

	rec := do(e, http.MethodPost, "/v1/settings/purchase", `{"max_unpaid_orders":3,"max_subscriptions":2}`, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("post status %d body %s", rec.Code, rec.Body.String())
	}

	rec = do(e, http.MethodGet, "/v1/settings/purchase", "", map[string]string{"X-Test-Auth": "1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("get status %d", rec.Code)
	}
	var out purchaseLimitsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.MaxUnpaidOrders != 3 || out.MaxSubscriptions != 2 {
		t.Fatalf("limits %+v", out)
	}

	rec = do(e, http.MethodPost, "/v1/settings/purchase", `{"max_unpaid_orders":3}`, hdrs)
	if rec.Code != http.StatusUnprocessableEntity || errCode(t, rec) != "validation_failed" {
		t.Fatalf("status %d code %s body %s", rec.Code, errCode(t, rec), rec.Body.String())
	}
}
