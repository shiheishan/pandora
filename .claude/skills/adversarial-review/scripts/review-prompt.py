#!/usr/bin/env python3
"""只读：按模板拼审查用的 prompt / 消息，把能从 git 算出来的占位都填好，打印到 stdout。

用法（在主目录或任一 worktree 里跑）：
  review-prompt.py first <主线> <分支> --what "<一句话内容>" --scratchpad <总协调 scratchpad> \\
      [--name <名字>] [--head <sha>] [--design <设计稿路径>] [-o <输出文件>]
  review-prompt.py round <分支> --round N --scratchpad <…> [--name] [-o]            （别名 round2）
  review-prompt.py rereview <分支> --round N --from <上一轮审查时的头> --scratchpad <…> [--name] [--head] [-o]

- first：填 templates/reviewer-prompt.md。merge-base 用 <主线>（给分支名，不给 brief 里的原基点）；
  头缺省取分支当前的提交，写成完整 sha；检查表节名取 triggers.py 命中的领域；材料只列存在的文件。
  只留「这次的重点」给人写。triggers.py 退出 1（无触发）时照样生成，stderr 提醒。
- round：填 templates/round-brief.md（第 N 轮修复消息，N ≥ 2）。依据的审查报告：N=2 是 review.md，
  N≥3 是 review-r{N-1}.md；交回存 report-r{N}.md；探针目录是上一次审查的副本。发现逐条留给人写。
- rereview：填 templates/rereview-prompt.md（第 N 轮修完后的复审）。范围 <上一轮的头>..<头>；范围里有
  合主线的提交时，自动只取分支自己提交（first-parent、非合并）改过的文件，再加合并时手工解决冲突的文件，
  只带进主线改动的合并就此去掉。材料是 review(-r{N-1}).md 与 report-r{N}.md，副本 review-<名字>-r{N}。
- 所有模板里的 `GOTOOLCHAIN=<go 版本>` 从 panel/go.mod 的 go 指令读（pdnd/go.mod 不一致时 stderr 提醒）。
- 名字缺省取分支名去掉 feat/panel-redesign- 前缀；worktree 从 git worktree list 按分支找。
- first 有没替换的占位就失败；round、rereview 把留给人写的占位列到 stderr。
"""
import argparse, pathlib, re, subprocess, sys

HERE = pathlib.Path(__file__).resolve().parent.parent
PREFIX = "feat/panel-redesign-"
# triggers.py 的领域名 → checklist.md 的节名（不同名的才写）
SECTION = {"并发与锁": "并发、锁与后台任务"}
# 每次都发给审查员的节，模板里按名点到
ALWAYS = ["通用（每次都查）", "测试有效性（每次都查）"]


def git(*args, cwd=None):
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True, cwd=cwd).stdout.strip()


def worktree_of(branch):
    path = None
    for line in git("worktree", "list", "--porcelain").splitlines():
        if line.startswith("worktree "):
            path = line[len("worktree "):]
        elif line == f"branch refs/heads/{branch}":
            return pathlib.Path(path)
    sys.exit(f"找不到分支 {branch} 的 worktree（已合入、worktree 已删的分支请手填模板）")


def go_version(wt):
    """panel/go.mod 的 go 指令，写成 GOTOOLCHAIN 认的 go1.x.y。"""
    vers = {}
    for mod in ("panel/go.mod", "pdnd/go.mod"):
        m = re.search(r"^go (\S+)$", (wt / mod).read_text(encoding="utf-8"), re.M)
        vers[mod] = m[1] if m else None
    if not vers["panel/go.mod"]:
        sys.exit(f"{wt}/panel/go.mod 里读不到 go 指令")
    if vers["pdnd/go.mod"] != vers["panel/go.mod"]:
        print(f"提醒：go 指令不一致 {vers}，模板按 panel 的填", file=sys.stderr)
    return "go" + vers["panel/go.mod"]


def shortstat(rng, paths=()):
    s = git("diff", "--shortstat", rng, "--", *paths)
    num = lambda pat: (re.search(pat, s) or [0, "0"])[1]
    return num(r"(\d+) files? changed"), num(r"(\d+) insertions?"), num(r"(\d+) deletions?")


