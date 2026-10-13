package service

// 节点输入校验纯函数的单测（不连库）。

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNodeValidateName(t *testing.T) {
	got, err := nodeValidateName("  香港-1  ")
	if err != nil || got != "香港-1" {
		t.Fatalf("trim 后 %q %v", got, err)
	}
	if _, err := nodeValidateName(strings.Repeat("字", 64)); err != nil {
		t.Fatalf("64 字应通过：%v", err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("字", 65), "a\x00b"} {
		_, err := nodeValidateName(bad)
		e := nodeTestErr(t, err, errs.ValidationFailed)
		if e.Fields["name"] == "" {
			t.Fatalf("%q 应标在 name", bad)
		}
	}
}

func TestNodeValidateDisplay(t *testing.T) {
	if v, err := nodeValidateDisplay("  "); err != nil || v != nil {
		t.Fatalf("空展示名应为 nil：%v %v", v, err)
	}
	if v, err := nodeValidateDisplay(" 香港 "); err != nil || v == nil || *v != "香港" {
		t.Fatalf("展示名 trim：%v %v", v, err)
	}
	for _, bad := range []string{strings.Repeat("a", 65), "a\x00"} {
		_, err := nodeValidateDisplay(bad)
		nodeTestErr(t, err, errs.ValidationFailed)
	}
}

func TestNodeNormalizeCountry(t *testing.T) {
	if v, err := nodeNormalizeCountry(" cn "); err != nil || v == nil || *v != "CN" {
		t.Fatalf("cn → CN：%v %v", v, err)
	}
	if v, err := nodeNormalizeCountry(""); err != nil || v != nil {
		t.Fatalf("空应为 nil：%v %v", v, err)
	}
	for _, bad := range []string{"CHN", "1A", "中国"} {
		_, err := nodeNormalizeCountry(bad)
		e := nodeTestErr(t, err, errs.ValidationFailed)
		if e.Fields["country_code"] == "" {
			t.Fatalf("%q 应标在 country_code", bad)
		}
	}
}

func TestNodeValidHost(t *testing.T) {
	for _, ok := range []string{"203.0.113.10", "2001:db8::1", "edge.example.test", "edge.example.test.", "a-b.c"} {
		if !nodeValidHost(ok) {
			t.Fatalf("%q 应合法", ok)
		}
	}
	for _, bad := range []string{"", "a_b.example", "a b.example", "-a.com", "a-.com", "a..b", "中文.example"} {
		if nodeValidHost(bad) {
			t.Fatalf("%q 应不合法", bad)
		}
	}
}

func TestNodeCheckRate(t *testing.T) {
	for _, ok := range []string{"0", "1", "0.5", "9999.99"} {
		if err := nodeCheckRate(decimal.RequireFromString(ok)); err != nil {
			t.Fatalf("%s 应通过：%v", ok, err)
		}
	}
	for _, bad := range []string{"-0.01", "10000", "1.234"} {
		e := nodeTestErr(t, nodeCheckRate(decimal.RequireFromString(bad)), errs.ValidationFailed)
		if e.Fields["traffic_rate"] == "" {
			t.Fatalf("%s 应标在 traffic_rate", bad)
		}
	}
}

func TestNodeSameJSON(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":[1,2]}`, `{"b":[1,2],"a":1}`, true},
		{`{"a":1}`, `{"a":1.0}`, false},
		{`{"a":1}`, `{"a":2}`, false},
		{`{"a":`, `{"a":1}`, false},
	}
	for _, c := range cases {
		if got := nodeSameJSON([]byte(c.a), []byte(c.b)); got != c.want {
			t.Fatalf("nodeSameJSON(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestNodeProtocolReady(t *testing.T) {
	typ, host, port := "shadowsocks", "203.0.113.10", 8388
	ready := model.Node{NodeType: &typ, ServerHost: &host, ServerPort: &port, ProtocolSchemaVersion: 1}
	if !nodeProtocolReady(ready) {
		t.Fatal("配齐的节点应就绪")
	}
	for name, n := range map[string]model.Node{
		"缺类型":  {ServerHost: &host, ServerPort: &port, ProtocolSchemaVersion: 1},
		"缺地址":  {NodeType: &typ, ServerPort: &port, ProtocolSchemaVersion: 1},
		"缺端口":  {NodeType: &typ, ServerHost: &host, ProtocolSchemaVersion: 1},
		"版本 0": {NodeType: &typ, ServerHost: &host, ServerPort: &port},
	} {
		if nodeProtocolReady(n) {
			t.Fatalf("%s 不应就绪", name)
		}
	}
}

func TestNodeValidateReason(t *testing.T) {
	if v, err := nodeValidateReason("  换机 "); err != nil || v != "换机" {
		t.Fatalf("%q %v", v, err)
	}
	for _, bad := range []string{strings.Repeat("字", 501), "a\x00"} {
		_, err := nodeValidateReason(bad)
		nodeTestErr(t, err, errs.ValidationFailed)
	}
}
