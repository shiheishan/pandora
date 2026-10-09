package routebudget

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRaisesCatchesWiderBudgets(t *testing.T) {
	base := []byte("public GET /a 2 1\npublic GET /b 3 1\npublic GET /c 1 0\npublic GET /d - -\npublic GET /gone 1 1\n")
	node := func(n string) []byte { return []byte("package node\n\nconst pushWALBudget = " + n + "\n") }

	// 改小、新填预算、删路由都不算变宽
	same, err := Raises(base, []byte("public GET /a 1 1\npublic GET /b 3 1\npublic GET /c 1 0\npublic GET /d 4 0\n"),
		node("2688"), node("1900"))
	if err != nil || len(same) != 0 {
		t.Fatalf("narrowing reported as raise: %v %v", same, err)
	}

	got, err := Raises(base, []byte("public GET /a 3 1\npublic GET /b 3 2\npublic GET /c - -\npublic GET /d - -\n"),
		node("2688"), node("4096"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"public GET /a raised from 2 db / 1 kv to 3 db / 1 kv",
		"public GET /b raised from 3 db / 1 kv to 3 db / 2 kv",
		"public GET /c left the budget",
		"pushWALBudget raised from 2688 to 4096",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if len(got) != 4 {
		t.Errorf("raises = %d, want 4:\n%s", len(got), joined)
	}

	// 当前文件里找不到 WAL 上限（改名、挪走）要报错，不能静默放过
	if _, err := Raises(nil, nil, node("2688"), []byte("package node\n")); err == nil {
		t.Fatal("a vanished pushWALBudget must be an error")
	}
	// 基点还没有预算文件：不比
	if got, err := Raises(nil, base, nil, node("1")); err != nil || len(got) != 0 {
		t.Fatalf("missing base files: %v %v", got, err)
	}
}

func TestRaiseTrailer(t *testing.T) {
	if !HasRaiseTrailer("fix: x\n\nBudget-Raise: login now verifies a second factor\n") {
		t.Fatal("trailer with a reason not recognized")
	}
	for _, msg := range []string{"fix: x\n", "fix: x\n\nBudget-Raise:\n", "fix: x Budget-Raise: inline\n"} {
		if HasRaiseTrailer(msg) {
			t.Fatalf("accepted %q", msg)
		}
	}
}

// 守卫读的就是仓库里的这两份文件：当前的都能解析出预算。
func TestRatchetReadsCurrentBudgets(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	routes, err := os.ReadFile(filepath.Join(root, RoutesFile))
	if err != nil {
		t.Fatal(err)
	}
	nodeTest, err := os.ReadFile(filepath.Join(root, NodeBudgetFile))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Raises(routes, routes, nodeTest, nodeTest); err != nil || len(got) != 0 {
		t.Fatalf("current budgets compared with themselves: %v %v", got, err)
	}
}
