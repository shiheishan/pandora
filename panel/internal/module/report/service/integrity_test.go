package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/module/report/model"
	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// ---------------------------------------------------------------------------
// 巡检用到的假实现（fakeStore 的 integrity_checks、fakePorts 的各属主检查与 notify 告警）
// ---------------------------------------------------------------------------

func (f *fakeStore) LockIntegrityCheck(_ context.Context, code string) (model.IntegrityCheck, bool, error) {
	if err := f.err("LockIntegrityCheck"); err != nil {
		return model.IntegrityCheck{}, false, err
	}
	c, ok := f.integrity[code]
	return c, ok, nil
}

func (f *fakeStore) SaveIntegrityCheck(_ context.Context, c model.IntegrityCheck) error {
	if err := f.err("SaveIntegrityCheck"); err != nil {
		return err
	}
	f.integrity[c.Code] = c
	return nil
}

func (f *fakeStore) IntegrityChecks(context.Context) ([]model.IntegrityCheck, error) {
	if err := f.err("IntegrityChecks"); err != nil {
		return nil, err
	}
	out := make([]model.IntegrityCheck, 0, len(f.integrity))
	for _, c := range f.integrity {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakePorts) check(name string) (integrity.Result, error) {
	if r, ok := f.checks[name]; ok {
		return r, f.err(name)
	}
	return integrity.Result{Sample: []string{}}, f.err(name)
}

func (f *fakePorts) CheckHolds(context.Context) (integrity.Result, error) {
	return f.check("CheckHolds")
}

func (f *fakePorts) CheckPaidUnfulfilled(context.Context) (integrity.Result, error) {
	return f.check("CheckPaidUnfulfilled")
}

func (f *fakePorts) CheckReservations(context.Context) (integrity.Result, error) {
	return f.check("CheckReservations")
}

func (f *fakePorts) CheckExpireOverdue(context.Context, time.Time) (integrity.Result, error) {
	return f.check("CheckExpireOverdue")
}

func (f *fakePorts) CheckCloseOverdue(context.Context, time.Time) (integrity.Result, error) {
	return f.check("CheckCloseOverdue")
}

func (f *fakePorts) CheckRedemptions(context.Context) (integrity.Result, error) {
	return f.check("CheckRedemptions")
}

func (f *fakePorts) SettledPaymentsSince(context.Context, time.Time) ([]integrity.OrderRef, error) {
	return f.refs["SettledPaymentsSince"], f.err("SettledPaymentsSince")
}

func (f *fakePorts) EntriesSince(context.Context, time.Time) ([]integrity.OrderRef, error) {
	return f.refs["EntriesSince"], f.err("EntriesSince")
}

func (f *fakePorts) UnsettledAmong(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	f.unsettledCalls = append(f.unsettledCalls, slices.Clone(ids))
	var out []uuid.UUID
	for _, id := range ids {
		if f.unsettled[id] {
			out = append(out, id)
		}
	}
	return out, f.err("UnsettledAmong")
}

func (f *fakePorts) NotifyAdminsIntegrity(_ context.Context, in notifysvc.IntegrityAlert) (bool, error) {
	if err := f.err("NotifyAdminsIntegrity"); err != nil {
		return false, err
	}
	if f.noGroup {
		return false, nil
	}
	f.alerts = append(f.alerts, in)
	return true, nil
}

// runIntegrity 把时钟拨到 at 跑一轮巡检。
func runIntegrity(t *testing.T, e *testEnv, at time.Time) {
	t.Helper()
	e.svc.now = func() time.Time { return at }
	if err := e.svc.RunIntegrity(context.Background()); err != nil {
		t.Fatalf("RunIntegrity: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 告警规则
// ---------------------------------------------------------------------------

func TestRunIntegrityWritesEveryCheck(t *testing.T) {
	e := newTestEnv()
	runIntegrity(t, e, testNow)
	if len(e.store.integrity) != len(e.svc.integrityChecks()) {
		t.Fatalf("应每项一行，实际 %d 行", len(e.store.integrity))
	}
	for _, c := range e.svc.integrityChecks() {
		row := e.store.integrity[c.Code]
		if row.Count != 0 || row.Sample != "[]" || !row.CheckedAt.Equal(testNow) || row.FailingSince != nil || row.AlertedCount != 0 {
			t.Fatalf("%s 正常数据的行不对 %+v", c.Code, row)
		}
	}
	if len(e.ports.alerts) != 0 {
		t.Fatalf("全为 0 不该告警 %+v", e.ports.alerts)
	}
}

func TestRunIntegrityAlertRules(t *testing.T) {
	e := newTestEnv()
	t0 := testNow
	set := func(n int64, sample ...string) {
		e.ports.checks["CheckDrift"] = integrity.Result{Count: n, Sample: sample}
	}

	// 0 → 非 0：发一条，记下起点与告警时的数
	set(2, "a2", "a1")
	runIntegrity(t, e, t0)
	if len(e.ports.alerts) != 1 {
		t.Fatalf("0→非 0 应告警一次，实际 %d", len(e.ports.alerts))
	}
	a := e.ports.alerts[0]
	if a.Code != CheckLedgerDrift || a.Name != "账本漂移" || a.Count != 2 || !a.Since.Equal(t0) {
		t.Fatalf("告警内容 %+v", a)
	}
	row := e.store.integrity[CheckLedgerDrift]
	if row.Count != 2 || row.Sample != `["a2","a1"]` || row.FailingSince == nil || !row.FailingSince.Equal(t0) || row.AlertedCount != 2 {
		t.Fatalf("行 %+v", row)
	}

	// 数不变：不重发，起点沿用
	t1 := t0.Add(30 * time.Minute)
	runIntegrity(t, e, t1)
	if len(e.ports.alerts) != 1 {
		t.Fatalf("数不变不该重发，实际 %d", len(e.ports.alerts))
	}
	row = e.store.integrity[CheckLedgerDrift]
	if !row.CheckedAt.Equal(t1) || !row.FailingSince.Equal(t0) || row.AlertedCount != 2 {
		t.Fatalf("数不变后的行 %+v", row)
	}

	// 变少：不发，告警时的数不动
	set(1, "a1")
	runIntegrity(t, e, t1.Add(30*time.Minute))
	if len(e.ports.alerts) != 1 || e.store.integrity[CheckLedgerDrift].AlertedCount != 2 {
		t.Fatalf("变少不该发 %d %+v", len(e.ports.alerts), e.store.integrity[CheckLedgerDrift])
	}

	// 变多（超过上次告警时的 2）：再发，起点仍是 t0
	set(3, "a3", "a2", "a1")
	runIntegrity(t, e, t1.Add(time.Hour))
	if len(e.ports.alerts) != 2 {
		t.Fatalf("变多应再发，实际 %d", len(e.ports.alerts))
	}
	if a := e.ports.alerts[1]; a.Count != 3 || !a.Since.Equal(t0) {
		t.Fatalf("变多的告警 %+v", a)
	}
	if e.store.integrity[CheckLedgerDrift].AlertedCount != 3 {
		t.Fatalf("告警时的数应记 3 %+v", e.store.integrity[CheckLedgerDrift])
	}

	// 回到 0：不发，清掉起点与告警时的数
	set(0)
	runIntegrity(t, e, t1.Add(90*time.Minute))
	if len(e.ports.alerts) != 2 {
		t.Fatalf("回到 0 不该发，实际 %d", len(e.ports.alerts))
	}
	row = e.store.integrity[CheckLedgerDrift]
	if row.Count != 0 || row.Sample != "[]" || row.FailingSince != nil || row.AlertedCount != 0 {
		t.Fatalf("回到 0 后的行 %+v", row)
	}

	// 再出现是新的一段：起点换成这一次
	t2 := t1.Add(2 * time.Hour)
	set(2, "b2", "b1")
	runIntegrity(t, e, t2)
	if len(e.ports.alerts) != 3 || !e.ports.alerts[2].Since.Equal(t2) || e.ports.alerts[2].Count != 2 {
		t.Fatalf("新一段应按新起点告警 %+v", e.ports.alerts)
	}
}

func TestRunIntegrityNoGroup(t *testing.T) {
	e := newTestEnv()
	e.ports.noGroup = true
	e.ports.checks["CheckPaidUnfulfilled"] = integrity.Result{Count: 1, Sample: []string{"o1"}}
	runIntegrity(t, e, testNow)
	row := e.store.integrity[CheckOrderPaidUnfulfilled]
	if len(e.ports.alerts) != 0 || row.Count != 1 || row.AlertedCount != 0 || row.FailingSince == nil {
		t.Fatalf("没配群组：不发、不记告警时的数，结果照写 alerts=%d row=%+v", len(e.ports.alerts), row)
	}

	// 群组配好之后，同一段违例下一轮照发
	e.ports.noGroup = false
	runIntegrity(t, e, testNow.Add(30*time.Minute))
	if len(e.ports.alerts) != 1 || e.ports.alerts[0].Code != CheckOrderPaidUnfulfilled || !e.ports.alerts[0].Since.Equal(testNow) {
		t.Fatalf("配好群组后应补发 %+v", e.ports.alerts)
	}
	if e.store.integrity[CheckOrderPaidUnfulfilled].AlertedCount != 1 {
		t.Fatalf("补发后应记告警时的数 %+v", e.store.integrity[CheckOrderPaidUnfulfilled])
	}
}

func TestRunIntegrityCheckErrorKeepsOthers(t *testing.T) {
	e := newTestEnv()
	e.store.integrity[CheckLedgerHolds] = model.IntegrityCheck{Code: CheckLedgerHolds, Count: 4, Sample: `["u"]`,
		CheckedAt: testNow.Add(-time.Hour), FailingSince: ptrTime(testNow.Add(-2 * time.Hour)), AlertedCount: 4}
	e.ports.errFor["CheckHolds"] = errBoom
	e.svc.now = func() time.Time { return testNow }
	err := e.svc.RunIntegrity(context.Background())
	if !errors.Is(err, errBoom) {
		t.Fatalf("应带回失败的检查 err=%v", err)
	}
	if row := e.store.integrity[CheckLedgerHolds]; row.Count != 4 || !row.CheckedAt.Equal(testNow.Add(-time.Hour)) {
		t.Fatalf("失败的检查应保留上一次的结果 %+v", row)
	}
	if _, ok := e.store.integrity[CheckGiftcardRedemptions]; !ok {
		t.Fatal("别的检查应照跑")
	}
}

func TestRunIntegrityNotifyErrorKeepsRow(t *testing.T) {
	e := newTestEnv()
	e.ports.checks["CheckRedemptions"] = integrity.Result{Count: 1, Sample: []string{"c1"}}
	e.ports.errFor["NotifyAdminsIntegrity"] = errBoom
	e.svc.now = func() time.Time { return testNow }
	if err := e.svc.RunIntegrity(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("告警失败应报错 err=%v", err)
	}
	if _, ok := e.store.integrity[CheckGiftcardRedemptions]; ok {
		t.Fatal("告警失败时这一项不写（事务回滚，下一轮重来）")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// ---------------------------------------------------------------------------
// 跨模块的两项：属主给「记录 → 订单」，order 核对状态
// ---------------------------------------------------------------------------

func TestUnsettledRefs(t *testing.T) {
	e := newTestEnv()
	mk := func() uuid.UUID { return uuid.Must(uuid.NewV7()) }
	paidOrder, unpaidOrder := mk(), mk()
	p1, p2, p3 := mk(), mk(), mk() // 按时间先后
	e.ports.refs["SettledPaymentsSince"] = []integrity.OrderRef{
		{ID: p1, OrderID: unpaidOrder},
		{ID: p2, OrderID: paidOrder},
		{ID: p3, OrderID: unpaidOrder},
	}
	e.ports.unsettled[unpaidOrder] = true
	e1, e2 := mk(), mk()
	e.ports.refs["EntriesSince"] = []integrity.OrderRef{{ID: e1, OrderID: paidOrder}, {ID: e2, OrderID: paidOrder}}

	runIntegrity(t, e, testNow)

	pay := e.store.integrity[CheckPaymentUnsettled]
	if pay.Count != 2 || pay.Sample != `["`+p3.String()+`","`+p1.String()+`"]` {
		t.Fatalf("收款检查 %+v", pay)
	}
	if com := e.store.integrity[CheckCommissionUnpaidOrder]; com.Count != 0 || com.Sample != "[]" {
		t.Fatalf("佣金检查 %+v", com)
	}
	// 订单 id 去重后再交给 order
	if len(e.ports.unsettledCalls) != 2 || len(e.ports.unsettledCalls[0]) != 2 || len(e.ports.unsettledCalls[1]) != 1 {
		t.Fatalf("UnsettledAmong 调用 %v", e.ports.unsettledCalls)
	}
}

func TestUnsettledRefsChunks(t *testing.T) {
	e := newTestEnv()
	refs := make([]integrity.OrderRef, unsettledChunk+1)
	for i := range refs {
		refs[i] = integrity.OrderRef{ID: uuid.New(), OrderID: uuid.New()}
	}
	res, err := e.svc.unsettledRefs(context.Background(), refs)
	if err != nil || res.Count != 0 || res.Sample == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(e.ports.unsettledCalls) != 2 || len(e.ports.unsettledCalls[0]) != unsettledChunk || len(e.ports.unsettledCalls[1]) != 1 {
		t.Fatalf("应分两批核对 %d", len(e.ports.unsettledCalls))
	}
}

func TestUnsettledRefsEmpty(t *testing.T) {
	e := newTestEnv()
	res, err := e.svc.unsettledRefs(context.Background(), nil)
	if err != nil || res.Count != 0 || res.Sample == nil || len(e.ports.unsettledCalls) != 0 {
		t.Fatalf("没有记录时不调 order res=%+v err=%v calls=%d", res, err, len(e.ports.unsettledCalls))
	}
}
