#!/usr/bin/env bash
# 总进度快照：主线头与 CI、各任务 worktree 的进度、Cursor 的 Composer 在跑对账、TASKS.md 里的未完成项与下一步顺序。只读。
# 用法：snapshot.sh   在主目录或任一 worktree 里执行（都读主目录的 TASKS.md）
# cursor-agent 一节：从 TASKS「正在跑」表里取登记的日志（cursor-*.log），逐份判在跑 / 已结束 exit=N / 被打断，
# 再列出在跑、但哪份登记日志都对不上的 cursor-agent 进程。日志写相对路径（scratchpad/…）的按文件名在各会话 scratchpad 里找。
set -uo pipefail
root="$(cd "$(git rev-parse --path-format=absolute --git-common-dir)/.." && pwd)"
cd "$root"
main=feat/panel-redesign
git fetch -q origin 2>/dev/null || echo "（fetch 失败：远端状态可能过时）"

echo "== 主线 $main"
git log -1 --format='  %h %s（%cr）' "$main"
ahead=$(git rev-list --count "origin/$main..$main" 2>/dev/null || echo "?")
echo "  本地领先远端 $ahead 个提交"
if [ -x .claude/skills/accept-task/scripts/ci-status.sh ]; then
  bash .claude/skills/accept-task/scripts/ci-status.sh "$(git rev-parse "origin/$main")" 2>/dev/null | sed -n '2,$p' | sed 's/^/  /'
fi

echo
echo "== 任务 worktree（相对主线：未合提交数 / 未提交改动数 / TASKS 勾选进度 / 最后提交）"
git worktree list --porcelain | awk '/^worktree /{print $2}' | while read -r wt; do
  [ "$wt" = "$root" ] && continue
  br=$(git -C "$wt" rev-parse --abbrev-ref HEAD 2>/dev/null) || continue
  case "$br" in "$main"-*) ;; *) continue ;; esac
  name=${br#"$main"-}
  pending=$(git rev-list --count --no-merges "$main..$br" 2>/dev/null)
  dirty=$(git -C "$wt" status --porcelain 2>/dev/null | wc -l | tr -d ' ')
  done_n=0; todo_n=0
  if [ -f "$wt/.claude/TASKS.md" ]; then
    done_n=$(grep -cE '^\s*- \[x\]' "$wt/.claude/TASKS.md")
    todo_n=$(grep -cE '^\s*- \[ \]' "$wt/.claude/TASKS.md")
  fi
  # 已全部合入且干净的旧 worktree 不列
  [ "$pending" = 0 ] && [ "$dirty" = 0 ] && [ "$todo_n" = 0 ] && continue
  last=$(git -C "$wt" log -1 --format='%cr' 2>/dev/null)
  report=""; [ -f "$wt/.claude/report.md" ] && report="  有报告"
  printf '  %-12s 未合 %3s  改动 %3s  清单 %s/%s  最后提交 %s%s\n' "$name" "$pending" "$dirty" "$done_n" "$((done_n+todo_n))" "$last" "$report"
done

echo
echo "== cursor-agent（TASKS「正在跑」登记的日志 × 进程；Claude 子 agent 不在这里，以会话通知为准）"
procs="$(pgrep -fl 'index\.js -p .*--workspace' 2>/dev/null | sed -nE 's/^([0-9]+) .*--workspace ([^ ]+).*/\1 \2/p')"
python3 -I - "$root" "$procs" <<'PY'
import glob, os, re, sys
root, procs = sys.argv[1], sys.argv[2]
running = {}  # workspace -> pid
for line in procs.splitlines():
    pid, ws = line.split(" ", 1)
    running[ws] = pid
slug = root.replace("/", "-")
text = open(os.path.join(root, ".claude/TASKS.md"), encoding="utf-8").read()
m = re.search(r"^\*\*正在跑.*?$(.*?)(?=^\s*$|^#)", text, re.M | re.S)
rows = [l for l in (m.group(1).splitlines() if m else []) if l.startswith("|")]
seen_ws = set()
for row in rows:
    for ref in dict.fromkeys(re.findall(r"[\w./~-]*cursor-[\w.-]+?\.log", row)):
        path = os.path.expanduser(ref)
        if not os.path.isabs(path):
            hits = sorted(glob.glob(f"/private/tmp/claude-{os.getuid()}/{slug}/*/scratchpad/{os.path.basename(path)}"),
                          key=os.path.getmtime)
            path = hits[-1] if hits else path
        name = re.sub(r"(-sub-[\w.-]+?)?(-r\d+)?(-\d+)?\.log$", "", os.path.basename(path))[len("cursor-"):]
        ws = os.path.join(os.path.dirname(root), f"pandora-{name}")
        seen_ws.add(ws)
        if not os.path.exists(path):
            state = "日志找不到"
        else:
            ends = [l for l in open(path, encoding="utf-8", errors="replace").read().splitlines() if l.startswith("exit=")]
            pidf = path + ".pid"
            alive = (os.path.exists(pidf) and os.system(f"kill -0 {open(pidf).read().strip()} 2>/dev/null") == 0) or ws in running
            state = f"已结束 {ends[-1]}" if ends else ("在跑" + (f" PID {running[ws]}" if ws in running else "")) if alive else "被打断（没有 exit= 行、进程不在）"
        print(f"  {name:<14} {state:<28} {path}")
# opus 子 agent 转手 Composer（cursor-launch.sh --sub）的进程不单独登记日志，归到它那一路的 worktree
lane_ws = {os.path.join(os.path.dirname(root), f"pandora-{n}") for row in rows for n in re.findall(r"\.\./pandora-([\w-]+)", row)}
for ws, pid in running.items():
    if ws in seen_ws:
        continue
    if ws in lane_ws:
        print(f"  {os.path.basename(ws)[len('pandora-'):]:<14} {'在跑 PID ' + pid:<28} 子 agent 转手的 Composer（--sub，日志在该路 --log-dir）")
    else:
        print(f"  未登记的进程 PID {pid}  {ws}（补进 TASKS「正在跑」）")
if not rows:
    print("  TASKS 里找不到「正在跑」表")
PY

echo
echo "== 主目录 .claude/TASKS.md：未完成项"
grep -nE '^\s*- \[ \]' .claude/TASKS.md | cut -c1-200
echo
echo "== 下一步顺序"
grep -E '^\*\*下一步顺序\*\*' .claude/TASKS.md | cut -c1-600
