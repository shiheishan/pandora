// Command scalepanel 是节点端性能闸门的模拟面板（gRPC + HTTP 控制口）。
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const usersFullChunkSize = 5000

type nodeEntry struct {
	proto      string
	nodeID     string
	nodeType   string
	configJSON []byte
	configSHA  string
}

// ConfigAckState 是某协议最近一次配置回执各阶段时刻。
type ConfigAckState struct {
	SwitchedUnixMs     int64  `json:"switched_unix_ms"`
	HealthPassedUnixMs int64  `json:"health_passed_unix_ms"`
	FailedUnixMs       int64  `json:"failed_unix_ms"`
	FailedReason       string `json:"failed_reason"`
}

// UsersAckState 是某协议最近一次名单回执。
type UsersAckState struct {
	Version string `json:"version"`
	OK      bool   `json:"ok"`
	Reason  string `json:"reason"`
	UnixMs  int64  `json:"unix_ms"`
}

// RosterStatus 是名单相关观测。
type RosterStatus struct {
	Gen             int                      `json:"gen"`
	Version         string                   `json:"version"`
	FullSentUnixMs  int64                    `json:"full_sent_unix_ms"`
	DeltaSentUnixMs int64                    `json:"delta_sent_unix_ms"`
	UsersFullSent   int                      `json:"users_full_sent"`
	UsersDeltaSent  int                      `json:"users_delta_sent"`
	Acks            map[string]UsersAckState `json:"acks"`
}

// Status 是 GET /status 的 JSON 形状（契约 §5.3.1）。
type Status struct {
	NowUnixMs           int64                     `json:"now_unix_ms"`
	Down                bool                      `json:"down"`
	Enrolls             int                       `json:"enrolls"`
	EnrollRejections    int                       `json:"enroll_rejections"`
	SessionsTotal       int                       `json:"sessions_total"`
	Active              bool                      `json:"active"`
	FirstHelloUnixMs    int64                     `json:"first_hello_unix_ms"`
	LastHelloUnixMs     int64                     `json:"last_hello_unix_ms"`
	ConfigsSent         int                       `json:"configs_sent"`
	ConfigSentUnixMs    int64                     `json:"config_sent_unix_ms"`
	ConfigAcks          map[string]ConfigAckState `json:"config_acks"`
	Roster              RosterStatus              `json:"roster"`
	UsersAcksOK         int                       `json:"users_acks_ok"`
	UsersAcksFailed     int                       `json:"users_acks_failed"`
	Heartbeats          int                       `json:"heartbeats"`
	LastHeartbeatUnixMs int64                     `json:"last_heartbeat_unix_ms"`
	Runtime             map[string]string         `json:"runtime"`
	TrafficReports      int                       `json:"traffic_reports"`
	TrafficEntries      int                       `json:"traffic_entries"`
	OnlineReports       int                       `json:"online_reports"`
	OnlineEntries       int                       `json:"online_entries"`
	Renewals            int                       `json:"renewals"`
}

type panel struct {
	nodev1.UnimplementedEnrollmentServiceServer
	nodev1.UnimplementedControlServiceServer

	pki      *pki
	creds    credentials.TransportCredentials
	prof     *profile.Profile
	token    string
	serverID string

	nodes     []nodeEntry
	nodeByID  map[string]string // node_id → proto
	releaseID string

	mu                  sync.Mutex
	gen                 int
	rosterVersion       string
	issued              map[string][]byte
	tokenUsedBy         string
	sessionsTotal       int
	enrolls             int
	enrollRejections    int
	configsSent         int
	configSentUnixMs    int64
	firstHelloUnixMs    int64
	lastHelloUnixMs     int64
	usersAcksOK         int
	usersAcksFailed     int
	heartbeats          int
	lastHeartbeatUnixMs int64
	trafficReports      int
	trafficEntries      int
	onlineReports       int
	onlineEntries       int
	renewals            int
	configAcks          map[string]ConfigAckState
	rosterAcks          map[string]UsersAckState
	runtime             map[string]string
	usersFullSent       int
	usersDeltaSent      int
	fullSentUnixMs      int64
	deltaSentUnixMs     int64

	down bool

	intervalSecPush uint32
	intervalSecHB   uint32

	activeMu     sync.Mutex
	activeSess   *session
	activeCancel context.CancelFunc

	grpcListen string
	grpcSrv    *grpc.Server
	grpcLis    net.Listener
}

