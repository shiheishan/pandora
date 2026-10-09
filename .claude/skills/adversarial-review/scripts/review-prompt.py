#!/usr/bin/env python3
"""只读：按模板拼审查用的 prompt / 消息，把能从 git 算出来的占位都填好，打印到 stdout。

用法（在主目录或任一 worktree 里跑）：
  review-prompt.py first <主线> <分支> --what "<一句话内容>" --scratchpad <总协调 scratchpad> \\
      [--name <名字>] [--head <sha>] [--design <设计稿路径>] [-o <输出文件>]
  review-prompt.py rereview <分支> --from <第一轮审查时的头> --scratchpad <…> [--name] [--head] [-o]
  review-prompt.py round2 <分支> --scratchpad <…> [--name] [-o]

- first：填 templates/reviewer-prompt.md。merge-base 用 <主线>（给分支名，不给 brief 里的原基点）；
  头缺省取分支当前的提交，写成完整 sha；检查表节名取 triggers.py 命中的领域；材料只列存在的文件。
  只留「这次的重点」给人写。triggers.py 退出 1（无触发）时照样生成，stderr 提醒。
- rereview：填 templates/rereview-prompt.md 的范围、shortstat、副本路径；「派去修的」「重点」留给人写。
- round2：填 templates/round2-brief.md 的名字、worktree、分支、探针目录；发现逐条留给人写。
- 名字缺省取分支名去掉 feat/panel-redesign- 前缀；worktree 从 git worktree list 按分支找。
"""
import argparse, pathlib, re, subprocess, sys

HERE = pathlib.Path(__file__).resolve().parent.parent
PREFIX = "feat/panel-redesign-"
# triggers.py 的领域名 → checklist.md 的节名（不同名的才写）
SECTION = {"并发与锁": "并发、锁与后台任务"}


def git(*args):
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout.strip()


def worktree_of(branch):
    path = None
    for line in git("worktree", "list", "--porcelain").splitlines():
        if line.startswith("worktree "):
            path = line[len("worktree "):]
        elif line == f"branch refs/heads/{branch}":
            return pathlib.Path(path)
    sys.exit(f"找不到分支 {branch} 的 worktree（已合入、worktree 已删的分支请手填模板）")


def shortstat(rng):
    s = git("diff", "--shortstat", rng)
    num = lambda pat: (re.search(pat, s) or [0, "0"])[1]
    return num(r"(\d+) files? changed"), num(r"(\d+) insertions?"), num(r"(\d+) deletions?")


def common(a):
    name = a.name or a.branch.removeprefix(PREFIX)
    wt = worktree_of(a.branch)
    head = git("rev-parse", a.head or a.branch)
    copy = pathlib.Path(a.scratchpad) / (f"review-{name}-r2" if a.cmd == "rereview" else f"review-{name}")
    return name, wt, head, copy


def sections(main, branch, wt):
    r = subprocess.run([sys.executable, "-I", str(HERE / "scripts" / "triggers.py"), main, branch, "-C", str(wt)],
                       capture_output=True, text=True)
    if r.returncode == 2:
        sys.exit("triggers.py 出错：" + (r.stderr or r.stdout).strip())
    areas = [l[3:].strip() for l in r.stdout.splitlines() if l.startswith("## ")]
    have = {l[3:].strip() for l in (HERE / "checklist.md").read_text(encoding="utf-8").splitlines() if l.startswith("## ")}
    names = [SECTION.get(x, x) for x in areas]
    missing = [n for n in names if n not in have]
    if missing:
        sys.exit(f"checklist.md 里没有这些节：{missing}（triggers.py 或检查表改过节名？同步 SECTION）")
    if r.returncode == 1:
        print("triggers.py：无触发，按 SKILL.md 第 1 节不必审；仍要审时自己挑节名", file=sys.stderr)
    return names, r.stdout


def materials(wt, design):
    c = wt / ".claude"
    out = []
    if (c / "brief.md").exists():
        out.append(f"- 开工说明 `{c / 'brief.md'}`")
    reps = sorted(c.glob("report*.md"), key=lambda p: p.stat().st_mtime)
    if len(reps) == 1:
        out.append(f"- 实现方报告 `{reps[0]}`")
    elif reps:
        out.append("- 实现方报告 " + "、".join(f"`{p}`" for p in reps) + f"（{reps[-1].name} 最新）")
    else:
        out.append("- 实现方报告：没有，以下面的重点为准")
    out.append(f"- 设计稿 `{design}`" if design else "- 设计稿：无")
    return "\n".join(out)


