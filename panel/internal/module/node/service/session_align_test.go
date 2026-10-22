package service

// 建流对齐合批（session_align.go）的单测：一批只读一次、单条流取消或超时不连累同批、批失败整批失败、同批去重、
// 同一批里先取配置序号再取名单序号。涉及并发，要用 -race 跑：
//   GOTOOLCHAIN=go1.26.10 go test -race -count=3 -run 'TestAlign' ./internal/module/node/service/

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/reqid"
)

// alignQueued 是排在下一批的请求数。
func (m *Sessions) alignQueued() int {
	m.align.mu.Lock()
	defer m.align.mu.Unlock()
	return len(m.align.pending)
}

// alignRunning 报告有没有 goroutine 在跑批。
func (m *Sessions) alignRunning() bool {
	m.align.mu.Lock()
	defer m.align.mu.Unlock()
	return m.align.running
}

// gateFirstConfigs 让 b 的第一次 ServerConfigs 卡住：进入时关闭 entered，release 被调后才继续；之后的调用不卡。
// 要在任何 Open 之前调。release 可重复调用，测试结束时也会自动调（t.Cleanup），不留卡住的 goroutine。
func gateFirstConfigs(t *testing.T, b *fakeBackend) (entered <-chan struct{}, release func()) {
	t.Helper()
	in := make(chan struct{})
	gate := make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	b.configsHook = func() {
		if calls.Add(1) == 1 {
			close(in)
			<-gate
		}
	}
	t.Cleanup(release)
	return in, release
}

// asyncOpen 是后台 Open 的结果。
type asyncOpen struct {
	s   *Session
	err error
}

// openAsync 在后台 Open（Hello 为空），结果从返回的通道取。
func openAsync(ctx context.Context, m *Sessions, serverID uuid.UUID) <-chan asyncOpen {
	ch := make(chan asyncOpen, 1)
	go func() {
		s, err := m.Open(ctx, serverID, Hello{})
		ch <- asyncOpen{s: s, err: err}
	}()
	return ch
}

// waitOpened 在 3 秒内取后台 Open 的结果，超时测试失败。
func waitOpened(t *testing.T, ch <-chan asyncOpen, what string) asyncOpen {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatalf("等待超时：%s", what)
		return asyncOpen{}
	}
}

// waitClosed 在 3 秒内等 ch 关闭，超时测试失败。
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("等待超时：%s", what)
	}
}

// addAlignServer 给 b 加一台服务器：一个节点，配置摘要按 i 区分，名单版本 "u1-s<i>"、唯一用户 uid = i+1。
func addAlignServer(b *fakeBackend, i int) (serverID, nodeID uuid.UUID) {
	serverID, nodeID = uuid.New(), uuid.New()
	b.setConfigs(serverID, []NodeConfigDoc{mkDoc(nodeID, byte('a'+i%26))})
	b.setRosters(serverID, []NodeRoster{mkRoster(nodeID, fmt.Sprintf("s%d", i), int64(i+1))})
	return serverID, nodeID
}

// expectDelivered 取走 s 队列里的全部消息，核对依次是 Welcome、本服务器那份配置（只有节点 nodeID）、版本 version 的全量名单。
func expectDelivered(t *testing.T, s *Session, nodeID uuid.UUID, version string) {
	t.Helper()
	msgs := pending(s)
	if len(msgs) != 3 || msgs[0].Kind != OutWelcome || msgs[1].Kind != OutConfig || msgs[2].Kind != OutUsersFull {
		kinds := make([]OutKind, 0, len(msgs))
		for _, o := range msgs {
			kinds = append(kinds, o.Kind)
		}
		t.Fatalf("应为 Welcome、Config、UsersFull，得 %v", kinds)
	}
	if nodes := msgs[1].Config.Nodes; len(nodes) != 1 || nodes[0].NodeID != nodeID {
		t.Fatalf("配置应只有节点 %s，得 %+v", nodeID, nodes)
	}
	if r := msgs[2].Roster; r == nil || r.NodeID != nodeID || r.Version != version {
		t.Fatalf("名单应为节点 %s 的 %s，得 %+v", nodeID, version, r)
	}
}

