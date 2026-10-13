package node

import (
	"slices"
	"testing"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
)

// 换凭据的行为用例，与协议无关（假内核），按「同一用户 ID、凭据变了」的各种名单形状写：
// 增量只给新记录、增量删旧加新、全量换凭据、空凭据。约束只有一条——凭据作废就要立刻
// 作废：旧凭据不在内核里、用它建立的连接被断、别人的连接不受牵连，内核操作先删后加。
// 真内核上的同一组断言见 users_rotation_kernel_test.go。

// credKernel 在 fakeKernel 上加两样：按顺序记下的名单操作，以及按用户 ID 登记的
// 已建立连接。真内核各适配器的 DelUsers 按被删条目的用户 ID 断开已有连接
// （sessions.revoke），这里照此建模。
type credKernel struct {
	*fakeKernel
	ops   []string
	conns []*credConn
}

type credConn struct {
	id     int64
	uuid   string
	closed bool
}

func newCredKernel() *credKernel { return &credKernel{fakeKernel: newFakeKernel()} }

func (k *credKernel) AddUsers(tag string, users []core.User) error {
	for _, u := range users {
		k.ops = append(k.ops, "add:"+u.UUID)
	}
	return k.fakeKernel.AddUsers(tag, users)
}

func (k *credKernel) UpsertUsers(tag string, users []core.User) error {
	for _, u := range users {
		k.ops = append(k.ops, "upsert:"+u.UUID)
	}
	return k.fakeKernel.UpsertUsers(tag, users)
}

func (k *credKernel) DelUsers(tag string, uuids []string) error {
	k.mu.Lock()
	var revoked []int64
	for _, id := range uuids {
		k.ops = append(k.ops, "del:"+id)
		if in := k.inbounds[tag]; in != nil {
			if u, ok := in.users[id]; ok {
				revoked = append(revoked, u.ID)
			}
		}
	}
	k.mu.Unlock()
	if err := k.fakeKernel.DelUsers(tag, uuids); err != nil {
		return err
	}
	for _, c := range k.conns {
		if slices.Contains(revoked, c.id) {
			c.closed = true
		}
	}
	return nil
}

// connect 用某个凭据建一条连接；凭据不在内核里就连不上。
func (k *credKernel) connect(uuid string) (*credConn, bool) {
	k.mu.Lock()
	var u core.User
	ok := false
	if in := k.inbounds["n1"]; in != nil {
		u, ok = in.users[uuid]
	}
	k.mu.Unlock()
	if !ok {
		return nil, false
	}
	c := &credConn{id: u.ID, uuid: uuid}
	k.conns = append(k.conns, c)
	return c, true
}

func (k *credKernel) opIndex(op string) int { return slices.Index(k.ops, op) }

const (
	rotAliceID  = int64(7)
	rotAliceOld = "alice-old"
	rotAliceNew = "alice-new"
	rotBobID    = int64(8)
	rotBob      = "bob"
)

// rotationNode 装好节点 n1 与 alice（旧凭据）、bob 两人，名单版本 v1，并各建一条连接。
func rotationNode(t *testing.T) (*testServer, *credKernel, *credConn, *credConn) {
	t.Helper()
	kernel := newCredKernel()
	ts := startedNode(t, kernel)
	ts.handleEventUsers("n1", "v1", protoUser(rotAliceID, rotAliceOld), protoUser(rotBobID, rotBob))
	alice, ok := kernel.connect(rotAliceOld)
	if !ok {
		t.Fatal("alice 装好后连不上")
	}
	bob, ok := kernel.connect(rotBob)
	if !ok {
		t.Fatal("bob 装好后连不上")
	}
	kernel.ops = nil
	return ts, kernel, alice, bob
}

// 同一用户 ID 换了凭据：不论面板给的是哪种形状，结果都一样。
func TestCredentialRotationRevokesOldCredential(t *testing.T) {
	shapes := []struct {
		name  string
		apply func(ts *testServer)
	}{
		{"增量只给新记录", func(ts *testServer) {
			ts.usersDelta("n1", "v1", "v2", []*nodev1.User{protoUser(rotAliceID, rotAliceNew)})
		}},
		{"增量删旧加新", func(ts *testServer) {
			ts.usersDelta("n1", "v1", "v2", []*nodev1.User{protoUser(rotAliceID, rotAliceNew)}, rotAliceID)
		}},
		{"全量换凭据", func(ts *testServer) {
			ts.handleEventUsers("n1", "v2", protoUser(rotAliceID, rotAliceNew), protoUser(rotBobID, rotBob))
		}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			ts, kernel, alice, bob := rotationNode(t)
			shape.apply(ts)
			n := ts.node(t, "n1")

			if n.userVersion != "v2" {
				t.Fatalf("名单没有被应用（userVersion=%q）", n.userVersion)
			}
			if got, want := kernel.uuids("n1"), []string{rotAliceNew, rotBob}; !slices.Equal(got, want) {
				t.Fatalf("内核名单 = %v，期望 %v（旧凭据必须删掉）", got, want)
			}
			if !alice.closed {
				t.Fatal("用旧凭据建立的连接没有被断开")
			}
			if bob.closed {
				t.Fatal("别的用户的连接被牵连断开")
			}
			if _, ok := kernel.connect(rotAliceOld); ok {
				t.Fatal("旧凭据仍能连上")
			}
			if _, ok := kernel.connect(rotAliceNew); !ok {
				t.Fatal("新凭据连不上")
			}
			// 先删后加：内核按用户 ID 断线，反过来会把刚用新凭据连上的会话一并踢掉。
			del, add := kernel.opIndex("del:"+rotAliceOld), kernel.opIndex("add:"+rotAliceNew)
			if del < 0 || add < 0 || del > add {
				t.Fatalf("内核操作顺序 %v，期望先删旧凭据再加新凭据", kernel.ops)
			}
			assertMirror(t, n, map[int64]string{rotAliceID: rotAliceNew, rotBobID: rotBob})
		})
	}
}

