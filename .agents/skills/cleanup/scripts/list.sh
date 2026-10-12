#!/usr/bin/env bash
# 只读：列出开发侧可清理的资源，给用户确认用。不删、不改、不 fetch。
#   worktree：已合并且干净的列为候选；运行中、未合提交、未提交改动、近期有改动、锁定的列为保留并写原因
#   本地分支、远端分支：已合并的列为候选
#   ops-local 原始数据（raw 目录）与 scratchpad 遗留的 worktree
# 最后打印建议命令，**不执行**。
# 用法：list.sh [--size]       --size 额外统计每个候选 worktree 的磁盘占用（慢一些）
# 环境变量：BASE（默认 feat/panel-redesign）、ACTIVE_MIN（默认 120，.claude 下文件这么多分钟内改过就算「近期有改动」）、
#           KEEP（空格分隔的额外保留名，如 "w7portal w7subb"）
# 可在主目录或任一 worktree 里跑，结果相同。
set -euo pipefail

SIZE=0
[[ "${1:-}" == "--size" ]] && SIZE=1

MAIN="$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")"
BASE="${BASE:-feat/panel-redesign}"
RBASE="origin/$BASE"
ACTIVE_MIN="${ACTIVE_MIN:-120}"
PREFIX="feat/panel-redesign-"
PARENT="$(dirname "$MAIN")"
TASKS="$MAIN/.claude/TASKS.md"
G() { git -C "$MAIN" "$@"; }

# 永不进清单的分支：main、基线、集成分支。worktree pandora-s 在 flush_wt 里按路径再挡一次。
is_protected_branch() { [[ "$1" == main || "$1" == "$BASE" || "$1" == "feat/panel-redesign-s" ]]; }

# ---- 运行中的任务：.claude/TASKS.md「正在跑 / 运行中」表里状态不以 ✅ 开头的行，取行里出现的 w<波次><名字> ----
RUNNING=""
TABLE_FOUND=0
if [[ -f "$TASKS" ]]; then
  table="$(awk '/^\*\*(正在跑|运行中)/ {f=1; next} f && /^\|/ {print; got=1; next} f && got {exit}' "$TASKS")"
  if [[ -n "$table" ]]; then
    TABLE_FOUND=1
    while IFS= read -r row; do
      [[ "$row" =~ ^\|[[:space:]]*-+ ]] && continue
      [[ "$row" =~ ^\|[[:space:]]*任务[[:space:]]*\| ]] && continue
      status="$(awk -F'|' '{s=$(NF-1); gsub(/^[ \t]+|[ \t]+$/, "", s); print s}' <<<"$row")"
      [[ "$status" == ✅* ]] && continue
      RUNNING+=" $( (grep -oE 'w[0-9]+[a-z][a-z0-9]*' <<<"$row" || true) | tr '\n' ' ')"
    done <<<"$table"
  fi
fi
RUNNING+=" ${KEEP:-}"
is_running() { [[ " $RUNNING " == *" $1 "* ]]; }

short_of_branch() { local b="$1"; b="${b#origin/}"; echo "${b#"$PREFIX"}"; }
short_of_path() { local n; n="$(basename "$1")"; echo "${n#pandora-}"; }