type session struct {
	p      *panel
	stream grpc.BidiStreamingServer[nodev1.SessionRequest, nodev1.SessionResponse]
	sendMu sync.Mutex
	seq    uint64
}

func (s *session) send(resp *nodev1.SessionResponse) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.seq++
	resp.Seq = s.seq
	return s.stream.Send(resp)
}

func newPanel(prof *profile.Profile, token string, serverName string) (*panel, error) {
	token = stringsTrim(token)
	if token == "" {
		return nil, errors.New("令牌为空")
	}
	pki, err := newPKI()
	if err != nil {
		return nil, err
	}
	cfg, err := pki.tlsConfig(serverName)
	if err != nil {
		return nil, err
	}
	p := &panel{
		pki:           pki,
		creds:         credentials.NewTLS(cfg),
		prof:          prof,
		token:         token,
		serverID:      uuid.NewString(),
		nodeByID:      make(map[string]string),
		issued:        make(map[string][]byte),
		gen:           0,
		configAcks:    make(map[string]ConfigAckState),
		rosterAcks:    make(map[string]UsersAckState),
		runtime:       make(map[string]string),
		rosterVersion: prof.RosterVersion(0),
	}
	var releaseParts []byte
	for _, proto := range prof.Protocols {
		doc, sha, err := prof.ConfigDoc(proto)
		if err != nil {
			return nil, err
		}
		id := profile.NodeID(prof.Seed, proto)
		p.nodes = append(p.nodes, nodeEntry{
			proto: proto, nodeID: id, nodeType: profile.NodeType(proto),
			configJSON: doc, configSHA: sha,
		})
		p.nodeByID[id] = proto
		releaseParts = append(releaseParts, []byte(sha)...)
	}
	sum := sha256.Sum256(releaseParts)
	p.releaseID = hex.EncodeToString(sum[:])[:16]
	for _, n := range p.nodes {
		p.configAcks[n.proto] = ConfigAckState{}
		p.rosterAcks[n.proto] = UsersAckState{}
	}
	return p, nil
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

func (p *panel) caFingerprint() string { return p.pki.fingerprint() }

func (p *panel) setActiveSession(sess *session, cancel context.CancelFunc) {
	p.activeMu.Lock()
	if p.activeCancel != nil {
		p.activeCancel()
	}
	p.activeSess = sess
	p.activeCancel = cancel
	p.activeMu.Unlock()
}

func (p *panel) clearActiveSession(sess *session) {
	p.activeMu.Lock()
	if p.activeSess == sess {
		p.activeSess = nil
		p.activeCancel = nil
	}
	p.activeMu.Unlock()
}

func (p *panel) cancelActiveSession() bool {
	p.activeMu.Lock()
	if p.activeCancel == nil {
		p.activeMu.Unlock()
		return false
	}
	p.activeCancel()
	p.activeCancel = nil
	p.activeSess = nil
	p.activeMu.Unlock()
	return true
}

func (p *panel) hasActiveSession() bool {
	p.activeMu.Lock()
	ok := p.activeSess != nil
	p.activeMu.Unlock()
	return ok
}

func (p *panel) Enroll(_ context.Context, req *nodev1.EnrollRequest) (*nodev1.EnrollResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	reject := func(msg string) (*nodev1.EnrollResponse, error) {
		p.enrollRejections++
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
	p.enrolls++
	return p.enrollResponse(cert), nil
}

func (p *panel) enrollResponse(cert []byte) *nodev1.EnrollResponse {
	return &nodev1.EnrollResponse{ServerId: p.serverID, CertChainPem: cert, CaBundlePem: p.pki.caPEM}
}

func (p *panel) Session(stream grpc.BidiStreamingServer[nodev1.SessionRequest, nodev1.SessionResponse]) error {
	ctx := stream.Context()
	p.mu.Lock()
	p.sessionsTotal++
	p.mu.Unlock()

	if !p.clientCertOK(ctx) {
		return status.Error(codes.Unauthenticated, "客户端证书不对")
	}
	sessCtx, cancel := context.WithCancel(ctx)
	sess := &session{p: p, stream: stream}
	p.setActiveSession(sess, cancel)
	defer func() {
		cancel()
		p.clearActiveSession(sess)
	}()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "第一条消息必须是 Hello")
	}
	now := time.Now().UnixMilli()
	p.mu.Lock()
	if p.firstHelloUnixMs == 0 {
		p.firstHelloUnixMs = now
	}
	p.lastHelloUnixMs = now
	pushSec, hbSec := p.pushIntervalSec(), p.heartbeatIntervalSec()
	p.mu.Unlock()

	if err := sess.send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Welcome{Welcome: &nodev1.Welcome{
		SessionId: uuid.NewString(), PushIntervalSec: pushSec, HeartbeatIntervalSec: hbSec, ServerTimeUnixMs: now,
	}}}); err != nil {
		return err
	}

	if !helloHasAllConfigs(hello, p.nodes) {
		nodes := make([]*nodev1.NodeConfig, 0, len(p.nodes))
		for _, n := range p.nodes {
			nodes = append(nodes, &nodev1.NodeConfig{
				NodeId: n.nodeID, NodeType: n.nodeType, ConfigJson: n.configJSON, ContentSha256: n.configSHA,
			})
		}
		if err := sess.send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Config{Config: &nodev1.ServerConfig{
			ReleaseId: p.releaseID, Nodes: nodes,
		}}}); err != nil {
			return err
		}
		p.mu.Lock()
		p.configsSent++
		p.configSentUnixMs = time.Now().UnixMilli()
		p.mu.Unlock()
	}

	p.mu.Lock()
	curVer := p.rosterVersion
	gen := p.gen
	p.mu.Unlock()
	for _, n := range p.nodes {
		if helloNeedsUsers(hello, n.nodeID, curVer) {
			msgs := usersFullMessages(n.nodeID, curVer, p.prof.Roster(gen))
			for _, uf := range msgs {
				if err := sess.send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_UsersFull{UsersFull: uf}}); err != nil {
					return err
				}
				p.mu.Lock()
				p.usersFullSent++
				if p.fullSentUnixMs == 0 {
					p.fullSentUnixMs = time.Now().UnixMilli()
				}
				p.mu.Unlock()
			}
		}
	}

	return p.sessionLoop(sessCtx, sess)
}