// sessionSeqs 是 s 最近接受的配置与名单序号。
func sessionSeqs(s *Session) (cfg, users uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfgSeq, s.usersSeq
}

// sortedIDs 按 sessionCmpID 升序（与对齐器传给后端的顺序一致）。
func sortedIDs(ids ...uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, sessionCmpID)
	return out
}

// waitAlignIdle 等对齐 goroutine 在队列空后退出。
func waitAlignIdle(t *testing.T, m *Sessions) {
	t.Helper()
	waitFor(t, 3*time.Second, func() bool { return !m.alignRunning() }, "队列空后对齐 goroutine 退出")
}

func TestAlignBatchesConcurrentOpens(t *testing.T) {
	bg := context.Background()
	b := newFakeBackend()
	const n = 50
	servers, nodes := make([]uuid.UUID, n), make([]uuid.UUID, n)
	for i := 0; i < n; i++ {
		servers[i], nodes[i] = addAlignServer(b, i)
	}

	entered, release := gateFirstConfigs(t, b)
	m := NewSessionManager(b, nil, fastOptions())

	results := make([]<-chan asyncOpen, n)
	results[0] = openAsync(bg, m, servers[0])
	waitClosed(t, entered, "第一批进入 ServerConfigs")

	for i := 1; i < n; i++ {
		results[i] = openAsync(bg, m, servers[i])
	}
	waitFor(t, 3*time.Second, func() bool { return m.alignQueued() == n-1 }, "其余 49 条排进下一批")

	release()

	opened := make([]*Session, n)
	for i := 0; i < n; i++ {
		r := waitOpened(t, results[i], fmt.Sprintf("等待流 %d 打开", i))
		if r.err != nil || r.s == nil {
			t.Fatalf("流 %d 打开失败：err=%v, s=%v", i, r.err, r.s)
		}
		expectDelivered(t, r.s, nodes[i], fmt.Sprintf("u1-s%d", i))
		opened[i] = r.s
	}

	rosterCalls, configCalls := b.callLog()
	if len(configCalls) != 2 || len(rosterCalls) != 2 {
		t.Fatalf("ServerConfigs、Rosters 应各调 2 次（第一批 1 台，第二批其余），得 configCalls=%d, rosterCalls=%d", len(configCalls), len(rosterCalls))
	}
	if !slices.Equal(configCalls[0], []uuid.UUID{servers[0]}) || !slices.Equal(rosterCalls[0], []uuid.UUID{servers[0]}) {
		t.Fatalf("ServerConfigs、Rosters 应各调 2 次（第一批 1 台，第二批其余）：第一批不符")
	}
	if !slices.Equal(configCalls[1], sortedIDs(servers[1:]...)) || !slices.Equal(rosterCalls[1], sortedIDs(servers[1:]...)) {
		t.Fatalf("ServerConfigs、Rosters 应各调 2 次（第一批 1 台，第二批其余）：第二批不符")
	}

	// 同一批共用一对序号，先取配置序号再取名单序号；第二批在第一批之后取号
	c0, u0 := sessionSeqs(opened[0])
	if c0 == 0 || c0 >= u0 {
		t.Fatalf("opened[0] 序号应满足 0 < c0 < u0，得 c0=%d, u0=%d", c0, u0)
	}
	c1, u1 := sessionSeqs(opened[1])
	if c1 <= u0 || c1 >= u1 {
		t.Fatalf("opened[1] 序号应满足 u0 < c1 < u1，得 u0=%d, c1=%d, u1=%d", u0, c1, u1)
	}
	for i := 2; i < n; i++ {
		ci, ui := sessionSeqs(opened[i])
		if ci != c1 || ui != u1 {
			t.Fatalf("opened[%d] 序号应等于 (c1=%d, u1=%d)，得 (%d, %d)", i, c1, u1, ci, ui)
		}
	}

	if m.Len() != n {
		t.Fatalf("m.Len() 应为 %d，得 %d", n, m.Len())
	}
	b.mu.Lock()
	hellosCount := len(b.hellos)
	b.mu.Unlock()
	if hellosCount != n {
		t.Fatalf("len(b.hellos) 应为 %d，得 %d", n, hellosCount)
	}

	waitAlignIdle(t, m)
}

