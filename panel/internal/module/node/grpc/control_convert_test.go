package grpc

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

// 每个字段都取最长编码（负 UID 的 varint 10 字节、seq 取最大）时，每一块满员的全量名单都不落进 gRPC 1 MB 那一档发送缓冲
// （UsersChunkSize 的注释）。档位直接问 gRPC 的缺省缓冲池，升级 grpc-go 改了档位这里会发现。
func TestUsersFullChunkFitsSendBuffer(t *testing.T) {
	users := make([]service.RosterUser, 2*service.UsersChunkSize)
	for i := range users {
		users[i] = service.RosterUser{
			UID: math.MinInt64, UUID: uuid.New(), SpeedLimitKbps: math.MaxUint32, DeviceLimit: math.MaxUint32,
		}
	}
	r := &service.NodeRoster{NodeID: uuid.New(), Version: "u1-" + strings.Repeat("f", 64), Users: users}
	var chunks int
	for msg := range usersFullChunks(r) {
		chunks++
		msg.Seq = math.MaxUint64
		size := proto.Size(msg)
		buf := mem.DefaultBufferPool().Get(size)
		got := cap(*buf)
		mem.DefaultBufferPool().Put(buf)
		if got >= 1<<20 {
			t.Fatalf("第 %d 块编码后 %d 字节，占了 %d 字节的发送缓冲", chunks, size, got)
		}
	}
	if chunks != 2 {
		t.Fatalf("块数 %d，应为 2", chunks)
	}
}

// 空名单也发一块；中途不要了（发送失败）就不再转后面的块（迭代器在 yield 返回 false 后还调会 panic）。
func TestUsersFullChunksEmptyAndStop(t *testing.T) {
	var n int
	for msg := range usersFullChunks(&service.NodeRoster{NodeID: uuid.New(), Version: "u1-empty"}) {
		if uf := msg.GetUsersFull(); uf.GetChunkTotal() != 1 || len(uf.GetUsers()) != 0 {
			t.Fatalf("空名单块：total=%d users=%d", uf.GetChunkTotal(), len(uf.GetUsers()))
		}
		n++
	}
	if n != 1 {
		t.Fatalf("空名单发了 %d 块", n)
	}
	r := &service.NodeRoster{NodeID: uuid.New(), Version: "u1-big", Users: make([]service.RosterUser, 3*service.UsersChunkSize)}
	n = 0
	for range usersFullChunks(r) {
		n++
		break
	}
	if n != 1 {
		t.Fatalf("break 后还在出块：%d", n)
	}
}

