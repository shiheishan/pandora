#!/usr/bin/env python3
"""列出 Claude Code 会话记录里重复出现的用户消息，给 skill 审查第二部分「重复的业务流程」当证据。

用法：
  python3 -I repeated-prompts.py [--min 2] [--since 2026-10-08] [--grep 关键词] [转录.jsonl ...]
  python3 -I repeated-prompts.py --agents [--sim 0.5] [--min 2] [--since …] [--grep …] [转录.jsonl ...]
不给文件时读 ~/.claude/projects/-Users-a1-ai-projects-pandora/*.jsonl。
只读、只打印。同一条消息在转录里会以多条记录出现（排队、重放、压缩后重写），按「md5 + 分钟」去重后再计数。
时间是转录里的 UTC。
- 缺省：用户消息，逐字相同才算一组。每组给 md5 前 8 位、次数、各次时间、首行。文字略有出入的版本算不同的组，用 --grep 一起看。
- --agents：总协调手写的 Agent 调用 prompt、SendMessage 的 message，以及 Bash 里 `cursor-agent -p` 的开工指令
  （取命令里最长的一段双引号串；没有就取整条命令），按相似度分组：sha、路径、数字、分支名
  w12xxx 先抹平，再比字符五元组的重合度（交集 ÷ 较短一方），和组里任一条 ≥ --sim 就并进这一组。
  每组给次数（Agent / SendMessage / Cursor 各几次）、首末时间、各条的 description（SendMessage 给 summary 或首行，
  Cursor 给 --workspace 目录）。用 cursor-launch.sh 起的不会出现在这里：指令由模板生成，不算手写。
  照模板派的会聚成一组；每次手写、措辞各异的（例如第二轮修复消息）聚不到一起，用 --grep 关键词 --min 1 看。
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

# 真正执行的调用：cursor-agent 在命令开头、&&、;、|| 或换行之后（可带 VAR=… 前缀）；写在引号、反引号里的文字说明不算
LAUNCH = re.compile(r"(?:^|&&|;|\|\||\n)\s*(?:\w+=\S*\s+)*(cursor-agent -p\b)")
QUOTED = re.compile(r'"((?:[^"\\]|\\.)*)"', re.S)

def cursor_prompt(cmd):
    """Bash 命令里真正执行的 cursor-agent -p 的开工指令：-p 之后最长的一段双引号串；没有就取整条命令。"""
    m = LAUNCH.search(cmd)
    if not m:
        return None
    rest = cmd[m.start(1):]
    quoted = max(QUOTED.findall(rest), key=len, default="")
    ws = re.search(r"--workspace\s+(\S+)", rest)
    return (ws.group(1) if ws else "cursor-agent"), (quoted if len(quoted) > 40 else rest)

def agent_texts(rec):
    c = rec.get("message", {}).get("content")
    if not isinstance(c, list):
        return
    for x in c:
        if isinstance(x, dict) and x.get("type") == "tool_use" and x.get("name") == "Bash":
            got = cursor_prompt((x.get("input") or {}).get("command") or "")
            if got:
                yield "Cursor", got[0], got[1]
            continue
        if isinstance(x, dict) and x.get("type") == "tool_use" and x.get("name") in ("Agent", "Task", "SendMessage"):
            i = x.get("input") or {}
            t = i.get("prompt") if x["name"] != "SendMessage" else i.get("message")
            if not isinstance(t, str):
                t = json.dumps(t, ensure_ascii=False) if t else ""
            label = i.get("description") or i.get("summary") or ""
            yield ("Agent" if x["name"] != "SendMessage" else "SendMessage"), label, t

def shape(t):
    # 抹平每次都不一样的部分，只留结构和措辞
    t = re.sub(r"/[\w./~-]+", "P", t)
    t = re.sub(r"\b[0-9a-f]{7,40}\b", "H", t)
    t = re.sub(r"\bw\d+[a-z0-9-]*", "W", t)
    t = re.sub(r"\d+", "0", t)
    t = re.sub(r"\s+", " ", t)
    return {hash(t[i:i + 5]) for i in range(max(len(t) - 4, 1))}

def agents(a, files):
    seen, msgs = set(), []
    for fn in files:
        with open(fn, encoding="utf-8", errors="replace") as f:
            for line in f:
                if '"tool_use"' not in line:
                    continue
                try:
                    rec = json.loads(line)
                except ValueError:
                    continue
                ts = rec.get("timestamp", "")
                if rec.get("type") != "assistant" or ts < a.since:
                    continue
                for kind, label, t in agent_texts(rec):
                    t = t.strip()
                    if len(t) < a.minlen or (a.grep and a.grep not in t):
                        continue
                    key = (hashlib.md5(t.encode()).hexdigest(), ts[:16])
                    if key in seen:
                        continue
                    seen.add(key)
                    msgs.append((ts[:16], kind, label or t.splitlines()[0][:60], shape(t)))
    msgs.sort(key=lambda m: m[0])
    groups = []  # 每组是条目列表
    for m in msgs:
        best, bs = None, 0.0
        for g in groups:
            sim = max(len(m[3] & x[3]) / max(min(len(m[3]), len(x[3])), 1) for x in g)
            if sim > bs:
                best, bs = g, sim
        if best is not None and bs >= a.sim:
            best.append(m)
        else:
            groups.append([m])
    rows = sorted((g for g in groups if len(g) >= a.min), key=len, reverse=True)
    print(f"{len(msgs)} 条（去重后），{len(groups)} 组，其中 ≥{a.min} 次的 {len(rows)} 组")
    for ms in rows:
        n = {k: sum(1 for m in ms if m[1] == k) for k in ("Agent", "SendMessage", "Cursor")}
        print(f"{len(ms)} 次（Agent {n['Agent']} / SendMessage {n['SendMessage']} / Cursor {n['Cursor']}）  {ms[0][0]} → {ms[-1][0]}")
        labels = list(dict.fromkeys(m[2] for m in ms))
        for l in labels[:4]:
            print(f"    {l[:70]}")
        if len(labels) > 4:
            print(f"    …另 {len(labels) - 4} 个")
    return 0 if rows else 1

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("files", nargs="*")
    ap.add_argument("--min", type=int, default=2, help="至少出现几次才列出")
    ap.add_argument("--since", default="", help="只看这个时间（ISO 前缀）之后的")
    ap.add_argument("--grep", default="", help="只看含这个关键词的消息")
    ap.add_argument("--minlen", type=int, default=80, help="短于这个字数的消息不算（「继续」「好」之类）")
    ap.add_argument("--agents", action="store_true", help="看 Agent prompt、SendMessage 与 cursor-agent 开工指令，按相似度分组")
    ap.add_argument("--sim", type=float, default=0.4, help="--agents 下算同一组的相似度下限（0–1）")
    a = ap.parse_args()
    files = a.files or sorted(glob.glob(os.path.expanduser(
        "~/.claude/projects/-Users-a1-ai-projects-pandora/*.jsonl")))
    if a.agents:
        return agents(a, files)
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
