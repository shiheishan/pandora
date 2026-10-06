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

// 三层：规则 节点 → 组（按组序）→ 全局；出站 全局 → 组（后排先铺、先排覆盖）→ 节点，同 tag 保位覆盖
func TestMergeRoutingWithGroups(t *testing.T) {
	layers := []RoutingLayer{
		{Scope: "node", Outbounds: []NodeOutbound{ob("relay", "vless")}, Routes: []NodeRoute{rt("relay")}},
		{Scope: "group", GroupID: "g1", GroupName: "香港", Outbounds: []NodeOutbound{ob("unlock", "trojan"), ob("hk", "socks")},
			Routes: []NodeRoute{rt("unlock")}},
		{Scope: "group", GroupID: "g2", GroupName: "日本", Outbounds: []NodeOutbound{ob("unlock", "http"), ob("jp", "socks")},
			Routes: []NodeRoute{rt("jp")}},
		{Scope: "global", Outbounds: []NodeOutbound{ob("relay", "socks"), ob("unlock", "shadowsocks"), ob("pub", "http")},
			Routes: []NodeRoute{rt("pub"), rt("block")}},
	}
	outs, routes := MergeRouting(layers)
	want := []string{"relay:vless", "unlock:trojan", "pub:http", "jp:socks", "hk:socks"}
	if got := tagsOf(outs); !reflect.DeepEqual(got, want) {
		t.Fatalf("outbounds = %v, want %v", got, want)
	}
	if got, want := routeTags(routes), []string{"relay", "unlock", "jp", "pub", "block"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}

	// 带来源的合并：胜出的出站与每条规则都能追溯到层
	m := mergeRoutingLayers(layers)
	src := map[string]string{}
	for _, o := range m.outbounds {
		src[o.Tag] = layers[o.layer].Scope + "/" + layers[o.layer].GroupName
	}
	for tag, want := range map[string]string{"relay": "node/", "unlock": "group/香港", "pub": "global/", "jp": "group/日本", "hk": "group/香港"} {
		if src[tag] != want {
			t.Errorf("outbound %s source = %s, want %s", tag, src[tag], want)
		}
	}
	var routeLayers []int
	for _, r := range m.routes {
		routeLayers = append(routeLayers, r.layer)
	}
	if !reflect.DeepEqual(routeLayers, []int{0, 1, 2, 3, 3}) {
		t.Fatalf("route layers = %v", routeLayers)
	}
}

// 内置出站引用在下发时规范成小写：库里已有的 " Direct " / "BLOCK" 旧行也能被 pdnd 认出；
// 自定义出站原样不动（引用按原样精确匹配）
func TestMergeRoutingCanonicalizesBuiltinRefs(t *testing.T) {
	_, routes := MergeRouting([]RoutingLayer{
		{Scope: "node", Routes: []NodeRoute{rt(" Direct "), rt("BLOCK"), rt("HK")}},
		{Scope: "global", Routes: []NodeRoute{rt("direct")}},
	})
	if got, want := routeTags(routes), []string{"direct", "block", "HK", "direct"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	m := mergeRoutingLayers([]RoutingLayer{{Scope: "node", Routes: []NodeRoute{rt(" Direct ")}}})
	if m.routes[0].OutboundTag != "direct" {
		t.Fatalf("preview path must canonicalize too, got %q", m.routes[0].OutboundTag)
	}
	if canonicalRouteTag("Directly") != "Directly" || canonicalRouteTag(" hk ") != " hk " {
		t.Fatal("custom outbound tags must pass through unchanged")
	}
}
