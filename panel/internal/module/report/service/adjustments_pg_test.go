package service

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestAdjustmentCreateReversePG(t *testing.T) {
	e, ctx := newPGEnv(t)
	actx, admin := e.asAdmin(t, ctx)

	orig, err := e.svc.CreateAdjustment(actx, CreateAdjustmentInput{
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

	list, err := e.svc.ListAdjustments(actx)
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

// 直接 UPDATE 或 DELETE 一行调整被触发器拒绝。
func TestAdjustmentMutationDeniedPG(t *testing.T) {
	e, ctx := newPGEnv(t)
	actx, _ := e.asAdmin(t, ctx)

	orig, err := e.svc.CreateAdjustment(actx, CreateAdjustmentInput{
		Amount:      dec("50.00"),
		Reason:      "只追加防篡改用例",
		EffectiveOn: "2026-03-01",
	})
	if err != nil {
		t.Fatal(err)
	}

	// UPDATE 必被触发器 deny_mutation 拦截
	err = e.exec(actx, `UPDATE revenue_report_adjustments SET reason = '尝试修改理由' WHERE id = ?`, orig.ID)
	if err == nil {
		t.Fatal("UPDATE 期望被触发器拒绝，但成功了")
	}

	// DELETE 必被触发器 deny_mutation 拦截
	err = e.exec(actx, `DELETE FROM revenue_report_adjustments WHERE id = ?`, orig.ID)
	if err == nil {
		t.Fatal("DELETE 期望被触发器拒绝，但成功了")
	}
}

// 两个 goroutine 并发冲正同一行，只有一个成功、另一个是冲突错误。
func TestAdjustmentConcurrentReversePG(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过并发 PG 测试")
	}
	e, ctx := newCommitPGEnv(t)
	actx, _ := e.asAdmin(t, ctx)

	orig, err := e.svc.CreateAdjustment(actx, CreateAdjustmentInput{
		Amount:      dec("200.00"),
		Reason:      "并发冲正原行",
		EffectiveOn: "2026-03-01",
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var okCount, conflictCount int
	var otherErrors []error

	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, revErr := e.svc.ReverseAdjustment(actx, orig.ID, fmt.Sprintf("并发冲正第%d号", idx))
			mu.Lock()
			defer mu.Unlock()
			if revErr == nil {
				okCount++
			} else {
				var pe *errs.Error
				if errors.As(revErr, &pe) && pe.Code == errs.Conflict {
					conflictCount++
				} else {
					otherErrors = append(otherErrors, revErr)
				}
			}
		}(i)
	}

	close(start)
	wg.Wait()

	for _, oErr := range otherErrors {
		t.Errorf("意外错误: %v", oErr)
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("并发冲正期望 1 成功 1 冲突，得到: ok=%d, conflict=%d", okCount, conflictCount)
	}
}
