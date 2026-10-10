package node

import (
	"context"
	"slices"
	"testing"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// 换凭据的行为用例，与协议无关（假内核），按「同一用户 ID、凭据变了」的各种名单形状写：
// 增量只给新记录、增量删旧加新、全量换凭据、空凭据。约束只有一条——凭据作废就要立刻
// 作废：旧凭据不在内核里、用它建立的连接被断、别人的连接不受牵连，内核操作先删后加。
// 真内核上的同一组断言见 users_rotation_kernel_test.go。

// credKernel 在 userTableCore 上加两样：按顺序记下的名单操作，以及按用户 ID 登记的
// 已建立连接。真内核各适配器的 DelUsers 按被删条目的用户 ID 断开已有连接
// （sessions.revoke），这里照此建模。
type credKernel struct {
	*userTableCore
	ops   []string
	conns []*credConn
}

type credConn struct {
	id     int64
	uuid   string
	closed bool
}

func newCredKernel() *credKernel { return &credKernel{userTableCore: newUserTableCore()} }

func (k *credKernel) AddUsers(tag string, users []core.User) error {
	for _, u := range users {
		k.ops = append(k.ops, "add:"+u.UUID)
	}
	return k.userTableCore.AddUsers(tag, users)
}

func (k *credKernel) UpsertUsers(tag string, users []core.User) error {
	for _, u := range users {
		k.ops = append(k.ops, "upsert:"+u.UUID)
	}
	return k.userTableCore.UpsertUsers(tag, users)
}

func (k *credKernel) DelUsers(tag string, uuids []string) error {
	k.mu.Lock()
	var revoked []int64
	for _, id := range uuids {
		k.ops = append(k.ops, "del:"+id)
		if u, ok := k.users[id]; ok {
			revoked = append(revoked, u.ID)
		}
	}
	k.mu.Unlock()
	if err := k.userTableCore.DelUsers(tag, uuids); err != nil {
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
	u, ok := k.users[uuid]
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

// rotationNode 装好 alice（旧凭据）与 bob 两人，增量基准为 v1，并各建一条连接。
func rotationNode(t *testing.T) (*Node, *credKernel, *credConn, *credConn) {
	t.Helper()
	kernel := newCredKernel()
	client := panel.New(panel.Options{BaseURL: "http://127.0.0.1:1", NodeID: "n1", NodeType: "vless", Token: "token"})
	n := New(client, kernel, testLogger())
	n.started = true
	if err := n.applyUsers([]core.User{{ID: rotAliceID, UUID: rotAliceOld}, {ID: rotBobID, UUID: rotBob}}); err != nil {
		t.Fatal(err)
	}
	n.userVersion = panel.UsersVersionKey(`"v1"`)
	alice, ok := kernel.connect(rotAliceOld)
	if !ok {
		t.Fatal("alice 装好后连不上")
	}
	bob, ok := kernel.connect(rotBob)
	if !ok {
		t.Fatal("bob 装好后连不上")
	}
	kernel.ops = nil
	return n, kernel, alice, bob
}

// 同一用户 ID 换了凭据：不论面板给的是哪种形状，结果都一样。
func TestCredentialRotationRevokesOldCredential(t *testing.T) {
	shapes := []struct {
		name  string
		apply func(n *Node)
	}{
		{"增量只给新记录", func(n *Node) {
			n.applyStreamEvent(context.Background(), panel.StreamEvent{
				Type: panel.EventSyncUserDelta, FromVersion: `"v1"`, ToVersion: `"v2"`,
				Added: []core.User{{ID: rotAliceID, UUID: rotAliceNew}},
			})
		}},
		{"增量删旧加新", func(n *Node) {
			n.applyStreamEvent(context.Background(), panel.StreamEvent{
				Type: panel.EventSyncUserDelta, FromVersion: `"v1"`, ToVersion: `"v2"`,
				Removed: []int64{rotAliceID},
				Added:   []core.User{{ID: rotAliceID, UUID: rotAliceNew}},
			})
		}},
		{"全量换凭据", func(n *Node) {
			n.applyStreamEvent(context.Background(), panel.StreamEvent{
				Type: panel.EventSyncUsers, Version: `"v2"`,
				Users: []core.User{{ID: rotAliceID, UUID: rotAliceNew}, {ID: rotBobID, UUID: rotBob}},
			})
		}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			n, kernel, alice, bob := rotationNode(t)
			shape.apply(n)

			if n.userVersion != panel.UsersVersionKey(`"v2"`) {
				t.Fatalf("名单没有被应用（userVersion=%q）", n.userVersion)
			}
			if got, want := kernel.uuids(), []string{rotAliceNew, rotBob}; !slices.Equal(got, want) {
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
	n, kernel, alice, _ := rotationNode(t)
	n.applyStreamEvent(context.Background(), panel.StreamEvent{
		Type: panel.EventSyncUserDelta, FromVersion: `"v1"`, ToVersion: `"v2"`,
		Added: []core.User{{ID: rotAliceID, UUID: rotAliceOld, SpeedLimit: 1000, DeviceLimit: 2}},
	})
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
	n, kernel, alice, bob := rotationNode(t)
	n.applyStreamEvent(context.Background(), panel.StreamEvent{
		Type: panel.EventSyncUserDelta, FromVersion: `"v1"`, ToVersion: `"v2"`,
		Added: []core.User{{ID: rotAliceID}},
	})
	if got, want := kernel.uuids(), []string{rotBob}; !slices.Equal(got, want) {
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
