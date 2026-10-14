package protocol

// 由 .agents/skills/subscription-e2e/scripts/run.sh 经 `go test -overlay` 注入 protocol 包，不落仓库。
//
// 从 parityFixtures() 的 form 组与可选 E2E_EXTRA 生成 fixtures_index.json：每项含 type、port、表单 config、
// kernel（KernelConfig 输出）、source、admin_valid、admin_fields（Validate 结论）。依赖 parityFixtures、
// parityRealityPri、parityRealityPub、Validate、KernelConfig、CanonicalType、schemaVersion。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EFixtures(t *testing.T) {
	out := os.Getenv("E2E_OUT")
	if out == "" {
		t.Skip("E2E_OUT 未设置：这个测试只由 subscription-e2e 的 run.sh 调用")
	}

	type fixtureEntry struct {
		id, typ, source string
		port            int
		config          string
	}
	var entries []fixtureEntry
	formCount := 0
	for _, f := range parityFixtures() {
		if f.group != "form" {
			continue
		}
		entries = append(entries, fixtureEntry{id: f.name, typ: f.typ, source: "form", port: f.port, config: f.config})
		formCount++
	}

	extraCount := 0
	if p := os.Getenv("E2E_EXTRA"); p != "" {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读额外夹具: %v", err)
		}
		s := strings.NewReplacer("@REALITY_PRIVATE@", parityRealityPri, "@REALITY_PUBLIC@", parityRealityPub).Replace(string(raw))
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
			entries = append(entries, fixtureEntry{
				id: x.ID, typ: x.Type, source: "extra", port: x.Port, config: string(x.Config),
			})
			extraCount++
		}
	}

	index := map[string]any{}
	for _, e := range entries {
		if _, dup := index[e.id]; dup || e.id == "ALL" || e.id == "EMPTY" {
			t.Fatalf("夹具 id 重复或占用了保留名: %s", e.id)
		}
		var stored map[string]any
		if err := json.Unmarshal([]byte(e.config), &stored); err != nil || stored == nil {
			t.Fatalf("%s: config 不是 JSON 对象: %v", e.id, err)
		}
		version, fields := Validate(e.typ, "pandora-native", e.port, json.RawMessage(e.config))
		index[e.id] = map[string]any{
			"type":         CanonicalType(e.typ),
			"port":         e.port,
			"config":       json.RawMessage(e.config),
			"kernel":       KernelConfig(e.typ, stored),
			"source":       e.source,
			"admin_valid":  version == schemaVersion && len(fields) == 0,
			"admin_fields": fields,
		}
	}

	b, err := json.MarshalIndent(index, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	e2eWriteFixtures(t, filepath.Join(out, "fixtures_index.json"), b)
	t.Logf("夹具 %d 个（表单 %d、额外 %d）", len(entries), formCount, extraCount)
}

func e2eWriteFixtures(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
