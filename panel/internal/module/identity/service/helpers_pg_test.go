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
	"github.com/aegispanel/aegis/internal/module/identity/repo"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

// 假依赖：记录调用。
type fakeRevoker struct{ calls []revokeCall }
type revokeCall struct {
	user   uuid.UUID
	except string
}

func (f *fakeRevoker) RevokeUser(_ context.Context, userID uuid.UUID, exceptSessionID string) error {
	f.calls = append(f.calls, revokeCall{user: userID, except: exceptSessionID})
	return nil
}

type fakeGuard struct {
	err   error
	calls []uuid.UUID
}

func (f *fakeGuard) EnsureNotLastAdmin(_ context.Context, userID uuid.UUID) error {
	f.calls = append(f.calls, userID)
	return f.err
}

type fakeMailer struct{ sent []sentMail }
type sentMail struct {
	template, address string
	vars              map[string]string
	userID            *uuid.UUID
}

func (f *fakeMailer) SendToAddress(_ context.Context, templateCode, address string, vars map[string]string, userID *uuid.UUID) error {
	f.sent = append(f.sent, sentMail{template: templateCode, address: address, vars: vars, userID: userID})
	return nil
}

type fakeStaff struct {
	staff     map[uuid.UUID]bool
	manageErr error
}

func (f *fakeStaff) IsStaff(_ context.Context, userID uuid.UUID) (bool, error) {
	return f.staff[userID], nil
}

func (f *fakeStaff) EnsureCanManage(_ context.Context, _, _ uuid.UUID) error {
	return f.manageErr
}

type env struct {
	svc      *Service
	settings *settingssvc.Service
	revoker  *fakeRevoker
	guard    *fakeGuard
	mailer   *fakeMailer
	staff    *fakeStaff
	row      func(ctx context.Context, sql string, args ...any) *sql.Row
	exec     func(ctx context.Context, sql string, args ...any) error
	build    func() *Service
}

func newEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	_, rdb := testkit.Redis(t)

	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x01
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}

	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	settings := settingssvc.New(settingsrepo.New(d), tx.NewManager(d), sealer, rdb, auditSvc)
	cfg := &config.Config{Env: config.EnvTest}
	cfg.Auth.PasswordHash = config.PasswordHash{
		PublicConcurrency: 2,
		AdminConcurrency:  1,
		QueueTimeout:      5 * time.Second,
	}

	idRepo := repo.New(d)
	txm := tx.NewManager(d)
	revoker := &fakeRevoker{}
	guard := &fakeGuard{}
	mailer := &fakeMailer{}
	staff := &fakeStaff{staff: map[uuid.UUID]bool{}}

	svc := New(idRepo, txm, settings, auditSvc, keys, rdb, ratelimit.New(rdb), cfg)
	svc.UseSessionRevoker(revoker)
	svc.UseAdminGuard(guard)
	svc.UseVerificationMailer(mailer)
	svc.UseStaffGuard(staff)

	ctx := testkit.TxCtx(t, d)
	ctx = reqid.With(ctx, reqid.New())
	ctx = reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.7", UserAgent: "test-agent/1.0"})

	e := &env{
		svc:      svc,
		settings: settings,
		revoker:  revoker,
		guard:    guard,
		mailer:   mailer,
		staff:    staff,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
		build: func() *Service {
			return New(idRepo, txm, settings, auditSvc, keys, rdb, ratelimit.New(rdb), cfg)
		},
	}
	return e, ctx
}

func asAdmin(ctx context.Context, id uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{
		UserID:    id,
		SessionID: "s-admin",
		Audience:  authn.AudienceAdmin,
	})
	return ctx
}

func asUser(ctx context.Context, id uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{
		UserID:    id,
		SessionID: "s-cur",
		Audience:  authn.AudiencePublic,
	})
	return ctx
}

func enableMail(t *testing.T, ctx context.Context, settings *settingssvc.Service) {
	t.Helper()
	if err := settings.SetMail(ctx, settingssvc.MailUpdate{
		Host:        "smtp.example.test",
		Port:        587,
		Encryption:  settingssvc.MailEncryptionTLS,
		FromAddress: "noreply@example.test",
	}); err != nil {
		t.Fatal(err)
	}
}

func setRegistration(t *testing.T, ctx context.Context, settings *settingssvc.Service, mode string, verify bool) {
	t.Helper()
	if verify {
		enableMail(t, ctx, settings)
	}
	if err := settings.SetRegistration(ctx, settingssvc.Registration{
		Mode:              mode,
		EmailVerification: verify,
	}); err != nil {
		t.Fatal(err)
	}
}

func uniqEmail(t *testing.T, local string) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s-%s@example.test", local, hex.EncodeToString(b))
}

func (e *env) mkUser(t *testing.T, ctx context.Context, email, password string) User {
	t.Helper()
	u, err := e.svc.CreateUser(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func codeOf(err error) errs.Code {
	if err == nil {
		return ""
	}
	return errs.As(err).Code
}

func (e *env) auditCount(ctx context.Context, action string, resourceID uuid.UUID) int64 {
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, action, resourceID).Scan(&n); err != nil {
		return 0
	}
	return n
}

func weakPHC(t *testing.T, password string) string {
	t.Helper()
	const m, it, par = uint32(8192), uint32(1), uint8(1)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	key := argon2.IDKey([]byte(password), salt, it, m, par, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, m, it, par,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}