func (p *panel) pushIntervalSec() uint32      { return p.intervalSecPush }
func (p *panel) heartbeatIntervalSec() uint32 { return p.intervalSecHB }

func (p *panel) setIntervals(push, hb time.Duration) {
	p.intervalSecPush = uint32(push / time.Second)
	p.intervalSecHB = uint32(hb / time.Second)
}

// recvResult 是后台 Recv 的一次结果。
type recvResult struct {
	req *nodev1.SessionRequest
	err error
}

// sessionLoop 处理节点发来的消息。Recv 放在后台 goroutine：会话被替换、fault/cut 取消 ctx 时
// handler 要立刻返回（返回后 gRPC 结束这条流，后台 Recv 随之出错退出）。
func (p *panel) sessionLoop(ctx context.Context, sess *session) error {
	stream := sess.stream
	recvCh := make(chan recvResult, 16)
	go func() {
		for {
			req, err := stream.Recv()
			select {
			case recvCh <- recvResult{req, err}:
			case <-stream.Context().Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		var req *nodev1.SessionRequest
		select {
		case <-ctx.Done():
			return status.Error(codes.Unavailable, "会话已被替换或切断")
		case r := <-recvCh:
			if r.err != nil {
				return r.err
			}
			req = r.req
		}
		switch body := req.GetBody().(type) {
		case *nodev1.SessionRequest_Heartbeat:
			hb := body.Heartbeat
			now := time.Now().UnixMilli()
			p.mu.Lock()
			p.heartbeats++
			p.lastHeartbeatUnixMs = now
			for _, n := range hb.GetNodes() {
				if proto, ok := p.nodeByID[n.GetNodeId()]; ok {
					p.runtime[proto] = n.GetStatus().String()
				}
			}
			p.mu.Unlock()
		case *nodev1.SessionRequest_ConfigAck:
			ack := body.ConfigAck
			proto := p.nodeByID[ack.GetNodeId()]
			if proto == "" {
				continue
			}
			now := time.Now().UnixMilli()
			p.mu.Lock()
			st := p.configAcks[proto]
			switch ack.GetPhase() {
			case nodev1.ConfigPhase_CONFIG_PHASE_SWITCHED:
				st.SwitchedUnixMs = now
			case nodev1.ConfigPhase_CONFIG_PHASE_HEALTH_PASSED:
				st.HealthPassedUnixMs = now
			case nodev1.ConfigPhase_CONFIG_PHASE_FAILED:
				st.FailedUnixMs = now
				st.FailedReason = ack.GetReasonCode()
			}
			p.configAcks[proto] = st
			p.mu.Unlock()
		case *nodev1.SessionRequest_UsersAck:
			ua := body.UsersAck
			proto := p.nodeByID[ua.GetNodeId()]
			if proto == "" {
				continue
			}
			now := time.Now().UnixMilli()
			p.mu.Lock()
			p.rosterAcks[proto] = UsersAckState{
				Version: ua.GetVersion(), OK: ua.GetOk(), Reason: ua.GetReasonCode(), UnixMs: now,
			}
			if ua.GetOk() {
				p.usersAcksOK++
			} else {
				p.usersAcksFailed++
			}
			p.mu.Unlock()
			if !ua.GetOk() {
				if err := p.sendFullToNode(sess, ua.GetNodeId()); err != nil {
					return err
				}
			}
		case *nodev1.SessionRequest_Traffic:
			tr := body.Traffic
			p.mu.Lock()
			p.trafficReports++
			p.trafficEntries += len(tr.GetEntries())
			p.mu.Unlock()
			if err := sess.send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_TrafficAck{
				TrafficAck: &nodev1.TrafficAck{ReportId: tr.GetReportId()},
			}}); err != nil {
				return err
			}
		case *nodev1.SessionRequest_Online:
			on := body.Online
			p.mu.Lock()
			p.onlineReports++
			p.onlineEntries += len(on.GetEntries())
			p.mu.Unlock()
		case *nodev1.SessionRequest_Renew:
			cert, err := p.pki.signClient(body.Renew.GetCsrDer(), p.serverID)
			if err != nil {
				return status.Error(codes.InvalidArgument, err.Error())
			}
			p.mu.Lock()
			p.renewals++
			p.mu.Unlock()
			if err := sess.send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_Certificate{
				Certificate: &nodev1.Certificate{CertChainPem: cert, CaBundlePem: p.pki.caPEM},
			}}); err != nil {
				return err
			}
		}
	}
}