def prev_review(n):
    return "review.md" if n == 2 else f"review-r{n - 1}.md"


def copy_dir(scratchpad, name, n):
    """第 n 次审查的副本：n=1 是首轮，n≥2 是第 n 轮修完后的复审。"""
    return pathlib.Path(scratchpad) / (f"review-{name}" if n == 1 else f"review-{name}-r{n}")


def finding_prefix(n):
    """复审新发现的编号前缀：第 2 轮 N、第 3 轮 M、第 4 轮 L……（沿用已有报告的写法）。"""
    return chr(ord("N") - (n - 2))


def common(a):
    name = a.name or a.branch.removeprefix(PREFIX)
    wt = worktree_of(a.branch)
    head = git("rev-parse", (getattr(a, "head", None) or a.branch))
    return name, wt, head


def sections(main, branch, wt):
    r = subprocess.run([sys.executable, "-I", str(HERE / "scripts" / "triggers.py"), main, branch, "-C", str(wt)],
                       capture_output=True, text=True)
    if r.returncode == 2:
        sys.exit("triggers.py 出错：" + (r.stderr or r.stdout).strip())
    areas = [l[3:].strip() for l in r.stdout.splitlines() if l.startswith("## ")]
    have = {l[3:].strip() for l in (HERE / "checklist.md").read_text(encoding="utf-8").splitlines() if l.startswith("## ")}
    names = [SECTION.get(x, x) for x in areas]
    missing = [n for n in names + ALWAYS if n not in have]
    if missing:
        sys.exit(f"checklist.md 里没有这些节：{missing}（triggers.py 或检查表改过节名？同步 SECTION / ALWAYS）")
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


def fill(s, pairs):
    for k, v in pairs.items():
        s = s.replace(k, v)
    return s


def first(a):
    name, wt, head = common(a)
    copy = copy_dir(a.scratchpad, name, 1)
    mb = git("merge-base", a.main, head)
    n, plus, minus = shortstat(f"{mb}..{head}")
    names, _ = sections(a.main, head, wt)
    s = (HERE / "templates" / "reviewer-prompt.md").read_text(encoding="utf-8")
    s = re.sub(r"(\*\*材料\*\*[^\n]*\n)(- [^\n]*\n)+", lambda m: m[1] + materials(wt, a.design) + "\n", s)
    s = fill(s, {"<名字>": name, "<一句话内容>": a.what, "<副本>": str(copy), "<worktree>": str(wt),
                 "<头>": head, "<merge-base>": mb, "<N>": n, "<a>": plus, "<b>": minus,
                 "<go 版本>": go_version(wt),
                 "<节名>": "、".join(names) if names else "（触发脚本无命中，按 diff 自己挑）"})
    print(f"merge-base {mb[:7]}，头 {head[:7]}，{n} 个文件 +{plus}/−{minus}，节：{'、'.join(names) or '无'}",
          file=sys.stderr)
    return s


def own_paths(prev, head):
    """范围里有合并提交时，返回分支自己改过的文件（去掉只由合并带进来的主线改动）；没有合并返回 None。"""
    merges = git("rev-list", "--merges", "--first-parent", f"{prev}..{head}").split()
    if not merges:
        return None, []
    own = set(git("log", "--no-merges", "--first-parent", "--format=", "--name-only", f"{prev}..{head}").split())
    for m in merges:  # 合并时手工解决冲突（结果与两个父提交都不同）的文件也算分支自己的
        own |= set(git("diff-tree", "--cc", "--name-only", "--no-commit-id", m).split())
    return sorted(own), merges


