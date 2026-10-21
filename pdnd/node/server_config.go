package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/panel"
)

// 配置下发：面板每次发这台服务器全部节点的全量（ServerConfig），节点按列表顺序逐个装，
// 不在列表里的停掉。版本身份只认 content_sha256（配置文档原字节的摘要）。
//
// 回执（ConfigAck）：switched 装上即报；health_passed 在切换后稳定 healthStabilityWindow、
// 内核确认入站就绪才报；failed 每个版本每条流只报一次。回执按「摘要 + 阶段」记台账，
// 每条新流复位：丢在断开的流上的回执由 Hello 对齐（报已应用版本）兜底，面板重发不同的配置时再报。
//
// 坏版本台账（applyFailure）：每个节点只记最近一个装不上的版本。旧配置仍在服务时按
// applyRetryDelay 退避重试（每次重试都可能重建入站、断一次在线连接）；节点已停时没有
// 可保的东西，按 stoppedRetryInterval 重试（端口一度被占、随后释放的情形能自己恢复）。
// 面板重发同一个坏版本（重连后的对齐）时不重装、只补报 failed。

const (
	// stoppedRetryInterval 是节点已停（没有旧配置在服务）时重试装配置的间隔。
	stoppedRetryInterval = 60 * time.Second
	// healthStabilityWindow 是切换后报 health_passed 之前要稳定运行的时长。
	healthStabilityWindow = 5 * time.Second
	// maxReportDetailBytes 是回执 detail 的上限（面板截断到同样长度）。
	maxReportDetailBytes = 2048
)

// 装配置失败的原因码（另有内核给的 RuntimeReason，如 port_in_use:…）。
const (
	reasonDigestMismatch = "digest_mismatch"
	reasonInvalidJSON    = "invalid_json"
	reasonApplyFailedAck = "apply_failed"
)

// applyFailure 是一个节点最近一个装不上的配置版本（坏版本台账），只占一个槽。
type applyFailure struct {
	sha       string
	cfg       *nodev1.NodeConfig // 重试时原样再装
	attempts  int
	nextRetry time.Time
	reason    string
	detail    string
	// fromCache：冷启动用落盘缓存没装上记的（certs 审查 S4）。面板确认（Welcome）之前重装照冷启动的做法
	// 带落盘名单、装上算「靠缓存服务」；Welcome 时清掉，之后按面板下发走。
	fromCache bool
}

// applyError 带原因码的装配置错误（摘要不符、不是 JSON 对象）。
type applyError struct {
	reason string
	err    error
}

func (e *applyError) Error() string { return e.err.Error() }
func (e *applyError) Unwrap() error { return e.err }

// reasonOf 取装配置失败的原因码：自己的 applyError、内核的 RuntimeReason，都没有给 apply_failed。
func reasonOf(err error) string {
	var ae *applyError
	if errors.As(err, &ae) {
		return ae.reason
	}
	return applyErrReason(err, reasonApplyFailedAck)
}

// applyServerConfig 处理一份全量 ServerConfig。
func (s *Server) applyServerConfig(cfg *nodev1.ServerConfig) {
	if cfg == nil {
		return
	}
	// 先把全部 acme 标识符交给证书管理（不论这次能不能装上），新出现的立即排队申请。
	s.wantCerts(cfg.GetNodes())
	seen := make(map[string]bool, len(cfg.GetNodes()))
	order := make([]string, 0, len(cfg.GetNodes()))
	for _, nc := range cfg.GetNodes() {
		id := nc.GetNodeId()
		if id == "" {
			s.log.Warn("面板下发的节点配置缺 node_id，跳过")
			continue
		}
		if seen[id] {
			s.log.Warn("面板下发的节点配置重复，只认第一份", "node", id)
			continue
		}
		seen[id] = true
		order = append(order, id)
		n := s.nodes[id]
		if n == nil {
			n = s.newNode(id, nc.GetNodeType())
			s.nodes[id] = n
		}
		s.applyNodeConfig(n, nc)
	}
	for id, n := range s.nodes {
		if !seen[id] {
			s.removeNode(n)
		}
	}
	s.order = order
	// 停掉的节点、换了证书名字并已装上的节点，旧标识符不再有人用：交回证书管理，让它们开始闲置计时
	s.syncCertWant()
	if s.cache != nil {
		if err := s.cache.saveOrder(order); err != nil {
			s.log.Warn("节点顺序落盘失败（不影响服务）", "err", err)
		}
	}
	s.publishApplied()
	s.log.Info("已处理面板下发的配置", "release_id", cfg.GetReleaseId(), "节点数", len(order))
}

