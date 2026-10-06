package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestSubscriptionNodePreviewRouteIsAuthenticatedGET(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	router := pkg.Decl("NewRouter")
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
	if strings.Contains(pkg.Source(), `Post("/me/subscriptions/{id}/nodes"`) {
		t.Fatal("subscription node preview must remain read-only")
	}
}

func TestSubscriptionNodePreviewHandlerHasSafeResponseBoundary(t *testing.T) {
	handler := sourcetest.Load(t, ".").Decl("handlers.meSubscriptionNodes")
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
