package grpc

// ControlService 的 proto 与 service 类型互转。节点发来的 id 解析不了的条目丢掉；摘要一律转小写。

import (
	"net/netip"
	"regexp"
	"strings"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

// reportIDPattern 是流量上报编号的合法形状（proto：1–64 个 [A-Za-z0-9._:-]）。
var reportIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func parseID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(s))
	return id, err == nil
}

func helloFromProto(h *nodev1.Hello) service.Hello {
	out := service.Hello{
		AgentVersion:   h.GetAgentVersion(),
		BinarySHA256:   h.GetBinarySha256(),
		Features:       h.GetFeatures(),
		AppliedConfigs: map[uuid.UUID]string{},
		AppliedUsers:   map[uuid.UUID]string{},
	}
	if host := h.GetHost(); host != nil {
		out.Host = service.HostInfo{
			Hostname: host.GetHostname(), OS: host.GetOs(), Arch: host.GetArch(),
			CPUCores: host.GetCpuCores(), MemoryBytes: host.GetMemoryBytes(), DiskBytes: host.GetDiskBytes(),
		}
	}
	for _, a := range h.GetAppliedConfigs() {
		if id, ok := parseID(a.GetNodeId()); ok && a.GetContentSha256() != "" {
			out.AppliedConfigs[id] = strings.ToLower(a.GetContentSha256())
		}
	}
	for _, a := range h.GetAppliedUsers() {
		if id, ok := parseID(a.GetNodeId()); ok && a.GetVersion() != "" {
			out.AppliedUsers[id] = a.GetVersion()
		}
	}
	return out
}

func runtimeStatusFromProto(s nodev1.RuntimeStatus) string {
	switch s {
	case nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING:
		return service.RuntimeRunning
	case nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED:
		return service.RuntimeDegraded
	case nodev1.RuntimeStatus_RUNTIME_STATUS_DOWN:
		return service.RuntimeDown
	default:
		return ""
	}
}

func heartbeatFromProto(hb *nodev1.Heartbeat) service.Heartbeat {
	var out service.Heartbeat
	if m := hb.GetMetrics(); m != nil {
		out.Metrics = &service.HostMetrics{
			CPUBp:         m.GetCpuBp(),
			MemUsedBytes:  m.GetMemUsedBytes(),
			MemTotalBytes: m.GetMemTotalBytes(),
			DiskUsedBytes: m.GetDiskUsedBytes(), DiskTotalBytes: m.GetDiskTotalBytes(),
			Load1Cp: m.GetLoad1Cp(), Load5Cp: m.GetLoad5Cp(), Load15Cp: m.GetLoad15Cp(),
			NetRxBytes: m.GetNetRxBytes(), NetTxBytes: m.GetNetTxBytes(),
			TCPConns:  m.GetTcpConns(),
			UptimeSec: m.GetUptimeSec(),
			Partial:   m.GetPartial(),
		}
	}
	for _, n := range hb.GetNodes() {
		id, ok := parseID(n.GetNodeId())
		st := runtimeStatusFromProto(n.GetStatus())
		if !ok || st == "" {
			continue
		}
		rt := service.NodeRuntime{NodeID: id, Status: st, Reason: n.GetReason()}
		if other, ok := parseID(n.GetReasonNodeId()); ok {
			rt.ReasonNodeID = &other
		}
		out.Nodes = append(out.Nodes, rt)
	}
	return out
}

// trafficFromProto 转流量上报；编号不合法时 ok 为 false。节点 id 解析不了的条目丢掉。
func trafficFromProto(r *nodev1.TrafficReport) (service.TrafficReport, bool) {
	if !reportIDPattern.MatchString(r.GetReportId()) {
		return service.TrafficReport{}, false
	}
	out := service.TrafficReport{ReportID: r.GetReportId()}
	for _, e := range r.GetEntries() {
		id, ok := parseID(e.GetNodeId())
		if !ok {
			continue
		}
		out.Entries = append(out.Entries, service.TrafficEntry{
			NodeID: id, UID: e.GetUid(), Upload: e.GetUpload(), Download: e.GetDownload(),
		})
	}
	return out, true
}

// onlineFromProto 转在线 IP；节点 id 或地址解析不了的丢掉。
func onlineFromProto(o *nodev1.OnlineIPs) []service.OnlineEntry {
	var out []service.OnlineEntry
	for _, e := range o.GetEntries() {
		id, ok := parseID(e.GetNodeId())
		if !ok {
			continue
		}
		ent := service.OnlineEntry{NodeID: id, UID: e.GetUid()}
		for _, s := range e.GetIps() {
			if ip, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
				ent.IPs = append(ent.IPs, ip)
			}
		}
		out = append(out, ent)
	}
	return out
}

// configAckFromProto 转配置回执；节点 id 解析不了或阶段未知时 ok 为 false。
func configAckFromProto(a *nodev1.ConfigAck) (service.ConfigAck, bool) {
	id, ok := parseID(a.GetNodeId())
	if !ok {
		return service.ConfigAck{}, false
	}
	var phase service.ConfigPhase
	switch a.GetPhase() {
	case nodev1.ConfigPhase_CONFIG_PHASE_SWITCHED:
		phase = service.ConfigSwitched
	case nodev1.ConfigPhase_CONFIG_PHASE_HEALTH_PASSED:
		phase = service.ConfigHealthPassed
	case nodev1.ConfigPhase_CONFIG_PHASE_FAILED:
		phase = service.ConfigFailed
	default:
		return service.ConfigAck{}, false
	}
	return service.ConfigAck{
		NodeID: id, SHA256: strings.ToLower(a.GetContentSha256()), Phase: phase,
		ReasonCode: a.GetReasonCode(), Detail: a.GetDetail(),
	}, true
}

