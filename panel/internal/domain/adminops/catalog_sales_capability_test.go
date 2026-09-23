package adminops

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type staticSalesCapability bool

func (c staticSalesCapability) AllowsP0BSales() bool { return bool(c) }

type nilTrueSalesCapability struct{}

func (*nilTrueSalesCapability) AllowsP0BSales() bool { return true }

type nilPanicSalesCapability struct{}

func (c *nilPanicSalesCapability) AllowsP0BSales() bool {
	if c == nil {
		panic("typed nil capability method must never be called")
	}
	return true
}

func requireCatalogErrorCode(t *testing.T, err error, want httpx.Code) {
	t.Helper()
	var got *httpx.Error
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *httpx.Error", err)
	}
	if got.Code != want {
		t.Fatalf("error code = %q, want %q", got.Code, want)
	}
}

func TestP0BSalesCapabilityDefaultsFailClosedBeforeMutationValidation(t *testing.T) {
	svc := NewService(nil)
	_, _, err := svc.PublishPlanVersion(t.Context(), "tenant", "plan", "version", "actor", 1, 1)
	requireCatalogErrorCode(t, err, httpx.CodeUnavailable)

	_, err = svc.CreatePlanPrice(t.Context(), "tenant", "plan", CreatePriceInput{})
	requireCatalogErrorCode(t, err, httpx.CodeUnavailable)

	for name, capability := range map[string]SalesCapability{
		"explicit false":           staticSalesCapability(false),
		"typed nil returning true": (*nilTrueSalesCapability)(nil),
		"typed nil panicking":      (*nilPanicSalesCapability)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			explicit := NewService(nil, capability)
			_, err := explicit.CreatePlanPrice(t.Context(), "tenant", "plan", CreatePriceInput{})
			requireCatalogErrorCode(t, err, httpx.CodeUnavailable)
		})
	}

	// Multiple values are configuration ambiguity, not an implicit OR.
	ambiguous := NewService(nil, staticSalesCapability(true), staticSalesCapability(true))
	_, err = ambiguous.CreatePlanPrice(t.Context(), "tenant", "plan", CreatePriceInput{})
	requireCatalogErrorCode(t, err, httpx.CodeUnavailable)
}

func TestP0BSalesCapabilityAllowsDomainValidationOnlyWhenInjected(t *testing.T) {
	svc := NewService(nil, staticSalesCapability(true))
	_, _, err := svc.PublishPlanVersion(t.Context(), "tenant", "plan", "version", "actor", 1, 1)
	requireCatalogErrorCode(t, err, httpx.CodeNotFound)
}

func TestRequiresP0BSalesResumeOnlyForActiveFalseToTrue(t *testing.T) {
	tests := []struct {
		name                               string
		status                             string
		beforeNew, beforeRenew, beforeUp   bool
		afterNew, afterRenew, afterUpgrade bool
		want                               bool
	}{
		{name: "active new purchase resumes", status: "active", afterNew: true, want: true},
		{name: "active renewal resumes", status: "active", afterRenew: true, want: true},
		{name: "active upgrade resumes", status: "active", afterUpgrade: true, want: true},
		{name: "active remains enabled", status: "active", beforeNew: true, beforeRenew: true, beforeUp: true, afterNew: true, afterRenew: true, afterUpgrade: true},
		{name: "active disables sales", status: "active", beforeNew: true, beforeRenew: true, beforeUp: true},
		{name: "draft enables flags", status: "draft", afterNew: true, afterRenew: true, afterUpgrade: true},
		{name: "archived is handled separately", status: "archived", afterNew: true, afterRenew: true, afterUpgrade: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := UpdatePlanInput{AllowNewPurchase: tc.afterNew, AllowRenewal: tc.afterRenew, AllowUpgrade: tc.afterUpgrade}
			if got := requiresP0BSalesResume(tc.status, tc.beforeNew, tc.beforeRenew, tc.beforeUp, in); got != tc.want {
				t.Fatalf("requiresP0BSalesResume = %v, want %v", got, tc.want)
			}
		})
	}
}
