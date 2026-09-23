package public

import (
	"os"
	"strings"
	"testing"
)

func TestSubscriptionNodePreviewRouteIsAuthenticatedGET(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	router := string(source)
	want := `r.Get("/me/subscriptions/{id}/nodes", h.meSubscriptionNodes)`
	index := strings.Index(router, want)
	if index < 0 {
		t.Fatalf("subscription node preview route missing %q", want)
	}
	auth := strings.LastIndex(router[:index], "r.Use(middleware.RequireAuth")
	group := strings.LastIndex(router[:index], "r.Group(func(r chi.Router)")
	if auth < group {
		t.Fatal("subscription node preview route is outside the authenticated group")
	}
	if strings.Contains(router, `Post("/me/subscriptions/{id}/nodes"`) {
		t.Fatal("subscription node preview must remain read-only")
	}
}

func TestSubscriptionNodePreviewHandlerHasSafeResponseBoundary(t *testing.T) {
	source, err := os.ReadFile("subscribe.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	start := strings.Index(body, "func (h *handlers) meSubscriptionNodes")
	end := strings.Index(body[start:], "func (h *handlers) rotateSubscriptionLink")
	if start < 0 || end < 0 {
		t.Fatal("subscription node preview handler boundary missing")
	}
	handler := body[start : start+end]
	for _, want := range []string{
		`errors.Is(err, subscription.ErrNotFound)`,
		`httpx.NotFoundOrForbidden()`,
		`w.Header().Set("Cache-Control", "no-store")`,
		`json:"name"`, `json:"protocol"`, `json:"traffic_rate"`,
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("subscription node preview handler missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`json:"id"`, `json:"host"`, `json:"port"`, `json:"config"`,
		`json:"server_id"`, `json:"protocol_config"`,
	} {
		if strings.Contains(handler, forbidden) {
			t.Fatalf("subscription node preview exposes forbidden field %q", forbidden)
		}
	}
}
