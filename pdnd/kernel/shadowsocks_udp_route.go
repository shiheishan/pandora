package kernel

// SS UDP 会话按首包目标选出站、建上游；之后发往别的目标的包，在目标变化时按该目标
// 再查一次路由，出站 tag 与会话的上游不一致（含 block）就丢这个包，不改会话的上游。
// 同一目标连续发包只查第一次（routedDest 缓存最近一个通过检查的目标）。

import (
	"net/netip"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/route"
)

// udpRouteSelector 是能报出 route.Meta 会走哪个出站的数据面（测试替身实现它）。
type udpRouteSelector interface {
	Select(route.Meta) string
}

// selectUDPRoute 报告 meta 会走哪个出站 tag；数据面不支持查询（或路由还没就绪）时 ok 为 false。
func selectUDPRoute(plane DataPlane, meta route.Meta) (tag string, ok bool) {
	switch p := plane.(type) {
	case *routedDataPlane:
		p.mu.RLock()
		r := p.current
		p.mu.RUnlock()
		if r == nil {
			return "", false
		}
		return r.Select(meta), true
	case udpRouteSelector:
		return p.Select(meta), true
	}
	return "", false
}

// ssUDPRouteMeta 是 SS UDP 包发往 dest 时的路由元数据（建上游与按目标复查共用）。
func ssUDPRouteMeta(dest vlessDestination, client netip.AddrPort) route.Meta {
	return route.Meta{
		Domain: dest.Domain, IP: dest.IP, Port: dest.Port,
		Network: "udp", Protocol: "shadowsocks",
		SourceIP: client.Addr(), SourcePort: client.Port(),
	}
}

// routeAllows 报告发往 dest 的包能否走本会话的上游。会话建立时数据面不支持查询
// （routeOK 为 false）就不查。
func (s *ssUDPSession) routeAllows(dest vlessDestination) bool {
	if !s.routeOK {
		return true
	}
	target := M.ParseSocksaddrHostPort(dest.Host, dest.Port)
	s.mu.Lock()
	same := target == s.routedDest
	s.mu.Unlock()
	if same {
		return true
	}
	tag, ok := selectUDPRoute(s.adapter.plane, ssUDPRouteMeta(dest, s.client))
	if !ok || tag != s.routeTag {
		return false
	}
	s.mu.Lock()
	s.routedDest = target
	s.mu.Unlock()
	return true
}
