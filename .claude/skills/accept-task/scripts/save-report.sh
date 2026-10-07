#!/usr/bin/env bash
# 把后台子 agent 的最终消息存成报告文件（子 agent 自己写不了 .claude/report.md）。
# 用法：save-report.sh <agent 的 output 文件（.output 或 .jsonl）> <目标 report.md>
# 取的是转录里最后一条带文字的 assistant 消息；agent 中途「等待 CI」之类的临时回复会被后面的终稿覆盖。
set -euo pipefail
src="${1:?agent output 文件}"; dst="${2:?目标文件}"
python3 - "$src" "$dst" <<'PY'
import json, sys
src, dst = sys.argv[1], sys.argv[2]
for line in reversed(open(src, encoding="utf-8").read().splitlines()):
    try:
        d = json.loads(line)
    except ValueError:
        continue
    c = d.get("message", {}).get("content")
    if d.get("message", {}).get("role", "assistant") == "assistant" and isinstance(c, list):
        texts = [x["text"] for x in c if x.get("type") == "text"]
        if texts:
            open(dst, "w", encoding="utf-8").write("\n".join(texts))
            print(f"{dst}（{sum(map(len, texts))} 字符）")
            break
else:
    sys.exit("没找到带文字的 assistant 消息")
PY
