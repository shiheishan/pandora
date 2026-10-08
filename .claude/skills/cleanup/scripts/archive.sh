#!/usr/bin/env bash
# 删 worktree 前归档：把它 .claude/ 下被 git 忽略的文件（brief.md、TASKS.md、report.md、ownership.txt、
# 调查时留下的 *.sql / *.log 等）复制到 ops-local/reports/<日期>/<名字>/。git worktree remove 会把它们一起删掉。
# 只复制：不删、不改源文件。目标已有同名文件时，内容一致就跳过，不一致就报错、不覆盖。
# 用法：archive.sh [--date YYYY-MM-DD] <短名或 worktree 路径>...
#   短名即目录名去掉 pandora- 前缀，如 w3core；与 list.sh 打印的建议命令一致。
# 退出码：0 全部成功；1 有冲突、校验失败或找不到 worktree（其余照常复制）；2 用法错误。
set -euo pipefail

MAIN="$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")"
DATE="$(date +%F)"
if [[ "${1:-}" == "--date" ]]; then
  DATE="${2:?--date 后面要跟 YYYY-MM-DD}"
  shift 2
fi
[[ "$DATE" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || { echo "日期格式要 YYYY-MM-DD：$DATE" >&2; exit 2; }
(( $# > 0 )) || { echo "用法: $0 [--date YYYY-MM-DD] <短名或 worktree 路径>..." >&2; exit 2; }
[[ -d "$MAIN/ops-local" ]] || { echo "$MAIN/ops-local 不存在：归档只放维护者本机的 ops-local，不要建在别处" >&2; exit 1; }

registered() { git -C "$MAIN" worktree list --porcelain | grep -qxF "worktree $1"; }

fail=0
for arg in "$@"; do
  if [[ -d "$arg" ]]; then
    wt="$(cd "$arg" && pwd -P)"
  else
    wt="$(dirname "$MAIN")/pandora-$arg"
  fi
  if [[ "$wt" == "$MAIN" ]]; then
    echo "跳过 $arg：这是主目录，不归档" >&2; fail=1; continue
  fi
  if [[ ! -d "$wt" ]] || ! registered "$wt"; then
    echo "跳过 $arg：$wt 不是本仓库登记的 worktree" >&2; fail=1; continue
  fi
  name="$(basename "$wt")"; name="${name#pandora-}"
  dest="$MAIN/ops-local/reports/$DATE/$name"
  copied=0; skipped=0
  for f in "$wt"/.claude/*; do
    [[ -f "$f" ]] || continue
    git -C "$wt" check-ignore -q "$f" || continue   # 入库的文件不用归档，删了也能从 git 找回
    d="$dest/$(basename "$f")"
    if [[ -e "$d" ]]; then
      if cmp -s "$f" "$d"; then skipped=$((skipped + 1)); continue; fi
      echo "冲突 $name：${d#"$MAIN"/} 已存在且内容不同，没有覆盖" >&2; fail=1; continue
    fi
    mkdir -p "$dest"
    cp -p "$f" "$d"
    if ! cmp -s "$f" "$d"; then echo "校验失败 $name：${d#"$MAIN"/}" >&2; fail=1; continue; fi
    copied=$((copied + 1))
  done
  if (( copied + skipped == 0 )); then
    echo "$name：.claude 下没有被忽略的文件，无可归档"
  else
    echo "$name：复制 $copied 个，已存在且一致 $skipped 个 → ${dest#"$MAIN"/}"
  fi
done
exit "$fail"
