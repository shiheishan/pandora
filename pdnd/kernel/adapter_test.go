package kernel

import (
	"context"
	"testing"

	"github.com/aegispanel/nodeagent/core"
)

func TestAdapterRegistryIsStrictAndNormalized(t *testing.T) {
	r := NewAdapterRegistry()
	if err := r.Register(" REALITY ", func(InboundSpec) (Adapter, error) {
		return fakeAdapter{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("reality", func(InboundSpec) (Adapter, error) {
		return fakeAdapter{}, nil
	}); err == nil {
		t.Fatal("duplicate protocol registration unexpectedly succeeded")
	}
	a, err := r.New(InboundSpec{Config: core.InboundConfig{Protocol: "reality"}})
	if err != nil || a.Protocol() != "reality" {
		t.Fatalf("normalized lookup = %v, %v", a, err)
	}
	if _, err := r.New(InboundSpec{Config: core.InboundConfig{Protocol: "xhttp"}}); err == nil {
		t.Fatal("unknown native protocol unexpectedly succeeded")
	}
}

type fakeAdapter struct{}

func (fakeAdapter) Protocol() string                                       { return "reality" }
func (fakeAdapter) Validate(InboundSpec) error                             { return nil }
func (fakeAdapter) Start(context.Context, InboundSpec, AdapterHooks) error { return nil }
func (fakeAdapter) Close() error                                           { return nil }
func (fakeAdapter) AddUsers([]core.User) error                             { return nil }
func (fakeAdapter) UpsertUsers([]core.User) error                          { return nil }
func (fakeAdapter) DelUsers([]string) error                                { return nil }
func (fakeAdapter) SnapshotTraffic() ([]core.UserTraffic, error)           { return nil, nil }
func (fakeAdapter) OnlineIPs() map[int64][]string                          { return nil }

func TestNativeUserBatchValidationIsAtomic(t *testing.T) {
	users := []core.User{{ID: 1, UUID: "valid"}, {ID: 2}}

	proxy := &proxyAdapter{protocol: "socks", users: make(map[string]proxyUser)}
	if err := proxy.AddUsers(users); err == nil {
		t.Fatal("proxy accepted an invalid user batch")
	}
	if len(proxy.users) != 0 {
		t.Fatalf("proxy partially published invalid batch: %v", proxy.users)
	}

	naive := &naiveAdapter{users: make(map[string]proxyUser)}
	if err := naive.AddUsers(users); err == nil {
		t.Fatal("naive accepted an invalid user batch")
	}
	if len(naive.users) != 0 {
		t.Fatalf("naive partially published invalid batch: %v", naive.users)
	}

	anytls := &anyTLSAdapter{users: make(map[string]int)}
	if err := anytls.AddUsers(users); err == nil {
		t.Fatal("anytls accepted an invalid user batch")
	}
	if len(anytls.users) != 0 || len(anytls.slots) != 0 {
		t.Fatalf("anytls partially published invalid batch: users=%v slots=%d", anytls.users, len(anytls.slots))
	}
}

func TestNativeProtocolUserBatchesAreAtomic(t *testing.T) {
	valid := "00000000-0000-4000-8000-000000000001"
	invalid := "not-a-uuid"
	users := []core.User{{ID: 1, UUID: valid}, {ID: 2, UUID: invalid}}

	vless := &vlessAdapter{users: make(map[string]core.User)}
	if err := vless.AddUsers(users); err == nil || len(vless.users) != 0 {
		t.Fatalf("vless partially published invalid batch: err=%v users=%v", err, vless.users)
	}

	vmess := &vmessAdapter{users: make(map[string]vmessUser)}
	if err := vmess.AddUsers(users); err == nil || len(vmess.users) != 0 {
		t.Fatalf("vmess partially published invalid batch: err=%v users=%v", err, vmess.users)
	}

	trojan := &trojanAdapter{users: make(map[string]trojanUser)}
	if err := trojan.AddUsers([]core.User{{ID: 1, UUID: "password"}, {ID: 2}}); err == nil || len(trojan.users) != 0 {
		t.Fatalf("trojan partially published invalid batch: err=%v users=%v", err, trojan.users)
	}

	ss := &shadowsocksAdapter{method: ssMethodSpec{KeyLen: 16}, users: make(map[string]ssUser)}
	if err := ss.AddUsers([]core.User{{ID: 1, UUID: "password"}, {ID: 2}}); err == nil || len(ss.users) != 0 {
		t.Fatalf("shadowsocks partially published invalid batch: err=%v users=%v", err, ss.users)
	}

	ss2022 := &ss2022Adapter{hasUser: true, user: core.User{ID: 7, UUID: valid}}
	if err := ss2022.AddUsers([]core.User{{ID: 1, UUID: valid}, {ID: 2, UUID: valid}}); err == nil || !ss2022.hasUser || ss2022.user.ID != 7 {
		t.Fatalf("shadowsocks 2022 partially published invalid batch: err=%v user=%+v has=%v", err, ss2022.user, ss2022.hasUser)
	}

	tuic := &tuicAdapter{users: make(map[string]int)}
	if err := tuic.AddUsers(users); err == nil || len(tuic.users) != 0 || len(tuic.slots) != 0 {
		t.Fatalf("tuic partially published invalid batch: err=%v users=%v slots=%d", err, tuic.users, len(tuic.slots))
	}

	juicity := &juicityAdapter{users: make(map[string]juicityUser)}
	if err := juicity.AddUsers(users); err == nil || len(juicity.users) != 0 {
		t.Fatalf("juicity partially published invalid batch: err=%v users=%v", err, juicity.users)
	}
}