func (p *panel) sendFullToNode(sess *session, nodeID string) error {
	p.mu.Lock()
	ver := p.rosterVersion
	gen := p.gen
	p.mu.Unlock()
	msgs := usersFullMessages(nodeID, ver, p.prof.Roster(gen))
	for _, uf := range msgs {
		if err := sess.send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_UsersFull{UsersFull: uf}}); err != nil {
			return err
		}
		p.mu.Lock()
		p.usersFullSent++
		p.mu.Unlock()
	}
	return nil
}

func (p *panel) clientCertOK(ctx context.Context) bool {
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

// helloHasAllConfigs 判断 Hello 是否已带上全部节点的配置摘要。
func helloHasAllConfigs(h *nodev1.Hello, nodes []nodeEntry) bool {
	for _, n := range nodes {
		found := false
		for _, a := range h.GetAppliedConfigs() {
			if a.GetNodeId() == n.nodeID && a.GetContentSha256() == n.configSHA {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// helloNeedsUsers 判断该节点是否仍需下发当前名单版本。
func helloNeedsUsers(h *nodev1.Hello, nodeID, version string) bool {
	for _, a := range h.GetAppliedUsers() {
		if a.GetNodeId() == nodeID && a.GetVersion() == version {
			return false
		}
	}
	return true
}

// usersFullMessages 把全名单按 5000 人一块切成 UsersFull 消息。
func usersFullMessages(nodeID, version string, roster []profile.User) []*nodev1.UsersFull {
	if len(roster) == 0 {
		return []*nodev1.UsersFull{{NodeId: nodeID, Version: version, ChunkIndex: 0, ChunkTotal: 1}}
	}
	total := (len(roster) + usersFullChunkSize - 1) / usersFullChunkSize
	out := make([]*nodev1.UsersFull, 0, total)
	for i := 0; i < total; i++ {
		start := i * usersFullChunkSize
		end := start + usersFullChunkSize
		if end > len(roster) {
			end = len(roster)
		}
		chunk := roster[start:end]
		users := make([]*nodev1.User, len(chunk))
		for j, u := range chunk {
			users[j] = &nodev1.User{Uid: u.UID, Uuid: u.UUID}
		}
		out = append(out, &nodev1.UsersFull{
			NodeId: nodeID, Version: version, ChunkIndex: uint32(i), ChunkTotal: uint32(total), Users: users,
		})
	}
	return out
}

// buildUsersDelta 构造单节点的 UsersDelta（gen 为目标代，from 为 gen-1）。
func buildUsersDelta(p *profile.Profile, nodeID string, gen int) *nodev1.UsersDelta {
	fromVer := p.RosterVersion(gen - 1)
	toVer := p.RosterVersion(gen)
	upserted, removed := p.Delta(gen)
	users := make([]*nodev1.User, len(upserted))
	for i, u := range upserted {
		users[i] = &nodev1.User{Uid: u.UID, Uuid: u.UUID}
	}
	return &nodev1.UsersDelta{
		NodeId: nodeID, FromVersion: fromVer, ToVersion: toVer, Upserted: users, RemovedUids: removed,
	}
}

func (p *panel) status() Status {
	p.mu.Lock()
	p.activeMu.Lock()
	active := p.activeSess != nil
	p.activeMu.Unlock()
	acks := make(map[string]UsersAckState, len(p.rosterAcks))
	for k, v := range p.rosterAcks {
		acks[k] = v
	}
	cfgAcks := make(map[string]ConfigAckState, len(p.configAcks))
	for k, v := range p.configAcks {
		cfgAcks[k] = v
	}
	rt := make(map[string]string, len(p.runtime))
	for k, v := range p.runtime {
		rt[k] = v
	}
	st := Status{
		NowUnixMs:        time.Now().UnixMilli(),
		Down:             p.down,
		Enrolls:          p.enrolls,
		EnrollRejections: p.enrollRejections,
		SessionsTotal:    p.sessionsTotal,
		Active:           active,
		FirstHelloUnixMs: p.firstHelloUnixMs,
		LastHelloUnixMs:  p.lastHelloUnixMs,
		ConfigsSent:      p.configsSent,
		ConfigSentUnixMs: p.configSentUnixMs,
		ConfigAcks:       cfgAcks,
		Roster: RosterStatus{
			Gen: p.gen, Version: p.rosterVersion, FullSentUnixMs: p.fullSentUnixMs, DeltaSentUnixMs: p.deltaSentUnixMs,
			UsersFullSent: p.usersFullSent, UsersDeltaSent: p.usersDeltaSent, Acks: acks,
		},
		UsersAcksOK:         p.usersAcksOK,
		UsersAcksFailed:     p.usersAcksFailed,
		Heartbeats:          p.heartbeats,
		LastHeartbeatUnixMs: p.lastHeartbeatUnixMs,
		Runtime:             rt,
		TrafficReports:      p.trafficReports,
		TrafficEntries:      p.trafficEntries,
		OnlineReports:       p.onlineReports,
		OnlineEntries:       p.onlineEntries,
		Renewals:            p.renewals,
	}
	p.mu.Unlock()
	return st
}

// swapRosterLocked 在持锁下 gen+1 并返回新旧版本与增量信息（调用方需已确认有活动会话）。
func (p *panel) swapRosterLocked() (gen int, fromVer, toVer string, deltaSentMs int64) {
	fromVer = p.rosterVersion
	p.gen++
	gen = p.gen
	toVer = p.prof.RosterVersion(gen)
	p.rosterVersion = toVer
	p.deltaSentUnixMs = time.Now().UnixMilli()
	return gen, fromVer, toVer, p.deltaSentUnixMs
}
