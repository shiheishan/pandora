#!/usr/bin/env bash
# 后台起一次 Cursor 的 Composer（cursor-agent -p，模型 composer-2.5；Grok 10-10 停用），日志进总协调的 scratchpad，打印 TASKS 登记行。
# 用法（在主目录或任一 worktree 里跑）：
#   cursor-launch.sh <名字> [--round N] [--resume "<从哪一步续>"] [--model <模型>] [--log-dir <目录>] [--dry-run]
#       任务 worktree ../pandora-<名字>。缺省读 .claude/brief.md、报告写 .claude/report.md；
#       --round N（N ≥ 2）先读 brief 再读 .claude/round-r{N}.md，报告写 .claude/report-r{N}.md（adversarial-review 第 4 节）。
#   cursor-launch.sh <名字> --sub <标签> [--model composer-2.5] [--resume …] [--log-dir …] [--dry-run]
#       opus 子 agent 把编码部分转手（根 CLAUDE.md「大任务拆子 agent」Composer 那条）：读 .claude/sub-<标签>.md，
#       报告写 .claude/report-sub-<标签>.md；开工指令取 templates/cursor-prompt.md「子 agent 转手」一节：只提交、不推送、不等 CI。
#   cursor-launch.sh --dir <只读巡检目录> [--resume …] [--log-dir …] [--dry-run]
#       读 <目录>/brief.md（必须含 templates/server-readonly.md 的「服务器只读红线」一节，或 templates/server-scripted.md
#       的「服务器脚本测试红线」一节），报告写 <目录>/report.md。
#   cursor-launch.sh --scan <盘点目录> [--resume …] [--log-dir …] [--dry-run]
#       只读盘点（审查员、设计员等只读的 opus 把盘点、分类、定位转出去）：读 <目录>/brief.md，报告写 <目录>/report.md；
#       目录必须在总协调 scratchpad（/private/tmp/claude-<uid>/）或主目录 ops-local/ 下；Composer 只可写这个目录，
#       不改任何仓库或 worktree（跑完脚本不核，由转出方用 git status 核）。
#   cursor-launch.sh --wait <日志>
#       等日志出现 exit= 行再打印末尾。--sub、--scan 起的那次：前台跑（Bash timeout 600000，截断就原样再跑），见根 CLAUDE.md「子 agent 等 CI 或等 Composer」。
#       任务 worktree 整路、--round、--dir 起的那次：用 Bash 的 run_in_background 跑，结束时会话收到通知（总协调用）。
# - 开工指令取自 templates/cursor-prompt.md；--resume 在末尾加续跑段（resume-work「Composer 的路」）。
# - 进程用 setsid 脱离会话（会话重启也不死），PID 写在 <日志>.pid，结束时日志末行是 exit=<退出码>。
# - --log-dir 缺省是本会话的 scratchpad（由 CLAUDE_CODE_SESSION_ID 推出）；日志名 cursor-<名字>[-r<N>].log，重名加序号。
# - 同一个目录已有 cursor-agent 在跑、报告文件已存在（续跑除外）、报告没被 git 忽略时拒绝启动。
# - --model 缺省且只认 composer-2.5（用户 10-10 停用 Grok），名字以 `cursor-agent --list-models` 为准。
# - CURSOR_AGENT_BIN 只给试跑桩用，缺省 cursor-agent。
set -euo pipefail
MODEL=composer-2.5
here="$(cd "$(dirname "$0")/.." && pwd)"
tpl="$here/templates/cursor-prompt.md"
die() { echo "cursor-launch: $*" >&2; exit 2; }

if [ "${1:-}" = "--wait" ]; then
  log="${2:?用法: cursor-launch.sh --wait <日志>}"
  [ -f "$log" ] || die "没有日志 $log"
  while ! grep -q '^exit=' "$log"; do
    if [ -f "$log.pid" ] && ! kill -0 "$(cat "$log.pid")" 2>/dev/null; then
      echo "进程 $(cat "$log.pid") 已不在，日志没有 exit= 行：当作被打断，按 resume-work「Composer 的路」续跑"
      tail -n 30 "$log"; exit 1
    fi
    sleep 20
  done
  tail -n 30 "$log"
  exit 0
fi

