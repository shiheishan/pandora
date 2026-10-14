package realtime

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// realtime.go 的 Topic* 常量与前端 core/query.ts 的 REALTIME_TOPICS 逐个对应：
// 前端少了，这个 topic 的事件没有查询会失效；多了，就是一个永远等不到的信号。
func TestRealtimeTopicsMatchFrontend(t *testing.T) {
	goSrc, err := os.ReadFile("realtime.go")
	if err != nil {
		t.Fatal(err)
	}
	var gotopics []string
	for _, m := range regexp.MustCompile(`(?m)^\s*Topic\w+\s*=\s*"([^"]+)"`).FindAllSubmatch(goSrc, -1) {
		gotopics = append(gotopics, string(m[1]))
	}
	if len(gotopics) == 0 {
		t.Fatal("realtime.go 里解析不出 Topic* 常量（格式变了就改这里的解析）")
	}

	tsSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "core", "query.ts"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const REALTIME_TOPICS = \[(.*?)\] as const`).FindSubmatch(tsSrc)
	if block == nil {
		t.Fatal("query.ts 里找不到 REALTIME_TOPICS（格式变了就改这里的解析）")
	}
	var ts []string
	for _, m := range regexp.MustCompile(`'([a-z._]+)'`).FindAllSubmatch(block[1], -1) {
		ts = append(ts, string(m[1]))
	}

	slices.Sort(gotopics)
	slices.Sort(ts)
	if !slices.Equal(gotopics, ts) {
		t.Fatalf("realtime.go 的 Topic* 与前端 REALTIME_TOPICS 不一致\nGo: %v\nTS: %v", gotopics, ts)
	}
}