func TestAlignCanceledWaiterDoesNotFailBatch(t *testing.T) {
	bg := context.Background()
	b := newFakeBackend()
	srvX, _ := addAlignServer(b, 0)
	srvA, _ := addAlignServer(b, 1)
	srvB, nodeOfB := addAlignServer(b, 2)
	srvC, nodeOfC := addAlignServer(b, 3)

	entered, release := gateFirstConfigs(t, b)
	m := NewSessionManager(b, nil, fastOptions())

	rx := openAsync(bg, m, srvX)
	waitClosed(t, entered, "第一批进入 ServerConfigs")

	idA := reqid.New()
	ctxA, cancelA := context.WithCancel(reqid.With(bg, idA))
	defer cancelA()
	ra := openAsync(ctxA, m, srvA)
	waitFor(t, 3*time.Second, func() bool { return m.alignQueued() == 1 }, "A 排进下一批（这批第一条）")

	rb := openAsync(bg, m, srvB)
	rc := openAsync(bg, m, srvC)
	waitFor(t, 3*time.Second, func() bool { return m.alignQueued() == 3 }, "B、C 排进下一批")

	cancelA()
	resA := waitOpened(t, ra, "等待 A 返回")
	if !errors.Is(resA.err, context.Canceled) || resA.s != nil {
		t.Fatalf("resA 应为 context.Canceled 且 s==nil，得 err=%v, s=%v", resA.err, resA.s)
	}
	if m.Online(srvA) {
		t.Fatalf("m.Online(srvA) 应为 false")
	}

	release()
	resX := waitOpened(t, rx, "等待 X 返回")
	if resX.err != nil {
		t.Fatalf("resX.err 应为 nil，得 %v", resX.err)
	}

	resB := waitOpened(t, rb, "等待 B 返回")
	if resB.err != nil {
		t.Fatalf("resB.err 应为 nil，得 %v", resB.err)
	}
	expectDelivered(t, resB.s, nodeOfB, "u1-s2")

	resC := waitOpened(t, rc, "等待 C 返回")
	if resC.err != nil {
		t.Fatalf("resC.err 应为 nil，得 %v", resC.err)
	}
	expectDelivered(t, resC.s, nodeOfC, "u1-s3")

	_, configCalls := b.callLog()
	if len(configCalls) != 2 {
		t.Fatalf("configCalls 长度应为 2，得 %d", len(configCalls))
	}
	if !slices.Equal(configCalls[1], sortedIDs(srvA, srvB, srvC)) {
		t.Fatalf("configCalls[1] 应包含 A、B、C，得 %v", configCalls[1])
	}

	b.mu.Lock()
	reqIDs := slices.Clone(b.configReqIDs)
	b.mu.Unlock()
	if len(reqIDs) != 2 || reqIDs[1] != idA {
		t.Fatalf("b.configReqIDs[1] 应为 %s，得 %v", idA, reqIDs)
	}

	if m.Online(srvA) {
		t.Fatalf("m.Online(srvA) 仍应为 false")
	}
	waitAlignIdle(t, m)
}

