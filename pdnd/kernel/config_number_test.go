package kernel

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// decodeSignedRaw 按签名通道（node/signed_config.go）的方式解码：UseNumber，
// 数字到达适配器时是 json.Number 而不是 float64。
func decodeSignedRaw(t *testing.T, payload string) map[string]any {
	t.Helper()
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader([]byte(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		t.Fatalf("decode %s: %v", payload, err)
	}
	return raw
}

// TestSignedConfigNumericFieldsAccepted：10-08 真节点测试里 hy2 带 bandwidth、
// tuic 写数值 heartbeat 的节点在签名下发路径上起不来（json.Number 不被认）。
// 每个协议的数值 / 时长字段都要按签名通道的解码结果喂一遍。
func TestSignedConfigNumericFieldsAccepted(t *testing.T) {
	adapters := map[string]Adapter{
		"hysteria2": &hysteria2Adapter{},
		"tuic":      &tuicAdapter{},
	}
	cases := []struct {
		name     string
		protocol string
		payload  string
	}{
		{"hysteria2 bandwidth 与 udp_timeout", "hysteria2",
			`{"cert_path":"c","key_path":"k","up_mbps":100,"down_mbps":200,"udp_timeout":15,"udp_queue_size":1024}`},
		{"tuic 数值时长", "tuic",
			`{"cert_path":"c","key_path":"k","auth_timeout":3,"heartbeat":10,"udp_timeout":60}`},
		{"tuic 字符串时长仍可用", "tuic",
			`{"cert_path":"c","key_path":"k","heartbeat":"15s"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := InboundSpec{Config: core.InboundConfig{Protocol: tc.protocol, Port: 443, Raw: decodeSignedRaw(t, tc.payload)}}
			if err := adapters[tc.protocol].Validate(spec); err != nil {
				t.Fatalf("签名通道下发的数值字段被拒: %v", err)
			}
		})
	}

	t.Run("hysteria2 取值", func(t *testing.T) {
		raw := decodeSignedRaw(t, `{"up_mbps":100,"udp_timeout":15,"udp_queue_size":2048}`)
		if n, ok := nonNegativeInt(raw["up_mbps"]); !ok || n != 100 {
			t.Fatalf("up_mbps = %d, %v", n, ok)
		}
		if d, err := parseHysteriaDuration(raw["udp_timeout"]); err != nil || d != 15*time.Second {
			t.Fatalf("udp_timeout = %v, %v", d, err)
		}
		if n, err := hysteria2UDPQueueSize(raw); err != nil || n != 2048 {
			t.Fatalf("udp_queue_size = %d, %v", n, err)
		}
	})

	t.Run("reality max_time_diff 与 xver", func(t *testing.T) {
		raw := decodeSignedRaw(t, `{"dest":"example.com:443","server_names":["example.com"],`+
			`"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","short_ids":["01"],"max_time_diff":60,"xver":1}`)
		cfg, err := ParseRealityServerConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MaxTimeDiff != time.Minute || cfg.Xver != 1 {
			t.Fatalf("reality = %+v", cfg)
		}
	})

	t.Run("xhttp 数值与范围", func(t *testing.T) {
		raw := decodeSignedRaw(t, `{"path":"/x","sc_max_buffered_posts":7,"server_max_header_bytes":4096,`+
			`"sc_max_each_post_bytes":{"from":1000,"to":2000}}`)
		cfg, err := ParseXHTTPConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MaxBufferedPosts != 7 || cfg.ServerMaxHeaderBytes != 4096 || cfg.MaxPost != (XHTTPRange{From: 1000, To: 2000}) {
			t.Fatalf("xhttp = %+v", cfg)
		}
	})

	t.Run("mkcp 数值", func(t *testing.T) {
		raw := decodeSignedRaw(t, `{"mtu":1350,"tti":20,"uplink_capacity":5,"downlink_capacity":20,"read_buffer_size":2,"write_buffer_size":2}`)
		cfg, err := ParseMKCPConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MTU != 1350 || cfg.Tick != 20*time.Millisecond {
			t.Fatalf("mkcp = %+v", cfg)
		}
	})

	t.Run("shadowtls version 与 server_port", func(t *testing.T) {
		raw := decodeSignedRaw(t, `{"version":3,"server_port":8443}`)
		if v := shadowRawInt(raw, "version", 0); v != 3 {
			t.Fatalf("version = %d", v)
		}
		if v := shadowRawInt(raw, "server_port", 443); v != 8443 {
			t.Fatalf("server_port = %d", v)
		}
	})
}
