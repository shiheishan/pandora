package support

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/middleware"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const supportPG18Fixture = "disposable-v1"

func TestSupportPG18(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("AEGIS_SUPPORT_PG18_FIXTURE"))
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_SUPPORT_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_SUPPORT_PG18_ADMIN_DSN"))
	expectedDatabase := strings.TrimSpace(os.Getenv("AEGIS_SUPPORT_PG18_DATABASE"))
	runID := strings.TrimSpace(os.Getenv("AEGIS_SUPPORT_PG18_RUN_ID"))
	if fixture == "" && appDSN == "" && adminDSN == "" && expectedDatabase == "" && runID == "" {
		t.Skip("support PostgreSQL 18 fixture is not configured")
	}
	if fixture != supportPG18Fixture || appDSN == "" || adminDSN == "" || expectedDatabase == "" || runID == "" {
		t.Fatal("disposable-v1, app/admin DSNs, exact database and run ID are required")
	}
	if !strings.HasPrefix(expectedDatabase, "pandora_node_preview_") ||
		!regexp.MustCompile(`^[a-z0-9]{32}$`).MatchString(runID) {
		t.Fatalf("refusing malformed disposable identity database=%q run_id=%q", expectedDatabase, runID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	adminDB, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	app, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	var database, marker, comment string
	var version int
	if err := app.QueryRow(ctx, `SELECT current_database(), current_setting('server_version_num')::int,
		(SELECT run_id FROM public.pandora_support_test_marker), shobj_description(oid,'pg_database')
		FROM pg_database WHERE datname=current_database()`).Scan(&database, &version, &marker, &comment); err != nil {
		t.Fatalf("identify PostgreSQL fixture: %v", err)
	}
	if database != expectedDatabase || version/10000 != 18 || marker != runID || comment != "pandora-node-preview-pg18:"+runID {
		t.Fatalf("refusing unexpected fixture database=%q version=%d marker=%q comment=%q", database, version, marker, comment)
	}

	const (
		tenantID   = "78000000-0000-4000-8000-000000000001"
		ownerID    = "78000000-0000-4000-8000-000000000011"
		outsiderID = "78000000-0000-4000-8000-000000000012"
		agentID    = "78000000-0000-4000-8000-000000000013"
	)
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'support-a','Support A','USD')`, []any{tenantID}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@support.invalid','Owner','active')`, []any{tenantID, ownerID}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'outsider@support.invalid','Outsider','active')`, []any{tenantID, outsiderID}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'agent@support.invalid','Agent','active')`, []any{tenantID, agentID}},
	} {
		if _, err := adminDB.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed support fixture: %v\nSQL: %s", err, row.sql)
		}
	}

	svc := NewService(app)
	createClaim := newSupportPG18Claim(t, ctx, adminDB, tenantID, ownerID, CreateIdempotencyScope, "create")
	created, err := svc.CreateAtomic(ctx, tenantID, CreateInput{
		UserID: ownerID, Body: "香港节点从今天早上开始无法连接，请协助排查具体原因。",
	}, createClaim)
	if err != nil {
		t.Fatalf("atomic create: %v", err)
	}
	if created.Ticket == nil || created.Ticket.ID == "" || created.PreparedResponse().StatusCode() != 201 {
		t.Fatalf("invalid create result: %+v", created)
	}
	var createStatus, createFormat string
	var createCode, ticketCount, messageCount, createAudits int
	var createPayload []byte
	if err := adminDB.QueryRow(ctx, `SELECT status,response_code,response_format,response_payload,
		(SELECT count(*)::int FROM tickets WHERE tenant_id=$2 AND id=$3),
		(SELECT count(*)::int FROM ticket_messages WHERE tenant_id=$2 AND ticket_id=$3),
		(SELECT count(*)::int FROM audit_events WHERE tenant_id=$2 AND resource_id=$3 AND action='ticket.create')
		FROM idempotency_keys WHERE id=$1`, createClaim.ID, tenantID, created.Ticket.ID).
		Scan(&createStatus, &createCode, &createFormat, &createPayload, &ticketCount, &messageCount, &createAudits); err != nil {
		t.Fatal(err)
	}
	if createStatus != "succeeded" || createCode != 201 || createFormat != "bytes" ||
		!bytes.Equal(createPayload, created.PreparedResponse().BodyBytes()) ||
		ticketCount != 1 || messageCount != 1 || createAudits != 1 {
		t.Fatalf("atomic create evidence status=%s code=%d format=%s ticket=%d message=%d audit=%d",
			createStatus, createCode, createFormat, ticketCount, messageCount, createAudits)
	}

	revokeAudit := func() {
		if _, err := adminDB.Exec(ctx, `REVOKE INSERT ON audit_events FROM aegis_app`); err != nil {
			t.Fatal(err)
		}
	}
	restoreAudit := func() {
		if _, err := adminDB.Exec(ctx, `GRANT INSERT ON audit_events TO aegis_app`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `GRANT INSERT ON audit_events TO aegis_app`) })

	replyClaim := newSupportPG18Claim(t, ctx, adminDB, tenantID, ownerID, UserReplyIdempotencyScope, "reply")
	revokeAudit()
	if _, err := svc.ReplyAsUserAtomic(ctx, tenantID, ownerID, created.Ticket.ID,
		"补充说明：手机和电脑都无法连接。", replyClaim); err == nil {
		t.Fatal("reply unexpectedly committed without audit permission")
	}
	restoreAudit()
	assertSupportPG18State(t, ctx, adminDB, created.Ticket.ID, replyClaim.ID, "open", 1, "in_flight", 0)
	if _, err := svc.ReplyAsUserAtomic(ctx, tenantID, ownerID, created.Ticket.ID,
		"补充说明：手机和电脑都无法连接。", replyClaim); err != nil {
		t.Fatalf("retry reply after rollback: %v", err)
	}
	assertSupportPG18State(t, ctx, adminDB, created.Ticket.ID, replyClaim.ID, "pending_agent", 2, "succeeded", 1)

	internalClaim := newSupportPG18Claim(t, ctx, adminDB, tenantID, agentID, AgentReplyIdempotencyScope, "internal")
	if _, err := svc.ReplyAsAgentAtomic(ctx, tenantID, AgentReplyInput{
		AgentID: agentID, TicketID: created.Ticket.ID,
		Body: "仅供客服内部排查的备注", InternalNote: true,
	}, internalClaim); err != nil {
		t.Fatalf("atomic internal note: %v", err)
	}
	userView, err := svc.GetForUser(ctx, tenantID, ownerID, created.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range userView.Messages {
		if message.InternalNote || strings.Contains(message.Body, "仅供客服") {
			t.Fatal("internal note leaked through user service view")
		}
	}
	var internalAudits int
	if err := adminDB.QueryRow(ctx, `SELECT count(*)::int FROM audit_events
		WHERE tenant_id=$1 AND resource_id=$2 AND action='ticket.internal_note'`, tenantID, created.Ticket.ID).
		Scan(&internalAudits); err != nil || internalAudits != 1 {
		t.Fatalf("internal note audit=%d err=%v", internalAudits, err)
	}

	outsiderClaim := newSupportPG18Claim(t, ctx, adminDB, tenantID, outsiderID, UserReplyIdempotencyScope, "outsider")
	if _, err := svc.ReplyAsUserAtomic(ctx, tenantID, outsiderID, created.Ticket.ID,
		"不属于我的工单", outsiderClaim); err == nil {
		t.Fatal("outsider reply unexpectedly succeeded")
	}
	assertSupportPG18State(t, ctx, adminDB, created.Ticket.ID, outsiderClaim.ID, "pending_agent", 3, "in_flight", 1)

	wrongGUCClaim := newSupportPG18Claim(t, ctx, adminDB, tenantID, ownerID, UserCloseIdempotencyScope, "wrong-guc")
	prepared, err := httpx.PrepareJSON(200, map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	err = app.InTx(ctx, platformdb.Scope{TenantID: tenantID, ActorID: outsiderID}, func(tx pgx.Tx) error {
		return middleware.CompleteSuccessJSONInTx(ctx, tx, wrongGUCClaim, prepared)
	})
	if !errors.Is(err, middleware.ErrIdempotencyClaimLost) {
		t.Fatalf("wrong GUC completion error=%v, want claim lost", err)
	}

	if _, err := adminDB.Exec(ctx, `UPDATE tickets SET sla_first_response_due=now()-interval '1 hour'
		WHERE id=$1`, created.Ticket.ID); err != nil {
		t.Fatal(err)
	}
	revokeAudit()
	if n, err := svc.EscalateOverdue(ctx, tenantID); err == nil || n != 0 {
		t.Fatalf("SLA audit fault n=%d err=%v", n, err)
	}
	restoreAudit()
	var status string
	if err := adminDB.QueryRow(ctx, `SELECT status FROM tickets WHERE id=$1`, created.Ticket.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending_agent" {
		t.Fatalf("SLA update committed despite audit failure: %s", status)
	}
	if n, err := svc.EscalateOverdue(ctx, tenantID); err != nil || n != 1 {
		t.Fatalf("SLA escalation n=%d err=%v", n, err)
	}
	var slaAudits int
	if err := adminDB.QueryRow(ctx, `SELECT status,
		(SELECT count(*)::int FROM audit_events WHERE tenant_id=$2 AND resource_id=$1 AND action='ticket.sla_escalate')
		FROM tickets WHERE id=$1`, created.Ticket.ID, tenantID).Scan(&status, &slaAudits); err != nil {
		t.Fatal(err)
	}
	if status != "escalated" || slaAudits != 1 {
		t.Fatalf("SLA evidence status=%s audits=%d", status, slaAudits)
	}

	var forced bool
	if err := app.QueryRow(ctx, `SELECT bool_and(relrowsecurity AND relforcerowsecurity)
		FROM pg_class WHERE oid IN ('tickets'::regclass,'ticket_messages'::regclass,
		'audit_events'::regclass,'idempotency_keys'::regclass)`).Scan(&forced); err != nil || !forced {
		t.Fatalf("support FORCE RLS proof failed forced=%v err=%v", forced, err)
	}
	t.Log("support_pg18_business=ok role=aegis_app rls=on schema=40 atomic=create,reply,internal,sla audit_rollback=reply,sla cross_owner=blocked guc_actor=bound")
}

func newSupportPG18Claim(
	t *testing.T,
	ctx context.Context,
	adminDB *pgxpool.Pool,
	tenantID, actorID, scope, label string,
) middleware.IdempotencyClaim {
	t.Helper()
	actorHash := sha256.Sum256([]byte(actorID))
	requestHash := sha256.Sum256([]byte(scope + ":" + label))
	claim := middleware.IdempotencyClaim{
		ID: uuid.NewString(), TenantID: tenantID, ActorID: actorID, Scope: scope,
		StorageScope: fmt.Sprintf("%s:actor:%x", scope, actorHash[:12]),
		Key:          "support-pg18-" + label, Generation: 1,
		LockedUntil: time.Now().UTC().Add(10 * time.Minute), RequestHash: requestHash,
	}
	if _, err := adminDB.Exec(ctx, `INSERT INTO idempotency_keys
		(id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until,claim_generation,response_format)
		VALUES($1,$2,$3,$4,$5,'in_flight',$6,$7,$8,'none')`,
		claim.ID, claim.TenantID, claim.StorageScope, claim.Key, claim.RequestHash[:],
		claim.ActorID, claim.LockedUntil, claim.Generation); err != nil {
		t.Fatalf("seed idempotency claim %s: %v", label, err)
	}
	return claim
}

func assertSupportPG18State(
	t *testing.T,
	ctx context.Context,
	adminDB *pgxpool.Pool,
	ticketID, claimID, wantTicketStatus string,
	wantMessages int,
	wantClaimStatus string,
	wantReplyAudits int,
) {
	t.Helper()
	var ticketStatus, claimStatus string
	var messages, audits int
	if err := adminDB.QueryRow(ctx, `SELECT t.status,
		(SELECT count(*)::int FROM ticket_messages WHERE ticket_id=t.id),
		(SELECT status FROM idempotency_keys WHERE id=$2),
		(SELECT count(*)::int FROM audit_events WHERE resource_id=t.id AND action='ticket.reply')
		FROM tickets t WHERE t.id=$1`, ticketID, claimID).
		Scan(&ticketStatus, &messages, &claimStatus, &audits); err != nil {
		t.Fatal(err)
	}
	if ticketStatus != wantTicketStatus || messages != wantMessages ||
		claimStatus != wantClaimStatus || audits != wantReplyAudits {
		t.Fatalf("state status=%s messages=%d claim=%s reply_audits=%d", ticketStatus, messages, claimStatus, audits)
	}
}
