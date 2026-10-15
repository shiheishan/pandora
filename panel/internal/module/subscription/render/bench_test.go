package render

// 渲染的基准：闸门里订阅拉取的 CPU 大头在这里（mihomo UA、200 个节点）。
// 改渲染热路径前后各跑一遍：go test -run '^$' -bench . -benchtime 2s -count 6 ./internal/module/subscription/render/

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// benchNodes 把夹具节点轮流复制到 n 个，名字加序号免得去重名改名。
func benchNodes(b *testing.B, n int) []Node {
	raw, err := os.ReadFile("testdata/nodes.json")
	if err != nil {
		b.Fatal(err)
	}
	var fx []fixtureNode
	if err := json.Unmarshal(raw, &fx); err != nil {
		b.Fatal(err)
	}
	out := make([]Node, 0, n)
	for i := 0; len(out) < n; i++ {
		nd := fx[i%len(fx)].toNode()
		nd.Name = fmt.Sprintf("%s %d", nd.Name, i)
		out = append(out, nd)
	}
	return out
}

func BenchmarkRenderClashMeta200(b *testing.B) {
	nodes := benchNodes(b, 200)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Render(FormatClash, nodes, "0b5d5d4e-7a1e-4c49-9d0e-3c3f0a6c2b11", "mihomo/1.19.0", Options{})
	}
}
