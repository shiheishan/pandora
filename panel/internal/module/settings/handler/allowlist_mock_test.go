package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// 前端假后端的只读模式豁免表必须与 adminWriteAllowlist 逐条相同，页面在假后端上的只读表现才可信。
func TestAdminWriteAllowlistMatchesMock(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "frontend", "dev", "mock-api.ts"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const ADMIN_WRITE_ALLOWLIST = \[(.*?)\] as const`).FindSubmatch(src)
	if block == nil {
		t.Fatal("mock-api.ts 里找不到 ADMIN_WRITE_ALLOWLIST（格式变了就改这里的解析）")
	}
	var mock []string
	for _, m := range regexp.MustCompile(`'([A-Z]+ /[^']*)'`).FindAllSubmatch(block[1], -1) {
		mock = append(mock, string(m[1]))
	}
	var want []string
	for k := range adminWriteAllowlist {
		want = append(want, k)
	}
	slices.Sort(mock)
	slices.Sort(want)
	if !slices.Equal(mock, want) {
		t.Fatalf("adminWriteAllowlist 与假后端 ADMIN_WRITE_ALLOWLIST 不一致\nGo:   %v\nmock: %v", want, mock)
	}
}
