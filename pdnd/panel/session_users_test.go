package panel

import (
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"google.golang.org/protobuf/proto"
)

func user(uid int64) *nodev1.User {
	return &nodev1.User{Uid: uid, Uuid: "u"}
}

func TestUsersAssemblerTwoChunks(t *testing.T) {
	a := newUsersAssembler()
	f0, err := a.add(&nodev1.UsersFull{NodeId: "n1", Version: "v1", ChunkIndex: 0, ChunkTotal: 2, Users: []*nodev1.User{user(1)}})
	if err != nil || f0 != nil {
		t.Fatalf("块0: full=%v err=%v", f0, err)
	}
	f1, err := a.add(&nodev1.UsersFull{NodeId: "n1", Version: "v1", ChunkIndex: 1, ChunkTotal: 2, Users: []*nodev1.User{user(2)}})
	if err != nil {
		t.Fatal(err)
	}
	if f1 == nil || f1.NodeID != "n1" || f1.Version != "v1" || len(f1.Users) != 2 {
		t.Fatalf("整份不对: %+v", f1)
	}
	if f1.Users[0].GetUid() != 1 || f1.Users[1].GetUid() != 2 {
		t.Fatal("用户顺序应按块拼接")
	}
}

func TestUsersAssemblerEmptySingleChunk(t *testing.T) {
	a := newUsersAssembler()
	f, err := a.add(&nodev1.UsersFull{NodeId: "n1", Version: "v0", ChunkIndex: 0, ChunkTotal: 0})
	if err != nil {
		t.Fatal(err)
	}
	if f == nil || len(f.Users) != 0 {
		t.Fatalf("空名单: %+v", f)
	}
}

func TestUsersAssemblerMissingChunk(t *testing.T) {
	a := newUsersAssembler()
	_, _ = a.add(&nodev1.UsersFull{NodeId: "n1", Version: "v1", ChunkIndex: 0, ChunkTotal: 3, Users: []*nodev1.User{user(1)}})
	_, err := a.add(&nodev1.UsersFull{NodeId: "n1", Version: "v1", ChunkIndex: 2, ChunkTotal: 3, Users: []*nodev1.User{user(3)}})
	if err == nil {
		t.Fatal("缺块应报错")
	}
	f, err := a.add(&nodev1.UsersFull{NodeId: "n1", Version: "v2", ChunkIndex: 0, ChunkTotal: 1, Users: []*nodev1.User{user(9)}})
	if err != nil || f == nil || f.Version != "v2" {
		t.Fatalf("重新开始: %+v err=%v", f, err)
	}
}

func TestUsersAssemblerVersionChangeMidway(t *testing.T) {
	a := newUsersAssembler()
	_, _ = a.add(&nodev1.UsersFull{NodeId: "n1", Version: "v1", ChunkIndex: 0, ChunkTotal: 2, Users: []*nodev1.User{user(1)}})
	_, err := a.add(&nodev1.UsersFull{NodeId: "n1", Version: "v2", ChunkIndex: 1, ChunkTotal: 2, Users: []*nodev1.User{user(2)}})
	if err == nil {
		t.Fatal("版本中途变化应报错")
	}
}

func TestUsersAssemblerInterleavedNodes(t *testing.T) {
	a := newUsersAssembler()
	if _, err := a.add(&nodev1.UsersFull{NodeId: "a", Version: "v1", ChunkIndex: 0, ChunkTotal: 2, Users: []*nodev1.User{user(1)}}); err != nil {
		t.Fatal(err)
	}
	fb, err := a.add(&nodev1.UsersFull{NodeId: "b", Version: "v1", ChunkIndex: 0, ChunkTotal: 1, Users: []*nodev1.User{user(10)}})
	if err != nil || fb == nil || fb.NodeID != "b" {
		t.Fatalf("b 应先收齐: %+v err=%v", fb, err)
	}
	fa, err := a.add(&nodev1.UsersFull{NodeId: "a", Version: "v1", ChunkIndex: 1, ChunkTotal: 2, Users: []*nodev1.User{user(2)}})
	if err != nil || fa == nil || len(fa.Users) != 2 {
		t.Fatalf("a: %+v err=%v", fa, err)
	}
}

func TestSessionUsersFullSkipChunkAcksIncomplete(t *testing.T) {
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)
	st := fc.takeStream(t, 5*time.Second)
	_ = st.SendWelcome(1, 1)
	nextEvent(t, s, EventWelcome)

	_ = st.SendUsersFull(&nodev1.UsersFull{NodeId: "n1", Version: "v1", ChunkIndex: 0, ChunkTotal: 2, Users: []*nodev1.User{user(1)}})
	_ = st.SendUsersFull(&nodev1.UsersFull{NodeId: "n1", Version: "v1", ChunkIndex: 2, ChunkTotal: 2, Users: []*nodev1.User{user(3)}})

	ack := st.Expect(t, "UsersAck").(*nodev1.UsersAck)
	if ack.GetOk() || ack.GetReasonCode() != UsersReasonIncomplete || ack.GetNodeId() != "n1" {
		t.Fatalf("UsersAck: %+v", ack)
	}
	drainNoUsersFull(t, s, 200*time.Millisecond)

	_ = st.SendUsersFull(&nodev1.UsersFull{NodeId: "n1", Version: "v1", ChunkIndex: 0, ChunkTotal: 2, Users: []*nodev1.User{user(1), user(2)}})
	_ = st.SendUsersFull(&nodev1.UsersFull{NodeId: "n1", Version: "v1", ChunkIndex: 1, ChunkTotal: 2, Users: []*nodev1.User{user(3)}})
	ev := nextEvent(t, s, EventUsersFull)
	if len(ev.UsersFull.Users) != 3 {
		t.Fatalf("用户数=%d", len(ev.UsersFull.Users))
	}
	st.end()
}

func drainNoUsersFull(t *testing.T, s *Session, d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == EventUsersFull {
				t.Fatal("不应收到 EventUsersFull")
			}
		case <-deadline:
			return
		}
	}
}

func TestSessionUsersDeltaEvent(t *testing.T) {
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)
	st := fc.takeStream(t, 5*time.Second)
	_ = st.SendWelcome(1, 1)
	nextEvent(t, s, EventWelcome)
	delta := &nodev1.UsersDelta{NodeId: "n1", FromVersion: "a", ToVersion: "b", Upserted: []*nodev1.User{user(1)}}
	_ = st.SendUsersDelta(delta)
	ev := nextEvent(t, s, EventUsersDelta)
	if !proto.Equal(ev.UsersDelta, delta) {
		t.Fatal("EventUsersDelta 应与下发一致")
	}
	st.end()
}
