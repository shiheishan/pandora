// Command ratchet 是往返预算「只降不升」的 CI 闸门：拿合并基点的预算与当前的比，
// 变宽了（某行数字变大、退回「- -」、push 的 WAL 上限变大）就要求基点到 HEAD 之间的
// 提交信息里有一行 `Budget-Raise: <理由>`，否则退出 1。
//
//	go run ./tools/routebudget/ratchet            # 在 panel/ 下
//	BUDGET_BASE=<提交> go run ./tools/routebudget/ratchet
//
// 基点：BUDGET_BASE；否则 GitHub 事件（push 取推送前的头，pull_request 取目标分支），
// 都与 HEAD 取合并基点；再否则（本机、检查机回放）取 HEAD 与 origin/feat/panel-redesign、
// origin/main 的合并基点。一个都找不到就跳过并说明。只用标准库与本机 git。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/aegispanel/aegis/tools/routebudget"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ratchet:", err)
		os.Exit(1)
	}
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// show 读基点里的文件；文件在基点还不存在时返回 nil。
func show(base, path string) []byte {
	out, err := exec.Command("git", "show", base+":"+path).Output()
	if err != nil {
		return nil
	}
	return out
}

func mergeBase(ref string) string {
	if ref == "" || strings.Trim(ref, "0") == "" {
		return ""
	}
	if _, err := git("cat-file", "-e", ref+"^{commit}"); err != nil {
		return ""
	}
	mb, err := git("merge-base", ref, "HEAD")
	if err != nil {
		return ""
	}
	return mb
}

func resolveBase() (base, why string) {
	if b := mergeBase(os.Getenv("BUDGET_BASE")); b != "" {
		return b, "BUDGET_BASE"
	}
	if path := os.Getenv("GITHUB_EVENT_PATH"); path != "" {
		var ev struct {
			Before      string `json:"before"`
			PullRequest *struct {
				Base struct {
					SHA string `json:"sha"`
				} `json:"base"`
			} `json:"pull_request"`
		}
		if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &ev) == nil {
			if ev.PullRequest != nil {
				if b := mergeBase(ev.PullRequest.Base.SHA); b != "" {
					return b, "pull request base"
				}
			}
			if b := mergeBase(ev.Before); b != "" {
				return b, "commit before the push"
			}
		}
	}
	for _, ref := range []string{"origin/feat/panel-redesign", "origin/main"} {
		if b := mergeBase(ref); b != "" {
			return b, "merge base with " + ref
		}
	}
	return "", ""
}

func run() error {
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("not in a git work tree: %w", err)
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	base, why := resolveBase()
	if base == "" {
		fmt.Println("ratchet: no base commit found (shallow clone or no remote refs); skipped")
		return nil
	}
	head := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		return b
	}
	raises, err := routebudget.Raises(show(base, routebudget.RoutesFile), head(routebudget.RoutesFile),
		show(base, routebudget.NodeBudgetFile), head(routebudget.NodeBudgetFile))
	if err != nil {
		return err
	}
	short := base
	if len(short) > 12 {
		short = short[:12]
	}
	if len(raises) == 0 {
		fmt.Printf("ratchet: no round-trip or WAL budget was raised since %s (%s)\n", short, why)
		return nil
	}
	msgs, err := git("log", "--format=%B", base+"..HEAD")
	if err != nil {
		return fmt.Errorf("read commit messages %s..HEAD: %w", short, err)
	}
	fmt.Printf("ratchet: budgets raised since %s (%s):\n  %s\n", short, why, strings.Join(raises, "\n  "))
	if routebudget.HasRaiseTrailer(msgs) {
		fmt.Printf("ratchet: allowed by a %s line in the commit messages\n", routebudget.RaiseTrailer)
		return nil
	}
	return fmt.Errorf("budgets only go down; to raise one on purpose, add a line\n  %s <why the extra round trips or WAL are needed>\n"+
		"to the commit message", routebudget.RaiseTrailer)
}
