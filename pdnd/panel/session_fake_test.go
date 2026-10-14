package panel

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func newTestIdentity(t *testing.T, pki *testPKI, serverID string, notAfter time.Time) *Identity {
	t.Helper()
	dir := t.TempDir()
	keyPEM, csr, err := newKeyAndCSR(serverID)
	if err != nil {
		t.Fatal(err)
	}
	cert := pki.signCSR(t, csr, serverID, notAfter)
	if err := stageAndPromote(dir, map[string][]byte{
		certFile:     cert,
		keyFile:      keyPEM,
		caFile:       pki.caPEM,
		serverIDFile: []byte(serverID + "\n"),
	}); err != nil {
		t.Fatal(err)
	}
	id, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newServerID() string { return uuid.NewString() }

// fakeStream 是测试里驱动假面板一侧的流包装。
type fakeStream struct {
	t    *testing.T
	grpc grpc.BidiStreamingServer[nodev1.SessionRequest, nodev1.SessionResponse]

	HelloMsg *nodev1.Hello
	done     chan struct{}
	endOnce  sync.Once
	seq      uint64

	mu   sync.Mutex
	msgs []*nodev1.SessionRequest
}

func (st *fakeStream) Send(body *nodev1.SessionResponse) error {
	st.mu.Lock()
	st.seq++
	body.Seq = st.seq
	st.mu.Unlock()
	return st.grpc.Send(body)
}

func (st *fakeStream) SendWelcome(push, hb uint32) error {
	return st.Send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Welcome{Welcome: &nodev1.Welcome{
		SessionId: "sess-1", PushIntervalSec: push, HeartbeatIntervalSec: hb,
	}}})
}

func (st *fakeStream) SendConfig(c *nodev1.ServerConfig) error {
	return st.Send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Config{Config: c}})
}

func (st *fakeStream) SendUsersFull(m *nodev1.UsersFull) error {
	return st.Send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_UsersFull{UsersFull: m}})
}

func (st *fakeStream) SendUsersDelta(m *nodev1.UsersDelta) error {
	return st.Send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_UsersDelta{UsersDelta: m}})
}

func (st *fakeStream) SendTrafficAck(id string) error {
	return st.Send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_TrafficAck{
		TrafficAck: &nodev1.TrafficAck{ReportId: id},
	}})
}

func (st *fakeStream) SendCertificate(chainPEM, caPEM []byte) error {
	return st.Send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Certificate{Certificate: &nodev1.Certificate{
		CertChainPem: chainPEM, CaBundlePem: caPEM,
	}}})
}

func (st *fakeStream) SendReconnect(inMs, jitterMs uint32) error {
	return st.Send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Command{Command: &nodev1.Command{
		Kind: &nodev1.Command_Reconnect{Reconnect: &nodev1.Reconnect{InMs: inMs, JitterMs: jitterMs}},
	}}})
}

func (st *fakeStream) SendTombstone(reason string) error {
	return st.Send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Command{Command: &nodev1.Command{
		Kind: &nodev1.Command_Tombstone{Tombstone: &nodev1.Tombstone{Reason: reason}},
	}}})
}

func (st *fakeStream) Recv() (*nodev1.SessionRequest, error) {
	m, err := st.grpc.Recv()
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	st.msgs = append(st.msgs, proto.Clone(m).(*nodev1.SessionRequest))
	st.mu.Unlock()
	return m, nil
}

func (st *fakeStream) Expect(t *testing.T, kind string) proto.Message {
	t.Helper()
	m, err := st.Recv()
	if err != nil {
		t.Fatalf("Expect %s: %v", kind, err)
	}
	var got string
	var body proto.Message
	switch b := m.GetBody().(type) {
	case *nodev1.SessionRequest_Hello:
		got, body = "Hello", b.Hello
	case *nodev1.SessionRequest_Heartbeat:
		got, body = "Heartbeat", b.Heartbeat
	case *nodev1.SessionRequest_Traffic:
		got, body = "Traffic", b.Traffic
	case *nodev1.SessionRequest_Online:
		got, body = "Online", b.Online
	case *nodev1.SessionRequest_ConfigAck:
		got, body = "ConfigAck", b.ConfigAck
	case *nodev1.SessionRequest_UsersAck:
		got, body = "UsersAck", b.UsersAck
	case *nodev1.SessionRequest_Renew:
		got, body = "Renew", b.Renew
	default:
		t.Fatalf("Expect %s: 未知 %T", kind, m.GetBody())
	}
	if got != kind {
		t.Fatalf("Expect %s: 收到 %s", kind, got)
	}
	return body
}

func (st *fakeStream) snapshot() []*nodev1.SessionRequest {
	st.mu.Lock()
	out := append([]*nodev1.SessionRequest(nil), st.msgs...)
	st.mu.Unlock()
	return out
}

// end 结束这条流的脚本；测试与假面板都可能调，幂等。
func (st *fakeStream) end() { st.endOnce.Do(func() { close(st.done) }) }

func leafURIMatches(leaf *x509.Certificate, serverID string) bool {
	if leaf == nil {
		return false
	}
	want := "spiffe://pandora/server/" + serverID
	for _, u := range leaf.URIs {
		if u.String() == want {
			return true
		}
	}
	return false
}

type fakeStreamRecord struct {
	requestID  string
	leafSerial string
	helloErr   error
	firstHello *nodev1.Hello
	msgs       []*nodev1.SessionRequest
}

