package service

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func bulkTag(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "bulk" + hex.EncodeToString(b)
}

func TestPGBulkFilterStatusAndGroup(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	actx := adminCtx(t, e, ctx)
	tag := bulkTag(t)

	_ = e.mkUser(t, ctx, uniqEmail(t, tag+"-a"), "Passw0rdLong1")
	b := e.mkUser(t, ctx, uniqEmail(t, tag+"-b"), "Passw0rdLong1")
	c := e.mkUser(t, ctx, uniqEmail(t, tag+"-c"), "Passw0rdLong1")
	if err := e.exec(ctx, `UPDATE users SET status = ? WHERE id = ?`, StatusSuspended, b.ID); err != nil {
		t.Fatal(err)
	}
	gid, err := e.svc.CreateGroup(actx, "", "bulk-grp-"+tag, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AssignGroup(actx, c.ID, &gid); err != nil {
		t.Fatal(err)
	}

	q := BulkFilter{Query: tag}
	prev, err := e.svc.PreviewBulk(actx, q)
	if err != nil || prev.Total != 3 {
		t.Fatalf("all total=%d err=%v", prev.Total, err)
	}

	prev, err = e.svc.PreviewBulk(actx, BulkFilter{Query: tag, Status: StatusSuspended})
	if err != nil || prev.Total != 1 || len(prev.Samples) != 1 || prev.Samples[0].Email != b.Email {
		t.Fatalf("suspended %+v err=%v", prev, err)
	}

	prev, err = e.svc.PreviewBulk(actx, BulkFilter{Query: tag, GroupID: &gid})
	if err != nil || prev.Total != 1 || len(prev.Samples) != 1 || prev.Samples[0].Email != c.Email {
		t.Fatalf("group %+v err=%v", prev, err)
	}

	_, err = e.svc.PreviewBulk(actx, BulkFilter{Query: tag, Status: "pending"})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["status"] == "" {
		t.Fatalf("pending status: %v fields=%v", err, fieldsOf(err))
	}

	_, err = e.svc.PreviewBulk(actx, BulkFilter{Query: tag, ExpiresWithinDays: 400})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["expires_within_days"] == "" {
		t.Fatalf("expires: %v", err)
	}

	_, err = e.svc.PreviewBulk(actx, BulkFilter{Query: tag, SubState: "x"})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["sub_state"] == "" {
		t.Fatalf("sub_state: %v", err)
	}

	_, err = e.svc.PreviewBulk(actx, BulkFilter{
		Query: tag, Status: "pending", ExpiresWithinDays: 400, SubState: "x",
	})
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("multi: %v", err)
	}
	f := fieldsOf(err)
	if f["status"] == "" || f["expires_within_days"] == "" || f["sub_state"] == "" {
		t.Fatalf("want three fields, got %v", f)
	}
}

func TestPGBulkSubFilterAndSamples(t *testing.T) {
	e, ctx, fs := opsEnv(t)
	actx := adminCtx(t, e, ctx)
	tag := bulkTag(t)

	a := e.mkUser(t, ctx, uniqEmail(t, tag+"-a"), "Passw0rdLong1")
	b := e.mkUser(t, ctx, uniqEmail(t, tag+"-b"), "Passw0rdLong1")
	c := e.mkUser(t, ctx, uniqEmail(t, tag+"-c"), "Passw0rdLong1")

	fs.ids = []uuid.UUID{a.ID}
	fs.exclude = false
	prev, err := e.svc.PreviewBulk(actx, BulkFilter{Query: tag, SubState: "active"})
	if err != nil || prev.Total != 1 {
		t.Fatalf("active sub total=%d err=%v", prev.Total, err)
	}
	if fs.lastFilter.State != "active" {
		t.Fatalf("filter state %q", fs.lastFilter.State)
	}

	fs.ids = []uuid.UUID{a.ID, b.ID}
	fs.exclude = true
	prev, err = e.svc.PreviewBulk(actx, BulkFilter{Query: tag, SubState: "none"})
	if err != nil || prev.Total != 1 || len(prev.Samples) != 1 || prev.Samples[0].Email != c.Email {
		t.Fatalf("none %+v err=%v", prev, err)
	}

	callsBefore := fs.filterCalls
	prev, err = e.svc.PreviewBulk(actx, BulkFilter{Query: tag})
	if err != nil || prev.Total != 3 {
		t.Fatalf("no sub total=%d err=%v", prev.Total, err)
	}
	if fs.filterCalls != callsBefore {
		t.Fatalf("无订阅条件不应再调端口: filterCalls %d want %d", fs.filterCalls, callsBefore)
	}

	fs.ids = nil
	fs.exclude = false
	prev, err = e.svc.PreviewBulk(actx, BulkFilter{Query: tag, SubState: "active"})
	if err != nil || prev.Total != 0 {
		t.Fatalf("empty ids total=%d err=%v", prev.Total, err)
	}

	pe := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
	fs.current[a.ID] = CurrentSub{PlanName: "月付", PeriodEnd: pe, Live: true, LiveCount: 2}
	prev, err = e.svc.PreviewBulk(actx, BulkFilter{Query: tag})
	if err != nil {
		t.Fatal(err)
	}
	var gotA, gotB *BulkSample
	for _, s := range prev.Samples {
		switch s.Email {
		case a.Email:
			gotA = &s
		case b.Email:
			gotB = &s
		}
	}
	if gotA == nil || gotA.PlanName == nil || *gotA.PlanName != "月付" || gotA.PeriodEnd == nil {
		t.Fatalf("A sample %+v", gotA)
	}
	if gotB == nil || gotB.PlanName != nil || gotB.PeriodEnd != nil {
		t.Fatalf("B sample %+v", gotB)
	}
}

