// Command acceptancepanel 是 runtime-acceptance.sh 的模拟面板（gRPC）。
//
// 实现面板的两个服务：EnrollmentService（一次性令牌 + CSR 换客户端证书）与 ControlService
// （mTLS 会话：Hello → Welcome，按 Hello 报的已应用版本决定发不发配置与名单，收心跳、回执、
// 流量并回 TrafficAck，会话内续期）。观测到的计数写进状态文件，脚本据此判定。
//
// 为什么是 Go：要 gRPC、mTLS 与证书签发，Go 本来就是构建前提，交叉编译一下就能带到没有 Go 的验收机上。
//
// CA 指纹、证书、会话规则按面板契约独立实现，刻意不 import pdnd/panel：夹具借用被测代码的实现，
// 两边一起错时验收照样是绿的。只有 main_test.go 用节点自己的客户端与它对打。
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// usersVersion 是模拟面板下发的名单版本。
const usersVersion = "u1-acceptance"

// State 是写进状态文件的观测计数。
type State struct {
	Enrolls                 int      `json:"enrolls"`
	EnrollRejections        int      `json:"enroll_rejections"`
	Sessions                int      `json:"sessions"`
	Hellos                  int      `json:"hellos"`
	HellosWithAppliedConfig int      `json:"hellos_with_applied_config"`
	Heartbeats              int      `json:"heartbeats"`
	HeartbeatsWithMetrics   int      `json:"heartbeats_with_metrics"`
	ConfigPhases            []string `json:"config_phases"`
	UsersAcksOK             int      `json:"users_acks_ok"`
	UsersAcksFailed         int      `json:"users_acks_failed"`
	TrafficReports          int      `json:"traffic_reports"`
	OnlineReports           int      `json:"online_reports"`
	Renewals                int      `json:"renewals"`
	BadClientCerts          int      `json:"bad_client_certs"`
	MissingRequestIDs       int      `json:"missing_request_ids"`
	LastRuntime             []string `json:"last_runtime"`
}

type options struct {
	serverName string
	nodePort   int
	statePath  string
	token      string
}

type fakePanel struct {
	nodev1.UnimplementedEnrollmentServiceServer
	nodev1.UnimplementedControlServiceServer

	pki        *pki
	creds      credentials.TransportCredentials
	statePath  string
	token      string
	serverID   string
	nodeID     string
	userUUID   string
	configJSON []byte
	configSHA  string

	mu sync.Mutex
	// issued 按 request_id 记已签发的证书（同一 request_id 重放返回同一张）；tokenUsedBy 是令牌被哪个 request_id 用了。
	issued      map[string][]byte
	tokenUsedBy string
	state       State
}

func newFakePanel(o options) (*fakePanel, error) {
	if o.nodePort <= 0 || o.nodePort > 65535 {
		return nil, fmt.Errorf("node-port 非法: %d", o.nodePort)
	}
	if strings.TrimSpace(o.token) == "" {
		return nil, errors.New("令牌为空")
	}
	p, err := newPKI()
	if err != nil {
		return nil, err
	}
	cfg, err := p.tlsConfig(o.serverName)
	if err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(map[string]any{"server_port": o.nodePort})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(configJSON)
	return &fakePanel{
		pki:        p,
		creds:      credentials.NewTLS(cfg),
		statePath:  o.statePath,
		token:      strings.TrimSpace(o.token),
		serverID:   uuid.NewString(),
		nodeID:     uuid.NewString(),
		userUUID:   uuid.NewString(),
		configJSON: configJSON,
		configSHA:  hex.EncodeToString(sum[:]),
		issued:     make(map[string][]byte),
		state:      State{ConfigPhases: []string{}, LastRuntime: []string{}},
	}, nil
}

func (p *fakePanel) caFingerprint() string { return p.pki.fingerprint() }

// serve 在 lis 上跑 gRPC，直到 lis 关闭。
func (p *fakePanel) serve(lis net.Listener) error {
	srv := grpc.NewServer(grpc.Creds(p.creds))
	nodev1.RegisterEnrollmentServiceServer(srv, p)
	nodev1.RegisterControlServiceServer(srv, p)
	return srv.Serve(lis)
}