// usersAckFromProto 转名单回执；节点 id 解析不了时 ok 为 false。
func usersAckFromProto(a *nodev1.UsersAck) (service.UsersAck, bool) {
	id, ok := parseID(a.GetNodeId())
	if !ok {
		return service.UsersAck{}, false
	}
	return service.UsersAck{NodeID: id, Version: a.GetVersion(), OK: a.GetOk(), ReasonCode: a.GetReasonCode()}, true
}

func usersToProto(users []service.RosterUser) []*nodev1.User {
	out := make([]*nodev1.User, 0, len(users))
	for _, u := range users {
		out = append(out, &nodev1.User{
			Uid: u.UID, Uuid: u.UUID.String(), SpeedLimitKbps: u.SpeedLimitKbps, DeviceLimit: u.DeviceLimit,
		})
	}
	return out
}

func durationMs(d int64) uint32 {
	ms := d / 1e6
	if ms < 0 {
		return 0
	}
	if ms > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(ms)
}

func command(c *nodev1.Command) *nodev1.SessionResponse {
	return &nodev1.SessionResponse{Body: &nodev1.SessionResponse_Command{Command: c}}
}

// outboundToProto 把一条逻辑消息转成要发的 proto 消息（UsersFull 按 UsersChunkSize 分块，空名单也发一块）。
// seq 由发送循环填。
func outboundToProto(o service.Outbound) []*nodev1.SessionResponse {
	switch o.Kind {
	case service.OutWelcome:
		w := o.Welcome
		return []*nodev1.SessionResponse{{Body: &nodev1.SessionResponse_Welcome{Welcome: &nodev1.Welcome{
			SessionId:            w.SessionID,
			Features:             w.Features,
			PushIntervalSec:      uint32(w.PushInterval.Seconds()),
			HeartbeatIntervalSec: uint32(w.HeartbeatInterval.Seconds()),
			ServerTimeUnixMs:     w.ServerTime.UnixMilli(),
		}}}}
	case service.OutConfig:
		nodes := make([]*nodev1.NodeConfig, 0, len(o.Config.Nodes))
		for _, d := range o.Config.Nodes {
			nodes = append(nodes, &nodev1.NodeConfig{
				NodeId: d.NodeID.String(), NodeType: d.NodeType, ConfigJson: d.JSON, ContentSha256: d.SHA256,
			})
		}
		return []*nodev1.SessionResponse{{Body: &nodev1.SessionResponse_Config{Config: &nodev1.ServerConfig{
			ReleaseId: o.Config.ReleaseID, Nodes: nodes,
		}}}}
	case service.OutUsersFull:
		r := o.Roster
		total := (len(r.Users) + service.UsersChunkSize - 1) / service.UsersChunkSize
		if total == 0 {
			total = 1
		}
		out := make([]*nodev1.SessionResponse, 0, total)
		for i := 0; i < total; i++ {
			lo, hi := i*service.UsersChunkSize, min((i+1)*service.UsersChunkSize, len(r.Users))
			out = append(out, &nodev1.SessionResponse{Body: &nodev1.SessionResponse_UsersFull{UsersFull: &nodev1.UsersFull{
				NodeId: r.NodeID.String(), Version: r.Version,
				ChunkIndex: uint32(i), ChunkTotal: uint32(total), //nolint:gosec // 块数以名单人数 / 5000 为界，远小于 uint32
				Users: usersToProto(r.Users[lo:hi]),
			}}})
		}
		return out
	case service.OutUsersDelta:
		return []*nodev1.SessionResponse{{Body: &nodev1.SessionResponse_UsersDelta{UsersDelta: &nodev1.UsersDelta{
			NodeId: o.Roster.NodeID.String(), FromVersion: o.DeltaFrom, ToVersion: o.Roster.Version,
			Upserted: usersToProto(o.Upserted), RemovedUids: o.Removed,
		}}}}
	case service.OutTrafficAck:
		return []*nodev1.SessionResponse{{Body: &nodev1.SessionResponse_TrafficAck{TrafficAck: &nodev1.TrafficAck{
			ReportId: o.ReportID,
		}}}}
	case service.OutCertificate:
		return []*nodev1.SessionResponse{{Body: &nodev1.SessionResponse_Certificate{Certificate: &nodev1.Certificate{
			CertChainPem: o.CertChainPEM, CaBundlePem: o.CAPEM,
		}}}}
	case service.OutReconnect:
		return []*nodev1.SessionResponse{command(&nodev1.Command{Kind: &nodev1.Command_Reconnect{Reconnect: &nodev1.Reconnect{
			InMs: durationMs(int64(o.Reconnect.In)), JitterMs: durationMs(int64(o.Reconnect.Jitter)),
		}}})}
	case service.OutTombstone:
		return []*nodev1.SessionResponse{command(&nodev1.Command{Kind: &nodev1.Command_Tombstone{Tombstone: &nodev1.Tombstone{
			Reason: o.Reason,
		}}})}
	}
	return nil
}
