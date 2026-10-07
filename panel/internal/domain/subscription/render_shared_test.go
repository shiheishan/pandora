package subscription

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

// 节点缓存里的 []Node 由所有命中者共享：Render 不能改它（包括协议配置 map 里嵌套的
// 切片与 map），并发渲染同一份也必须得到相同的输出。检查机的 race 跑这条。
func TestRenderLeavesCachedNodesUntouched(t *testing.T) {
	const credential = "019f9f00-1111-7222-8333-444444444444"
	raw := []struct {
		name, typ, config string
	}{
		{"香港", "vless", `{"network":"tcp","security":"reality","public_key":"pk","server_names":["a.example.com","b.example.com"],"short_ids":["01ab"],"flow":"xtls-rprx-vision"}`},
		{"香港", "vmess", `{"network":"ws","tls":true,"server_name":"edge.example.com","path":"/ws","host":"edge.example.com"}`},
		{"自动选择", "trojan", `{"network":"tcp","server_name":"edge.example.com"}`},
		{"", "hysteria2", `{"server_name":"edge.example.com","obfs_password":"secret"}`},
		{"东京", "shadowsocks", `{"method":"2022-blake3-aes-128-gcm"}`},
		{"东京", "tuic", `{"server_name":"edge.example.com","congestion_control":"bbr"}`},
		{"新加坡", "vless", `{"network":"mkcp","mask":"wechat-video","mask_password":"x"}`},
		{"首尔", "anytls", `{"server_name":"edge.example.com"}`},
	}
	nodes := make([]Node, 0, len(raw))
	for i, r := range raw {
		n := Node{Name: r.name, Type: r.typ, Host: "203.0.113.9", Port: 443 + i, TrafficRate: 1, HeartbeatFresh: true}
		// 与 listEligibleNodesTx 一样从 JSON 反序列化：嵌套的是 []any 与 map[string]any
		if err := json.Unmarshal([]byte(r.config), &n.Config); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	snapshot := deepCopyNodes(t, nodes)

	formats := []Format{FormatClash, FormatSingbox, FormatURI}
	want := map[Format][]byte{}
	for _, f := range formats {
		body, _, _ := Render(f, nodes, credential)
		want[f] = body
	}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for range 16 {
		for _, f := range formats {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body, _, _ := Render(f, nodes, credential)
				if !bytes.Equal(body, want[f]) {
					errs <- string(f)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for f := range errs {
		t.Fatalf("concurrent render of the shared node slice changed the %s output", f)
	}
	if !reflect.DeepEqual(nodes, snapshot) {
		t.Fatal("Render mutated the shared node slice or its protocol configs")
	}
}

func deepCopyNodes(t *testing.T, nodes []Node) []Node {
	t.Helper()
	out := make([]Node, len(nodes))
	for i, n := range nodes {
		out[i] = n
		raw, err := json.Marshal(n.Config)
		if err != nil {
			t.Fatal(err)
		}
		out[i].Config = nil
		if err := json.Unmarshal(raw, &out[i].Config); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
