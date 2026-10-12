#!/usr/bin/env python3
"""只读：找出哪些迁移的 Up 段定义或改过某个数据库对象，并打印当前生效那一版的原文。

用途：CREATE OR REPLACE 别人的函数 / 视图时，Up 要在「当前生效那一版」上改，Down 要逐字
还原成它。按迁移号从小到大扫 Up 段（Down 段不是现行结构），跳过 `--` 注释行。

用法：
  find-def.py <对象名> [--body] [--before <迁移号>]
    <对象名>      如 app.seed_tenant_defaults、assert_plan_change_order_trigger、user_generation_jobs
    --body        打印最后一次 CREATE 的完整语句（函数到结束的 $tag$ 与分号，其余到分号）
    --before N    只看号小于 N 的迁移：写 N 号迁移的 Down 时，「上一版」就是这里的最后一次 CREATE
"""
import argparse
import glob
import os
import re
import subprocess
import sys

UP = re.compile(r"^-- \+goose Up\s*$")
DOWN = re.compile(r"^-- \+goose Down\s*$")
DDL = re.compile(r"\b(CREATE|ALTER|DROP|GRANT|REVOKE|COMMENT\s+ON)\b", re.I)
CREATE = re.compile(r"\bCREATE\b", re.I)
DOLLAR = re.compile(r"\$(\w*)\$")


def up_lines(path):
    """返回 Up 段的 (行号, 行) 列表。"""
    out, section = [], 0
    with open(path, encoding="utf-8") as f:
        for no, line in enumerate(f, 1):
            bare = line.rstrip("\r\n")
            if UP.match(bare):
                section = 1
                continue
            if DOWN.match(bare):
                break
            if section == 1:
                out.append((no, line))
    return out


def statement_from(lines, start):
    """从 lines[start] 起取一条完整语句：遇到 $tag$ 先跳到配对的结束 tag，再取到分号。"""
    text = "".join(l for _, l in lines[start:])
    pos, tag = 0, None
    while pos < len(text):
        semi = text.find(";", pos)
        m = DOLLAR.search(text, pos)
        if m and (semi < 0 or m.start() < semi):
            tag = m.group(0)
            close = text.find(tag, m.end())
            if close < 0:
                return text
            pos = close + len(tag)
            continue
        if semi < 0:
            return text
        return text[: semi + 1]
    return text


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("name")
    ap.add_argument("--body", action="store_true")
    ap.add_argument("--before", type=int, default=None)
    a = ap.parse_args()

    root = subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()
    files = sorted(glob.glob(os.path.join(root, "panel", "migrations", "*.sql")))
    word = re.compile(r"(?<![\w])" + re.escape(a.name) + r"(?![\w])", re.I)

    last = None  # (文件, Up 行列表, 下标)
    hits = 0
    for path in files:
        base = os.path.basename(path)
        m = re.match(r"^(\d{5})_", base)
        if not m:
            continue
        if a.before is not None and int(m.group(1)) >= a.before:
            continue
        lines = up_lines(path)
        for i, (no, line) in enumerate(lines):
            if line.lstrip().startswith("--") or not word.search(line) or not DDL.search(line):
                continue
            hits += 1
            mark = ""
            if CREATE.search(line):
                last = (base, lines, i)
                mark = "  <- CREATE"
            print(f"{base}:{no}: {line.strip()}{mark}")
    if hits == 0:
        print(f"没有迁移的 Up 段对 {a.name} 做过 DDL", file=sys.stderr)
        return 1
    if last:
        print(f"\n当前生效（最后一次 CREATE）：{last[0]} 第 {last[1][last[2]][0]} 行")
        if a.body:
            print("-" * 60)
            print(statement_from(last[1], last[2]).rstrip())
    return 0


if __name__ == "__main__":
    sys.exit(main())
