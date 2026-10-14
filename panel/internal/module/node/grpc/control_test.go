package grpc

// ControlService.Session 的 bufconn 端到端测试。

import (
	"context"
	"errors"
	"math/big"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

var testServerID = uuid.MustParse("0190a000-0000-7000-8000-000000000001")

var testSessionOpt = service.SessionOptions{
	TerminateGrace: time.Second,
	ShutdownBase:   500 * time.Millisecond,
	ShutdownSpread: time.Second,
}

// fakeBackend 实现 service.SessionBackend，供本包 gRPC 测试用。
type fakeBackend struct {
	mu           sync.Mutex
	rosters      map[uuid.UUID][]service.NodeRoster
	configs      map[uuid.UUID][]service.NodeConfigDoc
	trafficErr   error
	ingested     []service.TrafficReport
	has          []bool
	renewSerials []*big.Int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		rosters: map[uuid.UUID][]service.NodeRoster{},
		configs: map[uuid.UUID][]service.NodeConfigDoc{},
	}
}

func (f *fakeBackend) setRosters(serverID uuid.UUID, r []service.NodeRoster) {
	f.mu.Lock()
	f.rosters[serverID] = r
	f.mu.Unlock()
}

func (f *fakeBackend) setConfigs(serverID uuid.UUID, c []service.NodeConfigDoc) {
	f.mu.Lock()
	f.configs[serverID] = c
	f.mu.Unlock()
}

func (f *fakeBackend) setTrafficErr(err error) {
	f.mu.Lock()
	f.trafficErr = err
	f.mu.Unlock()
}

