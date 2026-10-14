package render

// 由 .agents/skills/subscription-e2e/scripts/run.sh 经 `go test -overlay` 注入 render 包，不落仓库。
//
// 读 E2E_OUT/fixtures_index.json，逐个、再整份（ALL / EMPTY）渲染成 clash / clash-premium / singbox / uri，
// 写到 E2E_OUT：<id>.<格式>、ALL.<格式>、EMPTY.<格式>、summary.json。
// 依赖 Render、Node、Options、fixtureUUID、fixtureHost。格式名用字符串转 Format，不用包内常量（修前基点可能还没有某格式）。
// E2E_UA 只影响 sing-box 的版本写法，空即兼容写法。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestE2EDump(t *testing.T) {
	out := os.Getenv("E2E_OUT")
	if out == "" {
		t.Skip("E2E_OUT 未设置：这个测试只由 subscription-e2e 的 run.sh 调用")
	}

	raw, err := os.ReadFile(filepath.Join(out, "fixtures_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]struct {
		Type   string         `json:"type"`
		Port   int            `json:"port"`
		Kernel map[string]any `json:"kernel"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}

	names := "clash,clash-premium,singbox,uri"
	if v := os.Getenv("E2E_FORMATS"); v != "" {
		names = v
	}
	var formats []Format
	for _, n := range strings.Split(names, ",") {
		formats = append(formats, Format(strings.TrimSpace(n)))
	}

	ua := os.Getenv("E2E_UA")

	ids := make([]string, 0, len(fx))
	for id := range fx {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	summary := map[string]map[string]int{}
	var all []Node
	for _, id := range ids {
		f := fx[id]
		n := Node{Name: id, Type: f.Type, Host: fixtureHost, Port: f.Port, Config: f.Kernel, TrafficRate: 1}
		summary[id] = map[string]int{}
		for _, format := range formats {
			body, _, cnt := Render(format, []Node{n}, fixtureUUID, ua, Options{})
			summary[id][string(format)] = cnt
			e2eWrite(t, filepath.Join(out, fmt.Sprintf("%s.%s", id, format)), body)
		}
		all = append(all, n)
	}

	summary["ALL"], summary["EMPTY"] = map[string]int{}, map[string]int{}
	for _, format := range formats {
		body, _, cnt := Render(format, all, fixtureUUID, ua, Options{})
		summary["ALL"][string(format)] = cnt
		e2eWrite(t, filepath.Join(out, "ALL."+string(format)), body)
		body, _, cnt = Render(format, nil, fixtureUUID, ua, Options{})
		summary["EMPTY"][string(format)] = cnt
		e2eWrite(t, filepath.Join(out, "EMPTY."+string(format)), body)
	}

	b, err := json.MarshalIndent(summary, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	e2eWrite(t, filepath.Join(out, "summary.json"), b)
	t.Logf("渲染了 %d 个夹具 × %d 种格式", len(ids), len(formats))
}

func e2eWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
