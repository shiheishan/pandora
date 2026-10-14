package service

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"

	"github.com/google/uuid"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// 订阅分发测试用的固定值（虚构）。
const (
	testPrefix  = "a1b2c3d4e5f6"
	testBaseURL = "https://panel.example.test"
)

// fakeDelivery 实现 DeliverySettings：固定前缀，上限可改。
type fakeDelivery struct {
	mu     sync.Mutex
	prefix string
	limit  int
	calls  int
}

func (f *fakeDelivery) SubscriptionDelivery(context.Context) (settingssvc.SubscriptionDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return settingssvc.SubscriptionDelivery{PathPrefix: f.prefix, FetchLimitPerHour: f.limit}, nil
}

func (f *fakeDelivery) setLimit(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limit = n
}

// fakeOwners 实现 Owners：没登记的用户都是 active、默认组。
type fakeOwners struct {
	mu     sync.Mutex
	status map[uuid.UUID]string
	groups map[uuid.UUID]*uuid.UUID
}

func (f *fakeOwners) Get(_ context.Context, id uuid.UUID) (identitysvc.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.status[id]
	if !ok {
		st = "active"
	}
	return identitysvc.User{ID: id, Email: "owner@example.test", Status: st, UserGroupID: f.groups[id]}, nil
}

func (f *fakeOwners) set(id uuid.UUID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[id] = status
}

// setGroup 把用户放进一个用户组（nil 为默认组）。
func (f *fakeOwners) setGroup(id uuid.UUID, group *uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups[id] = group
}

// fakeNodes 实现 NodeSource：返回预设线路，记下每次的查询条件。
type fakeNodes struct {
	mu      sync.Mutex
	nodes   []render.Node
	err     error
	queries []NodeQuery
}

func (f *fakeNodes) DeliverableNodes(_ context.Context, q NodeQuery) ([]render.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.nodes, nil
}

// deliveryEnv 在 newEnv 之上装配订阅分发：固定前缀与上限的设置、可改状态的账号、miniredis 限流、替身线路来源。
type deliveryEnv struct {
	*env
	settings *fakeDelivery
	owners   *fakeOwners
	nodes    *fakeNodes
	sealer   *crypto.Sealer
}

// newDeliveryEnv 构造分发测试环境；withNodes 为假时不装配线路来源（P5 之前的真实状态）。
func newDeliveryEnv(t *testing.T, withNodes bool) *deliveryEnv {
	t.Helper()
	e := newEnv(t)
	// 与 newEnv 同一把主密钥：解得开它签发的令牌
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x02
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := keys.Sealer()
	if err != nil {
		t.Fatal(err)
	}
	_, rdb := testkit.Redis(t)
	de := &deliveryEnv{
		env:      e,
		settings: &fakeDelivery{prefix: testPrefix, limit: 60},
		owners:   &fakeOwners{status: map[uuid.UUID]string{}, groups: map[uuid.UUID]*uuid.UUID{}},
		nodes: &fakeNodes{nodes: []render.Node{{
			Name: "香港 01", Type: "vless", Host: "hk.example.test", Port: 443,
			Config: map[string]any{}, TrafficRate: 1,
		}}},
		sealer: sealer,
	}
	e.svc.UseDelivery(Delivery{
		Settings:      de.settings,
		Owners:        de.owners,
		Hasher:        keys,
		Limiter:       ratelimit.New(rdb),
		PublicBaseURL: testBaseURL,
	})
	if withNodes {
		e.svc.UseNodeSource(de.nodes)
	}
	return de
}

// pullCtx 给 base 加上来源信息（IP、UA），拉取从 ctx 的 reqmeta 取它们。
func pullCtx(base context.Context, ip, ua string) context.Context {
	return reqmeta.With(base, reqmeta.Meta{IP: ip, UserAgent: ua})
}

// tokenOf 解开一份订阅当前有效凭据的令牌原文。
func (de *deliveryEnv) tokenOf(t *testing.T, ctx context.Context, subID uuid.UUID) string {
	t.Helper()
	var sealed []byte
	if err := de.row(ctx, `SELECT token_encrypted FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`,
		subID).Scan(&sealed); err != nil {
		t.Fatalf("读凭据: %v", err)
	}
	plain, err := de.sealer.Open(sealed, CredentialAAD(subID))
	if err != nil {
		t.Fatalf("解封令牌: %v", err)
	}
	return string(plain)
}

// fetchLogs 数拉取日志：subID 为 nil 时数未认证失败（不带订阅）的那些。
func (de *deliveryEnv) fetchLogs(t *testing.T, ctx context.Context, subID *uuid.UUID, result string) int {
	t.Helper()
	if subID == nil {
		return countRows(t, de.env, ctx, `SELECT count(*) FROM subscription_fetch_log WHERE subscription_id IS NULL AND result = ?`, result)
	}
	return countRows(t, de.env, ctx, `SELECT count(*) FROM subscription_fetch_log WHERE subscription_id = ? AND result = ?`, *subID, result)
}
