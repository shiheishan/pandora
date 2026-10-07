#!/usr/bin/env bash
# 总进度快照：主线头与 CI、各任务 worktree 的进度、TASKS.md 里的未完成项与下一步顺序。只读。
# 用法：snapshot.sh   在仓库主目录执行
set -uo pipefail
root="$(git rev-parse --show-toplevel)"
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
echo "== 主目录 .claude/TASKS.md：未完成项"
grep -nE '^\s*- \[ \]' .claude/TASKS.md | cut -c1-200
echo
echo "== 下一步顺序"
grep -E '^\*\*下一步顺序\*\*' .claude/TASKS.md | cut -c1-600