name="" dir="" scan="" round="" sub="" resume="" logdir="" dry=0
while [ $# -gt 0 ]; do
  case "$1" in
    --round) round="${2:?}"; shift 2 ;;
    --sub) sub="${2:?}"; shift 2 ;;
    --resume) resume="${2:?}"; shift 2 ;;
    --dir) dir="${2:?}"; shift 2 ;;
    --scan) scan="${2:?}"; shift 2 ;;
    --log-dir) logdir="${2:?}"; shift 2 ;;
    --model) MODEL="${2:?}"; shift 2
      [[ "$MODEL" == composer-2.5 ]] || die "只认 composer-2.5（Grok 已停用），不认识 $MODEL" ;;
    --dry-run) dry=1; shift ;;
    -*) die "不认识的参数 $1" ;;
    *) [ -z "$name" ] || die "多余的参数 $1"; name="$1"; shift ;;
  esac
done

main="$(cd "$(git rev-parse --path-format=absolute --git-common-dir)/.." && pwd)"
if [ -n "$scan" ]; then
  [ -z "$dir$name$round$sub" ] || die "--scan 不和 --dir、<名字>、--round、--sub 一起用"
  W="$(cd "$scan" 2>/dev/null && pwd -P)" || die "目录不存在 $scan"
  name="$(basename "$W")"; branch=""; kind="只读盘点"
  start="$W/brief.md"; report="$W/report.md"
  [ -f "$start" ] || die "没有 $start"
  case "$W" in /private/tmp/claude-"$(id -u)"/*|"$main"/ops-local/*) ;; *) die "盘点目录要在 /private/tmp/claude-$(id -u)/ 或主目录 ops-local/ 下，现在是 $W" ;; esac
  git -C "$W" rev-parse --git-dir >/dev/null 2>&1 && ! git -C "$W" check-ignore -q "$W" && die "盘点目录在 git 仓库里且未被忽略：$W"
  dir="$W"
elif [ -n "$dir" ]; then
  [ -z "$name$round$sub" ] || die "--dir 不和 <名字>、--round、--sub 一起用"
  W="$(cd "$dir" 2>/dev/null && pwd)" || die "目录不存在 $dir"
  name="$(basename "$W")"; branch=""; kind="只读巡检"
  start="$W/brief.md"; report="$W/report.md"
  [ -f "$start" ] || die "没有 $start"
  if grep -q '^## 服务器脚本测试红线' "$start"; then kind="服务器脚本测试"
  else grep -q '^## 服务器只读红线' "$start" || die "$start 里没有「## 服务器只读红线」或「## 服务器脚本测试红线」一节（抄 templates/server-readonly.md 或 server-scripted.md）"; fi
  case "$W" in "$main"/ops-local/*) ;; *) die "巡检目录要在主目录 ops-local/ 下（git 忽略），现在是 $W" ;; esac
else
  [ -n "$name" ] || die "用法见脚本头"
  [[ "$name" =~ ^[a-z0-9-]+$ ]] || die "名字只用小写字母、数字、连字符"
  W="$(dirname "$main")/pandora-$name"; branch="feat/panel-redesign-$name"; kind="任务 worktree"
  [ -d "$W" ] || die "worktree 不存在 $W（先 new-worktree.sh $name）"
  [ "$(git -C "$W" rev-parse --abbrev-ref HEAD)" = "$branch" ] || die "$W 不在分支 $branch 上"
  [ -z "$round" ] || [ -z "$sub" ] || die "--round 与 --sub 不一起用"
  if [ -n "$sub" ]; then
    [[ "$sub" =~ ^[a-z0-9-]+$ ]] || die "--sub 标签只用小写字母、数字、连字符"
    start="$W/.claude/sub-$sub.md"; report="$W/.claude/report-sub-$sub.md"
  elif [ -n "$round" ]; then
    [[ "$round" =~ ^[0-9]+$ ]] && [ "$round" -ge 2 ] || die "--round 至少 2"
    start="$W/.claude/round-r$round.md"; report="$W/.claude/report-r$round.md"
  else
    start="$W/.claude/brief.md"; report="$W/.claude/report.md"
  fi
  [ -f "$start" ] || die "没有开工文件 $start"
  git -C "$W" check-ignore -q "$report" || die "$report 没被 git 忽略"
fi
if [ -e "$report" ] && [ -z "$resume" ]; then
  die "报告已存在 $report：上一次的结果先验收或挪走；续跑用 --resume"
fi
if pgrep -f "index\.js -p .*--workspace $W( |\$)" >/dev/null 2>&1; then
  die "$W 上已有 cursor-agent 在跑（pgrep -fl 'index\.js -p .*--workspace' 看）"
fi

gover="go$(sed -n 's/^go //p' "$main/panel/go.mod" | head -1)"
[ "$gover" != go ] || die "读不到 panel/go.mod 的 go 指令"
section="任务 worktree"; [ -n "$dir" ] && section="只读巡检"; [ "$kind" = 服务器脚本测试 ] && section="服务器脚本测试"; [ -n "$scan" ] && section="只读盘点"; [ -n "$sub" ] && section="子 agent 转手"
prompt="$(awk -v s="## $section" '$0==s{f=1;next} /^## /{f=0} f&&/^```text$/{c=1;next} c&&/^```$/{exit} c{print}' "$tpl")"
[ -n "$prompt" ] || die "templates/cursor-prompt.md 里找不到「## $section」的 text 块"
prompt="${prompt//<worktree>/$W}"
prompt="${prompt//<分支>/$branch}"
startdesc="$start"
if [ -n "$round" ]; then
  startdesc="$W/.claude/brief.md（原开工说明，通用规则照旧）与 $start（第 $round 轮修复，两者冲突时以它为准）"
fi
prompt="${prompt//<开工文件>/$startdesc}"
prompt="${prompt//<报告文件>/$report}"
prompt="${prompt//<主目录>/$main}"
prompt="${prompt//<go 版本>/$gover}"
if [ -n "$resume" ]; then
  prompt+="

这是续跑：上一次在中途断了。先回读现场：git log 看自己做过的提交，git status 看没提交的改动，读本目录的 .claude/TASKS.md 与已写的报告；已完成的不要重做，被中断的时间段写进报告的「中断与偏差」。从这里接着做：$resume"
fi
for left in '<worktree>' '<分支>' '<开工文件>' '<报告文件>' '<主目录>' '<go 版本>'; do
  case "$prompt" in *"$left"*) die "开工指令占位没替换干净：$left" ;; esac
done

if [ -z "$logdir" ]; then
  slug="$(printf '%s' "$main" | tr '/' '-')"
  logdir="/private/tmp/claude-$(id -u)/$slug/${CLAUDE_CODE_SESSION_ID:-?}/scratchpad"
fi
[ -d "$logdir" ] || die "日志目录不存在 $logdir（用 --log-dir 给总协调的 scratchpad）"
base="$logdir/cursor-$name${round:+-r$round}${sub:+-sub-$sub}"
log="$base.log"; i=2
while [ -e "$log" ]; do log="$base-$i.log"; i=$((i + 1)); done

bin="${CURSOR_AGENT_BIN:-cursor-agent}"
args=(-p --force --trust --sandbox disabled --workspace "$W" --model "$MODEL" --output-format text "$prompt")

basept=""
if [ -z "$dir" ]; then
  basept="$(sed -n '1,5s/^基点 \([^，]*\)，.*上游 \([^。]*\)。.*/基点 \1，上游 \2/p' "$W/.claude/brief.md" | head -1)"