// applyNodeConfig 处理运行期收到的一个节点的配置。
func (s *Server) applyNodeConfig(n *Node, nc *nodev1.NodeConfig) {
	defer s.publishApplied()
	defer s.flushUsersAck(n)
	sha := strings.ToLower(nc.GetContentSha256())
	if n.started && n.appliedSHA == sha && n.nodeType == strings.TrimSpace(nc.GetNodeType()) {
		// 已是这一版（面板回到了当前版，或重连后的对齐）：不重装，补报回执。
		n.failed = nil
		s.sendConfigAck(n, sha, nodev1.ConfigPhase_CONFIG_PHASE_SWITCHED, "", "")
		return
	}
	if f := n.failed; f != nil && f.sha == sha && n.started && s.clock().Before(f.nextRetry) {
		// 已知装不上、旧配置仍在服务、还没到重试的点：不重装，只补报 failed。
		s.sendConfigAck(n, sha, nodev1.ConfigPhase_CONFIG_PHASE_FAILED, f.reason, f.detail)
		return
	}
	s.tryInstall(n, nc)
}

// tryInstall 装一个节点的配置，按结果记坏版本台账、报回执。
func (s *Server) tryInstall(n *Node, nc *nodev1.NodeConfig) {
	sha := strings.ToLower(nc.GetContentSha256())
	oldCertID := ""
	if n.started {
		oldCertID = acmeIdentifier(n.activeConfig)
	}
	offline := n.failed != nil && n.failed.fromCache && n.failed.sha == sha
	n.offlineStart = offline
	err := s.installNodeConfig(n, nc)
	n.offlineStart = false
	if err != nil {
		f := n.failed
		if f == nil || f.sha != sha {
			f = &applyFailure{sha: sha, reason: reasonOf(err), detail: reportDetail(err.Error())}
			n.failed = f
		}
		f.cfg = nc
		f.attempts++
		now := s.clock()
		if n.started {
			f.nextRetry = now.Add(applyRetryDelay(f.attempts))
		} else {
			f.nextRetry = now.Add(panel.Jitter(stoppedRetryInterval))
		}
		s.sendConfigAck(n, sha, nodev1.ConfigPhase_CONFIG_PHASE_FAILED, f.reason, f.detail)
		n.log.Error("配置装不上", "版本", sha, "原因", f.reason, "第几次", f.attempts,
			"旧配置仍在服务", n.started, "下次重试", f.nextRetry.Sub(now).Round(time.Second), "err", err)
		return
	}
	if offline {
		// 面板还没连上：回执发不出去，Hello 会报这个版本；与冷启动装上的一样标成靠缓存服务
		n.fromCache = true
		n.healthPending = false
		n.log.Warn("面板确认之前用落盘缓存的配置起服务（冷启动时没装上，重试成功）", "版本", sha, "名单版本", n.userVersion)
		return
	}
	if oldCertID != "" && oldCertID != acmeIdentifier(n.activeConfig) {
		// 旧配置的证书名字不再在服务：重算一次，让旧证书开始闲置计时（新配置的名字已在 certWanted 里）
		s.syncCertWant()
	}
	s.sendConfigAck(n, sha, nodev1.ConfigPhase_CONFIG_PHASE_SWITCHED, "", "")
	n.log.Info("入站已就绪", "port", intFrom(n.activeConfig, "server_port"), "type", n.nodeType, "版本", sha)
}