// fakeControl 记录每条控制流并执行可配置脚本。
type fakeControl struct {
	nodev1.UnimplementedControlServiceServer

	script   func(st *fakeStream) error
	streamCh chan *fakeStream

	mu          sync.Mutex
	streamCount int
	records     []fakeStreamRecord
}

func newFakeControl(script func(st *fakeStream) error) *fakeControl {
	return &fakeControl{
		script:   script,
		streamCh: make(chan *fakeStream, 16),
	}
}

func (fc *fakeControl) Session(stream grpc.BidiStreamingServer[nodev1.SessionRequest, nodev1.SessionResponse]) error {
	ctx := stream.Context()
	reqID := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-request-id"); len(v) > 0 {
			reqID = v[0]
		}
	}
	serial := ""
	if leaf := peerLeaf(ctx); leaf != nil {
		serial = leaf.SerialNumber.String()
	}

	st := &fakeStream{grpc: stream, done: make(chan struct{})}
	fc.mu.Lock()
	fc.streamCount++
	idx := len(fc.records)
	fc.records = append(fc.records, fakeStreamRecord{requestID: reqID, leafSerial: serial})
	fc.mu.Unlock()

	first, err := stream.Recv()
	if err != nil {
		fc.storeRecord(idx, nil, err)
		return err
	}
	h := first.GetHello()
	if h == nil {
		e := errors.New("第一条消息不是 Hello")
		fc.storeRecord(idx, nil, e)
		return e
	}
	st.HelloMsg = proto.Clone(h).(*nodev1.Hello)
	st.mu.Lock()
	st.msgs = append(st.msgs, proto.Clone(first).(*nodev1.SessionRequest))
	st.mu.Unlock()
	fc.mu.Lock()
	fc.records[idx].firstHello = st.HelloMsg
	fc.mu.Unlock()

	if fc.script != nil {
		if err := fc.script(st); err != nil {
			st.end()
			fc.storeRecord(idx, st.snapshot(), err)
			return err
		}
	}
	st.end()
	fc.storeRecord(idx, st.snapshot(), nil)
	return nil
}

func (fc *fakeControl) storeRecord(idx int, msgs []*nodev1.SessionRequest, helloErr error) {
	fc.mu.Lock()
	if helloErr != nil {
		fc.records[idx].helloErr = helloErr
	}
	if msgs != nil {
		fc.records[idx].msgs = msgs
	}
	fc.mu.Unlock()
}

func (fc *fakeControl) streams() int {
	fc.mu.Lock()
	n := fc.streamCount
	fc.mu.Unlock()
	return n
}

func (fc *fakeControl) requestIDs() []string {
	fc.mu.Lock()
	out := make([]string, len(fc.records))
	for i, r := range fc.records {
		out[i] = r.requestID
	}
	fc.mu.Unlock()
	return out
}

func (fc *fakeControl) leafSerials() []string {
	fc.mu.Lock()
	out := make([]string, len(fc.records))
	for i, r := range fc.records {
		out[i] = r.leafSerial
	}
	fc.mu.Unlock()
	return out
}

// newHandoffControl 把每条新流交给测试 goroutine，脚本阻塞到 st.end。
func newHandoffControl() *fakeControl {
	fc := &fakeControl{streamCh: make(chan *fakeStream, 16)}
	fc.script = func(st *fakeStream) error {
		fc.streamCh <- st
		<-st.done
		return nil
	}
	return fc
}

func (fc *fakeControl) firstHello(i int) *nodev1.Hello {
	fc.mu.Lock()
	var h *nodev1.Hello
	if i < len(fc.records) {
		h = fc.records[i].firstHello
	}
	fc.mu.Unlock()
	return h
}

func (fc *fakeControl) takeStream(t *testing.T, timeout time.Duration) *fakeStream {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case st := <-fc.streamCh:
		st.t = t
		return st
	case <-timer.C:
		t.Fatal("等控制流超时")
		return nil
	}
}

func newSessionFixture(t *testing.T, pki *testPKI, id *Identity, fc *fakeControl, mut func(*SessionOptions)) (*Session, context.CancelFunc, <-chan error) {
	t.Helper()
	dialer := startFakeGRPC(t, pki.serverTLS(t, "127.0.0.1"), nil, fc)
	opts := SessionOptions{
		Address:      "127.0.0.1:9443",
		Identity:     id,
		AgentVersion: "test",
		BinarySHA256: "abc",
		Applied: func() ([]*nodev1.AppliedConfig, []*nodev1.AppliedUsers) {
			return []*nodev1.AppliedConfig{{NodeId: "n1", ContentSha256: "aa"}},
				[]*nodev1.AppliedUsers{{NodeId: "n1", Version: "u1-x"}}
		},
		Logger: slog.New(slog.DiscardHandler),
		Dialer: dialer,
		Rand:   func(n int64) int64 { return 0 },
	}
	if mut != nil {
		mut(&opts)
	}
	s, err := NewSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errc:
		case <-time.After(5 * time.Second):
			t.Error("Run 未在 5s 内结束")
		}
	})
	return s, cancel, errc
}

func nextEvent(t *testing.T, s *Session, kind EventKind) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == kind {
				return ev
			}
			if ev.Kind != EventDisconnected {
				t.Fatalf("等 %d，收到 %d", kind, ev.Kind)
			}
		case <-deadline:
			t.Fatalf("等 EventKind %d 超时", kind)
		}
	}
}

func waitStreams(t *testing.T, fc *fakeControl, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if fc.streams() >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("等 %d 条流，只有 %d", n, fc.streams())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
