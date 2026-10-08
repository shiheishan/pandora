#!/usr/bin/env bash
# 中断后核对测试机现场：还在跑的测试进程、临时改动（iptables、ufw、sysctl）、自撤销记录、stages.log 最后几行。只读。
# 用法：remote-state.sh <ssh 别名>...   先读该机 ~/ai/servers/<别名>/AGENTS.md 再用。
#   STAGES=/root/<目录>/stages.log 可指定远端时间线文件（默认找 /root/*/stages.log）。
set -uo pipefail
[ $# -gt 0 ] || { echo "用法：remote-state.sh <ssh 别名>..." >&2; exit 2; }
for h in "$@"; do
  echo "================ $h"
  ssh -n -o BatchMode=yes -o ConnectTimeout=15 "$h" "STAGES='${STAGES:-}' bash -s" <<'REMOTE' || echo "（连不上 $h）"
echo "-- 时间 $(date -u +%FT%TZ)，开机 $(uptime -p 2>/dev/null)"
echo "-- 会话外的测试进程（setsid 起的、非系统服务；按 PID 处理，不要 pkill -f）"
ps -eo pid,sid,etime,pcpu,rss,args --sort=start_time | awk 'NR==1 || ($2==$1 && $6 !~ /^(\/usr\/lib|\/lib|\/sbin|\/usr\/sbin|sshd|-bash|bash -s|systemd|\(sd-pam\)|ps |awk )/)' | cut -c1-160 | head -40
echo "-- systemd 里非系统的服务"
systemctl list-units --type=service --state=running --no-legend 2>/dev/null | awk '{print $1}' | grep -vE '^(systemd-|ssh|cron|dbus|getty|serial-getty|chrony|rsyslog|unattended|qemu-guest|polkit|ufw|networking|containerd|docker|user@)' | head -20
echo "-- iptables 非默认规则（故障注入的 DROP 应为 0 条）"
iptables -S 2>/dev/null | grep -vE '^-P |ufw|DOCKER|^-N ' | head -20
echo "   DROP/REJECT 计数：$(iptables -S 2>/dev/null | grep -vE 'ufw|DOCKER' | grep -cE -- '-j (DROP|REJECT)')"
echo "-- ufw 规则数：$(ufw status numbered 2>/dev/null | grep -c '^\[')（与 /root/README.md 记录的对照）"
for b in /root/*/sysctl.before; do
  [ -f "$b" ] || continue
  echo "-- sysctl 与改前值不同的项（$b）"
  grep ' = ' "$b" | while IFS= read -r line; do
    k="${line%% = *}"; v="${line#* = }"; now="$(sysctl -n "$k" 2>/dev/null | tr '\t' ' ')"
    [ "$now" = "$(echo "$v" | tr '\t' ' ')" ] || echo "   $k：改前 $v，现在 $now"
  done
done
if ls /root/selfrevert/*.log >/dev/null 2>&1; then
  echo "-- 自撤销记录（最后一行应是「已撤销 rc=0」）"
  for f in /root/selfrevert/*.log; do echo "   $(basename "$f")：$(tail -1 "$f")"; done
fi
for s in ${STAGES:-/root/*/stages.log}; do
  [ -f "$s" ] && { echo "-- $s 最后 5 行"; tail -5 "$s" | cut -c1-200; }
done
echo "-- 负载 $(cut -d' ' -f1-3 /proc/loadavg)，内存 $(free -m | awk '/Mem:/{print $3"/"$2"MB"}')，swap $(free -m | awk '/Swap:/{print $3"MB"}')"
REMOTE
done
