package grpc

// ControlService.Session 加固行为：登记后复核、结束宽限、trailer、流量与续期。

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

func TestSessionTombstoneSetsReenrollTrailer(t *testing.T) {
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
	if ts == nil || ts.GetTombstone() == nil {
		t.Fatalf("期望 Tombstone command，得到 %v", cmd)
	}
	if st, ok := status.FromError(recvErr(t, stream)); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("期望 PermissionDenied，得到 %v", st)
	}
	if got := stream.Trailer().Get(TrailerReenroll); got == nil || len(got) != 1 || got[0] != "required" {
		t.Fatalf("trailer %s=%v", TrailerReenroll, got)
	}
}

func TestSessionRecheckRevokedTombstones(t *testing.T) {
	b := newFakeBackend()
	client, _ := startControlAuth(t, b, testSessionOpt, true, &fakeAuth{err: service.ErrSessionRevoked})
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// 第一条仍是 Welcome（terminate 保留没发出的 Welcome），紧接着是 Tombstone
	drainWelcome(t, stream)
	cmd := recvResp(t, stream)
	ts := cmd.GetCommand()
	if ts == nil || ts.GetTombstone() == nil || ts.GetTombstone().GetReason() != service.TombstoneIdentityRevoked {
		t.Fatalf("期望 identity_revoked Tombstone，得到 %v", cmd)
	}
	if st, ok := status.FromError(recvErr(t, stream)); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("期望 PermissionDenied，得到 %v", st)
	}
	if got := stream.Trailer().Get(TrailerReenroll); got == nil || len(got) != 1 || got[0] != "required" {
		t.Fatalf("trailer %s=%v", TrailerReenroll, got)
	}
}

func TestSessionRecheckErrorUnavailable(t *testing.T) {
	b := newFakeBackend()
	client, _ := startControlAuth(t, b, testSessionOpt, true, &fakeAuth{err: errors.New("db down")})
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	_ = stream.Send(helloMsg(&nodev1.Hello{}))
	if st, ok := status.FromError(recvErr(t, stream)); !ok || st.Code() != codes.Unavailable {
		t.Fatalf("期望 Unavailable，得到 %v", st)
	}
}

func TestSessionEndsWhenNodeNeverReads(t *testing.T) {
	nodeID := uuid.MustParse("0190a000-0000-7000-8000-000000000004")
	users := make([]service.RosterUser, 12001)
	for i := range users {
		uid := int64(i + 1)
		users[i] = service.RosterUser{UID: uid, UUID: uuid.NewSHA1(uuid.Nil, []byte{byte(uid)})}
	}
	b := newFakeBackend()
	b.setConfigs(testServerID, []service.NodeConfigDoc{mkDoc(nodeID, 'c')})
	b.setRosters(testServerID, []service.NodeRoster{{NodeID: nodeID, Version: "u1-big", Users: users}})

	opt := testSessionOpt
	opt.TerminateGrace = 300 * time.Millisecond
	client, m := startControl(t, b, opt, true)
	ctx, cancel := streamCtx(t)
	t.Cleanup(cancel)
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return m.Len() == 1 }, "会话登记")
	m.Tombstone(testServerID, service.TombstoneIdentityRevoked)
	for i := 0; i < 3; i++ {
		_ = stream.Send(&nodev1.SessionRequest{
			Seq: uint64(i + 2),
			Body: &nodev1.SessionRequest_Traffic{Traffic: &nodev1.TrafficReport{
				ReportId: "r-after-end",
				Entries:  []*nodev1.TrafficEntry{{NodeId: nodeID.String(), Uid: 1, Upload: 1}},
			}},
		})
	}
	deadline := opt.TerminateGrace + sessionEndGrace + 2*time.Second
	waitFor(t, deadline, func() bool { return m.Len() == 0 }, "会话移出登记")
	b.mu.Lock()
	n := len(b.ingested)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("终止后流量不应入账，ingested=%d", n)
	}
}

func TestTrafficRightAfterWelcomeIsCharged(t *testing.T) {
	nodeID := uuid.MustParse("0190a000-0000-7000-8000-000000000099")
	b := newFakeBackend()
	b.setConfigs(testServerID, []service.NodeConfigDoc{mkDoc(nodeID, 'd')})
	b.setRosters(testServerID, []service.NodeRoster{mkRoster(nodeID, "u1-a", 7)})

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
	w := recvResp(t, stream)
	if w.GetWelcome() == nil {
		t.Fatal("期望 Welcome")
	}
	if err := stream.Send(&nodev1.SessionRequest{
		Seq: 2,
		Body: &nodev1.SessionRequest_Traffic{Traffic: &nodev1.TrafficReport{
			ReportId: "r-welcome-edge",
			Entries:  []*nodev1.TrafficEntry{{NodeId: nodeID.String(), Uid: 7, Upload: 1}},
		}},
	}); err != nil {
		t.Fatalf("Send traffic: %v", err)
	}
	for {
		resp := recvResp(t, stream)
		if ack := resp.GetTrafficAck(); ack != nil {
			if ack.GetReportId() != "r-welcome-edge" {
				t.Fatalf("report_id=%q", ack.GetReportId())
			}
			break
		}
	}
	b.mu.Lock()
	has := append([]bool(nil), b.has...)
	b.mu.Unlock()
	if len(has) != 1 || !has[0] {
		t.Fatalf("has=%v", has)
	}
}

func TestRenewPassesStreamCertSerial(t *testing.T) {
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
	if cert.GetCertificate() == nil {
		t.Fatal("期望 Certificate")
	}
	b.mu.Lock()
	serials := append([]*big.Int(nil), b.renewSerials...)
	b.mu.Unlock()
	if len(serials) != 1 || serials[0].Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("renew serials=%v", serials)
	}
}

func TestTrafficFromProtoKeepsUnparsableNodeID(t *testing.T) {
	good := uuid.MustParse("0190a000-0000-7000-8000-0000000000aa")
	r, ok := trafficFromProto(&nodev1.TrafficReport{
		ReportId: "r-1",
		Entries: []*nodev1.TrafficEntry{
			{NodeId: good.String(), Uid: 1, Upload: 10, Download: 20},
			{NodeId: "bad", Uid: 2, Upload: 3, Download: 4},
		},
	})
	if !ok {
		t.Fatal("ok=false")
	}
	if len(r.Entries) != 2 {
		t.Fatalf("len=%d", len(r.Entries))
	}
	e := r.Entries[1]
	if e.NodeID != uuid.Nil || e.UID != 2 || e.Upload != 3 || e.Download != 4 {
		t.Fatalf("entry[1]=%+v", e)
	}
}
