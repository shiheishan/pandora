package adminops

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func expectHTTPCode(t *testing.T, err error, code httpx.Code) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != code {
		t.Fatalf("got %v, want HTTP code %s", err, code)
	}
}

func TestValidatePriceAuthoringPolicy(t *testing.T) {
	valid := CreatePriceInput{Currency: "CNY", UnitAmount: 100, BillingInterval: "month", IntervalCount: 1}
	if err := validatePrice(valid); err != nil {
		t.Fatalf("valid price rejected: %v", err)
	}
	invalid := valid
	invalid.Currency = "EUR"
	expectHTTPCode(t, validatePrice(invalid), httpx.CodeValidationFailed)
	invalid = valid
	invalid.IntervalCount = 0
	expectHTTPCode(t, validatePrice(invalid), httpx.CodeValidationFailed)
	now := time.Now()
	before := now.Add(time.Hour)
	after := now
	invalid = valid
	invalid.ValidFrom = &before
	invalid.ValidUntil = &after
	expectHTTPCode(t, validatePrice(invalid), httpx.CodeValidationFailed)
}

func TestValidateVersionSafeReplacement(t *testing.T) {
	in := VersionSemanticsInput{QuotaResetStrategy: "billing_cycle", OveragePolicy: "suspend",
		Entitlements: []EntitlementInput{{Code: "feature.a", Value: json.RawMessage(`true`)}},
		Quotas:       []QuotaInput{{Metric: "traffic.bytes", Unit: "bytes", Period: "cycle"}}}
	if err := validateVersionSemantics(in); err != nil {
		t.Fatalf("valid semantics rejected: %v", err)
	}
	in.Entitlements = append(in.Entitlements, in.Entitlements[0])
	expectHTTPCode(t, validateVersionSemantics(in), httpx.CodeValidationFailed)
}

func TestVersionUpdatePoolContract(t *testing.T) {
	if err := validateVersionUpdatePoolContract(nil); err != nil {
		t.Fatalf("omitted pool_ids rejected: %v", err)
	}
	for _, poolIDs := range [][]string{{}, {"not-a-uuid"}} {
		err := validateVersionUpdatePoolContract(poolIDs)
		expectHTTPCode(t, err, httpx.CodeValidationFailed)
		var he *httpx.Error
		if !errors.As(err, &he) || !strings.Contains(he.Fields["pool_ids"], "POST /v1/plans/{id}/pools") {
			t.Fatalf("pool_ids error does not direct caller to dedicated endpoint: %#v", err)
		}
	}
}

func TestVersionUpdatePoolFieldPresence(t *testing.T) {
	var omitted, explicitEmpty VersionSemanticsInput
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"pool_ids":[]}`), &explicitEmpty); err != nil {
		t.Fatal(err)
	}
	if omitted.PoolIDs != nil {
		t.Fatalf("omitted pool_ids decoded as present: %#v", omitted.PoolIDs)
	}
	if explicitEmpty.PoolIDs == nil {
		t.Fatal("explicit empty pool_ids decoded as omitted")
	}
}

func TestVersionUpdateCannotMutatePoolBindings(t *testing.T) {
	b, err := os.ReadFile("catalog.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "func (s *Service) UpdatePlanVersion")
	end := strings.Index(src, "func (s *Service) PublishPlanVersion")
	if start < 0 || end <= start {
		t.Fatal("could not isolate UpdatePlanVersion source")
	}
	update := src[start:end]
	if !strings.Contains(update, "validateVersionUpdatePoolContract(in.PoolIDs)") {
		t.Fatal("UpdatePlanVersion does not enforce the legacy pool_ids rejection contract")
	}
	if strings.Contains(update, "plan_node_pools") {
		t.Fatal("UpdatePlanVersion can still mutate or inspect plan_node_pools")
	}
	if strings.Contains(update, `"pools":`) {
		t.Fatal("semantic update audit still claims to update pool bindings")
	}
}

func TestPublishPrerequisitesFailClosed(t *testing.T) {
	for _, tt := range []struct {
		name, visibility string
		price            bool
		pools            int
		node             bool
	}{
		{"invite grant missing", "invite_only", true, 1, true},
		{"price missing", "public", false, 1, true},
		{"pool missing", "public", true, 0, true},
		{"serviceable node missing", "public", true, 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expectHTTPCode(t, validatePublishPrerequisites(tt.visibility, tt.price, tt.pools, tt.node), httpx.CodeValidationFailed)
		})
	}
	if err := validatePublishPrerequisites("public", true, 1, true); err != nil {
		t.Fatalf("ready publication rejected: %v", err)
	}
}

func TestOptimisticConflictIs409(t *testing.T) {
	expectHTTPCode(t, rowConflict("套餐", 7), httpx.CodeConflict)
	expectHTTPCode(t, validatePublishTokens(2, 1, 4, 4), httpx.CodeConflict)
	expectHTTPCode(t, validatePublishTokens(2, 2, 4, 3), httpx.CodeConflict)
	if err := validatePublishTokens(2, 2, 4, 4); err != nil {
		t.Fatalf("matching dual tokens rejected: %v", err)
	}
}

func TestPublishGroupPriceCoverage(t *testing.T) {
	groups := []string{"a", "b"}
	if !publishPriceCoverage("group", groups, true, map[string]bool{}) {
		t.Fatal("public price did not cover all groups")
	}
	if !publishPriceCoverage("group", groups, false, map[string]bool{"a": true, "b": true}) {
		t.Fatal("complete group prices rejected")
	}
	if publishPriceCoverage("group", groups, false, map[string]bool{"a": true}) {
		t.Fatal("partial group coverage accepted")
	}
	if publishPriceCoverage("public", nil, false, map[string]bool{}) {
		t.Fatal("public plan without public price accepted")
	}
}
