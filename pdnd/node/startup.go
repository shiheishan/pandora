package node

import (
	"context"
	"strings"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// Start 是冷启动：在会话建立之前、主循环之前，把落盘缓存里的配置与名单装上。
//
// 面板不可达（升级、崩溃、VPS 重启时面板也在重启）时节点照常服务；面板回来后 Hello 报的
// 就是这些版本，面板只发不同的部分。按缓存顺序（即上次面板下发的顺序）装，同机端口冲突时
// 赢家与上次一致。
func (s *Server) Start() {
	if s.cache == nil {
		return
	}
	order, err := s.cache.loadOrder()
	if err != nil {
		s.log.Info("没有可用的落盘缓存，等面板下发配置", "err", err)
		return
	}
	var cached []*nodev1.NodeConfig
	for _, id := range order {
		if _, dup := s.nodes[id]; dup || id == "" {
			continue
		}
		nc, err := s.cache.node(id).loadConfig()
		if err != nil {
			s.log.Warn("节点的落盘配置不可用，等面板下发", "node", id, "err", err)
			continue
		}
		cached = append(cached, nc)
	}
	s.wantCerts(cached)
	for _, nc := range cached {
		id := nc.GetNodeId()
		if _, dup := s.nodes[id]; dup || id == "" {
			continue
		}
		n := s.newNode(id, nc.GetNodeType())
		s.nodes[id] = n
		s.order = append(s.order, id)
		n.offlineStart = true
		err = s.installNodeConfig(n, nc)
		n.offlineStart = false
		if err != nil {
			// 记进坏版本台账：缓存的 acme 证书不在（被删或坏了）时，证书一到 onCertReady 就能重装，
			// 别的原因按「已停」的间隔由内务节拍重试，重试照样带落盘名单；不记的话面板连上之前这个节点一直不服务（certs 审查 S4）
			n.failed = &applyFailure{
				sha: strings.ToLower(nc.GetContentSha256()), cfg: nc, attempts: 1,
				nextRetry: s.clock().Add(panel.Jitter(stoppedRetryInterval)),
				reason:    reasonOf(err), detail: reportDetail(err.Error()), fromCache: true,
			}
			n.log.Warn("没能用落盘缓存起服务，等面板下发或稍后重试", "err", err)
			continue
		}
		n.fromCache = true
		n.healthPending = false
		n.log.Warn("面板确认之前先用落盘缓存的配置起服务", "版本", n.appliedSHA, "名单版本", n.userVersion)
	}
	s.publishApplied()
}

// Shutdown 是进程退出时的最后一次上报：主循环已停（Run 返回之后）、会话还在跑时调用。
//
// drained 是内核关停后按入站 tag（即节点 id）交出的最后一轮流量（haveDrained 为真时）：先关内核、
// 让在途 TCP 连接把流量入账，再报——顺序反了的话每次重启、升级都丢掉在途连接的流量。内核不支持
// 关停交流量时（haveDrained 为假）退回先取再报。之后等面板确认，直到缓冲清空或 ctx 到期。
func (s *Server) Shutdown(ctx context.Context, drained map[string][]core.UserTraffic, haveDrained bool) {
	if haveDrained {
		for tag, t := range drained {
			id := tag
			if n := s.nodes[tag]; n != nil {
				id = n.id
			}
			s.bufferTraffic(id, t)
		}
	} else {
		for _, id := range s.order {
			if n := s.nodes[id]; n != nil && n.started {
				s.collectNodeTraffic(n)
			}
		}
	}
	s.flushUsersCache()

	if s.link != nil {
		s.flushTraffic()
	wait:
		for !s.traffic.empty() {
			select {
			case <-ctx.Done():
				break wait
			case ev := <-s.link.Events():
				switch ev.Kind {
				case panel.EventWelcome:
					s.connected = true
					s.flushTraffic()
				case panel.EventTrafficAck:
					s.onTrafficAck(ev.ReportID)
				case panel.EventDisconnected, panel.EventTombstone:
					s.connected = false
				}
			}
		}
	}
	if !s.traffic.empty() {
		s.log.Error("退出前流量没能交给面板，这部分已丢失", "字节", s.traffic.bytes(),
			"累计丢弃字节", s.traffic.droppedBytes)
	}
}
