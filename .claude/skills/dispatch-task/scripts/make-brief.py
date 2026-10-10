#!/usr/bin/env python3
"""拼开工说明：任务专属部分 + templates/common-rules.md（替换占位）+ 可选的共享热点段。

用法：
  make-brief.py <名字> <迁移号段或「无」> <任务文件.md> \\
      --background "<一两句背景>" --evidence "<证据绝对路径，逗号分隔>" [--shared <共享热点段.md>] [--upstream <分支>]

- 基点取该 worktree 当前 HEAD 的短 sha（worktree 由 new-worktree.sh 建好）。
- --upstream：子 agent 交付前要 merge 跟上的分支，缺省主线；叠在集成分支上的路给集成分支（SKILL.md「叠在集成分支上的路」）。
- 任务文件写：目标、归属、不碰、任务、完成标准（见 SKILL.md「写开工说明」）。
- 输出写到 ../pandora-<名字>/.claude/brief.md，并打印字数；已存在就拒绝覆盖（加 --force 覆盖）。
"""
import argparse, pathlib, subprocess, sys

here = pathlib.Path(__file__).resolve().parent.parent
ap = argparse.ArgumentParser()
ap.add_argument("name"); ap.add_argument("migrations"); ap.add_argument("task_file")
ap.add_argument("--background", required=True); ap.add_argument("--evidence", required=True)
ap.add_argument("--shared"); ap.add_argument("--force", action="store_true")
ap.add_argument("--upstream", default="feat/panel-redesign", help="交付前 merge 跟上的分支，缺省主线")
ap.add_argument("--dry-run", action="store_true", help="只打印到 stdout，不写文件")
a = ap.parse_args()

root = pathlib.Path(subprocess.check_output(["git", "rev-parse", "--path-format=absolute", "--git-common-dir"], text=True).strip()).parent
wt = root.parent / f"pandora-{a.name}"
if not wt.is_dir():
    sys.exit(f"worktree 不存在：{wt}（先跑 new-worktree.sh {a.name}）")
base = subprocess.check_output(["git", "-C", str(wt), "rev-parse", "--short", "HEAD"], text=True).strip()
out = wt / ".claude" / "brief.md"
if out.exists() and not a.force and not a.dry_run:
    sys.exit(f"已存在：{out}（确认要覆盖就加 --force）")

common = (here / "templates" / "common-rules.md").read_text(encoding="utf-8")
evidence = "；".join(e.strip() for e in a.evidence.split(",") if e.strip())
common = (common
    .replace("<一两句：为什么做这件事、实测或用户给的依据、这一波有几路、各管什么>", a.background)
    .replace("<列出报告、数据、EXPLAIN 原文的绝对路径>", evidence)
    .replace("<基点短 sha>", base)
    .replace("<上游>", a.upstream)
    .replace("<名字>", a.name)
    .replace("**迁移号**：只用分配给你的号段", f"**迁移号**：本路号段 {a.migrations}；只用分配给你的号段"))
for left in ("<一两句", "<列出报告", "<基点短 sha>", "<上游>", "<名字>"):
    if left in common:
        sys.exit(f"模板占位没替换干净：{left}（common-rules.md 改过措辞？）")

task = pathlib.Path(a.task_file).read_text(encoding="utf-8").strip()
head = f"# 开工说明：{a.name}\n\n基点 {base}，分支 feat/panel-redesign-{a.name}，上游 {a.upstream}。迁移号段：{a.migrations}。\n\n"
shared = ("\n" + pathlib.Path(a.shared).read_text(encoding="utf-8").strip() + "\n") if a.shared else ""
text = head + task + "\n" + shared + "\n" + common
if a.dry_run:
    print(text); sys.exit(0)
out.write_text(text, encoding="utf-8")
print(f"{out}（{len(out.read_text(encoding='utf-8'))} 字符，基点 {base}）")
