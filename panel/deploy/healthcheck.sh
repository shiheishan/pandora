#!/usr/bin/env bash
# 面板健康巡检（pandora-health.timer 每 10 分钟一次，以 root 跑）。
#
# 设计成「无声则无事」：一切正常时只往 /var/log/pandora/health.log 追一行 OK，不打扰任何人。
# 有问题才退非零 —— systemd 会把这个单元记成 failed，systemctl list-units --failed 一眼能看见。
#
# 查的：pandora-serve / pandora-worker 两个服务、门户与后台的 /readyz、worker 与定时任务调度器的 asynq 心跳、
# Redis、PostgreSQL、磁盘、备份新鲜度、面板 HTTPS 证书剩余天数与续期结论、通知积压、节点集体失联。
#
# 配了告警渠道的话还会推一条消息：在 /etc/pandora/deploy.env 里设 PANDORA_ALERT_TG_TOKEN 和 PANDORA_ALERT_TG_CHAT 即可。
# 刻意和面板给用户发通知用的那个 bot 分开 —— 面板挂了的时候，恰恰是它自己的通知链路最不可信。
#
# 口令只经环境变量交给单个客户端（psql 的 PGPASSWORD、redis-cli 的 REDISCLI_AUTH），Telegram 令牌经 curl 的
# 标准输入配置传：都不进命令行参数。
set -u

# 安装根目录取自脚本自己的位置（<根>/deploy/healthcheck.sh，即 /opt/pandora），不写死路径。
# HEALTHCHECK_* 只给桩测试覆盖（healthcheck_mock_test.sh 以 HEALTHCHECK_LIB=1 source 本文件取函数）。
ROOT="${HEALTHCHECK_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." 2>/dev/null && pwd)}"
LOG_DIR="${HEALTHCHECK_LOG_DIR:-/var/log/pandora}"
LOG="$LOG_DIR/health.log"
DEPLOY_ENV="${HEALTHCHECK_DEPLOY_ENV:-/etc/pandora/deploy.env}"
SECRETS_ENV="${HEALTHCHECK_SECRETS_ENV:-/etc/pandora/secrets.env}"
CONFIG_FILE="${HEALTHCHECK_CONFIG:-/etc/pandora/config.yaml}"
PANDORA_BIN="${HEALTHCHECK_PANDORA_BIN:-$ROOT/bin/pandora}"

PROBLEMS=()
note(){ PROBLEMS+=("$1"); }