func TestAlignTimeoutReturnsBusyWhileBatchStillDelivers(t *testing.T) {
	bg := context.Background()
	opt := fastOptions()
	opt.AlignTimeout = time.Second
	b := newFakeBackend()
	srvX, _ := addAlignServer(b, 0)
	srvW, _ := addAlignServer(b, 1)
	srvV, nodeOfV := addAlignServer(b, 2)

	entered, release := gateFirstConfigs(t, b)
	m := NewSessionManager(b, nil, opt)

	rx := openAsync(bg, m, srvX)
	waitClosed(t, entered, "第一批进入 ServerConfigs")
	rw := openAsync(bg, m, srvW)
	waitFor(t, 3*time.Second, func() bool { return m.alignQueued() == 1 }, "W 排进下一批")

	resW := waitOpened(t, rw, "等待 W 返回超时")
	if !errors.Is(resW.err, ErrAlignBusy) || resW.s != nil {
		t.Fatalf("resW 应为 ErrAlignBusy 且 s==nil，得 err=%v, s=%v", resW.err, resW.s)
	}
	resX := waitOpened(t, rx, "等待 X 返回超时")
	if !errors.Is(resX.err, ErrAlignBusy) || resX.s != nil {
		t.Fatalf("resX 应为 ErrAlignBusy 且 s==nil，得 err=%v, s=%v", resX.err, resX.s)
	}
	if m.Online(srvW) || m.Online(srvX) {
		t.Fatalf("m.Online 应均为 false，得 srvW=%v, srvX=%v", m.Online(srvW), m.Online(srvX))
	}

	rv := openAsync(bg, m, srvV)
	waitFor(t, 3*time.Second, func() bool { return m.alignQueued() == 2 }, "V 与 W 留下的请求排在下一批")
	release()

	resV := waitOpened(t, rv, "等待 V 正常返回")
	if resV.err != nil {
		t.Fatalf("resV.err 应为 nil，得 %v", resV.err)
	}
	expectDelivered(t, resV.s, nodeOfV, "u1-s2")

	_, configCalls := b.callLog()
	if len(configCalls) != 2 {
		t.Fatalf("configCalls 长度应为 2，得 %d", len(configCalls))
	}
	if !slices.Equal(configCalls[1], sortedIDs(srvW, srvV)) {
		t.Fatalf("configCalls[1] 应包含 W 与 V，得 %v", configCalls[1])
	}
	if m.Online(srvW) {
		t.Fatalf("m.Online(srvW) 须为 false")
	}
	waitAlignIdle(t, m)
}

func TestAlignBatchErrorFailsWholeBatch(t *testing.T) {
	bg := context.Background()
	b := newFakeBackend()
	errConfigs := errors.New("读配置失败")
	b.mu.Lock()
	b.configErr = errConfigs
	b.mu.Unlock()
	srvA, _ := addAlignServer(b, 0)
	srvB, _ := addAlignServer(b, 1)
	srvC, _ := addAlignServer(b, 2)

	entered, release := gateFirstConfigs(t, b)
	m := NewSessionManager(b, nil, fastOptions())

	ra := openAsync(bg, m, srvA)
	waitClosed(t, entered, "第一批进入 ServerConfigs")
	rb := openAsync(bg, m, srvB)
	rc := openAsync(bg, m, srvC)
	waitFor(t, 3*time.Second, func() bool { return m.alignQueued() == 2 }, "B、C 排进下一批")
	release()

	for idx, res := range []asyncOpen{waitOpened(t, ra, "等待 A"), waitOpened(t, rb, "等待 B"), waitOpened(t, rc, "等待 C")} {
		if !errors.Is(res.err, errConfigs) || res.s != nil {
			t.Fatalf("[%d] 应为 errConfigs 且 s==nil，得 err=%v, s=%v", idx, res.err, res.s)
		}
	}
	if m.Len() != 0 {
		t.Fatalf("m.Len() 应为 0，得 %d", m.Len())
	}

	rosterCalls, configCalls := b.callLog()
	if len(configCalls) != 2 {
		t.Fatalf("configCalls 长度应为 2，得 %d", len(configCalls))
	}
	if !slices.Equal(configCalls[1], sortedIDs(srvB, srvC)) {
		t.Fatalf("configCalls[1] 应包含 B、C，得 %v", configCalls[1])
	}
	if len(rosterCalls) != 0 {
		t.Fatalf("rosterCalls 长度应为 0，得 %d", len(rosterCalls))
	}

	// 名单读失败同样整批失败
	errRosters := errors.New("读名单失败")
	b.mu.Lock()
	b.configErr = nil
	b.rosterErr = errRosters
	b.mu.Unlock()
	srvD, _ := addAlignServer(b, 3)
	_, err := m.Open(bg, srvD, Hello{})
	if !errors.Is(err, errRosters) {
		t.Fatalf("m.Open(srvD) 应为 errRosters，得 %v", err)
	}
	if m.Online(srvD) {
		t.Fatalf("m.Online(srvD) 须为 false")
	}

	// 下一批正常
	b.mu.Lock()
	b.rosterErr = nil
	b.mu.Unlock()
	srvE, nodeOfE := addAlignServer(b, 4)
	s, err := m.Open(bg, srvE, Hello{})
	if err != nil {
		t.Fatalf("m.Open(srvE) err 应为 nil，得 %v", err)
	}
	expectDelivered(t, s, nodeOfE, "u1-s4")
	if m.Len() != 1 {
		t.Fatalf("m.Len() 应为 1，得 %d", m.Len())
	}
	waitAlignIdle(t, m)
}