fi
label="Cursor $name${round:+ 第 $round 轮}${sub:+ 转手 $sub}${resume:+ 续跑}"
where="${dir:+$W}"; [ -z "$where" ] && where="../pandora-$name${basept:+（$basept）}"

if [ "$dry" = 1 ]; then
  echo "== dry-run（不启动）：$kind"
  echo "cd $W && $bin ${args[*]:0:${#args[@]}-1} \"<开工指令>\" > $log 2>&1; echo \"exit=\$?\" >> $log"
  echo
  echo "== 开工指令"
  printf '%s\n' "$prompt"
  pid="<PID>"
else
  cd "$W"
  LOG="$log" nohup python3 -c 'import os,sys; os.setsid(); os.execvp(sys.argv[1], sys.argv[1:])' \
    sh -c '"$0" "$@" > "$LOG" 2>&1; echo "exit=$?" >> "$LOG"' "$bin" "${args[@]}" >/dev/null 2>&1 &
  pid=$!
  echo "$pid" > "$log.pid"
  echo "已启动 PID $pid，模型 $MODEL"
fi
echo
echo "日志：$log"
if [ -n "$sub" ] || [ -n "$scan" ]; then
  echo "等结束（前台跑，Bash timeout 600000，截断就原样再跑）：见根 CLAUDE.md「子 agent 等 CI 或等 Composer」"
  echo "bash $here/scripts/cursor-launch.sh --wait $log"
else
  echo "等结束（Bash run_in_background）：bash $here/scripts/cursor-launch.sh --wait $log"
fi
echo "TASKS「正在跑」登记行（「在做什么」写一句范围）："
echo "| $label（PID $pid） | cursor-agent $MODEL | $where | <范围>；开工 ${start#"$W"/}，报告 ${report#"$W"/}；日志 $log |"