// installNodeConfig 核对摘要、解码、装入站，成功时记下已应用版本并落盘；不报回执。
func (s *Server) installNodeConfig(n *Node, nc *nodev1.NodeConfig) error {
	sha := strings.ToLower(nc.GetContentSha256())
	if contentDigest(nc.GetConfigJson()) != sha {
		err := &applyError{reason: reasonDigestMismatch, err: errors.New("配置内容与 content_sha256 不符")}
		n.lastApplyErr = err
		return err
	}
	var cfg map[string]any
	dec := json.NewDecoder(bytes.NewReader(nc.GetConfigJson()))
	dec.UseNumber()
	if err := dec.Decode(&cfg); err != nil || cfg == nil {
		if err == nil {
			err = errors.New("不是 JSON 对象")
		}
		e := &applyError{reason: reasonInvalidJSON, err: fmt.Errorf("配置不是合法的 JSON 对象: %w", err)}
		n.lastApplyErr = e
		return e
	}
	if err := n.applyConfig(cfg, nc.GetNodeType()); err != nil {
		return err
	}
	n.appliedSHA = sha
	n.appliedAt = s.clock()
	n.healthPending = true
	n.failed = nil
	if n.cache != nil {
		if err := n.cache.saveConfig(nc); err != nil {
			n.log.Warn("配置落盘失败（不影响服务，只是面板不可达时重启无法用缓存）", "err", err)
		}
	}
	return nil
}

// removeNode 停掉不在面板配置里的节点：先收最后一轮流量，再删入站与缓存。
func (s *Server) removeNode(n *Node) {
	if n.started {
		s.collectNodeTraffic(n)
		if err := s.kernel.DelInbound(n.tag); err != nil {
			n.log.Warn("删除入站失败", "err", err)
		}
	}
	delete(s.nodes, n.id)
	if n.cache != nil {
		n.cache.remove()
	}
	n.log.Info("节点已不在面板配置里，已停掉")
}

// retryFailedConfig 是内务节拍：坏版本到了重试的点就再装一次。
func (s *Server) retryFailedConfig(n *Node, now time.Time) {
	f := n.failed
	if f == nil || f.cfg == nil || now.Before(f.nextRetry) {
		return
	}
	n.log.Info("重试之前装不上的配置", "版本", f.sha, "第几次", f.attempts+1)
	s.tryInstall(n, f.cfg)
	s.flushUsersAck(n)
	s.publishApplied()
}

// reportHealth 是内务节拍：切换后稳定够久、内核确认入站就绪，就报 health_passed。
func (s *Server) reportHealth(n *Node, now time.Time) {
	if !n.healthPending || !n.started || n.failed != nil || now.Sub(n.appliedAt) < healthStabilityWindow {
		return
	}
	if s.kernel.InboundReady(n.tag) != nil {
		return
	}
	if s.sendConfigAck(n, n.appliedSHA, nodev1.ConfigPhase_CONFIG_PHASE_HEALTH_PASSED, "", "") {
		n.healthPending = false
	}
}

// sendConfigAck 报配置回执；同一条流上同一版本同一阶段只报一次。没连着返回 false、不记台账。
func (s *Server) sendConfigAck(n *Node, sha string, phase nodev1.ConfigPhase, reasonCode, detail string) bool {
	key := sha + "/" + phase.String()
	if n.acked[key] {
		return true
	}
	if !s.connected || s.link == nil {
		return false
	}
	if !s.link.SendConfigAck(&nodev1.ConfigAck{
		NodeId: n.id, ContentSha256: sha, Phase: phase, ReasonCode: reasonCode, Detail: detail,
	}) {
		return false
	}
	n.acked[key] = true
	return true
}

// flushUsersAck 发出节点操作中产生的名单回执；没连着就丢掉（重连的 Hello 会报真实版本）。
func (s *Server) flushUsersAck(n *Node) {
	ack := n.pendingUsersAck
	if ack == nil {
		return
	}
	n.pendingUsersAck = nil
	if s.connected && s.link != nil {
		s.link.SendUsersAck(ack)
	}
}

// contentDigest 是配置文档的摘要：小写 hex(SHA-256(原字节))。
func contentDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// reportDetail 按 UTF-8 字符边界把回执 detail 截到 maxReportDetailBytes。
func reportDetail(detail string) string {
	if len(detail) <= maxReportDetailBytes {
		return detail
	}
	cut := maxReportDetailBytes
	for cut > 0 && !utf8.RuneStart(detail[cut]) {
		cut--
	}
	return detail[:cut]
}
