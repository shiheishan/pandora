package service

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
)

// TestSS2022KeyLenParityProtocolRender 对照两份「2022 方法 → 密钥长度」表（node/protocol 与 subscription/render 各一份，
// 纯函数包不能互相 import）：本包两边都依赖，按行为核对。protocol 开放的方法，render 在密钥长度对时写出节点、
// 长度差一个字节时跳过；protocol 不开放的 2022 方法（chacha20），render 也不写。
func TestSS2022KeyLenParityProtocolRender(t *testing.T) {
	const proxyUUID = "5f8c6a2e-3b1d-4c7a-9e2f-0a1b2c3d4e5f"
	methods := []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305"}
	rendered := func(method string, keyLen int) bool {
		cfg := map[string]any{"method": method, "password": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, keyLen))}
		nodes := []render.Node{{Name: "ss2022", Type: "shadowsocks", Host: "node.example.com", Port: 8390, Config: cfg}}
		_, _, n := render.RenderRequest(render.Request{Format: render.FormatSingbox, Nodes: nodes, ProxyUUID: proxyUUID})
		return n == 1
	}
	open := 0
	for _, m := range methods {
		keyLen, ok := protocol.SS2022KeyLen(m)
		if !ok {
			for _, n := range []int{16, 32} {
				if rendered(m, n) {
					t.Fatalf("%s 面板不开放，render 却写出了节点（密钥 %d 字节）", m, n)
				}
			}
			continue
		}
		open++
		if !rendered(m, keyLen) {
			t.Fatalf("%s：protocol 给密钥 %d 字节，render 不认这个长度", m, keyLen)
		}
		if rendered(m, keyLen-1) {
			t.Fatalf("%s：密钥 %d 字节（比 protocol 少 1）render 也写出了节点，两边长度表不一致", m, keyLen-1)
		}
	}
	if open != 2 {
		t.Fatalf("开放的 2022 方法 %d 种，期望 2（加方法时同步本用例与两份表）", open)
	}
}
