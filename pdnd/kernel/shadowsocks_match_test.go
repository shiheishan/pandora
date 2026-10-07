package kernel

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// 零分配 HKDF 必须与标准库 hkdf 参考实现逐字节一致：覆盖全部密钥长度、
// 同一暂存区连续复用、以及换 salt 后的状态重置。
func TestSSScratchSubkeyMatchesHKDF(t *testing.T) {
	sc := getSSScratch()
	defer putSSScratch(sc)
	for _, keyLen := range []int{16, 24, 32} {
		for round := 0; round < 64; round++ {
			master := make([]byte, keyLen)
			salt := make([]byte, keyLen)
			_, _ = rand.Read(master)
			_, _ = rand.Read(salt)
			want, err := deriveSSSubkey(master, salt, keyLen)
			if err != nil {
				t.Fatal(err)
			}
			sc.setSalt(salt)
			for repeat := 0; repeat < 2; repeat++ {
				if got := sc.subkey(master, keyLen); !bytes.Equal(got, want) {
					t.Fatalf("keyLen=%d round=%d repeat=%d: got %x want %x", keyLen, round, repeat, got, want)
				}
			}
		}
	}
}

func ssTestAddr(ip string) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000}
}

func ssHandshakeAs(t *testing.T, adapter *shadowsocksAdapter, password string, remote net.Addr) (core.User, error) {
	t.Helper()
	conn := &ssBenchConn{}
	conn.reset(buildSSClientRequest(t, adapter.method, password, []byte("x")), remote)
	user, _, _, err := adapter.readRequest(conn)
	return user, err
}

// 来源 IP 提示只排顺序：同一 IP 上换成别的用户、或发来解不开的字节，结果必须
// 与没有提示时一样——按真实密钥认证，或失败。
func TestShadowsocksSourceHintNeverSkipsAEAD(t *testing.T) {
	adapter := newSSBenchAdapter(t, "aes-128-gcm", 50)
	remote := ssTestAddr("198.51.100.9")
	user, err := ssHandshakeAs(t, adapter, ssBenchPassword(7), remote)
	if err != nil || user.ID != 8 {
		t.Fatalf("首次握手 user=%d err=%v", user.ID, err)
	}
	if hint := adapter.hints.lookup(ssSourceKey(remote)); hint[0] == nil || hint[0].ID != 8 {
		t.Fatalf("认证通过后应记下来源提示，got %+v", hint)
	}
	// 同一 IP 换用户：提示里的用户 8 解不开，必须落到真实用户 31。
	user, err = ssHandshakeAs(t, adapter, ssBenchPassword(30), remote)
	if err != nil || user.ID != 31 {
		t.Fatalf("同 IP 换用户 user=%d err=%v", user.ID, err)
	}
	// 不在用户表里的口令：提示命中也不能放行。
	if _, err := ssHandshakeAs(t, adapter, "not-a-user", remote); err == nil {
		t.Fatal("未知口令在有来源提示的 IP 上被放行")
	}
	// 篡改认证标签：提示里的用户密钥正确，但 AEAD 校验必须失败。
	wire := buildSSClientRequest(t, adapter.method, ssBenchPassword(7), []byte("x"))
	wire[adapter.method.SaltLen+2] ^= 0x01
	conn := &ssBenchConn{}
	conn.reset(wire, remote)
	if _, _, _, err := adapter.readRequest(conn); err == nil {
		t.Fatal("篡改过的长度块在有来源提示时被放行")
	}
}