# 逐键读 KEY=VALUE 文件里的一个值（不 source：deploy.env 与 secrets.env 里的值不进本脚本的环境，也不执行任何内容）。
# 去掉一层成对的单引号或双引号
#   file_value <文件> <键>
file_value() {
  local v
  v="$(awk -v k="$2" 'index($0, k "=") == 1 { sub(/^[^=]*=/, ""); v = $0 } END { print v }' "$1" 2>/dev/null)"
  if [[ "$v" =~ ^\"(.*)\"$ ]] || [[ "$v" =~ ^\'(.*)\'$ ]]; then v="${BASH_REMATCH[1]}"; fi
  printf '%s' "$v"
}

# 读 config.yaml 顶层的一个标量（如 public_base_url）；没有或文件读不到时为空。只认「键: 值」一行的写法
#   config_scalar <文件> <键>
config_scalar() {
  local v
  v="$(awk -v k="$2" 'index($0, k ":") == 1 { sub(/^[^:]*:[[:space:]]*/, ""); sub(/[[:space:]]+#.*$/, ""); sub(/[[:space:]]+$/, ""); v = $0 } END { print v }' "$1" 2>/dev/null)"
  if [[ "$v" =~ ^\"(.*)\"$ ]] || [[ "$v" =~ ^\'(.*)\'$ ]]; then v="${BASH_REMATCH[1]}"; fi
  printf '%s' "$v"
}

#--- HTTPS 证书 ---
# 面板自身的 HTTPS（nginx 用的证书，edge-tls.sh 管；节点证书在库里，不归这里）。
# 主机取对外地址 public_base_url（域名或公网 IPv4）。查的是 nginx 此刻真正下发的那张（经回环 443），
# 阈值按证书自己的寿命缩放：剩余不到寿命的 1/3（最多 14 天）才警告。6 天的 IP 证书过半就续，
# 约剩 2.2 天才报；90 天的域名证书 certbot 剩 30 天续，仍按 14 天报。
# 续期 timer（edge-tls.sh renew）的结论也接到这里：上次续期出错、或 timer 超过 36 小时没跑。
EDGE_CONF="${EDGE_CONF:-/etc/nginx/conf.d/pandora.conf}"
TLS_STATUS_FILE="${TLS_STATUS_FILE:-/var/lib/pandora/tls/status}"
PUBLIC_BASE_URL="${PUBLIC_BASE_URL:-}"

# openssl 打的日期或 ISO 时间转成 epoch 秒；GNU date 与 BSD date 都认
date_epoch() {
  date -u -d "$1" +%s 2>/dev/null \
    || date -u -j -f '%b %e %T %Y %Z' "$1" +%s 2>/dev/null \
    || date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$1" +%s 2>/dev/null
}

# nginx 在回环 443 上给这个主机下发的证书（PEM）。IP 不发 SNI（RFC 6066 不许填 IP），
# 与浏览器用 IP 直连时一致
served_cert() {
  local sni=()
  [[ "$1" =~ ^[0-9.]+$ ]] || sni=(-servername "$1")
  echo | timeout 10 openssl s_client -connect 127.0.0.1:443 "${sni[@]}" 2>/dev/null | openssl x509 2>/dev/null
}

check_tls() {
  local url host pem end start now left life threshold result message checked checked_at
  url="${PUBLIC_BASE_URL:-}"; url="${url%/}"
  # 没走 HTTPS 边缘的部署（没有 pandora.conf、对外地址不是 https://主机）不查
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
    note "证书续期 timer 超过 36 小时没跑（systemctl status pandora-tls-renew.timer）"
  fi
}

#--- 数据库 ---
# 本机客户端经 127.0.0.1:POSTGRES_PORT 以 postgres 超级用户连（deploy.env 的 POSTGRES_SUPER_PASSWORD），
# 口令只经 PGPASSWORD 给这一个 psql。连库 5 秒放弃；连上之后每条查询再限 10 秒（锁等待、IO 挂住时不把巡检拖到
# systemd 的 120 秒超时，那样一条告警也发不出）。PGOPTIONS 整个由这里给，调用方环境里的不带进来。
# 第一条 SELECT 1 失败后（DB_DOWN=1）后面的查询不再去连：已记了「数据库连不上」，不必每条再等一轮
DB_DOWN=0
PG_PORT=""
PG_DB=""
PG_PASSWORD=""
db_query() {
  [ "$DB_DOWN" != 1 ] || return 1
  [[ "$PG_PORT" =~ ^[0-9]+$ ]] && [ -n "$PG_DB" ] && [ -n "$PG_PASSWORD" ] || return 1
  PGPASSWORD="$PG_PASSWORD" PGCONNECT_TIMEOUT=5 PGSSLMODE=disable PGOPTIONS='-c statement_timeout=10s' \
    psql -h 127.0.0.1 -p "$PG_PORT" -U postgres -d "$PG_DB" -X -tAc "$1"
}

# 查一个计数：成功时把数字写进变量 $1、返回 0。查询失败、超时（statement_timeout 取消时 psql 非零退出、
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

#--- Redis 与 worker 心跳 ---
# 连接串取 /etc/pandora/secrets.env 的 PANDORA_REDIS_URL（redis://[用户][:口令]@主机:端口[/库号]，rediss:// 走 TLS），
# 口令只经 REDISCLI_AUTH 给这一个 redis-cli。worker 进程里的 asynq Server 每 5 秒往 asynq:servers（有序集合，
# 分数是过期时刻）写一次心跳，Scheduler 同样写 asynq:schedulers：数「还没过期」的成员就知道 worker 在不在、卡没卡住
REDIS_HOST=""
REDIS_PORT=""
REDIS_DB=""
REDIS_USER=""
REDIS_PASS=""
REDIS_TLS=0
# 解析连接串到上面几个变量；格式不对返回 1
parse_redis_url() {
  local url="$1"
  REDIS_HOST=""; REDIS_PORT=6379; REDIS_DB=0; REDIS_USER=""; REDIS_PASS=""; REDIS_TLS=0
  [[ "$url" =~ ^(rediss?)://(([^:@/]*)(:([^@/]*))?@)?([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:([0-9]+))?(/([0-9]+))?/?(\?.*)?$ ]] || return 1
  [ "${BASH_REMATCH[1]}" != rediss ] || REDIS_TLS=1
  REDIS_USER="${BASH_REMATCH[3]}"
  REDIS_PASS="${BASH_REMATCH[5]}"
  REDIS_HOST="${BASH_REMATCH[6]}"; REDIS_HOST="${REDIS_HOST#[}"; REDIS_HOST="${REDIS_HOST%]}"
  [ -z "${BASH_REMATCH[8]}" ] || REDIS_PORT="${BASH_REMATCH[8]}"
  [ -z "${BASH_REMATCH[10]}" ] || REDIS_DB="${BASH_REMATCH[10]}"
  # 口令里的 %XX 解码（安装器生成的是十六进制口令，用不到；手填的带特殊字符时才需要）
  if [[ "$REDIS_PASS" == *%* ]]; then REDIS_PASS="$(printf '%b' "${REDIS_PASS//%/\\x}")"; fi
  return 0
}
redis_cmd() {
  local args=(--no-auth-warning -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB")
  [ -z "$REDIS_USER" ] || args+=(--user "$REDIS_USER")
  [ "$REDIS_TLS" != 1 ] || args+=(--tls)
  REDISCLI_AUTH="$REDIS_PASS" timeout 5 redis-cli "${args[@]}" "$@"
}

# 可单测的部分到此为止
if [ "${HEALTHCHECK_LIB:-}" = 1 ]; then
  return 0 2>/dev/null || exit 0
fi

cd "$ROOT" || exit 1
# health.log 写不进去时不能让「无声则无事」变成「无声且无记录」：目录不在就试着建，建不了就只写 journal
mkdir -p "$LOG_DIR" 2>/dev/null || true
PG_PORT="$(file_value "$DEPLOY_ENV" POSTGRES_PORT)"
PG_DB="$(file_value "$DEPLOY_ENV" POSTGRES_DB)"
PG_PASSWORD="$(file_value "$DEPLOY_ENV" POSTGRES_SUPER_PASSWORD)"
PUBLIC_BASE_URL="${PUBLIC_BASE_URL:-$(config_scalar "$CONFIG_FILE" public_base_url)}"

#--- 服务存活 ---
for s in pandora-serve pandora-worker; do
  st=$(systemctl is-active "$s" 2>/dev/null)
  [ "$st" = "active" ] || note "服务 $s 状态为 $st"
done

#--- HTTP 就绪 ---
# 探的是 /readyz（serve 自己再 ping 一次 PostgreSQL 与 Redis）而不是 systemd 的状态：
# 进程活着但卡死在死锁里是最常见也最难发现的一种「假活」
for pair in "public:9000" "admin:9001"; do
  name=${pair%%:*}; port=${pair##*:}
  code=$(curl -sS -o /dev/null -w '%{http_code}' -m 8 "http://127.0.0.1:$port/readyz" 2>/dev/null)
  [ "$code" = "200" ] || note "$name 端口 $port 探活返回 ${code:-无响应}（/readyz）"
done

#--- Redis 与 worker ---
if ! parse_redis_url "$(file_value "$SECRETS_ENV" PANDORA_REDIS_URL)"; then
  note "读不出 Redis 连接串（$SECRETS_ENV 的 PANDORA_REDIS_URL）"
elif [ "$(redis_cmd PING 2>/dev/null)" != PONG ]; then
  note "Redis 连不上（或 5 秒内没回 PING）"
else
  now_s=$(date +%s)
  servers=$(redis_cmd ZCOUNT asynq:servers "$now_s" +inf 2>/dev/null)
  if ! [[ "$servers" =~ ^[0-9]+$ ]]; then
    note "读不出 worker 心跳（asynq:servers）"
  elif [ "$servers" -lt 1 ]; then
    note "worker 没有心跳（后台任务没人处理：systemctl status pandora-worker）"
  fi
  schedulers=$(redis_cmd ZCOUNT asynq:schedulers "$now_s" +inf 2>/dev/null)
  if ! [[ "$schedulers" =~ ^[0-9]+$ ]]; then
    note "读不出定时任务调度器心跳（asynq:schedulers）"
  elif [ "$schedulers" -lt 1 ]; then
    note "定时任务调度器没有心跳（到期、续费、清理都不会跑：systemctl status pandora-worker）"
  fi
fi

#--- 数据库 ---
if ! db_query 'SELECT 1' >/dev/null 2>&1; then
  note "数据库连不上（或 10 秒内查不出 SELECT 1）"
  DB_DOWN=1
fi

#--- 磁盘 ---
# /tmp 单列：它常是单独的小 tmpfs，被塞满过一次，表现是链接器报 no space left，而根分区当时还剩几十 G。
for mp in / /tmp; do
  use=$(df --output=pcent "$mp" 2>/dev/null | tail -1 | tr -dc '0-9')
  [ -n "$use" ] || continue
  [ "$use" -lt 85 ] || note "$mp 已用 ${use}%"
done

#--- 备份新鲜度 ---
# 只看「有没有跑」不够：备份脚本失败时旧文件还在，目录看着是满的。所以看最新那份的年龄。
# 目录取 config 的 backup.dir（pandora backup dir），与备份脚本、系统状态页是同一个
BK="${HEALTHCHECK_BACKUP_DIR:-$("$PANDORA_BIN" backup dir 2>/dev/null)}"
if [ -z "$BK" ]; then
  note "读不出备份目录（$PANDORA_BIN backup dir 失败）"
else
  newest=$(ls -t "$BK"/pandora-postgres-*.dump.age 2>/dev/null | head -1)
  if [ -z "$newest" ]; then
    note "备份目录 $BK 里没有任何备份"
  else
    age_h=$(( ($(date +%s) - $(stat -c %Y "$newest")) / 3600 ))
    [ "$age_h" -lt 36 ] || note "最新备份已经是 ${age_h} 小时前的（$(basename "$newest")）"
    sz=$(stat -c %s "$newest")
    [ "$sz" -gt 10240 ] || note "最新备份只有 ${sz} 字节，疑似空文件"
  fi
fi

#--- 证书到期与续期（见上面 check_tls） ---
check_tls

#--- 通知积压 ---
# 队列涨起来通常意味着 SMTP 挂了，而这件事本身不会让任何服务变成 inactive。
if db_count q "通知积压" \
  "SELECT count(*) FROM notification_deliveries WHERE status='queued' AND created_at < now() - interval '30 minutes'"; then
  [ "$q" -lt 200 ] || note "有 $q 条通知排队超过 30 分钟没发出去"
fi

if db_count f "通知发送失败" \
  "SELECT count(*) FROM notification_deliveries WHERE status='failed' AND created_at > now() - interval '6 hours'"; then
  [ "$f" -lt 50 ] || note "最近六小时有 $f 条通知发送失败"
fi

#--- 节点集体失联 ---
# 只在「本来有服务器在上报、现在全断了」时才报。节点端连上 gRPC 会话后，面板把心跳合并写进服务器的 last_heartbeat_at。
#
# 不能简单地数「有多少服务器心跳过期」：面板里完全可以有还没装节点端的服务器（草稿、还没交付的机器），
# 它们的 last_heartbeat_at 永远是空。按那个口径报警，第一天就会变成天天响、然后被忽略的那种告警。
# 只看就绪与排空中的、没删的服务器：退役的当然不再上报。
#
# 判据是两条同时成立：最近 30 分钟一条心跳都没有，且过去 7 天里曾经有过 —— 前者是「现在坏了」，后者是「以前是好的」。
if db_count recent "节点心跳（最近 30 分钟）" \
    "SELECT count(*) FROM servers WHERE deleted_at IS NULL AND status IN ('ready','draining') AND last_heartbeat_at > now() - interval '30 minutes'" &&
  db_count ever "节点心跳（7 天内）" \
    "SELECT count(*) FROM servers WHERE deleted_at IS NULL AND status IN ('ready','draining') AND last_heartbeat_at > now() - interval '7 days'"; then
  if [ "$recent" = 0 ] && [ "$ever" -gt 0 ]; then
    note "过去 30 分钟没有任何节点上报心跳（7 天内曾有 $ever 个在报）"
  fi
fi

#--- 汇报 ---
TS=$(date '+%Y-%m-%d %H:%M:%S')
if [ ${#PROBLEMS[@]} -eq 0 ]; then
  echo "$TS OK" >> "$LOG" 2>/dev/null || echo "$TS OK"
  exit 0
fi

MSG="潘多拉面板巡检发现 ${#PROBLEMS[@]} 个问题："
for p in "${PROBLEMS[@]}"; do MSG="$MSG"$'\n'"· $p"; done
echo "$TS ALERT ${PROBLEMS[*]}" >> "$LOG" 2>/dev/null || true
echo "$MSG" >&2

tg_token="$(file_value "$DEPLOY_ENV" PANDORA_ALERT_TG_TOKEN)"
tg_chat="$(file_value "$DEPLOY_ENV" PANDORA_ALERT_TG_CHAT)"
if [ -n "$tg_token" ] && [ -n "$tg_chat" ]; then
  # 令牌在 URL 里：经 curl 的标准输入配置（-K -）传，不进命令行参数
  if ! printf 'url = "https://api.telegram.org/bot%s/sendMessage"\n' "$tg_token" \
      | curl -sS -m 15 -K - -X POST \
          --data-urlencode "chat_id=${tg_chat}" \
          --data-urlencode "text=${MSG}" >/dev/null 2>&1; then
    echo "$TS 告警推送失败" >> "$LOG" 2>/dev/null || true
  fi
fi

exit 1
