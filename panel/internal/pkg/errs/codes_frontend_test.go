package errs

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// 前端 core/api.ts 按 SERVER_ERROR_CODES 识别错误码，表外的码会被当成 invalid_response。
func TestServerErrorCodesMatchFrontend(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "core", "api.ts"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const SERVER_ERROR_CODES = \[(.*?)\] as const`).FindSubmatch(src)
	if block == nil {
		t.Fatal("api.ts 里找不到 SERVER_ERROR_CODES（格式变了就改这里的解析）")
	}
	// 去掉行注释再取字符串，注释里的引号不算
	body := regexp.MustCompile(`//[^\n]*`).ReplaceAll(block[1], nil)
	var ts []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllSubmatch(body, -1) {
		ts = append(ts, string(m[1]))
	}
	var gocodes []string
	for c := range specs {
		gocodes = append(gocodes, string(c))
	}
	slices.Sort(ts)
	slices.Sort(gocodes)
	if !slices.Equal(ts, gocodes) {
		t.Fatalf("errs 的错误码表与前端 SERVER_ERROR_CODES 不一致\nGo: %v\nTS: %v", gocodes, ts)
	}
}
