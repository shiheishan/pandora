package routebudget

import (
	"fmt"
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
	joined := fmt.Sprint(got)
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

func TestRaiseTrailerNamesBudgets(t *testing.T) {
	got := ApprovedNames("feat: x\n\nBudget-Raise: public GET /v1/me the session now carries a second factor\n" +
		"Budget-Raise: pushWALBudget one more column per row\n" +
		"Budget-Raise: node POST /push\n" + // 没写理由
		"Budget-Raise: login is slower\n" + // 没点名
		"Budget-Raise:\n" +
		"text Budget-Raise: admin GET /x inline\n")
	want := map[string]bool{"public GET /v1/me": true, WALBudgetName: true}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("approved = %v, want %v", got, want)
	}
	left := Unapproved([]Change{{Name: "public GET /v1/me"}, {Name: "public GET /v1/me/balance"}, {Name: WALBudgetName}},
		"x\n\nBudget-Raise: public GET /v1/me reason\n")
	if len(left) != 2 || left[0].Name != "public GET /v1/me/balance" || left[1].Name != WALBudgetName {
		t.Fatalf("unapproved = %v", left)
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

// 当前的登记表读不到（文件挪走、常量没跟着改）要报错，与 WAL 一样，不能把基点的行
// 都当成「路由删了」放过（复审 N2）。
func TestRaisesRequiresCurrentRoutes(t *testing.T) {
	if _, err := Raises([]byte("public GET /a 2 1\n"), nil, nil, nil); err == nil {
		t.Fatal("a missing current routes.txt passed silently")
	}
}

// 改了模板、新行填了更大的预算：不是「删一条加一条」那么简单，新出现的预算行要单列出来。
func TestRaisesListsNewBudgetedRows(t *testing.T) {
	got, err := Raises([]byte("public GET /b 2 1\npublic GET /c - -\n"),
		[]byte("public GET /b2 9 9\npublic GET /c 4 1\n"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := fmt.Sprint(got)
	if !strings.Contains(joined, "public GET /b2") || strings.Contains(joined, "public GET /c") {
		t.Fatalf("new budgeted row /b2 must be listed, first measurement of /c must not: %v", got)
	}
}