archived_at() {
  local d
  for d in "$MAIN"/ops-local/reports/*/"$1"; do
    [[ -d "$d" ]] && { echo "${d#"$MAIN"/}"; return; }
  done
  echo "-"
}

ignored_claude_files() {
  local wt="$1" f n=0
  for f in "$wt"/.claude/*; do
    [[ -f "$f" ]] || continue
    git -C "$wt" check-ignore -q "$f" 2>/dev/null && n=$((n + 1))
  done
  echo "$n"
}

echo "主目录 $MAIN"
echo "基线   本地 $BASE @ $(G rev-parse --short "$BASE")；远端 $RBASE @ $(G rev-parse --short "$RBASE" 2>/dev/null || echo 无)"
if [[ -f "$MAIN/.git/FETCH_HEAD" ]]; then
  echo "远端引用最后一次 fetch：$(date -r "$MAIN/.git/FETCH_HEAD" '+%F %T')（远端结论以此为准；要最新先由总协调 git fetch --prune）"
fi
if (( TABLE_FOUND )); then
  echo "运行中（取自 TASKS.md「正在跑」表与 KEEP）：$(tr ' ' '\n' <<<"$RUNNING" | sed '/^$/d' | sort -u | tr '\n' ' ')"
else
  echo "!! 没在 $TASKS 找到「正在跑 / 运行中」表：所有 worktree 都按「待确认」处理，不进候选"
fi
echo

# ---- worktree ----
CAND_WT=()      # 候选：路径
CAND_SHORT=()   # 候选：短名
KEEP_ROWS=()
OTHER_ROWS=()
CAND_ROWS=()
total_kb=0

wt_path=""; wt_head=""; wt_branch=""; wt_locked=0; wt_prunable=0
flush_wt() {
  [[ -z "$wt_path" ]] && return
  local path="$wt_path" head="$wt_head" branch="$wt_branch" short reason="" ahead dirty recent refs arch ign
  if [[ "$path" == "$MAIN" ]]; then return; fi
  if (( wt_prunable )); then
    OTHER_ROWS+=("$path | 目录已不在（prunable） | 建议 git worktree prune（只清登记）")
    return
  fi
  # 集成分支刚合完主线时看起来已合并且干净，运行中表又认不出短名 s
  if [[ "$path" == "$PARENT/pandora-s" ]] || is_protected_branch "${branch:-}"; then
    return
  fi
  if [[ "$path" != "$PARENT"/pandora-* ]]; then
    local merged="未合并"
    G merge-base --is-ancestor "$head" "$BASE" && merged="已合并"
    local st="干净"
    [[ -n "$(git -C "$path" status --porcelain 2>/dev/null | head -1)" ]] && st="有改动"
    OTHER_ROWS+=("$path | ${branch:-detached} @ ${head:0:7}，$merged，$st | scratchpad 或项目外的遗留，确认无用后同样 git worktree remove")
    return
  fi
  short="$(short_of_path "$path")"
  ahead="$(G rev-list --count "$BASE..$head")"
  dirty="$(git -C "$path" status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
  recent="$(find "$path/.claude" -maxdepth 1 -type f -mmin "-$ACTIVE_MIN" 2>/dev/null | wc -l | tr -d ' ')"
  if is_running "$short"; then reason="运行中（TASKS 表）"
  elif (( dirty > 0 )); then reason="未提交改动或未跟踪文件 $dirty 项"
  elif (( ahead > 0 )); then reason="有 $ahead 个提交未进 $BASE"
  elif (( wt_locked )); then reason="worktree 被锁定"
  elif (( ! TABLE_FOUND )); then reason="待确认（没找到运行中表）"
  elif (( recent > 0 )); then reason="待确认：.claude 下 $recent 个文件近 $ACTIVE_MIN 分钟内改过"
  fi
  if [[ -n "$reason" ]]; then
    KEEP_ROWS+=("$short | ${branch:-detached} | $reason")
    return
  fi
  refs="$(grep -c "pandora-$short/" "$TASKS" 2>/dev/null || true)"
  arch="$(archived_at "$short")"
  ign="$(ignored_claude_files "$path")"
  local size=""
  if (( SIZE )); then
    local kb; kb="$(du -sk "$path" 2>/dev/null | awk '{print $1}')"
    total_kb=$((total_kb + kb)); size=" | $((kb / 1024))M"
  fi
  CAND_WT+=("$path"); CAND_SHORT+=("$short")
  CAND_ROWS+=("$short | ${branch:-detached} @ ${head:0:7} | $(git -C "$MAIN" log -1 --format=%cs "$head") | .claude 忽略文件 $ign 个 | TASKS 引用 ${refs:-0} 处 | 归档 $arch$size")
}
while IFS= read -r line; do
  case "$line" in
    "worktree "*) flush_wt; wt_path="${line#worktree }"; wt_head=""; wt_branch=""; wt_locked=0; wt_prunable=0 ;;
    "HEAD "*) wt_head="${line#HEAD }" ;;
    "branch "*) wt_branch="${line#branch refs/heads/}" ;;
    locked*) wt_locked=1 ;;
    prunable*) wt_prunable=1 ;;
  esac
done < <(G worktree list --porcelain)
flush_wt

echo "== worktree 候选：已合并进 $BASE、工作区干净、不在运行中（${#CAND_ROWS[@]} 个）"
header="短名 | 分支 @ 头 | 头提交日期 | 要归档的 .claude 忽略文件 | TASKS.md 里引用它的地方 | 已有归档"
(( SIZE )) && header+=" | 占用"
echo "$header"
printf '%s\n' "${CAND_ROWS[@]+"${CAND_ROWS[@]}"}"
(( SIZE )) && echo "候选合计约 $((total_kb / 1024)) MB"
echo
echo "== worktree 保留（${#KEEP_ROWS[@]} 个）"
printf '%s\n' "${KEEP_ROWS[@]+"${KEEP_ROWS[@]}"}"
echo
echo "== 其他 worktree（scratchpad、项目外、已失效）（${#OTHER_ROWS[@]} 个）"
printf '%s\n' "${OTHER_ROWS[@]+"${OTHER_ROWS[@]}"}"
echo

# ---- 本地分支 ----
in_cand() { local s="$1" x; for x in "${CAND_SHORT[@]+"${CAND_SHORT[@]}"}"; do [[ "$x" == "$s" ]] && return 0; done; return 1; }
checked_out() { G worktree list --porcelain | grep -qx "branch refs/heads/$1"; }

LB_FREE=(); LB_WITH_WT=(); LB_KEEP=()
while IFS=' ' read -r b track; do
  is_protected_branch "$b" && continue
  s="$(short_of_branch "$b")"
  if ! G merge-base --is-ancestor "$b" "$BASE"; then
    LB_KEEP+=("$b | 未合并（$(G rev-list --count "$BASE..$b") 个提交）"); continue
  fi
  if is_running "$s"; then LB_KEEP+=("$b | 运行中"); continue; fi
  note=""
  [[ "$track" == *ahead* ]] && note="（$track：-d 会因未推送而拒绝，先查）"
  if checked_out "$b"; then
    if in_cand "$s"; then LB_WITH_WT+=("$b$note"); else LB_KEEP+=("$b | 它的 worktree 在保留名单里"); fi
  else
    LB_FREE+=("$b$note")
  fi
done < <(G for-each-ref refs/heads --format='%(refname:short) %(upstream:track)')

echo "== 本地分支：已合并、没有 worktree（${#LB_FREE[@]} 个）"
printf '%s\n' "${LB_FREE[@]+"${LB_FREE[@]}"}"
echo "== 本地分支：已合并，随候选 worktree 删除后再删（${#LB_WITH_WT[@]} 个）"
printf '%s\n' "${LB_WITH_WT[@]+"${LB_WITH_WT[@]}"}"
echo "== 本地分支：保留（${#LB_KEEP[@]} 个）"
printf '%s\n' "${LB_KEEP[@]+"${LB_KEEP[@]}"}"
echo

# ---- 远端分支 ----
RB_CAND=(); RB_KEEP=()
while IFS= read -r r; do
  [[ "$r" == origin/HEAD || "$r" == origin ]] && continue
  b="${r#origin/}"
  is_protected_branch "$b" && continue
  s="$(short_of_branch "$b")"
  if ! G merge-base --is-ancestor "$r" "$RBASE" 2>/dev/null; then RB_KEEP+=("$b | 未合并进 $RBASE"); continue; fi
  if is_running "$s"; then RB_KEEP+=("$b | 运行中"); continue; fi
  if G show-ref -q --verify "refs/heads/$b" && checked_out "$b" && ! in_cand "$s"; then
    RB_KEEP+=("$b | 本地 worktree 在保留名单里"); continue
  fi
  RB_CAND+=("$b")
done < <(G for-each-ref refs/remotes/origin --format='%(refname:short)')

echo "== 远端分支：已合并进 $RBASE（${#RB_CAND[@]} 个）"
printf '%s\n' "${RB_CAND[@]+"${RB_CAND[@]}"}"
echo "== 远端分支：保留（${#RB_KEEP[@]} 个）"
printf '%s\n' "${RB_KEEP[@]+"${RB_KEEP[@]}"}"
echo

# ---- ops-local 原始数据 ----
if [[ -d "$MAIN/ops-local" ]]; then
  echo "== ops-local 原始数据（raw 目录；成绩单与汇总不列，永远保留）"
  find "$MAIN/ops-local" -type d -name raw -prune -print 2>/dev/null | sort | while IFS= read -r d; do
    note=""
    # 只看有没有 secrets 目录，不读里面的内容
    [[ -n "$(find "$d" -type d -name secrets -print -quit 2>/dev/null)" ]] && note=" | 含 secrets/：不压不删，交用户定"
    echo "${d#"$MAIN"/} | $(du -sh "$d" 2>/dev/null | awk '{print $1}')$note"
  done
  echo
fi

# ---- 建议命令（不执行） ----
echo "== 建议命令（未执行；整份清单先给用户确认）"
if (( ${#CAND_SHORT[@]} )); then
  echo "# 1 归档（只复制）"
  echo "bash $MAIN/.claude/skills/cleanup/scripts/archive.sh ${CAND_SHORT[*]}"
  echo "# 2 删 worktree（不加 --force；拒绝就停下查原因）"
  for p in "${CAND_WT[@]}"; do echo "git -C $MAIN worktree remove $p"; done
fi
if (( ${#LB_FREE[@]} + ${#LB_WITH_WT[@]} )); then
  echo "# 3 删本地分支（-d，不用 -D）"
  for b in "${LB_FREE[@]+"${LB_FREE[@]}"}" "${LB_WITH_WT[@]+"${LB_WITH_WT[@]}"}"; do
    [[ "$b" == *"（"* ]] && { echo "#   跳过 $b"; continue; }
    echo "git -C $MAIN branch -d $b"
  done
fi
if (( ${#RB_CAND[@]} )); then
  echo "# 4 删远端已合并分支（先 git fetch --prune 再跑一次本脚本确认）"
  echo "git -C $MAIN push origin --delete ${RB_CAND[*]}"
fi