func TestPGListUsersSubState(t *testing.T) {
	e, ctx, fs := opsEnv(t)
	actx := adminCtx(t, e, ctx)
	tag := bulkTag(t)

	a := e.mkUser(t, ctx, uniqEmail(t, tag+"-a"), "Passw0rdLong1")
	_ = e.mkUser(t, ctx, uniqEmail(t, tag+"-b"), "Passw0rdLong1")

	fs.ids = []uuid.UUID{a.ID}
	fs.exclude = false
	rows, total, err := e.svc.ListUsers(actx, UserFilter{Query: tag, SubState: "active"}, 50, 0)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != a.ID {
		t.Fatalf("list active total=%d rows=%d err=%v", total, len(rows), err)
	}

	_, _, err = e.svc.ListUsers(actx, UserFilter{Query: tag, SubState: "x"}, 50, 0)
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("bad sub_state: %v", err)
	}
}

func TestPGExportUsers(t *testing.T) {
	e, ctx, fs := opsEnv(t)
	actx := adminCtx(t, e, ctx)
	tag := bulkTag(t)

	a := e.mkUser(t, ctx, uniqEmail(t, tag+"-a"), "Passw0rdLong1")
	_ = e.mkUser(t, ctx, uniqEmail(t, tag+"-b"), "Passw0rdLong1")
	c := e.mkUser(t, ctx, uniqEmail(t, tag+"-c"), "Passw0rdLong1")
	gid, err := e.svc.CreateGroup(actx, "", "ex-grp-"+tag, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AssignGroup(actx, c.ID, &gid); err != nil {
		t.Fatal(err)
	}
	fs.current[a.ID] = CurrentSub{LiveCount: 2, Live: true, PlanName: "p"}

	rows, err := e.svc.ExportUsers(actx, BulkFilter{Query: tag}, 2)
	if err != nil || len(rows) != 2 {
		t.Fatalf("export len=%d err=%v", len(rows), err)
	}
	if rows[0].CreatedAt.Before(rows[1].CreatedAt) {
		t.Fatalf("order: %v before %v", rows[0].CreatedAt, rows[1].CreatedAt)
	}

	var cnt int64
	var maxRows *int
	if err := e.row(ctx, `SELECT count(*), max((data->>'rows')::int) FROM audit_events WHERE action = 'user.bulk_exported'`).
		Scan(&cnt, &maxRows); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 || maxRows == nil || *maxRows != 2 {
		t.Fatalf("audit cnt=%d maxRows=%v", cnt, maxRows)
	}

	all, err := e.svc.ExportUsers(actx, BulkFilter{Query: tag}, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("limit0 len=%d err=%v", len(all), err)
	}
	// 同一事务里注册时间相同，按 id 倒序取前 2 行时 A（最早建）不在里面，所以在全量里核对 A 与 C
	var aRow *ExportRow
	for i := range all {
		if all[i].Email == a.Email {
			aRow = &all[i]
		}
		if all[i].Email == c.Email && all[i].GroupName != "ex-grp-"+tag {
			t.Fatalf("C group %q", all[i].GroupName)
		}
	}
	if aRow == nil || aRow.ActiveSubs != 2 {
		t.Fatalf("A subs %+v", aRow)
	}
}

func TestPGBulkRecipients(t *testing.T) {
	e, ctx, fs := opsEnv(t)
	actx := adminCtx(t, e, ctx)
	tag := bulkTag(t)

	a := e.mkUser(t, ctx, uniqEmail(t, tag+"-a"), "Passw0rdLong1")
	_ = e.mkUser(t, ctx, uniqEmail(t, tag+"-b"), "Passw0rdLong1")
	_ = e.mkUser(t, ctx, uniqEmail(t, tag+"-c"), "Passw0rdLong1")
	pe := time.Now().UTC().Add(7 * 24 * time.Hour)
	fs.current[a.ID] = CurrentSub{PlanName: "月付", PeriodEnd: pe, Live: true, LiveCount: 1}

	rec, total, err := e.svc.BulkRecipients(actx, BulkFilter{Query: tag}, 2)
	if err != nil || rec != nil || total != 3 {
		t.Fatalf("over max rec=%v total=%d err=%v", rec, total, err)
	}

	rec, total, err = e.svc.BulkRecipients(actx, BulkFilter{Query: tag}, 10)
	if err != nil || total != 3 || len(rec) != 3 {
		t.Fatalf("recipients len=%d total=%d err=%v", len(rec), total, err)
	}
	var aRec *BulkRecipient
	for i := range rec {
		if rec[i].UserID == a.ID {
			aRec = &rec[i]
		}
	}
	if aRec == nil || aRec.PlanName != "月付" || aRec.PeriodEnd == nil {
		t.Fatalf("A recipient %+v", aRec)
	}
}

func TestBulkFilterDigestOnlyNonEmpty(t *testing.T) {
	gid := uuid.New()
	plan := uuid.New()
	live := true
	d := BulkFilterDigest(BulkFilter{
		Status:            StatusActive,
		GroupID:           &gid,
		Query:             "q",
		SubState:          "none",
		HasActiveSub:      &live,
		PlanID:            &plan,
		ExpiresWithinDays: 7,
	})
	if len(d) != 7 {
		t.Fatalf("full digest keys=%d %v", len(d), d)
	}
	if d["status"] != StatusActive || d["query"] != "q" || d["sub_state"] != "none" {
		t.Fatalf("digest %v", d)
	}
	empty := BulkFilterDigest(BulkFilter{})
	if len(empty) != 0 {
		t.Fatalf("empty digest %v", empty)
	}
}
