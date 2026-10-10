#!/usr/bin/env bash
# 把执行者交回的报告落成文件。用法：save-report.sh <来源> <目标 report.md>
# - Claude 子 agent：来源给 Agent 通知里的 output 文件（.output 或 .jsonl 转录），取最后一条带文字的 assistant 消息
#   （中途「等待 CI」之类的临时回复会被后面的终稿覆盖）。它自己写不了 .claude/report.md。
# - Cursor 的 Composer：来源给 cursor-launch.sh 打印的纯文本日志。Composer 按开工指令自己写目标文件：
#   目标已存在就不动它，只核日志末行 exit=、文件没被 git 跟踪；目标不存在时，把日志全文（去掉 exit= 行）存过去并警告。
# - 来源与目标是同一个文件（Composer 已写好的 report.md）：只做上面的核对。
# 退出码：0 存好或核对通过；1 有问题（没找到报告、进程没正常结束、报告被提交了）；2 参数错。
set -euo pipefail
src="${1:?用法: save-report.sh <来源> <目标 report.md>}"; dst="${2:?目标文件}"
[ -f "$src" ] || { echo "来源不存在：$src" >&2; exit 2; }
python3 -I - "$src" "$dst" <<'PY'
import json, os, subprocess, sys
src, dst = sys.argv[1], sys.argv[2]
lines = open(src, encoding="utf-8", errors="replace").read().splitlines()

def is_transcript():
    """首个非空行是 JSON 对象才算转录；纯文本日志里偶尔出现的 JSON 行不算。"""
    for line in lines:
        if line.strip():
            try:
                return isinstance(json.loads(line), dict)
            except ValueError:
                return False
    return False

def transcript_text():
    """JSON 转录里最后一条带文字的 assistant 消息；没有返回空串。"""
    for line in reversed(lines):
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if not isinstance(d, dict):
            continue
        m = d.get("message") or {}
        c = m.get("content") if isinstance(m, dict) else None
        if m.get("role", "assistant") == "assistant" and isinstance(c, list):
            texts = [x["text"] for x in c if isinstance(x, dict) and x.get("type") == "text"]
            if texts:
                return "\n".join(texts)
    return ""

def tracked(path):
    d = os.path.dirname(os.path.abspath(path))
    r = subprocess.run(["git", "-C", d, "ls-files", "--error-unmatch", os.path.basename(path)],
                       capture_output=True)
    return r.returncode == 0

def check_dst():
    size = len(open(dst, encoding="utf-8", errors="replace").read())
    print(f"{dst}（{size} 字符，执行者自己写的，未改动）")
    if size == 0:
        print("报告是空的", file=sys.stderr); return 1
    if tracked(dst):
        print("报告被提交进了分支：验收时让它撤出提交（报告不进仓库）", file=sys.stderr); return 1
    return 0

same = os.path.exists(dst) and os.path.samefile(src, dst)
text = transcript_text() if not same and is_transcript() else None
if text:
    open(dst, "w", encoding="utf-8").write(text)
    print(f"{dst}（{len(text)} 字符，取自转录）")
    sys.exit(0)
if text == "":
    sys.exit("转录里没找到带文字的 assistant 消息")

# Composer 路：纯文本日志，或来源就是报告本身
rc = 0
if not same:
    exits = [l for l in lines if l.startswith("exit=")]
    if not exits:
        print(f"日志没有 exit= 行：cursor-agent 还在跑或被打断（pgrep -fl cursor-agent；被打断按 resume-work「Composer 的路」）", file=sys.stderr)
        rc = 1
    elif exits[-1] != "exit=0":
        print(f"cursor-agent 非 0 退出：{exits[-1]}，看日志末尾", file=sys.stderr)
        rc = 1
if os.path.exists(dst):
    sys.exit(check_dst() or rc)
if same:
    sys.exit("报告不存在")
body = "\n".join(l for l in lines if not l.startswith("exit=")).strip()
if not body:
    sys.exit("日志是空的，也没有报告文件")
open(dst, "w", encoding="utf-8").write(
    f"<!-- Composer 没写报告文件，以下是日志全文：{src} -->\n\n{body}\n")
print(f"{dst}（{len(body)} 字符，取自日志全文）")
print("警告：Composer 没按开工指令写报告，日志只是它的最终回复，内容多半不全，验收时按 brief「报告」逐项补问", file=sys.stderr)
sys.exit(rc or 1)
PY
