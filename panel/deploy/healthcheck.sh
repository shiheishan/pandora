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

# 安装根目录取自脚本自己的位置（<根>/deploy/healthcheck.sh，即 /opt/pandora），不写死路径。
# HEALTHCHECK_ROOT 只给桩测试覆盖。
ROOT="${HEALTHCHECK_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." 2>/dev/null && pwd)}"
LOG=$ROOT/logs/health.log

PROBLEMS=()
note(){ PROBLEMS+=("$1"); }

#--- HTTPS 证书 ---
# 主机取对外地址 AEGIS_PUBLIC_BASE_URL（域名或公网 IPv4），不再从 nginx 的 server_name 用域名正则
# 猜：只有 IP 的部署那样取不到、根本不查。查的是 nginx 此刻真正下发的那张（经回环 443），
# 阈值按证书自己的寿命缩放：剩余不到寿命的 1/3（最多 14 天）才警告。6 天的 IP 证书过半就续，
# 约剩 2.2 天才报；90 天的域名证书 certbot 剩 30 天续，仍按 14 天报。
# 续期 timer（edge-tls.sh renew）的结论也接到这里：上次续期出错、或 timer 超过 36 小时没跑。
# 路径变量只给桩测试覆盖（healthcheck_mock_test.sh 以 HEALTHCHECK_LIB=1 source 本文件）。
EDGE_CONF="${EDGE_CONF:-/etc/nginx/conf.d/aegis.conf}"
TLS_STATUS_FILE="${TLS_STATUS_FILE:-/var/lib/aegispanel/tls/status}"

# openssl 打的日期或 ISO 时间转成 epoch 秒；GNU date 与 BSD date 都认
date_epoch() {
  date -u -d "$1" +%s 2>/dev/null \
    || date -u -j -f '%b %e %T %Y %Z' "$1" +%s 2>/dev/null \
    || date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$1" +%s 2>/dev/null
}

# nginx 在回环 443 上给这个主机下发的证书（PEM）。IP 不发 SNI（RFC 6066 不许填 IP），
# 与浏览器、节点用 IP 直连时一致
served_cert() {
  local sni=()
  [[ "$1" =~ ^[0-9.]+$ ]] || sni=(-servername "$1")
  echo | timeout 10 openssl s_client -connect 127.0.0.1:443 "${sni[@]}" 2>/dev/null | openssl x509 2>/dev/null
}