def first(a):
    name, wt, head, copy = common(a)
    mb = git("merge-base", a.main, head)
    n, plus, minus = shortstat(f"{mb}..{head}")
    names, trig = sections(a.main, head, wt)
    s = (HERE / "templates" / "reviewer-prompt.md").read_text(encoding="utf-8")
    s = re.sub(r"(\*\*材料\*\*[^\n]*\n)(- [^\n]*\n)+", lambda m: m[1] + materials(wt, a.design) + "\n", s)
    s = (s.replace("<名字>", name).replace("<一句话内容>", a.what).replace("<scratchpad>", a.scratchpad)
          .replace("`mkdir -p <副本>", f"`mkdir -p {copy}").replace("tar -x -C <副本>", f"tar -x -C {copy}")
          .replace("<worktree>", str(wt)).replace("<头>", head).replace("<merge-base>", mb)
          .replace("<N>", n).replace("<a>", plus).replace("<b>", minus)
          .replace("<节名>", "、".join(names) if names else "（触发脚本无命中，按 diff 自己挑）"))
    print(f"merge-base {mb[:7]}，头 {head[:7]}，{n} 个文件 +{plus}/−{minus}，节：{'、'.join(names) or '无'}",
          file=sys.stderr)
    return s, trig


def rereview(a):
    name, wt, head, copy = common(a)
    prev = git("rev-parse", a.prev)
    n, plus, minus = shortstat(f"{prev}..{head}")
    s = (HERE / "templates" / "rereview-prompt.md").read_text(encoding="utf-8")
    s = (s.replace("<名字>", name).replace("<副本>", str(copy)).replace("<worktree>", str(wt))
          .replace("<上一轮的头>", prev[:7]).replace("<头>", head[:7])
          .replace("<N>", n).replace("<a>", plus).replace("<b>", minus))
    for f in ("review.md", "report-r2.md"):
        if not (wt / ".claude" / f).exists():
            print(f"提醒：{wt}/.claude/{f} 还不存在（先用 accept-task 的 save-report.sh 存）", file=sys.stderr)
    merges = git("rev-list", "--merges", f"{prev}..{head}")
    if merges:
        print(f"提醒：范围里有合并提交 {merges.split()[0][:7]}，按模板去掉主线部分", file=sys.stderr)
    print(f"范围 {prev[:7]}..{head[:7]}，{n} 个文件 +{plus}/−{minus}", file=sys.stderr)
    return s, ""


def round2(a):
    name, wt, head, copy = common(a)
    s = (HERE / "templates" / "round2-brief.md").read_text(encoding="utf-8")
    probe = str(copy) if copy.exists() else f"{copy}（目录不存在：没有探针就删掉这句）"
    s = (s.replace("<名字>", name).replace("<worktree>", str(wt)).replace("<分支>", a.branch)
          .replace("<副本>", probe))
    if not (wt / ".claude" / "review.md").exists():
        print(f"提醒：{wt}/.claude/review.md 还不存在（先存审查报告）", file=sys.stderr)
    return s, ""


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    for c in ("first", "rereview", "round2"):
        p = sub.add_parser(c)
        if c == "first":
            p.add_argument("main")
        p.add_argument("branch")
        p.add_argument("--scratchpad", required=True)
        p.add_argument("--name")
        p.add_argument("-o", "--out")
        if c != "round2":
            p.add_argument("--head", help="缺省取分支当前的提交")
        if c == "first":
            p.add_argument("--what", required=True, help="一句话内容")
            p.add_argument("--design", help="设计稿路径")
        if c == "rereview":
            p.add_argument("--from", dest="prev", required=True, help="第一轮审查时写死的头")
    a = ap.parse_args()
    if a.cmd == "round2":
        a.head = None
    try:
        text, _ = {"first": first, "rereview": rereview, "round2": round2}[a.cmd](a)
    except subprocess.CalledProcessError as e:
        sys.exit(e.stderr.strip())
    if a.cmd == "first":
        left = sorted(set(re.findall(r"<[^<>\n]{1,30}>", text)))
        if left:
            sys.exit(f"模板占位没替换干净：{left}（reviewer-prompt.md 改过措辞？）")
    if a.out:
        pathlib.Path(a.out).write_text(text, encoding="utf-8")
        print(f"已写 {a.out}", file=sys.stderr)
    else:
        print(text, end="")


if __name__ == "__main__":
    main()
