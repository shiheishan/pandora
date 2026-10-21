package service

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// frontendOnlyActions 是前端 ACTION_NAMES 里不来自审计写入的代号：访问日志把订阅拉取日志归并进来时用的伪动作。
var frontendOnlyActions = []string{"subscription.fetch"}

// TestAuditActionsMatchFrontend 是审计动作的前后端对照守卫：
// 静态扫描 Go 源码（actions_scan_test.go）收集所有写进 audit Event.Action 的代号，
// 与前端 panel/frontend/src/admin/screens/security/logic.ts 的 ACTION_NAMES 表双向比对。
// 后端新增、改名、删除审计动作时，必须同步修改前端表；前端多出的代号只允许是访问日志伪动作并登记在 frontendOnlyActions。
func TestAuditActionsMatchFrontend(t *testing.T) {
	panelDir, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	goModRaw, err := os.ReadFile(filepath.Join(panelDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goModSrc := strings.ReplaceAll(string(goModRaw), "\r\n", "\n")
	lines := strings.Split(goModSrc, "\n")
	expectedMod := "module " + auditModulePath
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != expectedMod {
		t.Fatalf("panelDir 计算错误：%s（go.mod 第一行不是 %q，实际为 %q）", panelDir, expectedMod, lines[0])
	}

	actions, unresolved, err := scanAuditActions(panelDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(unresolved) > 0 {
		t.Fatalf("这些审计动作的写法守卫解析不了（改成字面量或常量，或扩展 actions_scan_test.go 的解析）：\n%s", strings.Join(unresolved, "\n"))
	}
	if len(actions) < 100 {
		t.Fatalf("只扫出 %d 个审计动作，扫描器坏了", len(actions))
	}

	logicPath := filepath.Join(panelDir, "frontend", "src", "admin", "screens", "security", "logic.ts")
	tsRaw, err := os.ReadFile(logicPath)
	if err != nil {
		t.Fatal(err)
	}
	tsSrc := strings.ReplaceAll(string(tsRaw), "\r\n", "\n")

	blockRe := regexp.MustCompile(`(?s)export const ACTION_NAMES: ReadonlyArray<readonly \[string, string\]> = \[\n(.*?)\n\]`)
	matches := blockRe.FindStringSubmatch(tsSrc)
	if len(matches) < 2 {
		t.Fatal("logic.ts 里找不到 ACTION_NAMES（格式变了就改这里的解析）")
	}

	entryRe := regexp.MustCompile(`(?m)^\s*\['([^']+)',`)
	entryMatches := entryRe.FindAllStringSubmatch(matches[1], -1)
	frontendActions := make(map[string]bool, len(entryMatches))
	for _, m := range entryMatches {
		frontendActions[m[1]] = true
	}

	frontendOnlySet := make(map[string]bool, len(frontendOnlyActions))
	for _, a := range frontendOnlyActions {
		frontendOnlySet[a] = true
	}

	// 1. Go 有、前端没有
	var goOnly []string
	for act := range actions {
		if !frontendActions[act] {
			goOnly = append(goOnly, act)
		}
	}
	slices.Sort(goOnly)
	if len(goOnly) > 0 {
		var lines []string
		for _, act := range goOnly {
			firstPos := ""
			if len(actions[act]) > 0 {
				firstPos = actions[act][0]
			}
			lines = append(lines, fmt.Sprintf("  %s  (%s)", act, firstPos))
		}
		t.Errorf("Go 写了这些审计动作，前端 ACTION_NAMES 没有中文名（在 panel/frontend/src/admin/screens/security/logic.ts 按代号排序加一行）：\n%s", strings.Join(lines, "\n"))
	}

	// 2. 前端有、Go 没有、且不在 frontendOnlyActions
	var tsOnly []string
	for act := range frontendActions {
		if _, ok := actions[act]; !ok && !frontendOnlySet[act] {
			tsOnly = append(tsOnly, act)
		}
	}
	slices.Sort(tsOnly)
	if len(tsOnly) > 0 {
		var lines []string
		for _, act := range tsOnly {
			lines = append(lines, fmt.Sprintf("  %s", act))
		}
		t.Errorf("前端 ACTION_NAMES 有、Go 已不写的审计动作（删掉这一行；访问日志伪动作登记在 frontendOnlyActions）：\n%s", strings.Join(lines, "\n"))
	}

	// 3. frontendOnlyActions 里的代号必须在前端表里，且不在 Go 集合里（否则提示把它从 frontendOnlyActions 删掉）
	for _, act := range frontendOnlyActions {
		if !frontendActions[act] {
			t.Errorf("frontendOnlyActions 里的代号 %s 不在前端 ACTION_NAMES 里", act)
		}
		if _, ok := actions[act]; ok {
			t.Errorf("frontendOnlyActions 里的代号 %s 已被 Go 代码使用，请从 frontendOnlyActions 删掉", act)
		}
	}
}