// 凭据没变、只改限速或设备数：原地更新，不删、不断线。
func TestCredentialUnchangedLimitUpdateKeepsConnection(t *testing.T) {
	ts, kernel, alice, _ := rotationNode(t)
	ts.usersDelta("n1", "v1", "v2", []*nodev1.User{{Uid: rotAliceID, Uuid: rotAliceOld, SpeedLimitKbps: 1000, DeviceLimit: 2}})
	n := ts.node(t, "n1")
	if want := []string{"upsert:" + rotAliceOld}; !slices.Equal(kernel.ops, want) {
		t.Fatalf("内核操作 %v，期望只有 %v", kernel.ops, want)
	}
	if alice.closed {
		t.Fatal("只改限速却断开了连接")
	}
	if got := mirrorUser(n, rotAliceID); got.SpeedLimit != 1000 || got.DeviceLimit != 2 {
		t.Fatalf("镜像没有跟上新限速：%+v", got)
	}
}

// 增量里给了一条空凭据的记录：这个 ID 已没有可用凭据，旧的照样作废（fail closed）。
func TestCredentialEmptyInDeltaRevokesOld(t *testing.T) {
	ts, kernel, alice, bob := rotationNode(t)
	ts.usersDelta("n1", "v1", "v2", []*nodev1.User{protoUser(rotAliceID, "")})
	n := ts.node(t, "n1")
	if got, want := kernel.uuids("n1"), []string{rotBob}; !slices.Equal(got, want) {
		t.Fatalf("内核名单 = %v，期望 %v", got, want)
	}
	if !alice.closed || bob.closed {
		t.Fatalf("断线不对：alice closed=%v bob closed=%v", alice.closed, bob.closed)
	}
	assertMirror(t, n, map[int64]string{rotBobID: rotBob})
}

// assertMirror 核对节点端镜像：每个用户 ID 恰好一份凭据。
func assertMirror(t *testing.T, n *Node, want map[int64]string) {
	t.Helper()
	got := make(map[int64]string, len(n.known))
	for _, u := range n.known {
		if prev, dup := got[u.ID]; dup {
			t.Fatalf("镜像里用户 %d 同时挂着 %q 和 %q", u.ID, prev, u.UUID)
		}
		got[u.ID] = u.UUID
	}
	if len(got) != len(want) {
		t.Fatalf("镜像 = %v，期望 %v", got, want)
	}
	for id, uuid := range want {
		if got[id] != uuid {
			t.Fatalf("镜像 = %v，期望 %v", got, want)
		}
	}
}

func mirrorUser(n *Node, id int64) core.User {
	for _, u := range n.known {
		if u.ID == id {
			return u
		}
	}
	return core.User{}
}

// 同一用户 ID 在一份名单里出现两次（面板有唯一约束，正常不会发）：以最后一条为准，
// 内核里装的与镜像里记的必须是同一份——多装进内核、镜像里没有的那条凭据，以后
// 任何同步都删不掉。增量与全量两种形状都要守住。
func TestDuplicateIDKeepsOnlyLastRecord(t *testing.T) {
	const first, last = "alice-first", "alice-last"
	shapes := []struct {
		name  string
		apply func(ts *testServer)
	}{
		{"增量 upserted 同 ID 两条", func(ts *testServer) {
			ts.usersDelta("n1", "v1", "v2", []*nodev1.User{protoUser(rotAliceID, first), protoUser(rotAliceID, last)})
		}},
		{"全量同 ID 两条", func(ts *testServer) {
			ts.handleEventUsers("n1", "v2", protoUser(rotAliceID, first), protoUser(rotBobID, rotBob), protoUser(rotAliceID, last))
		}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			ts, kernel, _, _ := rotationNode(t)
			shape.apply(ts)
			n := ts.node(t, "n1")
			if got, want := kernel.uuids("n1"), []string{last, rotBob}; !slices.Equal(got, want) {
				t.Fatalf("内核名单 = %v，期望 %v（同 ID 只留最后一条）", got, want)
			}
			assertMirror(t, n, map[int64]string{rotAliceID: last, rotBobID: rotBob})
		})
	}
}

// 装入站时随入站一起交给内核的名单（install_users.go）同样去重：交给内核的与
// mirrorOf 记下的是同一份。
func TestNormalizeUsersDropsEmptyAndKeepsLastPerID(t *testing.T) {
	got := normalizeUsers([]core.User{
		{ID: 1, UUID: "a-first"}, {ID: 2, UUID: ""}, {ID: 3, UUID: "c"}, {ID: 1, UUID: "a-last"},
	})
	want := []core.User{{ID: 1, UUID: "a-last"}, {ID: 3, UUID: "c"}}
	if !slices.Equal(got, want) {
		t.Fatalf("normalizeUsers = %+v，期望 %+v", got, want)
	}
}
