package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// 节点缓存里的 []Node 由所有命中者共享：Render 不能改它（包括协议配置 map 里嵌套的
// 切片与 map），并发渲染同一份也必须得到相同的输出。检查机的 race 跑这条。
//
// 夹具来自 testdata/nodes.json：经 JSON 反序列化，嵌套的是 []any 与 map[string]any，
// 和从库里读出来的一样；其中有重名、保留名、空名，去重名也不能改到原切片。
func TestRenderLeavesCachedNodesUntouched(t *testing.T) {
	nodes := loadNodes(t)
	snapshot := deepCopyNodes(t, nodes)

	type variant struct {
		f   Format
		ua  string
		opt Options
	}
	var variants []variant
	for _, f := range []Format{FormatClash, FormatClashPremium, FormatSingbox, FormatURI} {
		for _, ua := range []string{"", "SFA/1.14.2 (614; sing-box 1.14.2; language zh_CN)"} {
			for _, opt := range []Options{{}, {Singbox: SingboxTemplate{GeositeURLPrefix: "https://mirror.example.test/geosite/", BlockAds: true}}} {
				variants = append(variants, variant{f, ua, opt})
			}
		}
	}
	want := make([][]byte, len(variants))
	for i, v := range variants {
		want[i], _, _ = Render(v.f, nodes, fixtureUUID, v.ua, v.opt)
	}

	var wg sync.WaitGroup
	errs := make(chan string, len(variants)*16)
	for range 16 {
		for i, v := range variants {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body, _, _ := Render(v.f, nodes, fixtureUUID, v.ua, v.opt)
				if !bytes.Equal(body, want[i]) {
					errs <- fmt.Sprintf("%s ua=%q opt=%+v", v.f, v.ua, v.opt)
				}
			}()
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
