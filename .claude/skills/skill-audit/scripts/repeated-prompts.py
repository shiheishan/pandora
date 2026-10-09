#!/usr/bin/env python3
"""列出 Claude Code 会话记录里重复出现的用户消息，给 skill 审查第二部分「重复的业务流程」当证据。

用法：
  python3 -I repeated-prompts.py [--min 2] [--since 2026-10-08] [--grep 关键词] [转录.jsonl ...]
不给文件时读 ~/.claude/projects/-Users-a1-ai-projects-pandora/*.jsonl。
只读、只打印：每组给 md5 前 8 位、次数、各次时间、首行。同一条消息在转录里会以多条记录出现
（排队、重放），按「md5 + 分钟」去重后再计数。时间是转录里的 UTC。文字略有出入的版本算不同的组，用 --grep 一起看。
"""
import argparse, glob, hashlib, json, os, re, sys

def user_texts(rec):
    c = rec.get("message", {}).get("content")
    if isinstance(c, str):
        yield c
    elif isinstance(c, list):
        for x in c:
            if isinstance(x, dict) and x.get("type") == "text":
                yield x.get("text", "")

def norm(t):
    # 去掉粘贴包裹与首尾空白，其余原样：用户贴的是同一段就同一个 md5
    t = re.sub(r"</?pasted_content[^>]*>", "", t)
    return t.strip()

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("files", nargs="*")
    ap.add_argument("--min", type=int, default=2, help="至少出现几次才列出")
    ap.add_argument("--since", default="", help="只看这个时间（ISO 前缀）之后的")
    ap.add_argument("--grep", default="", help="只看含这个关键词的消息")
    ap.add_argument("--minlen", type=int, default=80, help="短于这个字数的消息不算（「继续」「好」之类）")
    a = ap.parse_args()
    files = a.files or sorted(glob.glob(os.path.expanduser(
        "~/.claude/projects/-Users-a1-ai-projects-pandora/*.jsonl")))
    groups = {}
    for fn in files:
        with open(fn, encoding="utf-8", errors="replace") as f:
            for line in f:
                try:
                    rec = json.loads(line)
                except ValueError:
                    continue
                if rec.get("type") != "user":
                    continue
                ts = rec.get("timestamp", "")
                if ts < a.since:
                    continue
                for t in user_texts(rec):
                    t = norm(t)
                    if len(t) < a.minlen or t.startswith("<") or (a.grep and a.grep not in t):
                        continue
                    h = hashlib.md5(t.encode()).hexdigest()[:8]
                    g = groups.setdefault(h, {"times": set(), "head": t.splitlines()[0][:60], "len": len(t)})
                    g["times"].add(ts[:16])
    rows = sorted(((len(g["times"]), h, g) for h, g in groups.items() if len(g["times"]) >= a.min), reverse=True)
    if not rows:
        print("没有重复 ≥%d 次的消息" % a.min)
        return 1
    for n, h, g in rows:
        print(f"{h}  {n} 次  {g['len']} 字  {g['head']}")
        print("    " + "  ".join(sorted(g["times"])))
    return 0

if __name__ == "__main__":
    sys.exit(main())
