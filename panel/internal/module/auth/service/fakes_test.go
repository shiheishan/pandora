package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/auth/repo"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/jwtx"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// testMasterB64 是 32 字节固定主密钥（base64）。
var testMasterB64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 32))

var testJWTPublicKey = bytes.Repeat([]byte{0x11}, 32)
var testJWTAdminKey = bytes.Repeat([]byte{0x22}, 32)

// fakeUsers 是 identity Users 的假实现。
type fakeUsers struct {
	mu        sync.Mutex
	users     map[uuid.UUID]identitysvc.User
	byEmail   map[string]uuid.UUID
	passwords map[uuid.UUID]string
	authErr   error
	// onAuth 在 Authenticate 校验通过后、返回前调用（测纪元竞态）。
	onAuth func()
	// touched 记录 TouchLastLogin 的调用。
	touched []uuid.UUID
	// displayNames 是 Profile 返回的显示名。
	displayNames map[uuid.UUID]*string
}

func (f *fakeUsers) TouchLastLogin(ctx context.Context, userID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, userID)
	return nil
}

func (f *fakeUsers) Authenticate(ctx context.Context, audience, email, password string) (identitysvc.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authErr != nil {
		return identitysvc.User{}, f.authErr
	}
	norm := normalizeEmail(email)
	id, ok := f.byEmail[norm]
	if !ok {
		return identitysvc.User{}, identitysvc.ErrInvalidCredentials
	}
	// 与 identity 一致：账号存在时出错也带回 User
	u := f.users[id]
	if f.passwords[id] != password {
		return u, identitysvc.ErrInvalidCredentials
	}
	if u.Status != UserStatusActive {
		return u, identitysvc.ErrAccountInactive
	}
	if f.onAuth != nil {
		f.onAuth()
	}
	return u, nil
}

func (f *fakeUsers) FindByEmail(ctx context.Context, email string) (identitysvc.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byEmail[normalizeEmail(email)]
	if !ok {
		return identitysvc.User{}, errs.New(errs.NotFound)
	}
	return f.users[id], nil
}

func (f *fakeUsers) Get(ctx context.Context, id uuid.UUID) (identitysvc.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return identitysvc.User{}, errs.New(errs.NotFound)
	}
	return u, nil
}

func (f *fakeUsers) Profile(ctx context.Context, id uuid.UUID) (identitysvc.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return identitysvc.Profile{}, errs.New(errs.Unauthorized)
	}
	return identitysvc.Profile{
		ID: u.ID, Email: u.Email, DisplayName: f.displayNames[id], Status: u.Status, CreatedAt: u.CreatedAt,
	}, nil
}

func (f *fakeUsers) GetMany(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]identitysvc.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[uuid.UUID]identitysvc.User, len(ids))
	for _, id := range ids {
		if u, ok := f.users[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

func (f *fakeUsers) VerifyPassword(ctx context.Context, audience string, userID uuid.UUID, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.passwords[userID] != password {
		return errs.New(errs.Unauthorized)
	}
	return nil
}

// addUser 登记用户并返回 ID。
func (f *fakeUsers) addUser(email, password, status string) uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.Must(uuid.NewV7())
	norm := normalizeEmail(email)
	f.users[id] = identitysvc.User{
		ID:        id,
		Email:     norm,
		Status:    status,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	f.byEmail[norm] = id
	f.passwords[id] = password
	return id
}

// fakeStore 是 store 的假实现：admins 是管理员名单（值为加入时间）。
type fakeStore struct {
	mu      sync.Mutex
	admins  map[uuid.UUID]time.Time
	locks   int
	inserts []uuid.UUID
	grantBy []*uuid.UUID
}

func (f *fakeStore) IsAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.admins[userID]
	return ok, nil
}

func (f *fakeStore) InsertAdmin(ctx context.Context, userID uuid.UUID, grantedBy *uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.admins[userID]; ok {
		return false, nil
	}
	f.admins[userID] = time.Now()
	f.inserts = append(f.inserts, userID)
	f.grantBy = append(f.grantBy, grantedBy)
	return true, nil
}

func (f *fakeStore) DeleteAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.admins[userID]; !ok {
		return false, nil
	}
	delete(f.admins, userID)
	return true, nil
}

