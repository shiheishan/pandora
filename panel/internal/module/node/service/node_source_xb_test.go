package service

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
)

func TestRelayRenderNode(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	t.Run("trojan TLS 父节点与浅拷贝断言", func(t *testing.T) {
		parent := render.Node{
			Name: "父节点",
			Type: "trojan",
			Host: "node.example.com",
			Port: 443,
			Config: map[string]any{
				"security": "none",
				"tls":      true,
			},
			TrafficRate:   1.5,
			ECHConfigList: []byte{0x01, 0x02},
		}
		rel := repo.DeliveryRelay{
			Name: "中转1",
			Host: "relay.example.com",
			Port: 8443,
		}

		child := relayRenderNode(parent, rel)

		if child.Name != "中转1" || child.Host != "relay.example.com" || child.Port != 8443 {
			t.Fatalf("中转节点基本属性不符: %+v", child)
		}
		if child.Config["server_name"] != "node.example.com" {
			t.Fatalf("server_name 应自动补父节点域名，实际: %v", child.Config["server_name"])
		}
		if child.Config["host"] != "node.example.com" {
			t.Fatalf("host 应自动补父节点 Host，实际: %v", child.Config["host"])
		}
		if child.TrafficRate != parent.TrafficRate || !slices.Equal(child.ECHConfigList, parent.ECHConfigList) {
			t.Fatalf("中转应继承父节点展示字段")
		}

		// 断言父节点 Config 未被改动
		if _, ok := parent.Config["server_name"]; ok {
			t.Fatal("父节点 Config 不应被注入 server_name")
		}
		if _, ok := parent.Config["host"]; ok {
			t.Fatal("父节点 Config 不应被注入 host")
		}
	})

	t.Run("中转给 sni 与 REALITY 父节点", func(t *testing.T) {
		parent := render.Node{
			Name: "父节点",
			Type: "vless",
			Host: "node.example.com",
			Port: 443,
			Config: map[string]any{
				"security": "reality",
				"tls":      true,
			},
		}
		rel := repo.DeliveryRelay{
			Name: "中转2",
			Host: "relay.example.com",
			Port: 8443,
			SNI:  strPtr("cdn.example.com"),
		}

		child := relayRenderNode(parent, rel)
		if child.Config["server_name"] != nil {
			t.Fatalf("REALITY 节点不应写 server_name: %v", child.Config["server_name"])
		}
		serverNames, ok := child.Config["server_names"].([]any)
		if !ok || len(serverNames) != 1 || serverNames[0] != "cdn.example.com" {
			t.Fatalf("REALITY server_names 应为 [cdn.example.com], 实际: %v", child.Config["server_names"])
		}

		// 普通 TLS 节点给 sni
		parentTLS := render.Node{
			Name: "父节点TLS",
			Type: "trojan",
			Host: "node.example.com",
			Port: 443,
			Config: map[string]any{
				"security": "tls",
				"tls":      true,
			},
		}
		childTLS := relayRenderNode(parentTLS, rel)
		if childTLS.Config["server_name"] != "cdn.example.com" {
			t.Fatalf("普通 TLS server_name 应为 cdn.example.com，实际: %v", childTLS.Config["server_name"])
		}
	})

	t.Run("父节点地址是 IP 时不补 server_name", func(t *testing.T) {
		parent := render.Node{
			Name: "父节点IP",
			Type: "trojan",
			Host: "203.0.113.10",
			Port: 443,
			Config: map[string]any{
				"security": "none",
				"tls":      true,
			},
		}
		rel := repo.DeliveryRelay{
			Name: "中转3",
			Host: "relay.example.com",
			Port: 8443,
		}

		child := relayRenderNode(parent, rel)
		if _, ok := child.Config["server_name"]; ok {
			t.Fatalf("父节点为 IP 时不应补 server_name: %v", child.Config["server_name"])
		}
		if child.Config["host"] != "203.0.113.10" {
			t.Fatalf("host 仍应补父节点地址: %v", child.Config["host"])
		}
	})
}

func TestDeliverableFromRowsXB(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, loc)

	rows := []repo.DeliveryNodeRow{
		{
			ID:             uuid.New(),
			Name:           "父1",
			NodeType:       "trojan",
			Host:           "p1.example.com",
			Port:           443,
			ProtocolConfig: `{"tls":true}`,
			TrafficRate:    decimal.NewFromInt(1),
			RateSchedule:   `[{"start":"00:00","end":"24:00","rate":"2"}]`,
			ECHConfigList:  []byte{0x00, 0x04, 0xfe, 0x0d},
			HeartbeatFresh: true,
			Relays: []repo.DeliveryRelay{
				{Name: "中转1a", Host: "r1a.example.com", Port: 10001},
				{Name: "中转1b", Host: "r1b.example.com", Port: 10002},
			},
		},
		{
			ID:             uuid.New(),
			Name:           "父2",
			NodeType:       "shadowsocks",
			Host:           "p2.example.com",
			Port:           8388,
			ProtocolConfig: `{"cipher":"aes-128-gcm"}`,
			TrafficRate:    decimal.NewFromInt(1),
			ECHConfigList:  []byte{0x00, 0x04, 0xfe, 0x0d},
			HeartbeatFresh: true,
		},
	}

	nodes, err := deliverableFromRows(rows, nil, now, loc)
	if err != nil {
		t.Fatalf("deliverableFromRows 失败: %v", err)
	}

	// 结果顺序是 父1、中转1a、中转1b、父2
	wantNames := []string{"父1", "中转1a", "中转1b", "父2"}
	gotNames := make([]string, len(nodes))
	for i, n := range nodes {
		gotNames[i] = n.Name
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("节点排序错误: 得到 %v，期望 %v", gotNames, wantNames)
	}

	// 时段倍率生效（父1 TrafficRate 应为 2）
	if nodes[0].TrafficRate != 2.0 || nodes[1].TrafficRate != 2.0 || nodes[2].TrafficRate != 2.0 {
		t.Fatalf("时段倍率未生效: 父1=%v 中转1a=%v", nodes[0].TrafficRate, nodes[1].TrafficRate)
	}

	// ECH: trojan TLS 原样下发
	if !reflect.DeepEqual(nodes[0].ECHConfigList, []byte{0x00, 0x04, 0xfe, 0x0d}) {
		t.Fatalf("trojan ECH 应保留: %v", nodes[0].ECHConfigList)
	}

	// ECH: shadowsocks 不支持 ECH，应为 nil
	if nodes[3].ECHConfigList != nil {
		t.Fatalf("shadowsocks ECHConfigList 应为 nil，实际: %v", nodes[3].ECHConfigList)
	}
}
