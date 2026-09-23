#!/usr/bin/env bash
# 面板健康巡检。
#
# 设计成「无声则无事」：一切正常时只往 health.log 追一行 OK，
# 不打扰任何人。有问题才退非零 —— systemd 会把这个单元记成 failed，
# systemctl list-units --failed 一眼能看见。
#
# 配了告警渠道的话还会推一条消息。渠道是可选的：
# 在 deploy/.env 里设 AEGIS_ALERT_TG_TOKEN 和 AEGIS_ALERT_TG_CHAT 即可。
# 刻意和面板给用户发通知用的那个 bot 分开 —— 面板挂了的时候，
# 恰恰是它自己的通知链路最不可信。
set -u

ROOT=/opt/aegispanel
LOG=$ROOT/logs/health.log
cd "$ROOT" || exit 1
set -a; . deploy/.env 2>/dev/null; set +a

PROBLEMS=()
note(){ PROBLEMS+=("$1"); }

#--- 服务存活 ---
for s in aegis-public aegis-admin aegis-node; do
  st=$(systemctl is-active "$s" 2>/dev/null)
  [ "$st" = "active" ] || note "服务 $s 状态为 $st"
done

#--- HTTP 探活 ---
# 探的是真实端口而不是 systemd 的状态：进程活着但卡死在死锁里
# 是最常见也最难发现的一种「假活」。
for pair in "public:9000" "admin:9001"; do
  name=${pair%%:*}; port=${pair##*:}
  code=$(curl -sS -o /dev/null -w '%{http_code}' -m 8 "http://127.0.0.1:$port/" 2>/dev/null)
  [ "$code" = "200" ] || note "$name 端口 $port 探活返回 $code"
done

#--- 数据库 ---
if ! ./deploy/psql.sh -tAc 'SELECT 1' >/dev/null 2>&1; then
  note "数据库连不上"
fi

#--- 磁盘 ---
# /tmp 单列：它是 2G 的 tmpfs，被构建产物塞满过一次，
# 表现是链接器报 no space left，而根分区当时还剩几十 G。
for mp in / /tmp; do
  use=$(df --output=pcent "$mp" 2>/dev/null | tail -1 | tr -dc '0-9')
  [ -n "$use" ] || continue
  [ "$use" -lt 85 ] || note "$mp 已用 ${use}%"
done

#--- 备份新鲜度 ---
# 只看「有没有跑」不够：备份脚本失败时旧文件还在，
# 目录看着是满的。所以看最新那份的年龄。
BK=${AEGIS_BACKUP_DIR:-/var/backups/aegispanel}
newest=$(ls -t "$BK"/*.age 2>/dev/null | head -1)
if [ -z "$newest" ]; then
  note "备份目录 $BK 里没有任何备份"
else
  age_h=$(( ($(date +%s) - $(stat -c %Y "$newest")) / 3600 ))
  [ "$age_h" -lt 36 ] || note "最新备份已经是 ${age_h} 小时前的（$(basename "$newest")）"
  sz=$(stat -c %s "$newest")
  [ "$sz" -gt 10240 ] || note "最新备份只有 ${sz} 字节，疑似空文件"
fi

#--- 证书到期 ---
DOMAIN=$(nginx -T 2>/dev/null | grep -oP 'server_name \K[a-z0-9.-]+\.[a-z]+' | head -1)
if [ -n "$DOMAIN" ]; then
  end=$(echo | openssl s_client -connect "$DOMAIN:443" -servername "$DOMAIN" 2>/dev/null \
        | openssl x509 -noout -enddate 2>/dev/null | cut -d= -f2)
  if [ -n "$end" ]; then
    left=$(( ($(date -d "$end" +%s) - $(date +%s)) / 86400 ))
    [ "$left" -gt 14 ] || note "$DOMAIN 的证书还有 ${left} 天到期"
  else
    note "取不到 $DOMAIN 的证书信息"
  fi
fi

#--- 通知队列积压 ---
# 队列涨起来通常意味着 SMTP 挂了或 Telegram token 失效，
# 而这两件事本身不会让任何服务变成 inactive。
q=$(./deploy/psql.sh -tAc \
  "SELECT count(*) FROM notification_deliveries WHERE status='queued' AND created_at < now() - interval '30 minutes'" \
  2>/dev/null | tr -dc '0-9')
[ -z "$q" ] || [ "$q" -lt 200 ] || note "有 $q 条通知排队超过 30 分钟没发出去"

f=$(./deploy/psql.sh -tAc \
  "SELECT count(*) FROM notification_deliveries WHERE status='failed' AND created_at > now() - interval '6 hours'" \
  2>/dev/null | tr -dc '0-9')
[ -z "$f" ] || [ "$f" -lt 50 ] || note "最近六小时有 $f 条通知发送失败"

#--- 节点集体失联 ---
# 只在「本来有节点在上报、现在全断了」时才报。
#
# 不能简单地数「有多少节点心跳过期」：面板里完全可以存在没挂 agent 的
# 节点条目（旧协议的存档、还没交付的机器），它们的 last_heartbeat_at
# 永远是空。按那个口径报警，第一天就会变成天天响、然后被忽略的那种告警。
#
# 只看在服和排空中的节点：已退役的当然不再上报，把它们算进「以前是好的」
# 会让这条告警在退役后还响满七天。
#
# 判据换成两条同时成立：最近 30 分钟一条心跳都没有，且过去 7 天里
# 曾经有过 —— 前者是「现在坏了」，后者是「以前是好的」。
recent=$(./deploy/psql.sh -tAc \
  "SELECT count(*) FROM nodes WHERE serving_status IN ('active','draining') AND last_heartbeat_at > now() - interval '30 minutes'" \
  2>/dev/null | tr -dc '0-9')
ever=$(./deploy/psql.sh -tAc \
  "SELECT count(*) FROM nodes WHERE serving_status IN ('active','draining') AND last_heartbeat_at > now() - interval '7 days'" \
  2>/dev/null | tr -dc '0-9')
if [ -n "$recent" ] && [ -n "$ever" ] && [ "$recent" = "0" ] && [ "$ever" -gt 0 ]; then
  note "过去 30 分钟没有任何节点上报心跳（7 天内曾有 $ever 个在报）"
fi

#--- 汇报 ---
TS=$(date '+%Y-%m-%d %H:%M:%S')
if [ ${#PROBLEMS[@]} -eq 0 ]; then
  echo "$TS OK" >> "$LOG"
  exit 0
fi

MSG="潘多拉面板巡检发现 ${#PROBLEMS[@]} 个问题："
for p in "${PROBLEMS[@]}"; do MSG="$MSG"$'\n'"· $p"; done
echo "$TS ALERT ${PROBLEMS[*]}" >> "$LOG"
echo "$MSG" >&2

if [ -n "${AEGIS_ALERT_TG_TOKEN:-}" ] && [ -n "${AEGIS_ALERT_TG_CHAT:-}" ]; then
  curl -sS -m 15 -X POST \
    "https://api.telegram.org/bot${AEGIS_ALERT_TG_TOKEN}/sendMessage" \
    --data-urlencode "chat_id=${AEGIS_ALERT_TG_CHAT}" \
    --data-urlencode "text=${MSG}" >/dev/null 2>&1 \
    || echo "$TS 告警推送失败" >> "$LOG"
fi

exit 1