func (f *fakeStore) Admins(ctx context.Context) ([]repo.AdminRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repo.AdminRow, 0, len(f.admins))
	for id, at := range f.admins {
		out = append(out, repo.AdminRow{UserID: id, CreatedAt: at})
	}
	return out, nil
}

func (f *fakeStore) LockAdminSet(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locks++
	return nil
}

// addAdmin 把用户放进管理员名单。
func (f *fakeStore) addAdmin(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admins[id] = time.Now()
}

type fakeTx struct{}

func (fakeTx) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type recorded struct {
	ev   auditsvc.Event
	p    authn.Principal
	hasP bool
}

// auditLog 记录审计写入及当时 ctx 里的主体。
type auditLog struct {
	events []recorded
}

func (a *auditLog) Write(ctx context.Context, e auditsvc.Event) error {
	p, ok := authn.From(ctx)
	a.events = append(a.events, recorded{ev: e, p: p, hasP: ok})
	return nil
}

// testNow 是可推进的固定时钟。
type testNow struct {
	t time.Time
}

func (n *testNow) Now() time.Time { return n.t }

func (n *testNow) Advance(d time.Duration) { n.t = n.t.Add(d) }

// testHarness 聚合单测常用句柄。
type testHarness struct {
	S     *Service
	Users *fakeUsers
	Store *fakeStore
	Audit *auditLog
	Redis *miniredis.Miniredis
	Clock *testNow
	RDB   *redis.Client
}

// testCtx 带固定 IP/UA 的请求 ctx。
func testCtx(ip string) context.Context {
	return reqmeta.With(context.Background(), reqmeta.Meta{
		IP:        ip,
		UserAgent: "test-ua",
	})
}

// newTestService 构造带 miniredis 的 auth service 与假依赖。
func newTestService(t *testing.T) *testHarness {
	t.Helper()
	mr, rdb := testkit.Redis(t)
	keys, err := crypto.NewKeys(testMasterB64)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	pub, err := jwtx.NewIssuer(jwtx.Public, testJWTPublicKey, nil, 15*time.Minute)
	if err != nil {
		t.Fatalf("public issuer: %v", err)
	}
	adm, err := jwtx.NewIssuer(jwtx.Admin, testJWTAdminKey, nil, 15*time.Minute)
	if err != nil {
		t.Fatalf("admin issuer: %v", err)
	}
	users := &fakeUsers{
		users:     map[uuid.UUID]identitysvc.User{},
		byEmail:   map[string]uuid.UUID{},
		passwords: map[uuid.UUID]string{},

		displayNames: map[uuid.UUID]*string{},
	}
	st := &fakeStore{admins: map[uuid.UUID]time.Time{}}
	al := &auditLog{}
	// 与 miniredis 的真实时钟对齐：会话用 EXPIREAT，固定日期过几天就会落到过去
	clk := &testNow{t: time.Now().UTC().Truncate(time.Second)}
	cfg := Config{
		AccessTTL:        15 * time.Minute,
		PublicSessionTTL: 720 * time.Hour,
		AdminSessionTTL:  168 * time.Hour,
		AdminIdleTimeout: 2 * time.Hour,
	}
	svc := newService(st, fakeTx{}, rdb, keys, pub, adm, users, al, cfg)
	svc.now = clk.Now
	return &testHarness{
		S:     svc,
		Users: users,
		Store: st,
		Audit: al,
		Redis: mr,
		Clock: clk,
		RDB:   rdb,
	}
}

func loginFailedEvents(al *auditLog) []recorded {
	var out []recorded
	for _, r := range al.events {
		if r.ev.Action == loginFailedAction {
			out = append(out, r)
		}
	}
	return out
}

func loginSuccessEvents(al *auditLog) []recorded {
	var out []recorded
	for _, r := range al.events {
		if r.ev.Action == "user.login" {
			out = append(out, r)
		}
	}
	return out
}

func hasAMR(amr []string, want string) bool {
	for _, a := range amr {
		if a == want {
			return true
		}
	}
	return false
}

func userSessCount(ctx context.Context, h *testHarness, uid uuid.UUID) int {
	members, err := h.S.sessions.rdb.SMembers(ctx, userSessKey(uid)).Result()
	if err != nil {
		return -1
	}
	return len(members)
}
