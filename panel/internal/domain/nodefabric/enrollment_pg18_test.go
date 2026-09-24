// [INPUT]: 依赖 platform/crypto 与 platform/db，依赖一次性 PG18 库（run-pg18-gates.sh 的 enrollment 域）
// [OUTPUT]: 对外提供 TestNodeEnrollmentPG18 与 openEnrollmentPG18（库护栏，server_token_pg18_test.go 共用）
// [POS]: domain/nodefabric 的 PG18 集成测试：节点两阶段接入的幂等与凭据激活
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openEnrollmentPG18 打开 enrollment 域的一次性 PG18 库（未配置则跳过），
// 同包其它 PG18 用例（server-token 签发）共用这道护栏，各用各的随机租户。
func openEnrollmentPG18(t *testing.T) (context.Context, *pgxpool.Pool, *db.Pool) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("AEGIS_ENROLLMENT_PG18_FIXTURE")) != "disposable-v1" {
		t.Skip("disposable PG18 fixture is required")
	}
	appDSN, adminDSN, expectedDB := os.Getenv("AEGIS_ENROLLMENT_PG18_DSN"), os.Getenv("AEGIS_ENROLLMENT_PG18_ADMIN_DSN"), os.Getenv("AEGIS_ENROLLMENT_PG18_DATABASE")
	if appDSN == "" || adminDSN == "" || expectedDB == "" {
		t.Skip("PG18 DSNs are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var dbName string
	var version int
	if err := admin.QueryRow(ctx, `SELECT current_database(),current_setting('server_version_num')::int`).Scan(&dbName, &version); err != nil {
		t.Fatal(err)
	}
	if dbName != expectedDB || version < 180000 || version >= 190000 {
		t.Fatalf("refusing unexpected PG target %s/%d", dbName, version)
	}
	app, err := db.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return ctx, admin, app
}

func TestNodeEnrollmentPG18(t *testing.T) {
	ctx, admin, app := openEnrollmentPG18(t)
	signerSeed := sha256.Sum256([]byte("enrollment-pg18-test-signer"))
	signer, err := platformcrypto.NewSigner(signerSeed[:])
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(app, signer)
	tenant := uuid.New()
	name := "enroll-" + uuid.NewString()
	token := "bootstrap-secret-" + uuid.NewString()
	if _, err := admin.Exec(ctx, `INSERT INTO tenants(id,slug,display_name) VALUES($1,$2,$3)`, tenant, "enrollment-"+uuid.NewString(), "Enrollment Test"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO bootstrap_tokens(tenant_id,token_hash,expires_at) VALUES($1,$2,now()+interval '20 minutes')`, tenant, bootstrapTokenHash(token, name)); err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runtimeToken := "runtime-" + uuid.NewString()
	runtimeHash := sha256.Sum256([]byte(runtimeToken))
	beginHash := sha256.Sum256([]byte("begin-evidence"))
	requestID := uuid.NewString()
	begin := func() (*EnrollmentOutput, error) {
		return svc.BeginEnrollment(ctx, tenant.String(), BeginEnrollmentInput{Token: token, NodeName: name, RequestID: requestID,
			PublicKey: base64.StdEncoding.EncodeToString(pub), RuntimeTokenSHA256: base64.StdEncoding.EncodeToString(runtimeHash[:]), BeginRequestSHA256: beginHash[:]})
	}
	first, err := begin()
	if err != nil {
		t.Fatal(err)
	}
	retry, err := begin()
	if err != nil {
		t.Fatal(err)
	}
	if first.EnrollmentID != retry.EnrollmentID || first.ConfigKeyID != retry.ConfigKeyID || first.ConfigPublicKey != retry.ConfigPublicKey {
		t.Fatal("begin retry changed immutable response")
	}
	var identities int
	var serverToken []byte
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2`, tenant, first.NodeID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT server_token_hash FROM nodes WHERE tenant_id=$1 AND id=$2`, tenant, first.NodeID).Scan(&serverToken); err != nil {
		t.Fatal(err)
	}
	if identities != 0 || len(serverToken) != 0 {
		t.Fatalf("begin activated credentials identities=%d token=%x", identities, serverToken)
	}
	commitHash := sha256.Sum256([]byte("commit-evidence"))
	evidence := CommitEnrollmentInput{EnrollmentID: first.EnrollmentID, CommitRequestSHA256: commitHash[:], AgentVersion: "test", Architecture: "amd64",
		BinarySHA256: strings.Repeat("1", 64), ConfigSHA256: strings.Repeat("2", 64), UnitSHA256: strings.Repeat("3", 64), PreflightSHA256: strings.Repeat("4", 64)}
	committed, err := svc.CommitEnrollment(ctx, tenant.String(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != "committed" {
		t.Fatalf("state=%s", committed.State)
	}
	if _, err := svc.CommitEnrollment(ctx, tenant.String(), evidence); err != nil {
		t.Fatalf("idempotent commit: %v", err)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='active'`, tenant, first.NodeID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT server_token_hash FROM nodes WHERE tenant_id=$1 AND id=$2`, tenant, first.NodeID).Scan(&serverToken); err != nil {
		t.Fatal(err)
	}
	if identities != 1 || !platformcrypto.ConstantTimeEqual(serverToken, runtimeHash[:]) {
		t.Fatalf("commit credentials mismatch identities=%d token=%x", identities, serverToken)
	}
	var evidenceJSON []byte
	var nodeStatus string
	if err := admin.QueryRow(ctx, `SELECT commit_evidence,status FROM node_enrollments e JOIN nodes n ON n.id=e.node_id WHERE e.tenant_id=$1 AND e.id=$2`, tenant, first.EnrollmentID).Scan(&evidenceJSON, &nodeStatus); err != nil {
		t.Fatal(err)
	}
	if len(evidenceJSON) == 0 || nodeStatus != "attesting" {
		t.Fatalf("commit evidence/lifecycle not persisted: evidence=%s status=%s", evidenceJSON, nodeStatus)
	}
	if _, err := svc.AbortEnrollment(ctx, tenant.String(), first.EnrollmentID, "late abort"); err == nil {
		t.Fatal("committed enrollment was aborted")
	}

	// A status materialization racing an abort must converge without a PG
	// deadlock and must leave no pending enrollment behind the deadline.
	name2, token2 := "expire-"+uuid.NewString(), "bootstrap-expire-"+uuid.NewString()
	if _, err := admin.Exec(ctx, `INSERT INTO bootstrap_tokens(tenant_id,token_hash,expires_at) VALUES($1,$2,now()+interval '500 milliseconds')`, tenant, bootstrapTokenHash(token2, name2)); err != nil {
		t.Fatal(err)
	}
	pub2, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runtimeHash2 := sha256.Sum256([]byte("runtime-" + uuid.NewString()))
	expireBegin, err := svc.BeginEnrollment(ctx, tenant.String(), BeginEnrollmentInput{Token: token2, NodeName: name2, RequestID: uuid.NewString(),
		PublicKey: base64.StdEncoding.EncodeToString(pub2), RuntimeTokenSHA256: base64.StdEncoding.EncodeToString(runtimeHash2[:]), BeginRequestSHA256: beginHash[:]})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(750 * time.Millisecond)
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := svc.GetEnrollment(ctx, tenant.String(), expireBegin.EnrollmentID)
		errCh <- err
	}()
	go func() {
		defer wg.Done()
		_, err := svc.AbortEnrollment(ctx, tenant.String(), expireBegin.EnrollmentID, "race")
		errCh <- err
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil && strings.Contains(err.Error(), "40P01") {
			t.Fatalf("status/abort deadlocked: %v", err)
		}
	}
	var finalState string
	if err := admin.QueryRow(ctx, `SELECT state FROM node_enrollments WHERE id=$1`, expireBegin.EnrollmentID).Scan(&finalState); err != nil {
		t.Fatal(err)
	}
	if finalState != "expired" && finalState != "aborted" {
		t.Fatalf("racing terminal state=%s", finalState)
	}
}