func TestUpgradeReportFromProto(t *testing.T) {
	job := uuid.MustParse("0190a000-0000-7000-8000-000000000099")
	cases := []struct {
		name string
		in   *nodev1.UpgradeReport
		ok   bool
		want service.UpgradeReport
	}{
		{
			name: "staged",
			in: &nodev1.UpgradeReport{
				JobId: job.String(), Phase: nodev1.UpgradePhase_UPGRADE_PHASE_STAGED,
				ReasonCode: "rc", Detail: "det",
			},
			ok:   true,
			want: service.UpgradeReport{JobID: job, Phase: service.UpgradePhaseStaged, ReasonCode: "rc", Detail: "det"},
		},
		{
			name: "failed",
			in:   &nodev1.UpgradeReport{JobId: job.String(), Phase: nodev1.UpgradePhase_UPGRADE_PHASE_FAILED},
			ok:   true,
			want: service.UpgradeReport{JobID: job, Phase: service.UpgradePhaseFailed},
		},
		{
			name: "rolled_back",
			in:   &nodev1.UpgradeReport{JobId: job.String(), Phase: nodev1.UpgradePhase_UPGRADE_PHASE_ROLLED_BACK},
			ok:   true,
			want: service.UpgradeReport{JobID: job, Phase: service.UpgradePhaseRolledBack},
		},
		{name: "empty job", in: &nodev1.UpgradeReport{Phase: nodev1.UpgradePhase_UPGRADE_PHASE_STAGED}, ok: false},
		{name: "bad job", in: &nodev1.UpgradeReport{JobId: "not-a-uuid", Phase: nodev1.UpgradePhase_UPGRADE_PHASE_STAGED}, ok: false},
		{name: "unspecified", in: &nodev1.UpgradeReport{JobId: job.String(), Phase: nodev1.UpgradePhase_UPGRADE_PHASE_UNSPECIFIED}, ok: false},
		{name: "unknown phase", in: &nodev1.UpgradeReport{JobId: job.String(), Phase: nodev1.UpgradePhase(99)}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := upgradeReportFromProto(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok=%v 期望 %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got.JobID != tc.want.JobID || got.Phase != tc.want.Phase || got.ReasonCode != tc.want.ReasonCode || got.Detail != tc.want.Detail {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestHeartbeatFromProtoCert(t *testing.T) {
	id := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	notAfter := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	next := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	hb := heartbeatFromProto(&nodev1.Heartbeat{
		Nodes: []*nodev1.NodeRuntime{{
			NodeId: id.String(),
			Status: nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING,
			Cert: &nodev1.CertStatus{
				Identifier:      "node.example.com",
				NotAfterUnix:    notAfter.Unix(),
				ErrorCode:       "challenge_failed",
				ErrorDetail:     "detail",
				NextAttemptUnix: next.Unix(),
			},
		}},
	})
	if len(hb.Nodes) != 1 {
		t.Fatalf("nodes=%d", len(hb.Nodes))
	}
	rt := hb.Nodes[0]
	if rt.Cert == nil {
		t.Fatal("Cert 应为非 nil")
	}
	if rt.Cert.Identifier != "node.example.com" || rt.Cert.ErrorCode != "challenge_failed" || rt.Cert.ErrorDetail != "detail" {
		t.Fatalf("Cert 字符串: %+v", rt.Cert)
	}
	if rt.Cert.NotAfter == nil || !rt.Cert.NotAfter.Equal(notAfter) {
		t.Fatalf("NotAfter=%v want %v", rt.Cert.NotAfter, notAfter)
	}
	if rt.Cert.NextAttempt == nil || !rt.Cert.NextAttempt.Equal(next) {
		t.Fatalf("NextAttempt=%v want %v", rt.Cert.NextAttempt, next)
	}

	hb0 := heartbeatFromProto(&nodev1.Heartbeat{
		Nodes: []*nodev1.NodeRuntime{{
			NodeId: id.String(),
			Status: nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING,
			Cert:   &nodev1.CertStatus{NotAfterUnix: 0, NextAttemptUnix: 0},
		}},
	})
	if hb0.Nodes[0].Cert == nil {
		t.Fatal("Cert 应为非 nil")
	}
	if hb0.Nodes[0].Cert.NotAfter != nil || hb0.Nodes[0].Cert.NextAttempt != nil {
		t.Fatalf("0 应转 nil: NotAfter=%v NextAttempt=%v", hb0.Nodes[0].Cert.NotAfter, hb0.Nodes[0].Cert.NextAttempt)
	}
}

// 节点端自报的时间戳越界（毫秒误写成秒、int64 极值、负数）当没报，整拍运行状态仍能序列化（certs 审查 S6）。
func TestHeartbeatFromProtoCertClampsTimestamps(t *testing.T) {
	id := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	for _, u := range []int64{math.MaxInt64, math.MinInt64, -1, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC).Unix()} {
		hb := heartbeatFromProto(&nodev1.Heartbeat{
			Nodes: []*nodev1.NodeRuntime{{
				NodeId: id.String(),
				Status: nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING,
				Cert:   &nodev1.CertStatus{Identifier: "node.example.com", NotAfterUnix: u, NextAttemptUnix: u},
			}},
		})
		c := hb.Nodes[0].Cert
		if c == nil || c.NotAfter != nil || c.NextAttempt != nil {
			t.Fatalf("u=%d 应转 nil: %+v", u, c)
		}
		if _, err := json.Marshal(hb.Nodes); err != nil {
			t.Fatalf("u=%d 序列化失败: %v", u, err)
		}
	}
}

func TestHelloFromProtoUpgradeFields(t *testing.T) {
	job := uuid.MustParse("0190a000-0000-7000-8000-000000000088")
	h := helloFromProto(&nodev1.Hello{
		BuildSerial: 1792540800,
		LastUpgrade: &nodev1.UpgradeReport{
			JobId: job.String(), Phase: nodev1.UpgradePhase_UPGRADE_PHASE_ROLLED_BACK, ReasonCode: "confirm_timeout",
		},
	})
	if h.BuildSerial != 1792540800 {
		t.Fatalf("BuildSerial=%d", h.BuildSerial)
	}
	if h.LastUpgrade == nil {
		t.Fatal("LastUpgrade 应为非 nil")
	}
	if h.LastUpgrade.JobID != job || h.LastUpgrade.Phase != service.UpgradePhaseRolledBack || h.LastUpgrade.ReasonCode != "confirm_timeout" {
		t.Fatalf("LastUpgrade=%+v", h.LastUpgrade)
	}

	h2 := helloFromProto(&nodev1.Hello{
		BuildSerial: 1,
		LastUpgrade: &nodev1.UpgradeReport{JobId: "bad", Phase: nodev1.UpgradePhase_UPGRADE_PHASE_STAGED},
	})
	if h2.LastUpgrade != nil {
		t.Fatalf("非法 LastUpgrade 应为 nil，得到 %+v", h2.LastUpgrade)
	}

	h3 := helloFromProto(&nodev1.Hello{BuildSerial: 2})
	if h3.LastUpgrade != nil {
		t.Fatal("无 LastUpgrade 应为 nil")
	}
}

func TestOutboundUpgradeToProto(t *testing.T) {
	job := uuid.MustParse("0190a000-0000-7000-8000-000000000077")
	manifest := []byte("{}")
	sig := []byte("sig")
	msgs := outboundToProto(service.Outbound{
		Kind: service.OutUpgrade,
		Upgrade: service.UpgradeCommand{
			JobID: job, Manifest: manifest, Signature: sig,
			BinaryURL: "https://panel.example.com/pdnd/bin/pandora-native-linux-amd64", ConfirmWithinSec: 180,
		},
	})
	if len(msgs) != 1 {
		t.Fatalf("消息数 %d", len(msgs))
	}
	u := msgs[0].GetCommand().GetUpgrade()
	if u == nil {
		t.Fatal("期望 Upgrade command")
	}
	if u.GetJobId() != job.String() {
		t.Fatalf("job_id=%q", u.GetJobId())
	}
	if !bytes.Equal(u.GetManifest(), manifest) || !bytes.Equal(u.GetSignature(), sig) {
		t.Fatalf("manifest/sig 不符")
	}
	if u.GetBinaryUrl() != "https://panel.example.com/pdnd/bin/pandora-native-linux-amd64" || u.GetConfirmWithinSec() != 180 {
		t.Fatalf("url/sec 不符 url=%q sec=%d", u.GetBinaryUrl(), u.GetConfirmWithinSec())
	}
}
