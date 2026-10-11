package adminops

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// TestGenerateUsersPG18 钉住批量生成账号的后台任务（w5account，00130）：登记即回、审计与幂等
// 完成同事务；worker 分批生成，中途中断后从 completed 接着做、不重复建号；结果只给提交人下载、
// 每次下载写审计、到期清除；分组被删时任务判失败。顺带钉住用户导出写审计，以及撞了已有邮箱
// 的那个换后缀重试、口令与账号一一对应。
func TestGenerateUsersPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenant = "7b000000-0000-4000-8000-000000000001"
		group  = "7b000000-0000-4000-8000-000000000002"
		group2 = "7b000000-0000-4000-8000-000000000003"
		actor  = "7b000000-0000-4000-8000-000000000011"
		other  = "7b000000-0000-4000-8000-000000000012"
	)
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','gen-users-pg18','Gen Users','CNY')`,
		`INSERT INTO user_groups(id,tenant_id,code,name) VALUES
		   ('` + group + `','` + tenant + `','dealer','经销商'),
		   ('` + group2 + `','` + tenant + `','dealer2','经销商二')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		   ('` + actor + `','` + tenant + `','ops@gen.invalid','Ops','active'),
		   ('` + other + `','` + tenant + `','ops2@gen.invalid','Ops2','active'),
		   (gen_random_uuid(),'` + tenant + `','gen-aaaaaaaa@gen.invalid','Taken','active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	env, err := crypto.NewEnvelope([]byte("generate-users-pg18-master-key-32bytes!!"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(app)
	auditCount := func(action string) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`,
			tenant, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 用户导出写审计（审计台账 2.3 第 1 条）
	if rows, err := svc.ExportUsers(ctx, tenant, actor, BulkFilter{Query: "gen"}, 10); err != nil || len(rows) == 0 {
		t.Fatalf("export users rows=%d err=%v", len(rows), err)
	}
	if n := auditCount("user.bulk_exported"); n != 1 {
		t.Fatalf("export audit rows=%d", n)
	}

	if _, err := svc.SubmitGenerateUsers(ctx, tenant, GenerateUsersInput{Count: 501, EmailPrefix: "gen",
		EmailDomain: "gen.invalid", ActorID: actor, Reason: "给经销商预制账号"}); err == nil {
		t.Fatal("count above 500 must be rejected")
	}
	// 被拒绝的登记不叫醒 worker；成功登记之后通道里有一个信号
	select {
	case <-svc.UserGenerationWake():
		t.Fatal("a rejected submission woke the worker")
	default:
	}
	job, err := svc.SubmitGenerateUsers(ctx, tenant, GenerateUsersInput{Count: 23, EmailPrefix: "Gen",
		EmailDomain: "GEN.invalid", GroupID: group, ActorID: actor, Reason: "给经销商预制账号"})
	if err != nil || job.Status != "queued" || job.Total != 23 ||
		job.EmailPrefix != "gen" || job.EmailDomain != "gen.invalid" {
		t.Fatalf("submit job=%+v err=%v", job, err)
	}
	if n := auditCount("user.bulk_generate_requested"); n != 1 {
		t.Fatalf("request audit rows=%d", n)
	}
	select {
	case <-svc.UserGenerationWake():
	default:
		t.Fatal("a successful submission did not wake the worker")
	}

	// 第一批写完后「进程挂了」：租约过期，下一轮从 completed=10 接着做
	w := svc.NewUserGenerationWorker(env, nil)
	claimed, err := w.claim(ctx, tenant)
	if err != nil || claimed == nil || claimed.id != job.ID || claimed.attempts != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	var creds []generatedCredential
	for range userGenerationBatch {
		c, err := w.newCredential(ctx)
		if err != nil {
			t.Fatal(err)
		}
		creds = append(creds, c)
	}
	if _, err := w.writeBatch(ctx, tenant, claimed, nil, creds); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	// 一个比当前代更旧的持有者写不进去
	stale := *claimed
	stale.attempts = 0
	if _, err := w.writeBatch(ctx, tenant, &stale, nil, creds[:1]); !errors.Is(err, errUserGenerationLeaseLost) {
		t.Fatalf("stale lease write err=%v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE user_generation_jobs SET lease_until = now() - interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOnce(ctx, tenant); err != nil || !ran {
		t.Fatalf("resume run=%v err=%v", ran, err)
	}
	got, err := svc.GetUserGenerationJob(ctx, tenant, job.ID)
	if err != nil || got.Status != "succeeded" || got.Completed != 23 || got.Failed != 0 ||
		!got.ResultAvailable || got.ResultExpiresAt == nil || got.FinishedAt == nil {
		t.Fatalf("finished job=%+v err=%v", got, err)
	}
	var made, attempts int
	if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM users WHERE tenant_id=$1 AND user_group_id=$2),
		(SELECT attempts FROM user_generation_jobs WHERE id=$3)`, tenant, group, job.ID).Scan(&made, &attempts); err != nil {
		t.Fatal(err)
	}
	if made != 23 || attempts != 2 {
		t.Fatalf("generated accounts=%d attempts=%d, want 23 accounts after one resume", made, attempts)
	}
	if n := auditCount("user.bulk_generated"); n != 1 {
		t.Fatalf("generation audit rows=%d", n)
	}

	// 只有提交人能下载，每次下载写审计；结果里的口令能验过库里的哈希
	if _, _, err := svc.UserGenerationResult(ctx, tenant, job.ID, other); err == nil {
		t.Fatal("another admin must not download the result")
	}
	sealed, _, err := svc.UserGenerationResult(ctx, tenant, job.ID, actor)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	plain, err := env.Open(sealed, UserGenerationResultAAD(job.ID))
	if err != nil {
		t.Fatal(err)
	}
	var results []GeneratedUser
	if err := json.Unmarshal(plain, &results); err != nil || len(results) != 23 {
		t.Fatalf("result users=%d err=%v", len(results), err)
	}
	seen := map[string]bool{}
	for _, u := range results {
		if !strings.HasPrefix(u.Email, "gen-") || !strings.HasSuffix(u.Email, "@gen.invalid") || seen[u.Email] {
			t.Fatalf("generated email %q", u.Email)
		}
		seen[u.Email] = true
		var phc, groupID string
		var verified bool
		if err := admin.QueryRow(ctx, `
			SELECT p.phc, u.user_group_id::text, u.email_verified_at IS NOT NULL
			  FROM users u JOIN user_passwords p ON p.user_id = u.id
			 WHERE u.tenant_id = $1 AND u.email = $2`, tenant, u.Email).Scan(&phc, &groupID, &verified); err != nil {
			t.Fatalf("read generated user %s: %v", u.Email, err)
		}
		if ok, _, err := crypto.VerifyPassword(u.Password, phc); err != nil || !ok || groupID != group || !verified {
			t.Fatalf("generated user %s: password ok=%v err=%v group=%s verified=%v", u.Email, ok, err, groupID, verified)
		}
	}
	if n := auditCount("user.bulk_generate_exported"); n != 1 {
		t.Fatalf("export audit rows=%d", n)
	}

	// 到期清结果：任务行留着，密文没了，再下载回 409
	if _, err := admin.Exec(ctx, `UPDATE user_generation_jobs SET result_expires_at = now() - interval '1 minute' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.PurgeExpiredUserGenerationResults(ctx, tenant); err != nil || n != 1 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
	var he *httpx.Error
	if _, _, err := svc.UserGenerationResult(ctx, tenant, job.ID, actor); !errors.As(err, &he) || he.Code != httpx.CodeConflict {
		t.Fatalf("download after purge err=%v", err)
	}

	// 分组在开工前被删：任务判失败，没生成的记进 failed
	gone, err := svc.SubmitGenerateUsers(ctx, tenant, GenerateUsersInput{Count: 3, EmailPrefix: "gone",
		EmailDomain: "gen.invalid", GroupID: group2, ActorID: actor, Reason: "分组会被删掉"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM user_groups WHERE id=$1`, group2); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RunOnce(ctx, tenant); err != nil {
		t.Fatalf("run gone-group job: %v", err)
	}
	if got, err := svc.GetUserGenerationJob(ctx, tenant, gone.ID); err != nil || got.Status != "failed" ||
		got.Failed != 3 || got.Completed != 0 || got.Error == nil || got.ResultAvailable {
		t.Fatalf("gone-group job=%+v err=%v", got, err)
	}
	if jobs, err := svc.ListUserGenerationJobs(ctx, tenant); err != nil || len(jobs) != 2 || jobs[0].ID != gone.ID {
		t.Fatalf("job list=%+v err=%v", jobs, err)
	}

	// 撞邮箱：第一份口令先拿到已被占用的后缀，换后缀后照样写入；口令与账号一一对应
	suffixes := []string{"aaaaaaaa", "bbbbbbbb", "cccccccc"}
	next := func() (string, error) {
		s := suffixes[0]
		suffixes = suffixes[1:]
		return s, nil
	}
	collide := []generatedCredential{{password: "first-password-1A"}, {password: "second-password-2B"}}
	for i := range collide {
		if collide[i].phc, err = crypto.HashPassword(collide[i].password, crypto.DefaultArgon2Params()); err != nil {
			t.Fatal(err)
		}
	}
	var users []GeneratedUser
	if err := app.InTx(ctx, db.Scope{TenantID: tenant, ActorID: actor}, func(tx pgx.Tx) error {
		users, err = insertGeneratedUsers(ctx, tx, tenant, nil, "gen", "gen.invalid", collide, next)
		return err
	}); err != nil {
		t.Fatalf("insert with a colliding suffix: %v", err)
	}
	if len(users) != 2 || users[0].Email != "gen-cccccccc@gen.invalid" || users[1].Email != "gen-bbbbbbbb@gen.invalid" {
		t.Fatalf("collision retry emails = %+v", users)
	}
	for i, u := range users {
		var phc string
		if err := admin.QueryRow(ctx, `
			SELECT p.phc FROM users u JOIN user_passwords p ON p.user_id = u.id
			 WHERE u.tenant_id = $1 AND u.email = $2`, tenant, u.Email).Scan(&phc); err != nil {
			t.Fatal(err)
		}
		if phc != collide[i].phc || u.Password != collide[i].password {
			t.Fatalf("user %s got password row of another credential", u.Email)
		}
	}
	t.Log("marker=generate_users_hash_outside_tx_ok")
}