func TestAlignDedupsSameServerInBatch(t *testing.T) {
	bg := context.Background()
	b := newFakeBackend()
	srvX, _ := addAlignServer(b, 0)
	srvA, nodeOfA := addAlignServer(b, 1)

	entered, release := gateFirstConfigs(t, b)
	m := NewSessionManager(b, nil, fastOptions())

	rx := openAsync(bg, m, srvX)
	waitClosed(t, entered, "第一批进入 ServerConfigs")
	r1 := openAsync(bg, m, srvA)
	waitFor(t, 3*time.Second, func() bool { return m.alignQueued() == 1 }, "A 的第一条流排进下一批")
	r2 := openAsync(bg, m, srvA)
	waitFor(t, 3*time.Second, func() bool { return m.alignQueued() == 2 }, "A 的第二条流排进下一批")
	release()

	resX := waitOpened(t, rx, "等待 X")
	if resX.err != nil {
		t.Fatalf("resX.err 应为 nil，得 %v", resX.err)
	}
	res1 := waitOpened(t, r1, "等待 A 流 1")
	if res1.err != nil {
		t.Fatalf("res1.err 应为 nil，得 %v", res1.err)
	}
	res2 := waitOpened(t, r2, "等待 A 流 2")
	if res2.err != nil {
		t.Fatalf("res2.err 应为 nil，得 %v", res2.err)
	}

	_, err := res1.s.Next(bg)
	if !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("res1.s.Next 应为 ErrSessionSuperseded，得 %v", err)
	}
	expectDelivered(t, res2.s, nodeOfA, "u1-s1")

	rosterCalls, configCalls := b.callLog()
	if len(configCalls) != 2 || len(rosterCalls) != 2 {
		t.Fatalf("configCalls 与 rosterCalls 长度均应为 2，得 config=%d, roster=%d", len(configCalls), len(rosterCalls))
	}
	if !slices.Equal(configCalls[1], []uuid.UUID{srvA}) || !slices.Equal(rosterCalls[1], []uuid.UUID{srvA}) {
		t.Fatalf("第二批应去重只有 1 台 srvA，得 config=%v, roster=%v", configCalls[1], rosterCalls[1])
	}
	if m.Len() != 2 {
		t.Fatalf("m.Len() 应为 2，得 %d", m.Len())
	}
	waitAlignIdle(t, m)
}

func TestAlignSkipsAlreadyCanceledStream(t *testing.T) {
	bg := context.Background()
	b := newFakeBackend()
	srv, _ := addAlignServer(b, 0)
	m := NewSessionManager(b, nil, fastOptions())

	ctx, cancel := context.WithCancel(bg)
	cancel()
	s, err := m.Open(ctx, srv, Hello{})
	if !errors.Is(err, context.Canceled) || s != nil {
		t.Fatalf("Open 已取消流应返回 context.Canceled 且 s==nil，得 err=%v, s=%v", err, s)
	}

	r, c := b.counts()
	if r != 0 || c != 0 {
		t.Fatalf("不应调用 Rosters 与 ServerConfigs，得 r=%d, c=%d", r, c)
	}
	if m.alignRunning() {
		t.Fatalf("m.alignRunning() 应为 false")
	}
	if m.Online(srv) {
		t.Fatalf("m.Online(srv) 须为 false")
	}
}
