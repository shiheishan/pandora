package subscription

// 由 .claude/skills/subscription-e2e/scripts/run.sh 经 `go test -overlay` 注入 subscription 包，不落仓库。
//
// 把表单形状夹具（render_fixtures_test.go 的 formFixtures）加上可选的额外夹具（E2E_EXTRA），
// 逐个、再整份渲染成 clash / clash-premium / singbox / uri，写到 E2E_OUT：
//   - <id>.<格式>、ALL.<格式>、EMPTY.<格式>：渲染结果；
//   - fixtures_index.json：每个夹具的协议、端口、表单配置、来源和后台校验结论（nodefabric 那一步要用）；
//   - summary.json：每个夹具在每种格式里实际写出的节点数（0 即被渲染器跳过）。
//
// 只用包里稳定的入口：Render、formFixtures、Node 与几个夹具常量。它们改名时同步改这里。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

func TestE2EDump(t *testing.T) {
	out := os.Getenv("E2E_OUT")
	if out == "" {
		t.Skip("E2E_OUT 未设置：这个测试只由 subscription-e2e 的 run.sh 调用")
	}
	// 格式名用字符串转换而不是包里的常量：修前的基点可能还没有 clash-premium，
	// 由 run.sh 的 --formats 传 E2E_FORMATS 去掉它即可编译
	names := "clash,clash-premium,singbox,uri"
	if v := os.Getenv("E2E_FORMATS"); v != "" {
		names = v
	}
	var formats []Format
	for _, n := range strings.Split(names, ",") {
		formats = append(formats, Format(strings.TrimSpace(n)))
	}

	type entry struct {
		id, typ, source string
		port            int
		config          string
	}
	var entries []entry
	for _, f := range formFixtures() {
		entries = append(entries, entry{id: f.id, typ: f.typ, source: "form", port: f.port, config: f.config})
	}
	if p := os.Getenv("E2E_EXTRA"); p != "" {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读额外夹具: %v", err)
		}
		// 额外夹具里不写密钥：REALITY 密钥对用占位符，换成包里那对测试专用的虚构值
		s := strings.NewReplacer("@REALITY_PRIVATE@", fixtureRealityPri, "@REALITY_PUBLIC@", fixtureRealityPub).Replace(string(raw))
		var extra []struct {
			ID     string          `json:"id"`
			Type   string          `json:"type"`
			Port   int             `json:"port"`
			Config json.RawMessage `json:"config"`
		}
		if err := json.Unmarshal([]byte(s), &extra); err != nil {
			t.Fatalf("额外夹具不是 [{id,type,port,config}] 数组: %v", err)
		}
		for _, x := range extra {
			entries = append(entries, entry{id: x.ID, typ: x.Type, source: "extra", port: x.Port, config: string(x.Config)})
		}
	}

	index := map[string]any{}
	summary := map[string]map[string]int{}
	var all []Node
	for _, e := range entries {
		if _, dup := index[e.id]; dup || e.id == "ALL" || e.id == "EMPTY" {
			t.Fatalf("夹具 id 重复或占用了保留名: %s", e.id)
		}
		n := Node{Name: e.id, Type: e.typ, Host: fixtureHost, Port: e.port}
		if err := json.Unmarshal([]byte(e.config), &n.Config); err != nil {
			t.Fatalf("%s: config 不是 JSON 对象: %v", e.id, err)
		}
		version, fields := nodefabric.ValidateAdminProtocolConfig(e.typ, "pandora-native", e.port, json.RawMessage(e.config))
		index[e.id] = map[string]any{
			"type": e.typ, "port": e.port, "config": json.RawMessage(e.config), "source": e.source,
			"admin_valid": version == nodefabric.StableProtocolSchemaVersion && len(fields) == 0, "admin_fields": fields,
		}
		summary[e.id] = map[string]int{}
		for _, f := range formats {
			body, _, cnt := Render(f, []Node{n}, fixtureUUID)
			summary[e.id][string(f)] = cnt
			e2eWrite(t, filepath.Join(out, fmt.Sprintf("%s.%s", e.id, f)), body)
		}
		all = append(all, n)
	}
	summary["ALL"], summary["EMPTY"] = map[string]int{}, map[string]int{}
	for _, f := range formats {
		body, _, cnt := Render(f, all, fixtureUUID)
		summary["ALL"][string(f)] = cnt
		e2eWrite(t, filepath.Join(out, "ALL."+string(f)), body)
		body, _, cnt = Render(f, nil, fixtureUUID)
		summary["EMPTY"][string(f)] = cnt
		e2eWrite(t, filepath.Join(out, "EMPTY."+string(f)), body)
	}
	b, _ := json.MarshalIndent(index, "", " ")
	e2eWrite(t, filepath.Join(out, "fixtures_index.json"), b)
	b, _ = json.MarshalIndent(summary, "", " ")
	e2eWrite(t, filepath.Join(out, "summary.json"), b)
	t.Logf("渲染了 %d 个夹具 × %d 种格式", len(entries), len(formats))
}

func e2eWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