// Enroll：令牌对且没被别的 request_id 用过才签；同一 request_id 重放返回同一张证书。
func (p *fakePanel) Enroll(_ context.Context, req *nodev1.EnrollRequest) (*nodev1.EnrollResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	reject := func(msg string) (*nodev1.EnrollResponse, error) {
		p.state.EnrollRejections++
		p.saveLocked()
		return nil, status.Error(codes.PermissionDenied, msg)
	}
	if subtle.ConstantTimeCompare([]byte(req.GetToken()), []byte(p.token)) != 1 {
		return reject("令牌无效")
	}
	rid := req.GetRequestId()
	if rid == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id 为空")
	}
	if cert, ok := p.issued[rid]; ok {
		return p.enrollResponse(cert), nil
	}
	if p.tokenUsedBy != "" {
		return reject("令牌已用过")
	}
	cert, err := p.pki.signClient(req.GetCsrDer(), p.serverID)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	p.tokenUsedBy = rid
	p.issued[rid] = cert
	p.state.Enrolls++
	p.saveLocked()
	return p.enrollResponse(cert), nil
}

func (p *fakePanel) enrollResponse(cert []byte) *nodev1.EnrollResponse {
	return &nodev1.EnrollResponse{ServerId: p.serverID, CertChainPem: cert, CaBundlePem: p.pki.caPEM}
}

// Session 是控制会话。
func (p *fakePanel) Session(stream grpc.BidiStreamingServer[nodev1.SessionRequest, nodev1.SessionResponse]) error {
	ctx := stream.Context()
	p.record(func(s *State) { s.Sessions++ })
	if !p.clientCertOK(ctx) {
		p.record(func(s *State) { s.BadClientCerts++ })
		return status.Error(codes.Unauthenticated, "客户端证书不对")
	}
	if md, _ := metadata.FromIncomingContext(ctx); len(md.Get("x-request-id")) == 0 || md.Get("x-request-id")[0] == "" {
		p.record(func(s *State) { s.MissingRequestIDs++ })
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "第一条消息必须是 Hello")
	}
	p.record(func(s *State) { s.Hellos++ })

	var seq uint64
	send := func(r *nodev1.SessionResponse) error {
		seq++
		r.Seq = seq
		return stream.Send(r)
	}
	if err := send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Welcome{Welcome: &nodev1.Welcome{
		SessionId: uuid.NewString(), PushIntervalSec: 1, HeartbeatIntervalSec: 1, ServerTimeUnixMs: time.Now().UnixMilli(),
	}}}); err != nil {
		return err
	}
	if p.helloHasConfig(hello) {
		p.record(func(s *State) { s.HellosWithAppliedConfig++ })
	} else if err := send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Config{Config: &nodev1.ServerConfig{
		ReleaseId: p.configSHA[:16],
		Nodes:     []*nodev1.NodeConfig{{NodeId: p.nodeID, NodeType: "socks", ConfigJson: p.configJSON, ContentSha256: p.configSHA}},
	}}}); err != nil {
		return err
	}
	if !p.helloHasUsers(hello) {
		if err := send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_UsersFull{UsersFull: &nodev1.UsersFull{
			NodeId: p.nodeID, Version: usersVersion, ChunkIndex: 0, ChunkTotal: 1,
			Users: []*nodev1.User{{Uid: 1, Uuid: p.userUUID}},
		}}}); err != nil {
			return err
		}
	}

	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		switch body := req.GetBody().(type) {
		case *nodev1.SessionRequest_Heartbeat:
			hb := body.Heartbeat
			p.record(func(s *State) {
				s.Heartbeats++
				if hb.GetMetrics() != nil {
					s.HeartbeatsWithMetrics++
				}
				s.LastRuntime = s.LastRuntime[:0]
				for _, n := range hb.GetNodes() {
					s.LastRuntime = append(s.LastRuntime, n.GetStatus().String())
				}
			})
		case *nodev1.SessionRequest_ConfigAck:
			phase := body.ConfigAck.GetPhase().String()
			p.record(func(s *State) { s.ConfigPhases = append(s.ConfigPhases, phase) })
		case *nodev1.SessionRequest_UsersAck:
			ok := body.UsersAck.GetOk()
			p.record(func(s *State) {
				if ok {
					s.UsersAcksOK++
				} else {
					s.UsersAcksFailed++
				}
			})
		case *nodev1.SessionRequest_Traffic:
			p.record(func(s *State) { s.TrafficReports++ })
			if err := send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_TrafficAck{
				TrafficAck: &nodev1.TrafficAck{ReportId: body.Traffic.GetReportId()},
			}}); err != nil {
				return err
			}
		case *nodev1.SessionRequest_Online:
			p.record(func(s *State) { s.OnlineReports++ })
		case *nodev1.SessionRequest_Renew:
			cert, err := p.pki.signClient(body.Renew.GetCsrDer(), p.serverID)
			if err != nil {
				return status.Error(codes.InvalidArgument, err.Error())
			}
			p.record(func(s *State) { s.Renewals++ })
			if err := send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Certificate{
				Certificate: &nodev1.Certificate{CertChainPem: cert, CaBundlePem: p.pki.caPEM},
			}}); err != nil {
				return err
			}
		}
	}
}

