package service

import (
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestPGSubmitGenerationValidation(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	admin := uuid.New()
	actx := asAdmin(ctx, admin)

	_, err := e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 0, EmailPrefix: "gen", EmailDomain: "example.test", Reason: "压测账号批量准备",
	})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["count"] == "" {
		t.Fatalf("count: %v", err)
	}

	_, err = e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 1, EmailPrefix: "Bad_Prefix", EmailDomain: "example.test", Reason: "压测账号批量准备",
	})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["email_prefix"] == "" {
		t.Fatalf("prefix: %v", err)
	}

	_, err = e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 1, EmailPrefix: "gen", EmailDomain: "localhost", Reason: "压测账号批量准备",
	})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["email_domain"] == "" {
		t.Fatalf("domain: %v", err)
	}

	_, err = e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 1, EmailPrefix: "gen", EmailDomain: "example.test", Reason: "短",
	})
	if codeOf(err) != errs.ValidationFailed || fieldsOf(err)["reason"] == "" {
		t.Fatalf("reason: %v", err)
	}

	_, err = e.svc.SubmitGeneration(ctx, GenerateInput{
		Count: 3, EmailPrefix: "gen", EmailDomain: "example.test", Reason: "压测账号批量准备",
	})
	if codeOf(err) != errs.Unauthorized {
		t.Fatalf("no principal: %v", err)
	}
}

func TestPGGenerationJobFlow(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	admin := uuid.New()
	actx := asAdmin(ctx, admin)

	job, err := e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 3, EmailPrefix: "gen", EmailDomain: "example.test", Reason: "压测账号批量准备",
	})
	if err != nil || job.Status != JobQueued || job.Total != 3 || job.ResultAvailable {
		t.Fatalf("submit %+v err=%v", job, err)
	}
	if e.auditCount(ctx, "user.bulk_generate_requested", job.ID) != 1 {
		t.Fatal("requested audit")
	}

	if err := e.svc.RunGenerationJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetGenerationJob(ctx, job.ID)
	if err != nil || got.Status != JobSucceeded || got.Completed != 3 || !got.ResultAvailable || got.FinishedAt == nil {
		t.Fatalf("after run %+v err=%v", got, err)
	}
	if got.ResultExpiresAt == nil {
		t.Fatal("expires nil")
	}
	expWant := time.Now().Add(generationResultTTL)
	if got.ResultExpiresAt.Before(expWant.Add(-time.Minute)) || got.ResultExpiresAt.After(expWant.Add(time.Minute)) {
		t.Fatalf("expires %v want ~%v", got.ResultExpiresAt, expWant)
	}
	if e.auditCount(ctx, "user.bulk_generated", job.ID) != 1 {
		t.Fatal("generated audit count")
	}
	var actor uuid.UUID
	if err := e.row(ctx, `SELECT actor_id FROM audit_events WHERE action = 'user.bulk_generated' AND resource_id = ?`, job.ID).
		Scan(&actor); err != nil || actor != admin {
		t.Fatalf("generated actor %v want %v err=%v", actor, admin, err)
	}

	users, err := e.svc.GenerationResult(actx, job.ID)
	if err != nil || len(users) != 3 {
		t.Fatalf("result len=%d err=%v", len(users), err)
	}
	seen := map[string]bool{}
	for _, u := range users {
		if !strings.HasPrefix(u.Email, "gen-") || !strings.HasSuffix(u.Email, "@example.test") {
			t.Fatalf("email %q", u.Email)
		}
		if len(u.Password) != 16 {
			t.Fatalf("password len %d", len(u.Password))
		}
		if seen[u.Email] {
			t.Fatalf("dup %s", u.Email)
		}
		seen[u.Email] = true
		var n int64
		if err := e.row(ctx, `SELECT count(*) FROM users WHERE email = ? AND status = ?`, u.Email, StatusActive).Scan(&n); err != nil || n != 1 {
			t.Fatalf("user %s n=%d err=%v", u.Email, n, err)
		}
		if err := e.row(ctx, `SELECT count(*) FROM user_passwords WHERE user_id = (SELECT id FROM users WHERE email = ?)`, u.Email).
			Scan(&n); err != nil || n != 1 {
			t.Fatalf("password row %s err=%v", u.Email, err)
		}
	}
	if e.auditCount(ctx, "user.bulk_generate_exported", job.ID) != 1 {
		t.Fatal("exported audit")
	}
	var hasPwd bool
	if err := e.row(ctx, `SELECT (data->'password') IS NOT NULL FROM audit_events WHERE action = 'user.bulk_generate_exported' AND resource_id = ?`, job.ID).
		Scan(&hasPwd); err != nil || hasPwd { // jsonb 的 ? 运算符会被 GORM 当占位符，改用 ->
		t.Fatalf("export audit password key hasPwd=%v err=%v", hasPwd, err)
	}

	_, err = e.svc.GenerationResult(asAdmin(ctx, uuid.New()), job.ID)
	if codeOf(err) != errs.NotFound {
		t.Fatalf("other admin: %v", err)
	}

	var before int64
	if err := e.row(ctx, `SELECT count(*) FROM users WHERE email LIKE 'gen-%@example.test'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RunGenerationJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := e.row(ctx, `SELECT count(*) FROM users WHERE email LIKE 'gen-%@example.test'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("rerun changed users %d -> %d", before, after)
	}
}

