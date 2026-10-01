// [INPUT]: 依赖 routing_merge.go 的 MergeRouting / RoutingLayer
// [OUTPUT]: 对外提供 TestMergeRouting* 单元测试
// [POS]: domain/nodefabric 生效路由合并口径的纯逻辑守卫：层序、出站同 tag 覆盖保位、规则拼接顺序
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"encoding/json"
	"reflect"
	"testing"
)

func ob(tag, typ string) NodeOutbound {
	return NodeOutbound{Tag: tag, Type: typ, Settings: json.RawMessage(`{}`)}
}

func rt(tag string) NodeRoute {
	return NodeRoute{Matcher: json.RawMessage(`{"port":[443]}`), OutboundTag: tag}
}

func tagsOf(outs []NodeOutbound) []string {
	var s []string
	for _, o := range outs {
		s = append(s, o.Tag+":"+o.Type)
	}
	return s
}

func routeTags(routes []NodeRoute) []string {
	var s []string
	for _, r := range routes {
		s = append(s, r.OutboundTag)
	}
	return s
}

// 节点私有覆盖全局同名出站且保留全局的位置；规则节点在前、全局在后
func TestMergeRoutingNodeOverGlobal(t *testing.T) {
	outs, routes := MergeRouting([]RoutingLayer{
		{Scope: "node", Outbounds: []NodeOutbound{ob("relay", "vless"), ob("own", "trojan")},
			Routes: []NodeRoute{rt("own"), rt("relay")}},
		{Scope: "global", Outbounds: []NodeOutbound{ob("relay", "socks"), ob("pub", "http")},
			Routes: []NodeRoute{rt("pub")}},
	})
	if got, want := tagsOf(outs), []string{"relay:vless", "pub:http", "own:trojan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outbounds = %v, want %v", got, want)
	}
	if got, want := routeTags(routes), []string{"own", "relay", "pub"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
}

// 没有任何层时两边都是 nil：BuildNodeConfig 据此不输出 outbounds / routes 键
func TestMergeRoutingEmpty(t *testing.T) {
	outs, routes := MergeRouting([]RoutingLayer{{Scope: "node"}, {Scope: "global"}})
	if outs != nil || routes != nil {
		t.Fatalf("empty layers must merge to nil, got %v %v", outs, routes)
	}
}