def rereview(a):
    if a.round < 2:
        sys.exit("--round 是被复审的修复轮次，至少 2")
    name, wt, head = common(a)
    prev = git("rev-parse", a.prev)
    paths, merges = own_paths(prev, head)
    n, plus, minus = shortstat(f"{prev}..{head}", paths or ())
    rng = f"git -C {wt} diff {prev[:7]}..{head[:7]}"
    note = ""
    if paths is not None:
        rng += " -- " + " ".join(paths)
        brought = set()
        for m in merges:
            brought |= set(git("diff", "--name-only", f"{m}^1", m).split())
        mixed = sorted(brought & set(paths))
        ms = "、".join(m[:7] for m in merges)
        note = f"；范围里的合并提交 {ms} 带进的主线改动已按路径去掉"
        if mixed:
            note += f"，但这些文件主线也改过，diff 里混有主线部分：{'、'.join(mixed)}"
        print(f"合并提交 {ms}：只取分支自己改过的 {len(paths)} 个文件", file=sys.stderr)
    s = (HERE / "templates" / "rereview-prompt.md").read_text(encoding="utf-8")
    s = fill(s, {"<名字>": name, "<轮>": str(a.round), "<副本>": str(copy_dir(a.scratchpad, name, a.round)),
                 "<worktree>": str(wt), "<头>": head[:7], "<范围命令>": rng, "<范围说明>": note,
                 "<N>": n, "<a>": plus, "<b>": minus, "<go 版本>": go_version(wt),
                 "<上一轮审查>": prev_review(a.round), "<本轮报告>": f"report-r{a.round}.md",
                 "<编号前缀>": finding_prefix(a.round)})
    for f in (prev_review(a.round), f"report-r{a.round}.md"):
        if not (wt / ".claude" / f).exists():
            print(f"提醒：{wt}/.claude/{f} 还不存在（先用 accept-task 的 save-report.sh 存）", file=sys.stderr)
    print(f"范围 {prev[:7]}..{head[:7]}，{n} 个文件 +{plus}/−{minus}", file=sys.stderr)
    return s


def round_brief(a):
    if a.round < 2:
        sys.exit("--round 是修复轮次，至少 2（第 1 轮是原开工说明）")
    name, wt, head = common(a)
    review = prev_review(a.round)
    copy = copy_dir(a.scratchpad, name, a.round - 1)
    probe = str(copy) if copy.exists() else f"{copy}（目录不存在：没有探针就删掉这句）"
    s = (HERE / "templates" / "round-brief.md").read_text(encoding="utf-8")
    s = fill(s, {"<名字>": name, "<轮>": str(a.round), "<worktree>": str(wt), "<分支>": a.branch,
                 "<副本>": probe, "<上一轮审查>": review, "<本轮报告>": f"report-r{a.round}.md",
                 "<go 版本>": go_version(wt)})
    if not (wt / ".claude" / review).exists():
        print(f"提醒：{wt}/.claude/{review} 还不存在（先存审查报告，再发消息）", file=sys.stderr)
    return s


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    for c, aliases in (("first", []), ("round", ["round2"]), ("rereview", [])):
        p = sub.add_parser(c, aliases=aliases)
        if c == "first":
            p.add_argument("main")
        p.add_argument("branch")
        p.add_argument("--scratchpad", required=True)
        p.add_argument("--name")
        p.add_argument("-o", "--out")
        if c != "round":
            p.add_argument("--head", help="缺省取分支当前的提交")
        if c != "first":
            p.add_argument("--round", type=int, default=2, help="第几轮修复（缺省 2）")
        if c == "first":
            p.add_argument("--what", required=True, help="一句话内容")
            p.add_argument("--design", help="设计稿路径")
        if c == "rereview":
            p.add_argument("--from", dest="prev", required=True, help="上一轮审查时写死的头")
    a = ap.parse_args()
    cmd = "round" if a.cmd == "round2" else a.cmd
    try:
        text = {"first": first, "rereview": rereview, "round": round_brief}[cmd](a)
    except subprocess.CalledProcessError as e:
        sys.exit(e.stderr.strip())
    left = sorted(set(re.findall(r"<[^<>\n]{1,80}>", text)))
    if cmd == "first" and left:
        sys.exit(f"模板占位没替换干净：{left}（reviewer-prompt.md 改过措辞？）")
    if left and cmd == "rereview":
        print(f"留给你写的占位：{'、'.join(left)}", file=sys.stderr)
    elif left:
        print(f"还有 {len(left)} 种占位留给你写（发现逐条、决定、理由）", file=sys.stderr)
    if a.out:
        pathlib.Path(a.out).write_text(text, encoding="utf-8")
        print(f"已写 {a.out}", file=sys.stderr)
    else:
        print(text, end="")


if __name__ == "__main__":
    main()