// 删除或替换用户后，提示里残留的旧记录不能再认证成功。
func TestShadowsocksSourceHintRetiredUsers(t *testing.T) {
	adapter := newSSBenchAdapter(t, "aes-128-gcm", 10)
	remote := ssTestAddr("203.0.113.5")
	if user, err := ssHandshakeAs(t, adapter, ssBenchPassword(3), remote); err != nil || user.ID != 4 {
		t.Fatalf("首次握手 user=%d err=%v", user.ID, err)
	}
	if err := adapter.UpsertUsers([]core.User{{ID: 400, UUID: ssBenchPassword(3), DeviceLimit: 2}}); err != nil {
		t.Fatal(err)
	}
	user, err := ssHandshakeAs(t, adapter, ssBenchPassword(3), remote)
	if err != nil || user.ID != 400 || user.DeviceLimit != 2 {
		t.Fatalf("替换后应认证为新记录，got %+v err=%v", user, err)
	}
	if err := adapter.DelUsers([]string{ssBenchPassword(3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ssHandshakeAs(t, adapter, ssBenchPassword(3), remote); err == nil {
		t.Fatal("已删除用户借来源提示通过了认证")
	}
	if user, err := ssHandshakeAs(t, adapter, ssBenchPassword(4), remote); err != nil || user.ID != 5 {
		t.Fatalf("其余用户不受影响 user=%d err=%v", user.ID, err)
	}
}

// 防重放不变：同一首包在命中来源提示的情况下第二次也必须被拦下。
func TestShadowsocksReplayRejectedWithSourceHint(t *testing.T) {
	adapter := newSSBenchAdapter(t, "chacha20-ietf-poly1305", 20)
	remote := ssTestAddr("192.0.2.77")
	wire := buildSSClientRequest(t, adapter.method, ssBenchPassword(5), []byte("x"))
	conn := &ssBenchConn{}
	conn.reset(wire, remote)
	if _, _, _, err := adapter.readRequest(conn); err != nil {
		t.Fatal(err)
	}
	conn.reset(wire, remote)
	if _, _, _, err := adapter.readRequest(conn); err == nil {
		t.Fatal("重放首包被放行")
	}
}

// 首块之后的剩余明文要原样留给 stream 读出。
func TestShadowsocksFirstChunkPendingPayload(t *testing.T) {
	adapter := newSSBenchAdapter(t, "aes-256-gcm", 3)
	conn := &ssBenchConn{}
	conn.reset(buildSSClientRequest(t, adapter.method, ssBenchPassword(1), []byte("hello-pending")), ssTestAddr("192.0.2.1"))
	_, stream, destination, err := adapter.readRequest(conn)
	if err != nil {
		t.Fatal(err)
	}
	if destination.Port != 443 || destination.IP != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("目标解析错误：%+v", destination)
	}
	got := make([]byte, 64)
	n, err := stream.Read(got)
	if err != nil || string(got[:n]) != "hello-pending" {
		t.Fatalf("pending=%q err=%v", got[:n], err)
	}
}

// UDP 与 TCP 共用匹配器：提示命中、换用户、解不开都与 TCP 同规则。
func TestShadowsocksUDPDecodeUsesSourceHint(t *testing.T) {
	adapter := newSSBenchAdapter(t, "aes-128-gcm", 30)
	client := &net.UDPAddr{IP: net.ParseIP("198.51.100.20"), Port: 5353}
	encode := func(password string) []byte {
		master := deriveSSMasterKey(password, adapter.method.KeyLen)
		salt := make([]byte, adapter.method.SaltLen)
		_, _ = rand.Read(salt)
		subkey, _ := deriveSSSubkey(master, salt, adapter.method.KeyLen)
		aead, _ := adapter.method.NewAEAD(subkey)
		return append(salt, aead.Seal(nil, makeSSNonce(0), []byte{1, 192, 0, 2, 9, 0, 53, 'q'}, nil)...)
	}
	for _, tc := range []struct {
		password string
		id       int64
	}{{ssBenchPassword(12), 13}, {ssBenchPassword(12), 13}, {ssBenchPassword(2), 3}} {
		user, destination, payload, err := adapter.decodeUDPPacket(encode(tc.password), client)
		if err != nil || user.ID != tc.id || destination.Port != 53 || string(payload) != "q" {
			t.Fatalf("UDP 解包 user=%d dst=%+v payload=%q err=%v", user.ID, destination, payload, err)
		}
	}
	if _, _, _, err := adapter.decodeUDPPacket(encode("stranger"), client); err == nil {
		t.Fatal("未知口令的 UDP 包被放行")
	}
}

// 提示表有界：大量不同来源不会让它无限增长。
func TestShadowsocksSourceHintsBounded(t *testing.T) {
	var hints ssSourceHints
	user := &ssUser{ID: 1}
	for i := 0; i < ssHintShards*ssHintPerGen*4; i++ {
		hints.remember(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), user)
	}
	for i := range hints.shards {
		shard := &hints.shards[i]
		if len(shard.current) > ssHintPerGen || len(shard.prev) > ssHintPerGen {
			t.Fatalf("分片 %d 超限：current=%d prev=%d", i, len(shard.current), len(shard.prev))
		}
	}
}

// IPv6 来源按 /64 聚合；IPv4-mapped 地址与 IPv4 同键。
func TestShadowsocksSourceKey(t *testing.T) {
	for _, tc := range []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.ParseIP("2001:db8:1:2:aaaa:bbbb:cccc:dddd")}, "2001:db8:1:2::"},
		{&net.TCPAddr{IP: net.ParseIP("::ffff:192.0.2.3")}, "192.0.2.3"},
		{&net.UDPAddr{IP: net.ParseIP("192.0.2.4")}, "192.0.2.4"},
		{ssStringAddr("192.0.2.5:443"), "192.0.2.5"},
	} {
		if got := ssSourceKey(tc.addr); got.String() != tc.want {
			t.Fatalf("%v → %v，期望 %s", tc.addr, got, tc.want)
		}
	}
	if ssSourceKey(nil).IsValid() || ssSourceKey(ssStringAddr("pipe")).IsValid() {
		t.Fatal("拿不到 IP 时不应产生提示键")
	}
}

