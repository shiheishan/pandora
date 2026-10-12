package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/google/uuid"
)

func testKeys(t *testing.T) *crypto.Keys {
	t.Helper()
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x01
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func mustService(t *testing.T, r *repo.Repo) *Service {
	t.Helper()
	svc, err := New(r, testKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func reqCtx(ctx context.Context, user uuid.UUID, audience string) context.Context {
	ctx = reqid.With(ctx, reqid.New())
	ctx = authn.With(ctx, authn.Principal{
		UserID:    user,
		SessionID: "s1",
		Audience:  audience,
	})
	return reqmeta.With(ctx, reqmeta.Meta{
		IP:        "203.0.113.7",
		UserAgent: "test-agent/1.0",
	})
}

func uniqueAction(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "t" + hex.EncodeToString(b) + ".x"
}

// rawAuditCols 对应按 action 查出的原始列。
type rawAuditCols struct {
	ActorKind     string
	ActorID       *uuid.UUID
	ActorAudience *string
	AuthContext   *string
	RequestID     *string
	IPHash        []byte
	IPEnc         []byte
	UserAgent     *string
	Outcome       string
	Reason        *string
}

// rawColsSQL 按 action 取原始列；测试里 scanRaw(t, d.Conn(ctx).Raw(rawColsSQL, a).Row())。
// （service 包的测试不能 import pkg/db 与 gorm，连接只经 testkit.DB 的返回值调用方法。）
const rawColsSQL = `
	SELECT actor_kind, actor_id, actor_audience, auth_context, request_id,
	       ip_hash, ip_enc, user_agent, outcome, data->>'reason' AS reason
	  FROM audit_events WHERE action = ?`

// countSQL 按 action 计数。
const countSQL = `SELECT count(*) FROM audit_events WHERE action = ?`

func scanRaw(t *testing.T, row *sql.Row) rawAuditCols {
	t.Helper()
	var r rawAuditCols
	if err := row.Scan(&r.ActorKind, &r.ActorID, &r.ActorAudience, &r.AuthContext, &r.RequestID,
		&r.IPHash, &r.IPEnc, &r.UserAgent, &r.Outcome, &r.Reason); err != nil {
		t.Fatalf("读审计原始列: %v", err)
	}
	return r
}

func scanCount(t *testing.T, row *sql.Row) int64 {
	t.Helper()
	var n int64
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