func (f *fakeBackend) Rosters(_ context.Context, ids []uuid.UUID) (map[uuid.UUID][]service.NodeRoster, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID][]service.NodeRoster{}
	for _, id := range ids {
		if r, ok := f.rosters[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (f *fakeBackend) ServerConfigs(_ context.Context, ids []uuid.UUID) (map[uuid.UUID][]service.NodeConfigDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID][]service.NodeConfigDoc{}
	for _, id := range ids {
		if c, ok := f.configs[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (f *fakeBackend) IngestTraffic(_ context.Context, _ uuid.UUID, r service.TrafficReport, m service.RosterMembership) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ingested = append(f.ingested, r)
	for _, e := range r.Entries {
		f.has = append(f.has, m.Has(e.NodeID, e.UID))
	}
	return f.trafficErr
}

func (f *fakeBackend) IngestOnline(context.Context, uuid.UUID, []service.OnlineEntry, service.RosterMembership) error {
	return nil
}

func (f *fakeBackend) RecordMetrics(context.Context, uuid.UUID, service.HostMetrics, time.Time) error {
	return nil
}

func (f *fakeBackend) RenewCertificate(_ context.Context, _ uuid.UUID, certSerial *big.Int, _ []byte) ([]byte, []byte, error) {
	f.mu.Lock()
	f.renewSerials = append(f.renewSerials, new(big.Int).Set(certSerial))
	f.mu.Unlock()
	return []byte("CHAIN"), []byte("CA"), nil
}

func (f *fakeBackend) RecordHello(context.Context, uuid.UUID, service.Hello, time.Time) error {
	return nil
}

func (f *fakeBackend) RecordHeartbeat(context.Context, uuid.UUID, []service.NodeRuntime, time.Time) error {
	return nil
}

func (f *fakeBackend) RecordConfigAck(context.Context, uuid.UUID, service.ConfigAck, time.Time) error {
	return nil
}

type serverIDStream struct {
	ggrpc.ServerStream
	ctx context.Context
}

func (s *serverIDStream) Context() context.Context { return s.ctx }

// fakeAuth 实现 sessionAuthorizer，供登记后复核测试用。
type fakeAuth struct {
	mu    sync.Mutex
	err   error
	calls int
	// failFrom > 0 时只有第 failFrom 次及以后的调用回 err（登记后复核失败的情形）
	failFrom int
}

func (f *fakeAuth) AuthorizeSession(_ context.Context, _ uuid.UUID, _ *big.Int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failFrom > 0 && f.calls < f.failFrom {
		return nil
	}
	return f.err
}

// startControl 起内存 gRPC 服务端并返回 Control 客户端与会话管理器。
func startControl(t *testing.T, b service.SessionBackend, opt service.SessionOptions, inject bool) (nodev1.ControlServiceClient, *service.Sessions) {
	return startControlAuth(t, b, opt, inject, &fakeAuth{})
}

func startControlAuth(t *testing.T, b service.SessionBackend, opt service.SessionOptions, inject bool, auth sessionAuthorizer) (nodev1.ControlServiceClient, *service.Sessions) {
	t.Helper()
	svc := service.New(nil, nil, nil, nil)
	m := service.NewSessionManager(b, nil, opt)
	svc.UseSessions(m)

	lis := bufconn.Listen(1 << 20)
	var opts []ggrpc.ServerOption
	if inject {
		opts = append(opts, ggrpc.StreamInterceptor(func(srv interface{}, ss ggrpc.ServerStream, _ *ggrpc.StreamServerInfo, handler ggrpc.StreamHandler) error {
			ctx := WithServerID(ss.Context(), testServerID)
			ctx = WithCertSerial(ctx, big.NewInt(1))
			wrapped := &serverIDStream{ServerStream: ss, ctx: ctx}
			return handler(srv, wrapped)
		}))
	}
	gs := ggrpc.NewServer(opts...)
	nodev1.RegisterControlServiceServer(gs, newControlServer(svc, auth))
	go func() { _ = gs.Serve(lis) }()

	conn, err := ggrpc.NewClient("passthrough:///bufnet",
		ggrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		ggrpc.WithTransportCredentials(insecure.NewCredentials()),
		// 固定 64KB 窗口（关掉 BDP 自动放大）：节点不读时服务端的 Send 一定卡在流控上，测试才稳定
		ggrpc.WithInitialWindowSize(1<<16),
		ggrpc.WithInitialConnWindowSize(1<<16),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		gs.Stop()
	})
	return nodev1.NewControlServiceClient(conn), m
}

func streamCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func recvResp(t *testing.T, stream nodev1.ControlService_SessionClient) *nodev1.SessionResponse {
	t.Helper()
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	return resp
}

func recvErr(t *testing.T, stream nodev1.ControlService_SessionClient) error {
	t.Helper()
	_, err := stream.Recv()
	return err
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func mkDoc(nodeID uuid.UUID, tag byte) service.NodeConfigDoc {
	sha := make([]byte, 64)
	for i := range sha {
		sha[i] = tag
	}
	return service.NodeConfigDoc{NodeID: nodeID, NodeType: "vless", JSON: []byte(`{}`), SHA256: string(sha)}
}

func mkRoster(nodeID uuid.UUID, version string, uids ...int64) service.NodeRoster {
	r := service.NodeRoster{NodeID: nodeID, Version: version}
	for _, uid := range uids {
		r.Users = append(r.Users, service.RosterUser{UID: uid, UUID: uuid.NewSHA1(uuid.Nil, []byte{byte(uid)})})
	}
	return r
}

func helloMsg(h *nodev1.Hello) *nodev1.SessionRequest {
	return &nodev1.SessionRequest{Seq: 1, Body: &nodev1.SessionRequest_Hello{Hello: h}}
}

func drainWelcome(t *testing.T, stream nodev1.ControlService_SessionClient) {
	t.Helper()
	resp := recvResp(t, stream)
	if resp.GetSeq() != 1 || resp.GetWelcome() == nil {
		t.Fatalf("期望 Welcome seq=1，得到 seq=%d", resp.GetSeq())
	}
}

func TestSessionRequiresServerID(t *testing.T) {
	b := newFakeBackend()
	client, _ := startControl(t, b, testSessionOpt, false)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	// 服务端不读就回错，Send 可能已经拿到 EOF，忽略它，以 Recv 的状态为准
	_ = stream.Send(helloMsg(&nodev1.Hello{}))
	if st, ok := status.FromError(recvErr(t, stream)); !ok || st.Code() != codes.Unauthenticated {
		t.Fatalf("期望 Unauthenticated，得到 %v", st)
	}
}

func TestSessionFirstMessageMustBeHello(t *testing.T) {
	b := newFakeBackend()
	client, _ := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(&nodev1.SessionRequest{
		Seq:  1,
		Body: &nodev1.SessionRequest_Heartbeat{Heartbeat: &nodev1.Heartbeat{}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if st, ok := status.FromError(recvErr(t, stream)); !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("期望 InvalidArgument，得到 %v", st)
	}
}

func TestSessionAlignedSendsOnlyWelcome(t *testing.T) {
	nodeID := uuid.MustParse("0190a000-0000-7000-8000-000000000002")
	doc := mkDoc(nodeID, 'a')
	b := newFakeBackend()
	b.setConfigs(testServerID, []service.NodeConfigDoc{doc})
	b.setRosters(testServerID, []service.NodeRoster{mkRoster(nodeID, "u1-x", 1, 2)})

	client, _ := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{
		Features: []string{"users_delta"},
		AppliedConfigs: []*nodev1.AppliedConfig{{
			NodeId: nodeID.String(), ContentSha256: doc.SHA256,
		}},
		AppliedUsers: []*nodev1.AppliedUsers{{
			NodeId: nodeID.String(), Version: "u1-x",
		}},
	})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	w := recvResp(t, stream)
	if w.GetSeq() != 1 {
		t.Fatalf("Welcome seq=%d", w.GetSeq())
	}
	wel := w.GetWelcome()
	if wel == nil {
		t.Fatal("期望 Welcome")
	}
	if wel.GetPushIntervalSec() != 60 || wel.GetHeartbeatIntervalSec() != 30 {
		t.Fatalf("间隔不符 push=%d hb=%d", wel.GetPushIntervalSec(), wel.GetHeartbeatIntervalSec())
	}
	if !slices.Equal(wel.GetFeatures(), []string{"users_delta"}) {
		t.Fatalf("features=%v", wel.GetFeatures())
	}
	if err := stream.Send(&nodev1.SessionRequest{
		Seq:  2,
		Body: &nodev1.SessionRequest_Traffic{Traffic: &nodev1.TrafficReport{ReportId: "r-1"}},
	}); err != nil {
		t.Fatalf("Send traffic: %v", err)
	}
	ack := recvResp(t, stream)
	if ack.GetTrafficAck() == nil || ack.GetTrafficAck().GetReportId() != "r-1" {
		t.Fatalf("期望 TrafficAck r-1，得到 %v", ack)
	}
}

func TestSessionMismatchSendsFull(t *testing.T) {
	nodeID := uuid.MustParse("0190a000-0000-7000-8000-000000000003")
	b := newFakeBackend()
	doc := mkDoc(nodeID, 'b')
	b.setConfigs(testServerID, []service.NodeConfigDoc{doc})
	b.setRosters(testServerID, []service.NodeRoster{mkRoster(nodeID, "u1-m", 10, 20)})

	client, _ := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for seq := 1; seq <= 3; seq++ {
		resp := recvResp(t, stream)
		if resp.GetSeq() != uint64(seq) {
			t.Fatalf("期望 seq=%d，得到 %d", seq, resp.GetSeq())
		}
		switch seq {
		case 1:
			if resp.GetWelcome() == nil {
				t.Fatal("seq1 应为 Welcome")
			}
		case 2:
			cfg := resp.GetConfig()
			if cfg == nil || len(cfg.GetNodes()) != 1 {
				t.Fatal("seq2 应为 Config")
			}
			nc := cfg.GetNodes()[0]
			if nc.GetContentSha256() != doc.SHA256 {
				t.Fatalf("sha256 不符")
			}
			if len(cfg.GetReleaseId()) < 3 || cfg.GetReleaseId()[:3] != "r1-" {
				t.Fatalf("release_id=%q", cfg.GetReleaseId())
			}
		case 3:
			uf := resp.GetUsersFull()
			if uf == nil || uf.GetChunkIndex() != 0 || uf.GetChunkTotal() != 1 || len(uf.GetUsers()) != 2 {
				t.Fatalf("seq3 UsersFull: %+v", uf)
			}
			if uf.GetUsers()[0].GetUid() != 10 || uf.GetUsers()[1].GetUid() != 20 {
				t.Fatalf("uid 不对")
			}
			u0 := uf.GetUsers()[0].GetUuid()
			if u0 != uuid.NewSHA1(uuid.Nil, []byte{10}).String() {
				t.Fatalf("uuid0=%s", u0)
			}
		}
	}
}

func TestSessionUsersFullChunks(t *testing.T) {
	nodeID := uuid.MustParse("0190a000-0000-7000-8000-000000000004")
	users := make([]service.RosterUser, 2*service.UsersChunkSize+1)
	for i := range users {
		uid := int64(i + 1)
		users[i] = service.RosterUser{UID: uid, UUID: uuid.NewSHA1(uuid.Nil, []byte{byte(uid)})}
	}
	b := newFakeBackend()
	b.setConfigs(testServerID, []service.NodeConfigDoc{mkDoc(nodeID, 'c')})
	b.setRosters(testServerID, []service.NodeRoster{{NodeID: nodeID, Version: "u1-big", Users: users}})

	client, _ := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	recvResp(t, stream) // Welcome
	recvResp(t, stream) // Config
	counts := []int{service.UsersChunkSize, service.UsersChunkSize, 1}
	for i := 0; i < 3; i++ {
		resp := recvResp(t, stream)
		uf := resp.GetUsersFull()
		if uf == nil {
			t.Fatalf("chunk %d 不是 UsersFull", i)
		}
		if uf.GetChunkTotal() != 3 || uf.GetChunkIndex() != uint32(i) {
			t.Fatalf("chunk %d index/total 不对", i)
		}
		if len(uf.GetUsers()) != counts[i] {
			t.Fatalf("chunk %d 人数 %d", i, len(uf.GetUsers()))
		}
	}
}

func TestSessionTrafficAckOnConflict(t *testing.T) {
	b := newFakeBackend()
	client, _ := startControl(t, b, testSessionOpt, true)

	// 冲突仍回 ack
	b.setTrafficErr(service.ErrTrafficReportConflict)
	ctx1, cancel1 := streamCtx(t)
	defer cancel1()
	stream1, err := client.Session(ctx1)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream1.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainWelcome(t, stream1)
	if err := stream1.Send(&nodev1.SessionRequest{
		Seq:  2,
		Body: &nodev1.SessionRequest_Traffic{Traffic: &nodev1.TrafficReport{ReportId: "r-c"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	ack := recvResp(t, stream1)
	if ack.GetTrafficAck() == nil || ack.GetTrafficAck().GetReportId() != "r-c" {
		t.Fatalf("冲突时应 TrafficAck，得到 %v", ack)
	}
	cancel1()

	// 其它错误不回 ack，Renew 应拿到 Certificate
	b.setTrafficErr(errors.New("db down"))
	ctx2, cancel2 := streamCtx(t)
	defer cancel2()
	stream2, err := client.Session(ctx2)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream2.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainWelcome(t, stream2)
	if err := stream2.Send(&nodev1.SessionRequest{
		Seq:  2,
		Body: &nodev1.SessionRequest_Traffic{Traffic: &nodev1.TrafficReport{ReportId: "r-fail"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := stream2.Send(&nodev1.SessionRequest{
		Seq:  3,
		Body: &nodev1.SessionRequest_Renew{Renew: &nodev1.RenewRequest{CsrDer: []byte{1, 2, 3}}},
	}); err != nil {
		t.Fatalf("Send renew: %v", err)
	}
	resp := recvResp(t, stream2)
	if resp.GetCertificate() == nil {
		t.Fatalf("期望 Certificate 而非 TrafficAck，得到 %v", resp)
	}
	if resp.GetTrafficAck() != nil {
		t.Fatal("不应收到失败流量的 TrafficAck")
	}
}

func TestSessionRenewReturnsCertificate(t *testing.T) {
	b := newFakeBackend()
	client, _ := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainWelcome(t, stream)
	if err := stream.Send(&nodev1.SessionRequest{
		Seq:  2,
		Body: &nodev1.SessionRequest_Renew{Renew: &nodev1.RenewRequest{CsrDer: []byte{9}}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	cert := recvResp(t, stream)
	c := cert.GetCertificate()
	if c == nil {
		t.Fatal("期望 Certificate")
	}
	if string(c.GetCertChainPem()) != "CHAIN" || string(c.GetCaBundlePem()) != "CA" {
		t.Fatalf("pem 不符 chain=%q ca=%q", c.GetCertChainPem(), c.GetCaBundlePem())
	}
}

func TestSessionTombstone(t *testing.T) {
	b := newFakeBackend()
	client, m := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainWelcome(t, stream)
	waitFor(t, 2*time.Second, func() bool { return m.Len() == 1 }, "会话登记")
	m.Tombstone(testServerID, "revoked")
	cmd := recvResp(t, stream)
	ts := cmd.GetCommand()
	if ts == nil || ts.GetTombstone() == nil || ts.GetTombstone().GetReason() != "revoked" {
		t.Fatalf("期望 Tombstone command，得到 %v", cmd)
	}
	if st, ok := status.FromError(recvErr(t, stream)); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("期望 PermissionDenied，得到 %v", st)
	}
}

func TestSessionSupersede(t *testing.T) {
	b := newFakeBackend()
	client, m := startControl(t, b, testSessionOpt, true)

	ctx1, cancel1 := streamCtx(t)
	stream1, err := client.Session(ctx1)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream1.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainWelcome(t, stream1)

	ctx2, cancel2 := streamCtx(t)
	defer cancel2()
	stream2, err := client.Session(ctx2)
	if err != nil {
		t.Fatalf("Session2: %v", err)
	}
	if err := stream2.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send2: %v", err)
	}
	drainWelcome(t, stream2)

	if st, ok := status.FromError(recvErr(t, stream1)); !ok || st.Code() != codes.Aborted {
		t.Fatalf("旧流期望 Aborted，得到 %v", st)
	}
	cancel1()
	if m.Len() != 1 {
		t.Fatalf("Len=%d", m.Len())
	}
}

func TestSessionCleanupAfterClientClose(t *testing.T) {
	b := newFakeBackend()
	client, m := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainWelcome(t, stream)
	waitFor(t, 2*time.Second, func() bool { return m.Len() == 1 }, "会话登记")
	_ = stream.CloseSend()
	cancel()
	waitFor(t, 2*time.Second, func() bool { return m.Len() == 0 }, "会话清理")
}

func TestShutdownSendsReconnect(t *testing.T) {
	b := newFakeBackend()
	client, m := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainWelcome(t, stream)
	waitFor(t, 2*time.Second, func() bool { return m.Len() == 1 }, "会话登记")

	shCtx, shCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shCancel()
	var shErr error
	done := make(chan struct{})
	go func() {
		shErr = m.Shutdown(shCtx)
		close(done)
	}()

	cmd := recvResp(t, stream)
	rec := cmd.GetCommand().GetReconnect()
	if rec == nil {
		t.Fatalf("期望 reconnect command，得到 %v", cmd)
	}
	if rec.GetInMs() != uint32(testSessionOpt.ShutdownBase.Milliseconds()) {
		t.Fatalf("in_ms=%d 期望 %d", rec.GetInMs(), testSessionOpt.ShutdownBase.Milliseconds())
	}
	if rec.GetJitterMs() == 0 {
		t.Fatal("jitter_ms 应 > 0")
	}
	if st, ok := status.FromError(recvErr(t, stream)); !ok || st.Code() != codes.Unavailable {
		t.Fatalf("期望 Unavailable，得到 %v", st)
	}
	waitFor(t, 2*time.Second, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, "Shutdown 返回")
	if shErr != nil {
		t.Fatalf("Shutdown: %v", shErr)
	}
}
