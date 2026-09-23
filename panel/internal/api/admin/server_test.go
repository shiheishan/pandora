package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseServerListQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/servers?status=ready&q=%20hk%20", nil)
	in, err := parseServerListQuery(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if in.Status != "ready" || in.Query != "hk" {
		t.Fatalf("unexpected query: %+v", in)
	}

	r = httptest.NewRequest("GET", "/v1/servers?status=online", nil)
	if _, err := parseServerListQuery(r); err == nil {
		t.Fatal("expected invalid status")
	}
}

func TestValidateServerID(t *testing.T) {
	if err := validateServerID("019c1234-5678-7000-8000-000000000001"); err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
	if err := validateServerID("not-a-uuid"); err == nil {
		t.Fatal("invalid id accepted")
	}
}

func TestValidateServerTextLimitsUsesRunes(t *testing.T) {
	if err := validateServerTextLimits(map[string]string{"region": strings.Repeat("港", 64)}); err != nil {
		t.Fatalf("valid multibyte text rejected: %v", err)
	}
	if err := validateServerTextLimits(map[string]string{"region": strings.Repeat("港", 65)}); err == nil {
		t.Fatal("overlong region accepted")
	}
	if err := validateServerTextLimits(map[string]string{"reason": strings.Repeat("x", 501)}); err == nil {
		t.Fatal("overlong reason accepted")
	}
}