check_tls() {
  local url host pem end start now left life threshold result message checked checked_at
  url="${AEGIS_PUBLIC_BASE_URL:-}"; url="${url%/}"
  # 没走 HTTPS 边缘的部署（没有 aegis.conf、对外地址不是 https://主机）不查
  [ -f "$EDGE_CONF" ] || return 0
  [[ "$url" =~ ^https://([^/:]+)$ ]] || return 0
  host="${BASH_REMATCH[1],,}"
  now=$(date -u +%s)
  pem="$(served_cert "$host")"
  if [ -z "$pem" ]; then
    note "取不到 https://$host 在用的证书（nginx 没在 443 上提供 HTTPS？）"
  else
    end=$(printf '%s\n' "$pem" | openssl x509 -noout -enddate 2>/dev/null | cut -d= -f2)
    start=$(printf '%s\n' "$pem" | openssl x509 -noout -startdate 2>/dev/null | cut -d= -f2)
    end=$(date_epoch "$end"); start=$(date_epoch "$start")
    if [ -z "$end" ] || [ -z "$start" ]; then
      note "读不出 https://$host 证书的有效期"
    else
      left=$(( end - now )); life=$(( end - start ))
      threshold=$(( life / 3 )); [ "$threshold" -lt $(( 14 * 86400 )) ] || threshold=$(( 14 * 86400 ))
      if [ "$left" -le 0 ]; then
        note "https://$host 的证书已过期"
      elif [ "$left" -lt "$threshold" ]; then
        note "https://$host 的证书还剩 $(( left / 3600 )) 小时到期（寿命 $(( life / 86400 )) 天，续期没跟上）"
      fi
    fi
  fi
  [ -f "$TLS_STATUS_FILE" ] || return 0
  result=$(awk -F= '$1 == "RESULT" { sub(/^[^=]*=/, ""); print }' "$TLS_STATUS_FILE")
  message=$(awk -F= '$1 == "MESSAGE" { sub(/^[^=]*=/, ""); print }' "$TLS_STATUS_FILE")
  checked_at=$(awk -F= '$1 == "CHECKED_AT" { sub(/^[^=]*=/, ""); print }' "$TLS_STATUS_FILE")
  [ "$result" != error ] || note "HTTPS 证书续期出错：$message（sudo $ROOT/deploy/edge-tls.sh status）"
  checked=$(date_epoch "$checked_at")
  if [ -n "$checked" ] && [ $(( now - checked )) -gt $(( 36 * 3600 )) ]; then
    note "证书续期 timer 超过 36 小时没跑（systemctl status aegis-tls-renew.timer）"
  fi
}

# 查库走 deploy/psql.sh（以 .env 里 postgres 超级用户的口令经回环连）；它不在或连不上都算「数据库连不上」。
# psql.sh 连库 5 秒放弃；连上之后每条查询再限 10 秒（锁等待、IO 挂住时不把巡检拖到 systemd 的 120 秒超时，
# 那样一条告警也发不出）。PGOPTIONS 整个由这里给，调用方环境里的不带进来。
# 第一条 SELECT 1 失败后（DB_DOWN=1）后面的查询不再去连：已记了「数据库连不上」，不必每条再等一轮
DB_DOWN=0
db_query() {
  [ "$DB_DOWN" != 1 ] || return 1
  PGOPTIONS='-c statement_timeout=10s' "$ROOT/deploy/psql.sh" -X -tAc "$1"
}

# 查一个计数（G4）：成功时把数字写进变量 $1、返回 0。查询失败、超时（statement_timeout 取消时 psql 非零退出、
# 没有输出）或输出不是一个数字时，记一条「<检查项>查询失败或超时」并返回 1——不能当成「查出 0」记 OK。
# 数据库已判为连不上（DB_DOWN=1）时不再重复报，前面已记了一条
#   db_count <变量名> <检查项> <SQL>
db_count() {
  local out
  printf -v "$1" '%s' ''
  [ "$DB_DOWN" != 1 ] || return 1
  if out=$(db_query "$3" 2>/dev/null) && [[ "$out" =~ ^[[:space:]]*([0-9]+)[[:space:]]*$ ]]; then
    printf -v "$1" '%s' "${BASH_REMATCH[1]}"
    return 0
  fi
  note "$2查询失败或超时（每条查询限 10 秒）"
  return 1
}

# 可单测的部分到此为止
if [ "${HEALTHCHECK_LIB:-}" = 1 ]; then
  return 0 2>/dev/null || exit 0
fi

cd "$ROOT" || exit 1
# 安装器不建 logs/：health.log 写不进去时不能让「无声则无事」变成「无声且无记录」
mkdir -p "$ROOT/logs" 2>/dev/null || true
set -a; . deploy/.env 2>/dev/null; set +a

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
if ! db_query 'SELECT 1' >/dev/null 2>&1; then
  note "数据库连不上（或 10 秒内查不出 SELECT 1）"
  DB_DOWN=1
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
BK=${AEGIS_BACKUP_DIR:-/var/backups/pandora}
newest=$(ls -t "$BK"/*.age 2>/dev/null | head -1)
if [ -z "$newest" ]; then
  note "备份目录 $BK 里没有任何备份"
else
  age_h=$(( ($(date +%s) - $(stat -c %Y "$newest")) / 3600 ))
  [ "$age_h" -lt 36 ] || note "最新备份已经是 ${age_h} 小时前的（$(basename "$newest")）"
  sz=$(stat -c %s "$newest")
  [ "$sz" -gt 10240 ] || note "最新备份只有 ${sz} 字节，疑似空文件"
fi

#--- 证书到期与续期（见上面 check_tls） ---
check_tls

#--- 通知队列积压 ---
# 队列涨起来通常意味着 SMTP 挂了或 Telegram token 失效，
# 而这两件事本身不会让任何服务变成 inactive。
if db_count q "通知积压" \
  "SELECT count(*) FROM notification_deliveries WHERE status='queued' AND created_at < now() - interval '30 minutes'"; then
  [ "$q" -lt 200 ] || note "有 $q 条通知排队超过 30 分钟没发出去"
fi

if db_count f "通知发送失败" \
  "SELECT count(*) FROM notification_deliveries WHERE status='failed' AND created_at > now() - interval '6 hours'"; then
  [ "$f" -lt 50 ] || note "最近六小时有 $f 条通知发送失败"
fi

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
if db_count recent "节点心跳（最近 30 分钟）" \
    "SELECT count(*) FROM nodes WHERE serving_status IN ('active','draining') AND last_heartbeat_at > now() - interval '30 minutes'" &&
  db_count ever "节点心跳（7 天内）" \
    "SELECT count(*) FROM nodes WHERE serving_status IN ('active','draining') AND last_heartbeat_at > now() - interval '7 days'"; then
  if [ "$recent" = 0 ] && [ "$ever" -gt 0 ]; then
    note "过去 30 分钟没有任何节点上报心跳（7 天内曾有 $ever 个在报）"
  fi
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
