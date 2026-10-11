package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/tools/routebudget"
)

// gitRepo 在临时目录里造一个仓库：主线一个提交（预算基线），远端主线引用指向它，
// 再切到任务分支。只用本机 git，不连网络。
type gitRepo struct {
	t    *testing.T
	dir  string
	base string // 主线提交
}

func newRepo(t *testing.T, routes string) *gitRepo {
	t.Helper()
	r := &gitRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "feat/panel-redesign")
	r.git("config", "user.email", "ratchet@test.invalid")
	r.git("config", "user.name", "ratchet test")
	r.git("config", "commit.gpgsign", "false")
	r.write(routebudget.RoutesFile, routes)
	r.write(routebudget.NodeBudgetFile, "package node\n\nconst pushWALBudget = 2688\n")
	r.base = r.commit("mainline budgets")
	r.git("update-ref", "refs/remotes/origin/feat/panel-redesign", r.base)
	r.git("checkout", "-q", "-b", "feat/task")
	return r
}

func (r *gitRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *gitRepo) write(path, body string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *gitRepo) commit(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

// ratchetOnPush 在仓库里以 GitHub push 事件的样子跑闸门：before 是推送前的头，ref 是推到的分支。
func (r *gitRepo) ratchetOnPush(before, branch string) error {
	r.t.Helper()
	ev, _ := json.Marshal(map[string]string{"before": before, "ref": "refs/heads/" + branch})
	path := filepath.Join(r.t.TempDir(), "event.json")
	if err := os.WriteFile(path, ev, 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.t.Setenv("GITHUB_EVENT_PATH", path)
	r.t.Setenv("GITHUB_REF", "refs/heads/"+branch)
	r.t.Setenv("BUDGET_BASE", "")
	r.t.Chdir(r.dir)
	return run()
}

const baseRoutes = "public GET /a 2 1\npublic GET /b 3 1\n"

// 任务分支上的放宽不能被下一次不相干的推送洗绿（复审 N1）：基点是与主线的合并基点，
// 不是推送前的头。
func TestRaiseOnTaskBranchStaysRedAfterUnrelatedPush(t *testing.T) {
	r := newRepo(t, baseRoutes)
	r.write(routebudget.RoutesFile, "public GET /a 3 1\npublic GET /b 3 1\n")
	x := r.commit("feat: something that costs a round trip")
	if err := r.ratchetOnPush(r.base, "feat/task"); err == nil {
		t.Fatal("raise without a trailer passed")
	}
	r.write("README", "unrelated\n")
	r.commit("docs: unrelated")
	if err := r.ratchetOnPush(x, "feat/task"); err == nil {
		t.Fatal("an unrelated push after the raise washed the gate green")
	}
}

// 尾注只放过点名的那一条：点名 /a 时 /b 的放宽仍红；WAL 用固定名字 pushWALBudget 点名。
func TestRaiseTrailerApprovesOnlyNamedBudgets(t *testing.T) {
	r := newRepo(t, baseRoutes)
	r.write(routebudget.RoutesFile, "public GET /a 3 1\npublic GET /b 4 1\n")
	r.write(routebudget.NodeBudgetFile, "package node\n\nconst pushWALBudget = 4096\n")
	r.commit("feat: costs more\n\nBudget-Raise: public GET /a the new check needs one more read")
	err := r.ratchetOnPush(r.base, "feat/task")
	if err == nil || !strings.Contains(err.Error(), "public GET /b") || !strings.Contains(err.Error(), "pushWALBudget") ||
		strings.Contains(err.Error(), "public GET /a ") {
		t.Fatalf("only /a was named; err = %v", err)
	}
	r.commit("chore: name the rest\n\nBudget-Raise: public GET /b same reason\nBudget-Raise: pushWALBudget wider rows")
	if err := r.ratchetOnPush(r.base, "feat/task"); err != nil {
		t.Fatalf("every raise named, still red: %v", err)
	}
}

// 推到主线本身时基点才是推送前的头：合进来的那一段与上一个主线头比。
func TestMainlinePushComparesWithBefore(t *testing.T) {
	r := newRepo(t, baseRoutes)
	r.git("checkout", "-q", "feat/panel-redesign")
	r.write(routebudget.RoutesFile, "public GET /a 1 1\npublic GET /b 3 1\n")
	r.commit("perf: one fewer read")
	if err := r.ratchetOnPush(r.base, "feat/panel-redesign"); err != nil {
		t.Fatalf("narrowing on the mainline flagged: %v", err)
	}
	r.write(routebudget.RoutesFile, "public GET /a 5 1\npublic GET /b 3 1\n")
	r.commit("feat: wider")
	if err := r.ratchetOnPush(r.git("rev-parse", "HEAD~1"), "feat/panel-redesign"); err == nil {
		t.Fatal("raise pushed to the mainline passed")
	}
}

func TestBaseCandidates(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	refs := func(cs []candidate) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.ref)
		}
		return strings.Join(s, ",")
	}
	mainlineTail := "origin/feat/panel-redesign,origin/main"
	for _, c := range []struct {
		name  string
		env   map[string]string
		event string
		want  string
	}{
		{"local or checker replay", nil, "", mainlineTail},
		{"manual base", map[string]string{"BUDGET_BASE": "abc"}, "", "abc," + mainlineTail},
		{"push to a task branch ignores before", nil,
			`{"before":"b1","ref":"refs/heads/feat/panel-redesign-w12guard"}`, mainlineTail},
		{"push to the mainline uses before", nil, `{"before":"b1","ref":"refs/heads/feat/panel-redesign"}`,
			"b1," + mainlineTail},
		{"push to main uses before", nil, `{"before":"b2","ref":"refs/heads/main"}`, "b2," + mainlineTail},
		{"ref from the environment", map[string]string{"GITHUB_REF": "refs/heads/main"}, `{"before":"b3"}`,
			"b3," + mainlineTail},
		{"pull request uses its base", nil, `{"before":"b4","ref":"refs/pull/7/merge","pull_request":{"base":{"sha":"p1"}}}`,
			"p1," + mainlineTail},
		{"garbage event", nil, `{`, mainlineTail},
	} {
		if got := refs(baseCandidates(env(c.env), []byte(c.event))); got != c.want {
			t.Errorf("%s: candidates = %s, want %s", c.name, got, c.want)
		}
	}
}
