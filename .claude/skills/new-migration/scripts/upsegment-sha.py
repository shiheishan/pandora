#!/usr/bin/env python3
"""只读：按 panel/tools/migrationlint 的口径算迁移 Up 段的 SHA-256，输出 upsegments.txt 的登记行。

Up 段 = 从 `-- +goose Up` 那一行到 `-- +goose Down` 之前，去掉末尾空白再补一个换行
（与 parse.go 的 UpSegment 一致）。

用法：
  upsegment-sha.py <迁移.sql> ...      打印「sha256  文件名」，追加进 upsegments.txt 用
  upsegment-sha.py --check             核对 upsegments.txt 每一行与当前文件是否一致
"""
import hashlib
import os
import re
import subprocess
import sys

UP = re.compile(r"^-- \+goose Up[ \t\f\r\n\v]*$", re.ASCII)
DOWN = re.compile(r"^-- \+goose Down[ \t\f\r\n\v]*$", re.ASCII)


def up_segment(text: str) -> str:
    raw = []
    section = 0  # 0 文件头，1 Up，2 Down
    for line in text.splitlines(keepends=True):
        bare = line.rstrip("\r\n")
        if UP.match(bare):
            section = 1
            raw.append(line)
            continue
        if DOWN.match(bare):
            section = 2
            continue
        if section == 1:
            raw.append(line)
    if not raw:
        raise SystemExit("没有 `-- +goose Up` 标记")
    return "".join(raw).rstrip(" \t\r\n") + "\n"


def digest(path: str) -> str:
    with open(path, encoding="utf-8", newline="") as f:
        return hashlib.sha256(up_segment(f.read()).encode("utf-8")).hexdigest()


def main() -> int:
    args = sys.argv[1:]
    if not args:
        print(__doc__, file=sys.stderr)
        return 2
    if args == ["--check"]:
        root = subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()
        mig = os.path.join(root, "panel", "migrations")
        reg = os.path.join(root, "panel", "tools", "migrationlint", "upsegments.txt")
        bad = total = 0
        with open(reg, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#"):
                    continue
                total += 1
                want, name = line.split()
                got = digest(os.path.join(mig, name))
                if got != want:
                    bad += 1
                    print(f"变了：{name}（{got} ≠ 登记的 {want}）")
        print(f"核对 {total} 条，不一致 {bad} 条")
        return 1 if bad else 0
    if not args or any(a.startswith("-") for a in args):
        print("用法：upsegment-sha.py <迁移文件>...  或  upsegment-sha.py --check", file=sys.stderr)
        return 2
    for path in args:
        print(f"{digest(path)}  {os.path.basename(path)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
