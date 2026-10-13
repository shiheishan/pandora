package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/certs/repo"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
)

// certsCommitTables 是真提交用例前后要清的 certs 相关表（CASCADE 带走订单、版本等）。
var certsCommitTables = []string{
	"certificate_issuances", "certificate_orders", "certificate_versions", "certificates",
	"acme_accounts", "dns_credentials",
}

// pgEnv 在进程内 CA/DNS 夹具之上接真库与真 service。
type pgEnv struct {
	*issueEnv
	ctx   context.Context
	admin uuid.UUID
	row   func(ctx context.Context, q string, args ...any) *sql.Row
	exec  func(ctx context.Context, q string, args ...any) error
}

func randHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func cleanCertsTables(t *testing.T, e *pgEnv) {
	t.Helper()
	d := testkit.DB(t)
	testkit.Truncate(t, d, certsCommitTables...)
	if err := e.exec(e.ctx, `DELETE FROM settings WHERE key LIKE 'acme.%'`); err != nil {
		t.Fatalf("清 ACME 设置: %v", err)
	}
}

// newPGEnv 真提交：签发跨事务、并发用例需要库里的认领与租约行为。
func newPGEnv(t *testing.T) *pgEnv {
	t.Helper()
	d := testkit.DB(t)
	_, rdb := testkit.Redis(t)

	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x05}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	settingsSealer, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	txm := tx.NewManager(d)
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	settingsSvc := settingssvc.New(settingsrepo.New(d), txm, settingsSealer, rdb, auditSvc)

	e := newIssueEnv(t, Options{})
	opts := e.svc.opts
	e.svc = newService(repo.New(d), txm, auditSvc, settingsSvc, e.svc.sealer, nil, opts)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	pe := &pgEnv{
		issueEnv: e,
		ctx:      ctx,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	cleanCertsTables(t, pe)
	t.Cleanup(func() { cleanCertsTables(t, pe) })

	var admin uuid.UUID
	email := fmt.Sprintf("certs-%s@example.test", randHex(16))
	if err := pe.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	pe.admin = admin
	pe.ctx = authn.With(ctx, authn.Principal{
		UserID: admin, SessionID: "s-certs-pg", Audience: authn.AudienceAdmin, ReauthAt: time.Now(),
	})
	pe.ctx = reqid.With(pe.ctx, reqid.New())
	return pe
}

func (e *pgEnv) credential(t *testing.T, name, token string) VerifyResult {
	t.Helper()
	res, err := e.svc.CreateDNSCredential(e.ctx, DNSCredentialInput{
		Name: name, Provider: ProviderCloudflare, Zone: testZone,
		Secret: map[string]string{"api_token": token},
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return res
}

func (e *pgEnv) certificate(t *testing.T, name string, credID uuid.UUID, ids ...string) Certificate {
	t.Helper()
	c, err := e.svc.CreateCertificate(e.ctx, CertificateInput{
		Name: name, Identifiers: ids, DNSCredentialID: credID.String(),
	})
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return c
}

// run 代替 asynq：跳过 ARI/到期等级 sweep，巡检入队部分由 OpenOrders + IssueOrder 承担。
func (e *pgEnv) run(t *testing.T) {
	t.Helper()
	e.svc.lastSweep.Store(time.Now().UnixNano())
	if _, err := e.svc.Patrol(e.ctx); err != nil {
		t.Fatalf("patrol: %v", err)
	}
	ids, err := e.svc.store.OpenOrders(e.ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := e.svc.IssueOrder(e.ctx, id); err != nil {
			t.Fatalf("issue order %s: %v", id, err)
		}
	}
}

func (e *pgEnv) claim(t *testing.T, certID uuid.UUID) repo.ClaimedOrder {
	t.Helper()
	var orderID uuid.UUID
	if err := e.row(e.ctx, `SELECT id FROM certificate_orders
		 WHERE certificate_id = ? AND state = 'queued' ORDER BY created_at DESC, id DESC LIMIT 1`,
		certID).Scan(&orderID); err != nil {
		t.Fatalf("queued order: %v", err)
	}
	c, ok, err := e.svc.store.ClaimOrder(e.ctx, orderID, orderLease)
	if err != nil || !ok {
		t.Fatalf("claim order %s: ok=%v err=%v", orderID, ok, err)
	}
	return c
}

func (e *pgEnv) scalar(t *testing.T, q string, args ...any) any {
	t.Helper()
	var v any
	if err := e.row(e.ctx, q, args...).Scan(&v); err != nil {
		t.Fatalf("query %s: %v", q, err)
	}
	return v
}

func (e *pgEnv) order(t *testing.T, certID uuid.UUID) (state, reason string, code, replaces *string) {
	t.Helper()
	if err := e.row(e.ctx, `SELECT state, reason, error_code, replaces_ari_id FROM certificate_orders
		 WHERE certificate_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, certID).
		Scan(&state, &reason, &code, &replaces); err != nil {
		t.Fatalf("latest order: %v", err)
	}
	return state, reason, code, replaces
}

func (e *pgEnv) serial(t *testing.T, certID uuid.UUID, version int) string {
	t.Helper()
	var serial string
	if err := e.row(e.ctx, `SELECT serial FROM certificate_versions WHERE certificate_id = ? AND version = ?`,
		certID, version).Scan(&serial); err != nil {
		t.Fatalf("version serial: %v", err)
	}
	return serial
}