func TestPGGenerationJobListAndGet(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	admin := uuid.New()
	actx := asAdmin(ctx, admin)

	job, err := e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 1, EmailPrefix: "gen", EmailDomain: "example.test", Reason: "压测账号批量准备",
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.ListGenerationJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range list {
		if j.ID == job.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list missing job %s", job.ID)
	}
	_, err = e.svc.GetGenerationJob(ctx, uuid.New())
	if codeOf(err) != errs.NotFound {
		t.Fatalf("random id: %v", err)
	}
}

func TestPGGenerationResultExpiry(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	admin := uuid.New()
	actx := asAdmin(ctx, admin)

	job, err := e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 1, EmailPrefix: "gen", EmailDomain: "example.test", Reason: "压测账号批量准备",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RunGenerationJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE user_generation_jobs SET result_expires_at = now() - interval '1 minute' WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	requeued, purged, err := e.svc.ReconcileGeneration(ctx)
	if err != nil || purged < 1 {
		t.Fatalf("reconcile purged=%d requeued=%d err=%v", purged, requeued, err)
	}
	got, err := e.svc.GetGenerationJob(ctx, job.ID)
	if err != nil || got.ResultAvailable {
		t.Fatalf("after purge available=%v err=%v", got.ResultAvailable, err)
	}
	_, err = e.svc.GenerationResult(actx, job.ID)
	if codeOf(err) != errs.Conflict {
		t.Fatalf("result after purge: %v", err)
	}
}

func TestPGGenerationRetentionPurge(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	admin := uuid.New()
	actx := asAdmin(ctx, admin)

	job, err := e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 1, EmailPrefix: "gen", EmailDomain: "example.test", Reason: "压测账号批量准备",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RunGenerationJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE user_generation_jobs SET finished_at = now() - interval '31 days', result_expires_at = now() - interval '31 days', result_encrypted = NULL, result_purged_at = now() WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.PurgeGenerationJobs(ctx, time.Now().AddDate(0, 0, -30), 100)
	if err != nil || n != 1 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
	_, err = e.svc.GetGenerationJob(ctx, job.ID)
	if codeOf(err) != errs.NotFound {
		t.Fatalf("get after retention: %v", err)
	}
	_, err = e.svc.PurgeGenerationJobs(ctx, time.Now(), 0)
	if err == nil {
		t.Fatal("batch 0 should error")
	}
}

func TestPGGenerationReconcileRequeue(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	admin := uuid.New()
	actx := asAdmin(ctx, admin)
	q := &fakeJobQueue{}
	e.svc.UseJobQueue(q)

	job, err := e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 1, EmailPrefix: "gen", EmailDomain: "example.test", Reason: "压测账号批量准备",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE user_generation_jobs SET created_at = now() - interval '5 minutes' WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	requeued, _, err := e.svc.ReconcileGeneration(ctx)
	if err != nil || requeued < 1 {
		t.Fatalf("requeued=%d err=%v", requeued, err)
	}
	found := false
	for _, typ := range q.types {
		if typ == TaskGenerateUsers {
			found = true
		}
	}
	if !found {
		t.Fatalf("queue types %v", q.types)
	}
}
