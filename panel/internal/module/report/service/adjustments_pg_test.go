package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestAdjustmentCreateReversePG(t *testing.T) {
	e, ctx := newPGEnv(t)
	actx, admin := e.asAdmin(t, ctx)

	orig, err := e.svc.CreateAdjustment(actx, CreateAdjustmentInput{
		Currency:    "CNY",
		Amount:      dec("100.50"),
		Reason:      "PG 用例登记调整",
		EffectiveOn: "2026-03-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if orig.ID == uuid.Nil {
		t.Fatal("id zero")
	}
	if orig.CreatedBy != admin {
		t.Fatalf("created_by")
	}
	if orig.EffectiveOn != "2026-03-01" {
		t.Fatalf("effective_on: %q", orig.EffectiveOn)
	}
	if orig.CreatedByEmail == nil {
		t.Fatal("email nil")
	}

	rev, err := e.svc.ReverseAdjustment(actx, orig.ID, "PG 用例撤销调整")
	if err != nil {
		t.Fatal(err)
	}
	if !rev.Amount.Equal(dec("-100.50")) {
		t.Fatalf("rev amount: %s", rev.Amount)
	}
	if rev.ReversalOf == nil || *rev.ReversalOf != orig.ID {
		t.Fatalf("reversal_of")
	}
	if rev.EffectiveOn != "2026-03-01" {
		t.Fatalf("rev effective: %q", rev.EffectiveOn)
	}

	_, err = e.svc.ReverseAdjustment(actx, orig.ID, "PG 用例再次撤销")
	var pe *errs.Error
	if !errors.As(err, &pe) || pe.Code != errs.Conflict {
		t.Fatalf("second reverse orig: %v", err)
	}
	_, err = e.svc.ReverseAdjustment(actx, rev.ID, "PG 用例冲冲正行")
	if !errors.As(err, &pe) || pe.Code != errs.Conflict {
		t.Fatalf("reverse reversal: %v", err)
	}

	list, err := e.svc.ListAdjustments(actx, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	var foundOrig, foundRev bool
	for _, a := range list {
		if a.ID == orig.ID {
			foundOrig = true
			if !a.Reversed {
				t.Fatal("orig not reversed")
			}
		}
		if a.ID == rev.ID {
			foundRev = true
			if a.Reversed {
				t.Fatal("rev should not be reversed")
			}
		}
	}
	if !foundOrig || !foundRev {
		t.Fatalf("list missing rows: orig=%v rev=%v", foundOrig, foundRev)
	}

	var n int
	if err := e.row(actx, `SELECT count(*) FROM audit_events WHERE resource_id = ? AND action = ?`, orig.ID, ActionAdjustmentCreate).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("audit count: %d", n)
	}
}
