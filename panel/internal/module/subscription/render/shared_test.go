package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

// renderPlain 是不带后台模板、过滤参数与信息节点的 RenderRequest，测试里按格式逐个渲染时用。
func renderPlain(f Format, nodes []Node, proxyUUID, ua string, opt Options) ([]byte, string, int) {
	return RenderRequest(Request{Format: f, Nodes: nodes, ProxyUUID: proxyUUID, UA: ua, Options: opt})
}

// 节点缓存里的 []Node 由所有命中者共享：RenderRequest 不能改它（包括协议配置 map 里嵌套的
// 切片与 map），并发渲染同一份也必须得到相同的输出。覆盖全部格式与带前处理的 RenderRequest。
// 检查机的 race 跑这条。
//
// 夹具来自 testdata/nodes.json：经 JSON 反序列化，嵌套的是 []any 与 map[string]any，
// 和从库里读出来的一样；其中有重名、保留名、空名，去重名也不能改到原切片。
func TestRenderLeavesCachedNodesUntouched(t *testing.T) {
	nodes := loadNodes(t)
	snapshot := deepCopyNodes(t, nodes)

	allFormats := []Format{
		FormatClash, FormatClashPremium, FormatStash, FormatSingbox, FormatLoon,
		FormatSurfboard, FormatSIP008, FormatShadowrocket, FormatURI,
	}

	preprocessStatus := Status{
		Upload: 1 << 20, Download: 2 << 20, Total: 10 << 30,
		Expire: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC),
	}
	preprocessInfo := []string{"剩余流量：1.00 GB", "套餐到期：2026-11-14"}
	preprocessSubscribe := "https://panel.example.test/sub/abc"

	preprocessRequest := func(f Format) Request {
		return Request{
			Format: f, Nodes: nodes, ProxyUUID: fixtureUUID,
			ProtocolPrefix: true,
			InfoLines:      preprocessInfo,
			Status:         preprocessStatus,
			SubscribeURL:   preprocessSubscribe,
		}
	}

	var variants []Request
	for _, f := range allFormats {
		for _, ua := range []string{"", "SFA/1.14.2 (614; sing-box 1.14.2; language zh_CN)"} {
			for _, opt := range []Options{
				{},
				{Singbox: SingboxTemplate{GeositeURLPrefix: "https://mirror.example.test/geosite/", BlockAds: true}},
			} {
				variants = append(variants, Request{
					Format: f, Nodes: nodes, ProxyUUID: fixtureUUID, UA: ua, Options: opt,
				})
			}
		}
		variants = append(variants, preprocessRequest(f))
		filtered := preprocessRequest(f)
		filtered.Types = []string{"vless", "shadowsocks", "trojan"}
		filtered.Keywords = []string{"ss", "vless"}
		variants = append(variants, filtered)
	}
	variants = append(variants, Request{
		Format: FormatLoon, Nodes: nodes, ProxyUUID: fixtureUUID,
		ProtocolPrefix: true,
		InfoLines:      preprocessInfo,
		Status:         preprocessStatus,
		SubscribeURL:   preprocessSubscribe,
		Template:       "# 自定义\n{{proxies}}\n",
	})
	variants = append(variants, Request{
		Format: FormatSurfboard, Nodes: nodes, ProxyUUID: fixtureUUID,
		ProtocolPrefix: true,
		InfoLines:      preprocessInfo,
		Status:         preprocessStatus,
		SubscribeURL:   preprocessSubscribe,
		Template:       defaultTemplateFor(FormatSurfboard, Options{}) + "\n# 自定义\n",
	})

	want := make([][]byte, len(variants))
	for i, req := range variants {
		want[i], _, _ = RenderRequest(req)
	}

	var wg sync.WaitGroup
	errs := make(chan string, len(variants)*16)
	for range 16 {
		for i, req := range variants {
			wg.Add(1)
			go func(idx int, r Request) {
				defer wg.Done()
				body, _, _ := RenderRequest(r)
				if !bytes.Equal(body, want[idx]) {
					errs <- fmt.Sprintf("%s #%d", r.Format, idx)
				}
			}(i, req)
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent render of the shared node slice changed the output: %s", e)
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
