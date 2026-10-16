package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func adminCtx(admin uuid.UUID) context.Context {
	return authn.With(context.Background(), authn.Principal{
		UserID:   admin,
		Audience: authn.AudienceAdmin,
	})
}

func TestCreateAdjustmentSuccess(t *testing.T) {
	e := newTestEnv()
	admin := uuid.New()
	e.ports.emails[admin] = "admin@example.test"
	ctx := adminCtx(admin)

	got, err := e.svc.CreateAdjustment(ctx, CreateAdjustmentInput{
		Amount: dec("12.34"),
		Reason: "补录线下收款",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectiveOn != "2026-03-10" {
		t.Fatalf("EffectiveOn: got %q want 2026-03-10", got.EffectiveOn)
	}
	if len(e.store.adjustments) != 1 {
		t.Fatalf("store rows: %d", len(e.store.adjustments))
	}
	if e.store.adjustments[0].CreatedBy != admin {
		t.Fatalf("CreatedBy mismatch")
	}
	if len(e.audit.events) != 1 || e.audit.events[0].Action != ActionAdjustmentCreate {
		t.Fatalf("audit: %+v", e.audit.events)
	}
	if got.CreatedByEmail == nil || *got.CreatedByEmail != "admin@example.test" {
		t.Fatalf("email: %+v", got.CreatedByEmail)
	}
}

func TestCreateAdjustmentValidation(t *testing.T) {
	cases := []struct {
		name string
		in   CreateAdjustmentInput
		key  string
	}{
		{"amount zero", CreateAdjustmentInput{Amount: dec("0"), Reason: "补录线下收款说明"}, "amount"},
		{"amount over max", CreateAdjustmentInput{Amount: dec("10000000000.01"), Reason: "补录线下收款说明"}, "amount"},
		{"amount precision", CreateAdjustmentInput{Amount: dec("1.234"), Reason: "补录线下收款说明"}, "amount"},
		{"reason short", CreateAdjustmentInput{Amount: dec("1"), Reason: "短"}, "reason"},
		{"reason null", CreateAdjustmentInput{Amount: dec("1"), Reason: "abcde\x00f"}, "reason"},
		{"effective future", CreateAdjustmentInput{Amount: dec("1"), Reason: "补录线下收款说明", EffectiveOn: "2026-03-11"}, "effective_on"},
		{"effective bad format", CreateAdjustmentInput{Amount: dec("1"), Reason: "补录线下收款说明", EffectiveOn: "2026/03/01"}, "effective_on"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv()
			admin := uuid.New()
			_, err := e.svc.CreateAdjustment(adminCtx(admin), tc.in)
			var pe *errs.Error
			if !errors.As(err, &pe) || pe.Code != errs.ValidationFailed {
				t.Fatalf("err: %v", err)
			}
			if _, ok := pe.Fields[tc.key]; !ok {
				t.Fatalf("fields: %v", pe.Fields)
			}
			if len(e.store.adjustments) != 0 {
				t.Fatal("expected no rows")
			}
			if len(e.audit.events) != 0 {
				t.Fatal("expected no audit")
			}
		})
	}
}

func TestCreateAdjustmentUnauthorized(t *testing.T) {
	e := newTestEnv()
	_, err := e.svc.CreateAdjustment(context.Background(), CreateAdjustmentInput{
		Amount: dec("1"),
		Reason: "补录线下收款说明",
	})
	var pe *errs.Error
	if !errors.As(err, &pe) || pe.Code != errs.Unauthorized {
		t.Fatalf("err: %v", err)
	}
}

func TestReverseAdjustmentSuccess(t *testing.T) {
	e := newTestEnv()
	admin := uuid.New()
	ctx := adminCtx(admin)

	orig, err := e.svc.CreateAdjustment(ctx, CreateAdjustmentInput{
		Amount: dec("12.34"),
		Reason: "补录线下收款",
	})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := e.svc.ReverseAdjustment(ctx, orig.ID, "撤销补录线下收款")
	if err != nil {
		t.Fatal(err)
	}
	if !rev.Amount.Equal(dec("-12.34")) {
		t.Fatalf("amount: %s", rev.Amount)
	}
	if rev.EffectiveOn != orig.EffectiveOn {
		t.Fatalf("effective: %s vs %s", rev.EffectiveOn, orig.EffectiveOn)
	}
	if rev.ReversalOf == nil || *rev.ReversalOf != orig.ID {
		t.Fatalf("reversal_of: %+v", rev.ReversalOf)
	}
	if len(e.store.locked) != 1 || e.store.locked[0] != orig.ID {
		t.Fatalf("locked: %v", e.store.locked)
	}
	if len(e.audit.events) != 2 || e.audit.events[1].Action != ActionAdjustmentReverse {
		t.Fatalf("audit: %+v", e.audit.events)
	}
}

func TestReverseAdjustmentErrors(t *testing.T) {
	e := newTestEnv()
	admin := uuid.New()
	ctx := adminCtx(admin)

	orig, err := e.svc.CreateAdjustment(ctx, CreateAdjustmentInput{
		Amount: dec("12.34"),
		Reason: "补录线下收款",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.ReverseAdjustment(ctx, orig.ID, "撤销补录线下收款")
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.svc.ReverseAdjustment(ctx, orig.ID, "再次撤销补录收款")
	var pe *errs.Error
	if !errors.As(err, &pe) || pe.Code != errs.Conflict {
		t.Fatalf("double reverse: %v", err)
	}

	revID := e.store.adjustments[len(e.store.adjustments)-1].ID
	_, err = e.svc.ReverseAdjustment(ctx, revID, "冲冲正行不允许")
	if !errors.As(err, &pe) || pe.Code != errs.Conflict {
		t.Fatalf("reverse reversal row: %v", err)
	}

	_, err = e.svc.ReverseAdjustment(ctx, uuid.New(), "撤销不存在的记录")
	if !errors.As(err, &pe) || pe.Code != errs.NotFound {
		t.Fatalf("not found: %v", err)
	}

	_, err = e.svc.ReverseAdjustment(ctx, orig.ID, "短")
	if !errors.As(err, &pe) || pe.Code != errs.ValidationFailed {
		t.Fatalf("short reason: %v", err)
	}
}

func TestListAdjustments(t *testing.T) {
	e := newTestEnv()
	admin := uuid.New()
	ctx := adminCtx(admin)

	adj1, err := e.svc.CreateAdjustment(ctx, CreateAdjustmentInput{
		Amount: dec("1"), Reason: "补录线下收款说明",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreateAdjustment(ctx, CreateAdjustmentInput{
		Amount: dec("2"), Reason: "补录线下收款说明",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.ReverseAdjustment(ctx, adj1.ID, "撤销补录线下收款")
	if err != nil {
		t.Fatal(err)
	}

	all, err := e.svc.ListAdjustments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all: %d", len(all))
	}
	if all[0].CreatedAt.Before(all[1].CreatedAt) {
		t.Fatal("expected newest first")
	}
	var origReversed bool
	for _, a := range all {
		if a.ID == adj1.ID && a.Reversed {
			origReversed = true
		}
	}
	if !origReversed {
		t.Fatal("original should be reversed")
	}
}