type ssStringAddr string

func (a ssStringAddr) Network() string { return "tcp" }
func (a ssStringAddr) String() string  { return string(a) }

// 来源稳定时握手的分配次数与用户数无关；冷启动时每个候选只剩 AEAD 构造的分配。
func TestShadowsocksHandshakeAllocations(t *testing.T) {
	for _, users := range []int{10, 1000} {
		adapter := newSSBenchAdapter(t, "chacha20-ietf-poly1305", users)
		wires := make([][]byte, 0, 64)
		for i := 0; i < 64; i++ {
			wires = append(wires, buildSSClientRequest(t, adapter.method, ssBenchPassword(users-1), []byte("x")))
		}
		remote := ssTestAddr("198.51.100.30")
		conn := &ssBenchConn{}
		next := 0
		allocs := testing.AllocsPerRun(len(wires)-1, func() {
			conn.reset(wires[next], remote)
			next++
			if _, _, _, err := adapter.readRequest(conn); err != nil {
				panic(fmt.Sprint(err))
			}
		})
		if allocs > 40 {
			t.Fatalf("users=%d 来源稳定时每次握手 %.0f 次分配，应与用户数无关", users, allocs)
		}
	}
}

// 扫表命中位置靠后、且距上次排序超过间隔时，快照按最近认证时间重排，
// 活跃用户换了 IP 也能很快被试到。
func TestShadowsocksSnapshotReordersByRecency(t *testing.T) {
	adapter := newSSBenchAdapter(t, "aes-128-gcm", 300)
	snapshot := adapter.loadUsers()
	last := snapshot.list[len(snapshot.list)-1]
	// 让快照「看起来」排过序很久了，下一次靠后命中就会触发重排。
	stale := &ssUserSnapshot{list: snapshot.list, byID: snapshot.byID, sortedAt: snapshot.sortedAt - int64(2*ssReorderInterval)}
	adapter.snapshot.Store(stale)
	var password string
	for i := 0; i < 300; i++ {
		if int64(i+1) == last.ID {
			password = ssBenchPassword(i)
		}
	}
	if user, err := ssHandshakeAs(t, adapter, password, ssTestAddr("192.0.2.200")); err != nil || user.ID != last.ID {
		t.Fatalf("握手 user=%d err=%v", user.ID, err)
	}
	if front := adapter.loadUsers().list[0]; front != last {
		t.Fatalf("重排后最近认证的用户应在最前，got ID %d", front.ID)
	}
	// 用户集合更新后的快照同样保持按最近认证排序。
	if err := adapter.AddUsers([]core.User{{ID: 9999, UUID: "fresh-user-password"}}); err != nil {
		t.Fatal(err)
	}
	if front := adapter.loadUsers().list[0]; front != last {
		t.Fatalf("发布新快照后最近认证的用户应仍在最前，got ID %d", front.ID)
	}
}

// BenchmarkShadowsocksHandshakeActiveSubset：5000 用户里 500 个活跃（计时前
// 都认证过），每次握手换新 IP。快照按最近认证排序后只需在活跃子集里扫。
func BenchmarkShadowsocksHandshakeActiveSubset(b *testing.B) {
	const users, active = 5000, 500
	adapter := newSSBenchAdapter(b, "aes-128-gcm", users)
	adapter.mu.Lock()
	for _, user := range adapter.users {
		if user.ID%(users/active) == 0 {
			user.lastAuth.Store(time.Now().UnixNano())
		}
	}
	adapter.publishUsersLocked()
	adapter.mu.Unlock()
	wires := make([][]byte, b.N)
	remotes := make([]net.Addr, b.N)
	for i := range wires {
		user := (i*7919)%active*(users/active) + users/active - 1
		wires[i] = buildSSClientRequest(b, adapter.method, ssBenchPassword(user), []byte("x"))
		remotes[i] = &net.TCPAddr{IP: net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)), Port: 40000}
	}
	conn := &ssBenchConn{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn.reset(wires[i], remotes[i])
		if _, _, _, err := adapter.readRequest(conn); err != nil {
			b.Fatal(err)
		}
	}
}
