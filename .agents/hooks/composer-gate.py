#!/usr/bin/env python3
"""composer 收尾检查（Claude Code 的 Stop hook，配置在 .claude/settings.json）。

composer（composer-dispatch 的 launch.sh 起的 claude -p，经本机 magpie）跑不了命令，改完常有编译错。
它说做完时，这里对本会话改过的文件做几秒钟的检查：
  Go：按模块对改过的文件 gofmt -l、对改过的包 go vet（顺带编译测试文件）；
  前端（panel/frontend 下的 .ts/.tsx）：tsc -p tsconfig.json --noEmit（只报改过的文件里的错）、eslint 改过的文件。
有错按 Stop hook 的约定退出 2，报错经 stderr 交回给 composer 接着改；最多退回两轮，第三次照常放行，交给派活的人核对。
不跑测试：防它改断言凑过；测试由派活的人跑。

只认经 magpie 的会话（launch.sh 设 ANTHROPIC_BASE_URL=http://127.0.0.1:3425），主会话、opus 子 Agent 直接放行。
"""
import json
import os
import subprocess
import sys
import tempfile

MAGPIE = "http://127.0.0.1:3425"
MAX_ROUNDS = 2
EDIT_TOOLS = {"Write", "Edit", "MultiEdit", "NotebookEdit"}
GO_ENV = {**os.environ, "GOTOOLCHAIN": "go1.26.9", "GOFLAGS": "-mod=readonly"}
LIMIT = 8000  # 交回的报错最多这么多字，太长模型反而抓不住重点


def edited_files(transcript):
    """本会话 Write / Edit 过、现在还在的文件（绝对路径）。"""
    files = set()
    try:
        with open(transcript, encoding="utf-8") as f:
            for line in f:
                try:
                    msg = json.loads(line).get("message") or {}
                except ValueError:
                    continue
                content = msg.get("content")
                if msg.get("role") != "assistant" or not isinstance(content, list):
                    continue
                for item in content:
                    if isinstance(item, dict) and item.get("type") == "tool_use" and item.get("name") in EDIT_TOOLS:
                        p = (item.get("input") or {}).get("file_path") or (item.get("input") or {}).get("notebook_path")
                        if p:
                            files.add(os.path.abspath(p))
    except OSError:
        return []
    return sorted(p for p in files if os.path.isfile(p))


def run(cmd, cwd, env=None, timeout=240):
    try:
        r = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
        return r.returncode, (r.stdout + r.stderr).strip()
    except subprocess.TimeoutExpired:
        return 0, ""  # 超时不拦：检查本身慢不是 composer 的错
    except OSError as e:
        return 0, f"(检查没跑起来：{e})"


def go_module(path, root):
    d = os.path.dirname(path)
    while d.startswith(root):
        if os.path.isfile(os.path.join(d, "go.mod")):
            return d
        if d == root:
            break
        d = os.path.dirname(d)
    return None


def check_go(files, root):
    problems = []
    by_mod = {}
    for f in files:
        if f.endswith(".go"):
            mod = go_module(f, root)
            if mod:
                by_mod.setdefault(mod, []).append(f)
    for mod, fs in sorted(by_mod.items()):
        rel = os.path.relpath(mod, root)
        _, out = run(["gofmt", "-l"] + fs, mod)
        if out:
            problems.append(f"[{rel}] gofmt 没格式化（按 gofmt 的格式改）：\n" + "\n".join(os.path.relpath(p, mod) for p in out.splitlines()))
        pkgs = sorted({"./" + os.path.relpath(os.path.dirname(f), mod) for f in fs})
        code, out = run(["go", "vet"] + pkgs, mod, GO_ENV)
        if code != 0 and out:
            problems.append(f"[{rel}] go vet {' '.join(pkgs)}：\n{out}")
    return problems


def check_frontend(files, root):
    fe = os.path.join(root, "panel", "frontend")
    fs = [f for f in files if f.startswith(fe + os.sep) and f.endswith((".ts", ".tsx")) and "node_modules" not in f]
    if not fs or not os.path.isdir(os.path.join(fe, "node_modules")):
        return []
    problems = []
    rels = [os.path.relpath(f, fe) for f in fs]
    code, out = run(["npx", "--no-install", "tsc", "-p", "tsconfig.json", "--noEmit"], fe)
    if code != 0:
        mine = [l for l in out.splitlines() if any(l.startswith(r + "(") for r in rels)]
        others = len([l for l in out.splitlines() if ": error TS" in l]) - len(mine)
        if mine:
            note = f"\n（另有 {others} 条报错在你没改过的文件里，多半是你改的接口连带的：在任务范围内就一并改，不在就停下写明）" if others > 0 else ""
            problems.append("[panel/frontend] tsc：\n" + "\n".join(mine) + note)
        elif others > 0:
            problems.append(f"[panel/frontend] tsc：你改过的文件没报错，但别的文件有 {others} 条，多半是你改的导出或类型连带的：\n" + out[:3000])
    code, out = run(["npx", "--no-install", "eslint"] + rels, fe)
    if code != 0 and out:
        problems.append("[panel/frontend] eslint：\n" + out)
    return problems


def main():
    if os.environ.get("ANTHROPIC_BASE_URL", "") != MAGPIE:
        return 0
    try:
        data = json.load(sys.stdin)
    except ValueError:
        return 0
    session = data.get("session_id") or "unknown"
    cwd = data.get("cwd") or os.getcwd()
    code, root = run(["git", "rev-parse", "--show-toplevel"], cwd)
    if code != 0 or not root:
        return 0
    counter = os.path.join(tempfile.gettempdir(), "composer-gate", session)
    rounds = int(open(counter).read() or 0) if os.path.isfile(counter) else 0
    if rounds >= MAX_ROUNDS:
        return 0
    files = edited_files(data.get("transcript_path") or "")
    if not files:
        return 0
    problems = check_go(files, root) + check_frontend(files, root)
    if not problems:
        return 0
    os.makedirs(os.path.dirname(counter), exist_ok=True)
    with open(counter, "w") as f:
        f.write(str(rounds + 1))
    text = "\n\n".join(problems)
    if len(text) > LIMIT:
        text = text[:LIMIT] + "\n……（后面截断）"
    sys.stderr.write(
        f"收尾检查没过（第 {rounds + 1}/{MAX_ROUNDS} 轮，自动跑的，不用你跑命令）。"
        "请只改你这次改过、且在任务说明「只许改的文件」里的文件，把下面的问题修掉再结束；"
        "要动范围外的文件才能修，就别改，在最后的回复里写明。\n\n" + text + "\n")
    return 2


if __name__ == "__main__":
    sys.exit(main())