// clientCertOK：带了本 CA 签的客户端证书（TLS 层已验链），且 URI SAN 是本服务器。
func (p *fakePanel) clientCertOK(ctx context.Context) bool {
	pr, ok := peer.FromContext(ctx)
	if !ok {
		return false
	}
	info, ok := pr.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.PeerCertificates) == 0 {
		return false
	}
	want := "spiffe://pandora/server/" + p.serverID
	for _, u := range info.State.PeerCertificates[0].URIs {
		if u.String() == want {
			return true
		}
	}
	return false
}

func (p *fakePanel) helloHasConfig(h *nodev1.Hello) bool {
	for _, a := range h.GetAppliedConfigs() {
		if a.GetNodeId() == p.nodeID && a.GetContentSha256() == p.configSHA {
			return true
		}
	}
	return false
}

func (p *fakePanel) helloHasUsers(h *nodev1.Hello) bool {
	for _, a := range h.GetAppliedUsers() {
		if a.GetNodeId() == p.nodeID && a.GetVersion() == usersVersion {
			return true
		}
	}
	return false
}

// record 改状态并立即落盘：脚本随时可能读，也可能在任意时刻杀掉本进程。
func (p *fakePanel) record(change func(*State)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(&p.state)
	p.saveLocked()
}

// snapshot 返回状态的一份拷贝（测试用）。
func (p *fakePanel) snapshot() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state
	s.ConfigPhases = append([]string(nil), s.ConfigPhases...)
	s.LastRuntime = append([]string(nil), s.LastRuntime...)
	return s
}

// saveLocked 原子写状态文件（写临时文件再 rename）；statePath 为空不写。
func (p *fakePanel) saveLocked() {
	if p.statePath == "" {
		return
	}
	raw, _ := json.Marshal(p.state)
	tmp, err := os.CreateTemp(filepath.Dir(p.statePath), ".state-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), p.statePath); err != nil {
		_ = os.Remove(tmp.Name())
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "监听地址")
	serverName := flag.String("server-name", "127.0.0.1", "面板证书的主机名或 IP")
	nodePort := flag.Int("node-port", 0, "下发给节点的入站端口（必填）")
	statePath := flag.String("state", "", "状态文件（必填）")
	tokenFile := flag.String("token-file", "", "接入令牌文件（必填）")
	fpOut := flag.String("ca-fingerprint-out", "", "CA 指纹写到这个文件（必填）")
	flag.Parse()
	if *statePath == "" || *tokenFile == "" || *fpOut == "" || *nodePort == 0 {
		log.Fatal("-node-port、-state、-token-file、-ca-fingerprint-out 都必填")
	}
	token, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	p, err := newFakePanel(options{serverName: *serverName, nodePort: *nodePort, statePath: *statePath, token: string(token)})
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*fpOut, []byte(p.caFingerprint()+"\n"), 0o600); err != nil {
		log.Fatal(err)
	}
	p.mu.Lock()
	p.saveLocked()
	p.mu.Unlock()
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("acceptance panel listening on %s", lis.Addr())
	log.Fatal(p.serve(lis))
}
