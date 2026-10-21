package node

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
)

// CertManager 是编排层要的证书管理（pdnd/certs.Manager 实现）。nil 表示本机不自动申请证书：acme 节点一律 cert_pending。
type CertManager interface {
	core.CertSource
	Want(ids []string) // 整体替换想要的标识符集合，非阻塞
	Status(id string) core.CertStatus
	Updates() <-chan string // 某标识符换上了新证书
	Purge() error           // 删掉账户与全部证书（Tombstone）
}

// acmeIdentifier 返回配置文档要的自动申请证书标识符：cert_mode 是 acme 且 cert_identifier 非空时返回规整后的标识符，否则 ""。
func acmeIdentifier(cfg map[string]any) string {
	if core.InboundCertMode(cfg) != core.CertModeACME {
		return ""
	}
	return core.InboundCertIdentifier(cfg)
}

// configACMEIdentifier 从 NodeConfig 的原始 JSON 里取 acmeIdentifier（解码失败返回 ""）。
func configACMEIdentifier(nc *nodev1.NodeConfig) string {
	if nc == nil {
		return ""
	}
	var cfg map[string]any
	if err := json.Unmarshal(nc.GetConfigJson(), &cfg); err != nil || cfg == nil {
		return ""
	}
	return acmeIdentifier(cfg)
}

// wantCerts 把 configs 里所有 acme 标识符（去重、去空）交给 Manager.Want。
// 不论这些节点这次能不能装上，certs 为 nil 时什么都不做。
func (s *Server) wantCerts(configs []*nodev1.NodeConfig) {
	if s.certs == nil {
		return
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, nc := range configs {
		id := configACMEIdentifier(nc)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	s.certs.Want(ids)
}

// certPrecheck 在装入站之前检查：acme 节点的证书还没到时返回 *core.CertPendingError，不碰内核；
// Detail 填 Manager.Status 里最近一次失败的 ErrorCode（有 ErrorDetail 时为 "code: detail"）。
// 非 acme 节点或证书已在：返回 nil。
func (n *Node) certPrecheck(cfg map[string]any) error {
	id := acmeIdentifier(cfg)
	if id == "" {
		return nil
	}
	if n.certs != nil {
		if _, ok := n.certs.Certificate(id); ok {
			return nil
		}
		st := n.certs.Status(id)
		detail := st.ErrorCode
		if st.ErrorCode != "" && st.ErrorDetail != "" {
			detail = st.ErrorCode + ": " + st.ErrorDetail
		} else if detail == "" && st.ErrorDetail != "" {
			detail = st.ErrorDetail
		}
		return &core.CertPendingError{Identifier: id, Detail: detail}
	}
	return &core.CertPendingError{Identifier: id}
}

// onCertReady 在主循环收到 Manager.Updates() 的 id 时调用：所有因这个标识符 cert_pending 的节点立刻重装（不等台账退避）。
func (s *Server) onCertReady(id string) {
	for _, nodeID := range s.order {
		n := s.nodes[nodeID]
		if n == nil {
			continue
		}
		if n.failed == nil || n.failed.cfg == nil || configACMEIdentifier(n.failed.cfg) != id {
			continue
		}
		var pending *core.CertPendingError
		if !errors.As(n.lastApplyErr, &pending) {
			continue
		}
		s.tryInstall(n, n.failed.cfg)
		s.flushUsersAck(n)
	}
	s.publishApplied()
}

// certStatus 是心跳里这个节点的 NodeRuntime.cert。
func (n *Node) certStatus() *nodev1.CertStatus {
	var pending *core.CertPendingError
	if n.failed != nil && n.failed.cfg != nil && errors.As(n.lastApplyErr, &pending) {
		id := configACMEIdentifier(n.failed.cfg)
		if id == "" {
			return nil
		}
		if n.certs == nil {
			return &nodev1.CertStatus{Identifier: id}
		}
		return certStatusProto(id, n.certs.Status(id))
	}
	if n.started && n.activeConfig != nil {
		if id := acmeIdentifier(n.activeConfig); id != "" {
			if n.certs == nil {
				return &nodev1.CertStatus{Identifier: id}
			}
			return certStatusProto(id, n.certs.Status(id))
		}
		if !n.fileCertNotAfter.IsZero() {
			return &nodev1.CertStatus{NotAfterUnix: n.fileCertNotAfter.Unix()}
		}
	}
	return nil
}

// certExpired 报告节点在服务的证书是否已过期。
func (n *Node) certExpired(now time.Time) bool {
	if !n.started || n.activeConfig == nil {
		return false
	}
	if id := acmeIdentifier(n.activeConfig); id != "" {
		if n.certs == nil {
			return false
		}
		st := n.certs.Status(id)
		if st.NotAfter.IsZero() {
			return false
		}
		return !now.Before(st.NotAfter)
	}
	if n.fileCertNotAfter.IsZero() {
		return false
	}
	return !now.Before(n.fileCertNotAfter)
}

// fileCertNotAfter 读 file 模式配置里 cert_path 指向的 PEM，取第一张证书的 NotAfter。
func fileCertNotAfter(cfg map[string]any) time.Time {
	if core.InboundCertMode(cfg) == core.CertModeACME {
		return time.Time{}
	}
	path, _ := cfg["cert_path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return time.Time{}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	for block, rest := pem.Decode(body); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return time.Time{}
		}
		return cert.NotAfter
	}
	return time.Time{}
}

// certStatusProto 把证书管理报的状态转成心跳字段：零值时间转 0，其余 Unix 秒；标识符以配置文档里的为准。
func certStatusProto(id string, st core.CertStatus) *nodev1.CertStatus {
	out := &nodev1.CertStatus{
		Identifier:  id,
		ErrorCode:   st.ErrorCode,
		ErrorDetail: st.ErrorDetail,
	}
	if !st.NotAfter.IsZero() {
		out.NotAfterUnix = st.NotAfter.Unix()
	}
	if !st.NextAttempt.IsZero() {
		out.NextAttemptUnix = st.NextAttempt.Unix()
	}
	return out
}
