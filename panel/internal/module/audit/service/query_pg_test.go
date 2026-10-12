package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

func TestAuditAppendOnlyTrigger(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	a := uniqueAction(t)
	if err := svc.Write(ctx, Event{Action: a}); err != nil {
		t.Fatal(err)
	}

	err := d.Conn(ctx).Exec("UPDATE audit_events SET action = 'x' WHERE action = ?", a).Error
	if err == nil {
		t.Fatal("UPDATE should fail")
	}
	if !strings.Contains(err.Error(), "只追加") {
		t.Fatalf("UPDATE err %q", err.Error())
	}

	ctx2 := testkit.TxCtx(t, d)
	a2 := uniqueAction(t)
	if err := svc.Write(ctx2, Event{Action: a2}); err != nil {
		t.Fatal(err)
	}
	err = d.Conn(ctx2).Exec("DELETE FROM audit_events WHERE action = ?", a2).Error
	if err == nil {
		t.Fatal("DELETE should fail")
	}
	if !strings.Contains(err.Error(), "只追加") {
		t.Fatalf("DELETE err %q", err.Error())
	}
}

func TestPurgeRetention(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	a := uniqueAction(t)

	for _, interval := range []string{"500 days", "401 days", "10 days"} {
		err := d.Conn(ctx).Exec(
			`INSERT INTO audit_events (actor_kind, action, occurred_at) VALUES ('system', ?, now() - interval '`+interval+`')`,
			a,
		).Error
		if err != nil {
			t.Fatal(err)
		}
	}

	n, err := svc.Purge(ctx, time.Now(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("purged %d want >= 2", n)
	}
	if scanCount(t, d.Conn(ctx).Raw(countSQL, a).Row()) != 1 {
		t.Fatalf("remaining rows for %s", a)
	}

	ctxBad := testkit.TxCtx(t, d)
	_, err = svc.Purge(ctxBad, time.Now(), 0)
	if err == nil {
		t.Fatal("batch 0 should error")
	}

	ctxClamp := testkit.TxCtx(t, d)
	aYoung := uniqueAction(t)
	if err := d.Conn(ctxClamp).Exec(
		`INSERT INTO audit_events (actor_kind, action, occurred_at) VALUES ('system', ?, now() - interval '300 days')`,
		aYoung,
	).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Purge(ctxClamp, time.Now(), 1000); err != nil {
		t.Fatal(err)
	}
	if scanCount(t, d.Conn(ctxClamp).Raw(countSQL, aYoung).Row()) != 1 {
		t.Fatal("300-day row should survive 400-day clamp")
	}
}

func TestListFilterAndPagination(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	prefix := uniqueAction(t)
	resID := uuid.New()

	actions := []string{prefix + ".one", prefix + ".one", prefix + ".one", prefix + ".two", prefix + ".two"}
	var writeOrder []string
	for i, act := range actions {
		e := Event{Action: act}
		if i == 2 {
			e.Outcome = OutcomeFailure
		}
		if i == 4 {
			e.ResourceType = "user"
			e.ResourceID = &resID
		}
		if err := svc.Write(ctx, e); err != nil {
			t.Fatal(err)
		}
		writeOrder = append(writeOrder, act)
	}

	_, total, err := svc.List(ctx, Filter{ActionPrefix: prefix}, Page{Limit: 50})
	if err != nil || total != 5 {
		t.Fatalf("prefix total %d err %v", total, err)
	}
	_, total, err = svc.List(ctx, Filter{ActionPrefix: prefix + ".two"}, Page{Limit: 50})
	if err != nil || total != 2 {
		t.Fatalf(".two total %d", total)
	}
	_, total, err = svc.List(ctx, Filter{ActionPrefix: prefix, Outcome: "failure"}, Page{Limit: 50})
	if err != nil || total != 1 {
		t.Fatalf("failure total %d", total)
	}

	rows, total, err := svc.List(ctx, Filter{ActionPrefix: prefix}, Page{Limit: 2, Offset: 0})
	if err != nil || total != 5 || len(rows) != 2 {
		t.Fatalf("page0 len=%d total=%d", len(rows), total)
	}
	rowsLast, total, err := svc.List(ctx, Filter{ActionPrefix: prefix}, Page{Limit: 2, Offset: 4})
	if err != nil || total != 5 || len(rowsLast) != 1 {
		t.Fatalf("page offset4 len=%d", len(rowsLast))
	}
	// 最后写入的应在最前（occurred_at 相同则 id 降序）
	lastWritten := writeOrder[len(writeOrder)-1]
	if rows[0].Action != lastWritten {
		t.Fatalf("first row action %q want %q", rows[0].Action, lastWritten)
	}

	byRes, _, err := svc.List(ctx, Filter{Query: resID.String()}, Page{Limit: 10})
	if err != nil || len(byRes) != 1 || byRes[0].ResourceID == nil || *byRes[0].ResourceID != resID {
		t.Fatalf("query by resource id")
	}
	part := prefix[1:5]
	byPart, _, err := svc.List(ctx, Filter{Query: part}, Page{Limit: 50})
	if err != nil || len(byPart) < 1 {
		t.Fatalf("query by action substring")
	}

	wildPrefix := prefix[:2] + "%"
	_, total, err = svc.List(ctx, Filter{ActionPrefix: wildPrefix}, Page{Limit: 50})
	if err != nil || total != 0 {
		t.Fatalf("escaped %% prefix total %d", total)
	}
}

// fakeActorDir 用于 ActorDirectory 测试。
type fakeActorDir struct {
	mu     sync.Mutex
	emails map[uuid.UUID]string
	calls  int
}

func (f *fakeActorDir) Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	out := make(map[uuid.UUID]string, len(ids))
	for _, id := range ids {
		if em, ok := f.emails[id]; ok {
			out[id] = em
		}
	}
	return out, nil
}

