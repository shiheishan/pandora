package middleware

import (
	"crypto/sha256"
	"testing"
	"time"
)

func validTopupClaim() IdempotencyClaim {
	tenantID := "91000000-0000-7000-8000-000000000001"
	actorID := "91000000-0000-7000-8000-000000000011"
	scope := "balance_topup_create"
	return IdempotencyClaim{
		ID:           "91000000-0000-7000-8000-000000000101",
		TenantID:     tenantID,
		ActorID:      actorID,
		Scope:        scope,
		StorageScope: idempotencyActorScopeCanonical(scope, actorID),
		Key:          "topup-key",
		Generation:   1,
		LockedUntil:  time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		RequestHash:  sha256.Sum256([]byte("request")),
	}
}

func TestValidateIdempotencyClaimAcceptsCompleteExactTopupClaim(t *testing.T) {
	claim := validTopupClaim()
	if err := ValidateIdempotencyClaim(
		claim, claim.TenantID, claim.ActorID, claim.Scope,
	); err != nil {
		t.Fatal(err)
	}
}

func TestValidateIdempotencyClaimRejectsEveryIdentityAndOwnershipMismatch(t *testing.T) {
	base := validTopupClaim()
	tests := []struct {
		name   string
		mutate func(*IdempotencyClaim)
	}{
		{"claim id", func(c *IdempotencyClaim) { c.ID = "" }},
		{"tenant", func(c *IdempotencyClaim) { c.TenantID = "91000000-0000-7000-8000-000000000002" }},
		{"actor", func(c *IdempotencyClaim) { c.ActorID = "91000000-0000-7000-8000-000000000012" }},
		{"base scope", func(c *IdempotencyClaim) { c.Scope = "order_create" }},
		{"storage scope", func(c *IdempotencyClaim) { c.StorageScope += "0" }},
		{"key", func(c *IdempotencyClaim) { c.Key = "" }},
		{"generation", func(c *IdempotencyClaim) { c.Generation = 0 }},
		{"lease", func(c *IdempotencyClaim) { c.LockedUntil = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claim := base
			test.mutate(&claim)
			if err := ValidateIdempotencyClaim(
				claim, base.TenantID, base.ActorID, base.Scope,
			); err == nil {
				t.Fatal("mismatched claim was accepted")
			}
		})
	}
}
