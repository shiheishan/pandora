#!/usr/bin/env bash
# 用法：detach.sh <日志> <命令> [参数…]
# 本机（macOS，没有 setsid）跨小时的调度脚本用它起：新开会话、脱离当前 Claude 会话，
# 换号或会话退出不会把它一起停掉。输出写进 <日志>，结束时日志末行是 exit=<退出码>，
# PID 写在 <日志>.pid。做法与 dispatch-task/scripts/cursor-launch.sh 起 cursor-agent 相同。
set -euo pipefail
[ $# -ge 2 ] || { echo "用法：$0 <日志> <命令> [参数…]" >&2; exit 2; }
log=$1; shift
command -v "$1" >/dev/null 2>&1 || [ -x "$1" ] || { echo "找不到命令：$1" >&2; exit 2; }
LOG="$log" nohup python3 -c 'import os,sys; os.setsid(); os.execvp(sys.argv[1], sys.argv[1:])' \
  sh -c '"$0" "$@" > "$LOG" 2>&1 < /dev/null; echo "exit=$?" >> "$LOG"' "$@" >/dev/null 2>&1 &
pid=$!
echo "$pid" > "$log.pid"
echo "已启动 PID $pid，日志 $log"
