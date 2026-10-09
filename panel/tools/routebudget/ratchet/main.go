// Command ratchet 是往返预算「只降不升」的 CI 闸门：拿基点的预算与当前的比，变宽了
// （某行数字变大、退回「- -」、新出现带预算的行、push 的 WAL 上限变大）就要求基点到 HEAD
// 之间的提交信息逐条点名放行（`Budget-Raise: public GET /v1/me <理由>`、
// `Budget-Raise: pushWALBudget <理由>`），没点名的照样退出 1。
//
//	go run ./tools/routebudget/ratchet            # 在 panel/ 下
//	BUDGET_BASE=<提交> go run ./tools/routebudget/ratchet
//
// 基点见 baseCandidates：任务分支一律是与 origin 主线的合并基点，只有推到主线本身才用
// 推送前的头。一个都取不到就跳过并说明。只用标准库与本机 git。
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

// mainlines 是主线分支：推到它们本身时才用推送前的头做基点。
var mainlines = []string{"feat/panel-redesign", "main"}

// candidate 是一个候选基点：与 HEAD 取合并基点后用它比较。
type candidate struct{ ref, why string }

// baseCandidates 按优先级列出候选基点（纯函数，单测直接验）：
//   - BUDGET_BASE（手动指定）；
//   - pull_request：目标分支；
//   - 推到主线本身：推送前的头，只看这次推送合进来的那一段；
//   - 其余（任务分支的推送、本机、检查机回放）：与 origin 主线的合并基点，即整条分支相对主线
//     的全部改动。不用推送前的头：那样一个已经红的放宽，再推一个不相干的提交就被洗绿。
func baseCandidates(getenv func(string) string, event []byte) []candidate {
	var out []candidate
	if b := getenv("BUDGET_BASE"); b != "" {
		out = append(out, candidate{b, "BUDGET_BASE"})
	}
	var ev struct {
		Before      string `json:"before"`
		Ref         string `json:"ref"`
		PullRequest *struct {
			Base struct {
				SHA string `json:"sha"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if len(event) > 0 && json.Unmarshal(event, &ev) == nil {
		switch {
		case ev.PullRequest != nil:
			out = append(out, candidate{ev.PullRequest.Base.SHA, "pull request base"})
		default:
			ref := ev.Ref
			if ref == "" {
				ref = getenv("GITHUB_REF")
			}
			for _, m := range mainlines {
				if ref == "refs/heads/"+m {
					out = append(out, candidate{ev.Before, "commit before the push to " + m})
				}
			}
		}
	}
	for _, m := range mainlines {
		out = append(out, candidate{"origin/" + m, "merge base with origin/" + m})
	}
	return out
}

func resolveBase() (base, why string) {
	var event []byte
	if path := os.Getenv("GITHUB_EVENT_PATH"); path != "" {
		event, _ = os.ReadFile(path)
	}
	for _, c := range baseCandidates(os.Getenv, event) {
		if b := mergeBase(c.ref); b != "" {
			return b, c.why
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
	fmt.Printf("ratchet: budgets widened since %s (%s):\n", short, why)
	for _, c := range raises {
		fmt.Println("  " + c.Detail)
	}
	left := routebudget.Unapproved(raises, msgs)
	if len(left) == 0 {
		fmt.Printf("ratchet: every one is named by a %s line in the commit messages\n", routebudget.RaiseTrailer)
		return nil
	}
	var lines []string
	for _, c := range left {
		lines = append(lines, "  "+routebudget.RaiseTrailer+" "+c.Name+" <why it needs more>")
	}
	return fmt.Errorf("budgets only go down; to widen one on purpose, name it in a commit message:\n%s",
		strings.Join(lines, "\n"))
}
