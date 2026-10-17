package handler

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
)

// 前端假后端的协议 schema 是 GET /v1/node-protocol-schemas 响应的原样导出。
// 改了 protocol 的 schema 或这里的 DTO 后，在 panel/ 下重导：
//
//	go test ./internal/module/node/handler/ -run TestMockNodeSchemasMatchGo -update
var updateMockSchemas = flag.Bool("update", false, "重写前端假后端的 node-schemas.ts")

var mockSchemasPath = filepath.Join("..", "..", "..", "..", "frontend", "dev", "mock", "admin", "node-schemas.ts")

// renderMockSchemas 按 GET /v1/node-protocol-schemas 的响应形状拼出 node-schemas.ts 的全文。
func renderMockSchemas(t *testing.T) []byte {
	t.Helper()
	list := protocol.Schemas()
	out := make([]nodeProtocolSchemaResponse, 0, len(list))
	for _, s := range list {
		out = append(out, nodeProtocolSchemaFrom(s))
	}
	b, err := json.MarshalIndent(nodeProtocolSchemasResponse{Schemas: out}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return []byte("// prettier-ignore\nexport const NODE_PROTOCOL_SCHEMAS = " + string(b) + " as const\n")
}

func TestMockNodeSchemasMatchGo(t *testing.T) {
	want := renderMockSchemas(t)
	if *updateMockSchemas {
		if err := os.WriteFile(mockSchemasPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(mockSchemasPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s 与 Go 的协议 schema 不一致；在 panel/ 下跑 go test ./internal/module/node/handler/ -run TestMockNodeSchemasMatchGo -update 重导", mockSchemasPath)
	}
}