func (f *fakeActorDir) IDsByEmail(ctx context.Context, q string, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for id, em := range f.emails {
		if strings.Contains(em, q) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *fakeActorDir) emailCalls() int {
	f.mu.Lock()
	n := f.calls
	f.mu.Unlock()
	return n
}

func TestActorDirectory(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	alice := uuid.New()
	bob := uuid.New()
	fake := &fakeActorDir{emails: map[uuid.UUID]string{
		alice: "alice@example.com",
		bob:   "bob@example.com",
	}}
	svc.UseActorDirectory(fake)

	prefix := uniqueAction(t)
	if err := svc.Write(ctx, Event{Action: prefix + ".a", ActorID: &alice}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Write(ctx, Event{Action: prefix + ".b", ActorID: &bob}); err != nil {
		t.Fatal(err)
	}

	rows, _, err := svc.List(ctx, Filter{ActionPrefix: prefix}, Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if fake.emailCalls() != 1 {
		t.Fatalf("Emails calls %d want 1", fake.emailCalls())
	}
	var sawAlice bool
	for _, r := range rows {
		if r.ActorEmail != nil && *r.ActorEmail == "alice@example.com" {
			sawAlice = true
		}
	}
	if !sawAlice {
		t.Fatal("missing alice email")
	}

	byMail, _, err := svc.List(ctx, Filter{ActionPrefix: prefix, Query: "alice@"}, Page{Limit: 10})
	if err != nil || len(byMail) != 1 {
		t.Fatalf("query alice@ len=%d", len(byMail))
	}
}

func TestExportCSV(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	prefix := uniqueAction(t)

	rowsData := []map[string]any{
		{"reason": "=SUM(A1)"},
		{"reason": "含,逗号\"引号\n换行"},
		{},
	}
	for i, data := range rowsData {
		if err := svc.Write(ctx, Event{Action: prefix + "." + string(rune('a'+i)), Data: data}); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	opened := 0
	if err := svc.ExportCSV(ctx, Filter{ActionPrefix: prefix}, func() io.Writer {
		opened++
		return &buf
	}); err != nil {
		t.Fatal(err)
	}
	if opened != 1 {
		t.Fatalf("opened %d", opened)
	}
	raw := buf.Bytes()
	if len(raw) < 3 || raw[0] != 0xEF || raw[1] != 0xBB || raw[2] != 0xBF {
		t.Fatal("missing BOM")
	}
	records, err := csv.NewReader(strings.NewReader(string(raw[3:]))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("csv rows %d want 4 (header+3)", len(records))
	}
	for i := range csvHeader {
		if records[0][i] != csvHeader[i] {
			t.Fatalf("header[%d]", i)
		}
	}
	// reason 列索引 9
	var sawFormula, sawComma bool
	for _, rec := range records[1:] {
		if len(rec) < 10 {
			t.Fatal("short row")
		}
		if rec[9] == "'=SUM(A1)" {
			sawFormula = true
		}
		if rec[9] == "含,逗号\"引号\n换行" {
			sawComma = true
		}
	}
	if !sawFormula || !sawComma {
		t.Fatalf("formula=%v comma=%v", sawFormula, sawComma)
	}

	exportRows, _, err := svc.List(ctx, Filter{ActionPrefix: "audit.export"}, Page{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(exportRows) == 0 {
		t.Fatal("no audit.export row")
	}
	latest := exportRows[0]
	if latest.Action != "audit.export" {
		t.Fatalf("action %q", latest.Action)
	}
	if latest.Reason != nil && *latest.Reason != "" {
		t.Fatalf("export trace reason %v", latest.Reason)
	}
}
