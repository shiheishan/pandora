#!/usr/bin/env bash
# install.sh 的常量与函数：PostgreSQL 集群与调参、.env 与加密备份、PostgreSQL 与 Valkey 的加固、
# 升级迁移的停服顺序、首装时交互式建管理员、要不要配 HTTPS 边缘、收尾提示。
# 只定义，不执行任何安装动作；由 install.sh 在校验过发布目录之后从发布目录 source（与 public-base-url.sh
# 同样的用法），不装到主机上。桩测试直接 source 它：install-pgcluster_mock_test.sh、install-backup_mock_test.sh、
# install-hardening_mock_test.sh、install-migrate-order_mock_test.sh、install-firstrun_mock_test.sh、
# install-chain_mock_test.sh；valkey-hardening_versions_docker_test.sh 拿它在几个版本的官方镜像里真起一遍。

# ── 常量 ──────────────────────────────────────────────
# 安装目录。systemd 单元里写的就是这个路径，单元原样安装、不改写
INSTALL_DIR="/opt/pandora"
# Pandora 只用这一个大版本的 main 集群（迁移用到 PG18 的 uuidv7() 等内建函数）
PANDORA_PG_MAJOR=18
SERVICES=(aegis-public aegis-admin aegis-node)
# 发布包 deploy/systemd/ 下的全部单元：install.sh 原样装到 /etc/systemd/system/
UNITS=(aegis-public.service aegis-admin.service aegis-node.service
  aegis-health.service aegis-health.timer
  aegis-backup.service aegis-backup.timer
  aegis-tls-renew.service aegis-tls-renew.timer)

say(){ printf '\033[1;32m%s\033[0m\n' "$*"; }
die(){ printf '\033[1;31m%s\033[0m\n' "$*" >&2; exit 1; }
need(){ command -v "$1" >/dev/null 2>&1 || die "缺少 $1"; }

#------------------------------------------------------------------------------
# PostgreSQL 集群（install-pgcluster_mock_test.sh）
#------------------------------------------------------------------------------

# 以 postgres 系统用户经本地 socket 跑 psql（peer 认证，不用口令）。SQL 经 -c 或标准输入给，
# 口令一律不拼进命令行。先 cd /：postgres 用户进不了调用方的当前目录（如 /root）时 psql 会告警
native_pg_peer() { (cd / && runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 "$@"); }

# <版本>/main 在线时打印它的端口（pg_lsclusters 为准，多版本共存时 PG18 不一定是 5432），否则返回 1
native_pg_cluster_port() {
  pg_lsclusters 2>/dev/null \
    | awk -v v="$1" '$1 == v && $2 == "main" && $4 ~ /^online/ { print $3; found = 1; exit } END { exit !found }'
}

# 确保 <版本>/main 存在并在线：没有就建，有但没起就启动。只建、只启动——不停、不升级、不删任何集群。
# 建的时候用 C.UTF-8（glibc 2.35 起内置，最小化系统也有；en_US.UTF-8 常缺，initdb 会失败）、UTF8 编码：
# 以前用 LC_ALL=C 建，编码随 C locale 成了 SQL_ASCII，库里的中文不校验编码
native_ensure_pg_cluster() {
  local ver="$1" _
  if ! pg_lsclusters 2>/dev/null | awk -v v="$ver" '$1 == v && $2 == "main" { found = 1 } END { exit !found }'; then
    say "  初始化 PostgreSQL ${ver}/main 集群"
    LC_ALL=C pg_createcluster --locale=C.UTF-8 --encoding=UTF8 "$ver" main 2>&1 | tail -3 \
      || die "PostgreSQL ${ver}/main 集群初始化失败"
  fi
  systemctl reset-failed "postgresql@${ver}-main" 2>/dev/null || true
  systemctl start "postgresql@${ver}-main" 2>/dev/null || true
  native_wait_pg_online "$ver" || die "PostgreSQL ${ver}/main 起不来：journalctl -u postgresql@${ver}-main -n 50"
}

# 等 <版本>/main 在线（缺省最多 15 秒；改配置重启时按检查点实测给）
#   native_wait_pg_online <版本> [秒]
native_wait_pg_online() {
  local _
  for _ in $(seq 1 "${2:-15}"); do
    native_pg_cluster_port "$1" >/dev/null && return 0
    sleep 1
  done
  return 1
}

# 端口上的集群里有没有 aegis 库：打印 yes / no / unknown（查不了）
native_pg_has_aegis() {
  local out
  out="$(native_pg_peer -p "$1" -d postgres -Atc "SELECT 1 FROM pg_catalog.pg_database WHERE datname = 'aegis'" 2>/dev/null)" \
    || { echo unknown; return 0; }
  case "$out" in 1) echo yes ;; '') echo no ;; *) echo unknown ;; esac
}

# 机器上 PG18 以外的集群（更早的 16/17、以后的 19）一律不碰：不停、不升级、不删。
# 以前这里对在线的旧集群跑 pg_upgrade，而且不管它成没成、跑没跑，最后都 pg_dropcluster——
# 旧库直接没了（新数据目录已初始化时 pg_upgrade 根本不跑）。现在只看旧集群里有没有 aegis 库，
# 有的话 PG18 是不是它的接班人：
#   - 首装而 PG18 里还没有 aegis 库：数据多半就在旧集群里，停下；
#   - 升级而 .env 的 POSTGRES_PORT 指着旧集群：这台面板一直跑在旧集群上，停下；
#   - 其余（PG18 已有 aegis 库、.env 指着 PG18）：旧集群是早先的遗留，只提示。
# 查不了旧集群按「有 aegis 库」算。升级时 .env 的 POSTGRES_PORT 不是 PG18 的端口也停下。
# 停下时什么都还没改。
#   native_check_foreign_clusters <PG 大版本> <它的端口> <install|upgrade> <.env 的 POSTGRES_PORT，仅升级>
native_check_foreign_clusters() {
  local want="$1" want_port="$2" mode="$3" env_port="${4:-}"
  local ver cluster port status _rest has blockers=() pg18_has=""
  if command -v pg_lsclusters >/dev/null 2>&1; then
    while read -r ver cluster port status _rest; do
      [[ "$ver" =~ ^[0-9]+$ ]] || continue
      [ "$ver" != "$want" ] || continue
      if [[ "$status" != online* ]]; then
        say "  ! 另有 PostgreSQL ${ver}/${cluster}（端口 ${port}，${status}）：不是 Pandora 用的集群，安装器不碰它"
        continue
      fi
      has="$(native_pg_has_aegis "$port")"
      if [ "$has" = no ]; then
        say "  另有 PostgreSQL ${ver}/${cluster}（端口 ${port}），里面没有 aegis 库，安装器不碰它"
        continue
      fi
      if [ "$mode" = upgrade ]; then
        if [ "$env_port" = "$port" ]; then
          blockers+=("PostgreSQL ${ver}/${cluster}（端口 ${port}）：.env 的 POSTGRES_PORT 指着它，这台面板的数据就在它里面")
          continue
        fi
      else
        [ -n "$pg18_has" ] || pg18_has="$(native_pg_has_aegis "$want_port")"
        if [ "$pg18_has" != yes ]; then
          blockers+=("PostgreSQL ${ver}/${cluster}（端口 ${port}）里$([ "$has" = yes ] && echo 有 || echo 查不清有没有) aegis 库，PG${want} 里还没有")
          continue
        fi
      fi
      say "  ! PostgreSQL ${ver}/${cluster}（端口 ${port}）里$([ "$has" = yes ] && echo 有 || echo 查不清有没有) aegis 库：面板用的是 PG${want}（端口 ${want_port}），它是早先的遗留，安装器不碰；确认没用后自己处理"
    done < <(pg_lsclusters 2>/dev/null)
  fi
  if [ "$mode" = upgrade ] && [ -n "$env_port" ] && [ "$env_port" != "$want_port" ] && [ "${#blockers[@]}" -eq 0 ]; then
    blockers+=(".env 的 POSTGRES_PORT=${env_port}，而 PostgreSQL ${want}/main 在端口 ${want_port}：不知道面板的数据在哪个集群")
  fi
  [ "${#blockers[@]}" -eq 0 ] && return 0
  printf '\033[1;31m%s\033[0m\n' "数据库不在安装器预期的地方，停下（什么都还没改，任何集群都没停、没删）：" >&2
  printf '  - %s\n' "${blockers[@]}" >&2
  die "旧集群里的 aegis 库要由人搬到 PG${want}（导出 → 恢复 → 核对，步骤见发布包 deploy/MIGRATION-RUNBOOK.md「旧版本集群里的 aegis 库」）；搬完、确认 .env 的 POSTGRES_PORT=${want_port} 后重跑本脚本"
}

#------------------------------------------------------------------------------
# .env 与加密备份。install-backup_mock_test.sh 测这几个函数
#------------------------------------------------------------------------------
# 加密备份目录（升级前备份也在这里）。aegis-backup.service 的 ReadWritePaths、edge-tls.sh 的缺省
# BACKUP_DIR 写的是同一个路径（install-chain_mock_test.sh 核对三处一致）
NATIVE_BACKUP_DIR=/var/backups/pandora

# 往 .env 末尾追加缺的键（参数是 KEY=VALUE），已有的行一个字节都不动——那里是随机生成的口令与密钥，
# 改错一个就连不上库、解不开信封加密的字段。先写同目录临时文件再改名，保持 0600。
# 追加了的键名放在 NATIVE_ENV_ADDED（空格分隔）
native_env_append_missing() {
  local env_file="$1" line tmp added=()
  shift
  NATIVE_ENV_ADDED=""
  for line in "$@"; do
    grep -q "^${line%%=*}=" "$env_file" || added+=("$line")
  done
  [ "${#added[@]}" -gt 0 ] || return 0
  tmp="$(mktemp "$env_file.tmp.XXXXXX")"
  chmod 0600 "$tmp"
  {
    cat "$env_file"
    printf '\n# install.sh 升级时补上的新键（原有的行没动）\n'
    printf '%s\n' "${added[@]}"
  } >"$tmp"
  mv -f -- "$tmp" "$env_file"
  for line in "${added[@]}"; do NATIVE_ENV_ADDED="${NATIVE_ENV_ADDED:+$NATIVE_ENV_ADDED }${line%%=*}"; done
}

# 备份加密用的 age 密钥：没有就生成（0600，目录 0700），打印公钥（recipient）。已有的绝不覆盖：
# 旧备份只能用它解开。解密私钥与备份在同一台机器，机器整体丢失时要另存（见收尾提示）
native_ensure_age_key() {
  local key="$1"
  install -d -m 0700 "$(dirname "$key")"
  if [ ! -f "$key" ]; then
    (umask 077 && age-keygen -o "$key" 2>/dev/null) || die "生成备份加密密钥失败（age-keygen）"
  fi
  chmod 0600 "$key"
  age-keygen -y "$key" || die "读不出备份加密密钥的公钥：$key"
}

# .env 里与加密备份、节点端分发目录有关的键（首装写进去，升级缺了才追加）。recipient 为空时不出那一行
#   native_default_env_lines <安装目录> <age recipient>
native_default_env_lines() {
  printf '%s\n' "AEGIS_BACKUP_DIR=$NATIVE_BACKUP_DIR" "AEGIS_BACKUP_RETENTION_DAYS=14"
  [ -z "$2" ] || printf '%s\n' "AEGIS_BACKUP_AGE_RECIPIENT=$2"
  printf '%s\n' "AEGIS_BACKUP_AGE_IDENTITY=$1/secrets/backup-age.key" "AEGIS_BACKUP_WEBDAV_BIN=$1/bin/aegis-backup-webdav"
  # 节点一键安装分发的二进制目录：显式写进 .env，不依赖网关里的缺省值
  printf '%s\n' "PANDORA_PDND_DIST_DIR=$1/pdnd-dist"
}

#------------------------------------------------------------------------------
# PostgreSQL 调参（conf.d）、PostgreSQL 与 Valkey 的加固（install-hardening_mock_test.sh）
#------------------------------------------------------------------------------
# 发行版包自带的单元几乎没有隔离：
#   - postgresql@.service（postgresql-common）除 OOMScoreAdjust 外没有任何加固，以 root 起、再由 pg_ctlcluster 降到 postgres；
#   - valkey-server / redis-server 的单元各版本不一：Debian 13 的 valkey 已经较严，Debian 12 的 redis 末尾又把
#     ProtectSystem 改回 true、能力集留着 SETUID / SETGID / SYS_RESOURCE、没有系统调用过滤；都没有进程可见性、
#     网络出口与内存上限。配置文件缺省会定期落盘（dump.rdb），也没禁危险命令。
# 下面的 drop-in 不依赖发行版单元写了什么，整套写全（挂载、进程、网络隔离，系统调用过滤，能力集清空，cgroup
# 内存上限 PostgreSQL 512M、Valkey 160M）；配置块补 Valkey 的行为（只听回环、禁 FLUSHALL / FLUSHDB、不落盘、
# 内存上限与淘汰）。对照表见 deploy/RUNBOOK.md「PostgreSQL 与 Valkey 的加固」。被挡的系统调用返回 EPERM，不杀进程

# PostgreSQL 与 Valkey 各一份 drop-in pandora.conf，两段：
#   - 资源约束，一直写：MemoryMax（2c4g 面板机上的内存预算）与 MemorySwapMax=0（面板的服务不换出到硬盘：
#     换入卡顿是性能问题，进程内存里的密钥也不该落交换区）。它们只要 cgroup，不要挂载命名空间，
#     所以不随加固开关去掉——关加固的容器化 VPS 照样受这两条约束；
#   - 隔离（沙箱），随 PANDORA_SYSTEMD_HARDENING：关掉时这一段不写。
# 一份文件、一次写、一次撤回，比两份文件各自撤回简单；内容随开关变，变了就算改动。
# 三个网关单元（systemd/aegis-*.service）自己带 MemorySwapMax=0。
NATIVE_DROPIN=pandora.conf

# PostgreSQL 18/main 的 drop-in。不加 MemoryDenyWriteExecute（超级用户会话的 JIT 要可写可执行内存）；
# 系统调用用拒绝清单（挡挂载、重启、内核模块、调试等），不用允许清单，免得扩展踩到
native_pg_hardening_dropin() {
  cat <<'UNIT'
User=postgres
Group=postgres
# 以 postgres 起之后 pg_ctlcluster 自己建不了 /run/postgresql（tmpfiles 缺省会建，这里兜底）；+ 表示这一步不受下面的限制
ExecStartPre=+/usr/bin/install -d -m 2775 -o postgres -g postgres /run/postgresql
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
PrivateTmp=yes
PrivateDevices=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/postgresql -/var/log/postgresql /run/postgresql
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
IPAddressDeny=any
IPAddressAllow=localhost
SystemCallArchitectures=native
SystemCallErrorNumber=EPERM
SystemCallFilter=~@clock @cpu-emulation @debug @module @mount @obsolete @raw-io @reboot @swap
UNIT
}

# PostgreSQL 18/main 的整份 drop-in：资源约束一直在，隔离随开关。
# 有了 MemoryMax、又不许换出，撞上限时内核会 OOM 杀掉 cgroup 里的某个进程。systemd 缺省 OOMPolicy=stop 会因此
# 停掉整个单元，Debian 的单元又是 Restart=no，库就一直停着。所以资源段写 OOMPolicy=continue：被杀的多半是某个后端，
# 交给 postmaster 自己做崩溃恢复（断开其他连接、重放 WAL、重新接客），单元不停（panel2 实测：撞 200M 杀掉一个后端，
# 约 0.2 秒后重新接客，单元一直 active）。
# 不写 Restart=（panel2 实测过两种）：这个 Type=forking 单元里 postmaster 被 kill -9 时 systemd 记为正常退出，
# on-abnormal 拉不起它；on-failure 又会因为 ExecStop 在库已停时报错，把 postgres 用户有意 pg_ctlcluster stop 的库
# 5 秒后拉回来（Debian 关掉自动重启正是为此）。postmaster 本身受 OOMScoreAdjust=-900 保护，真没了由巡检告警、人来起
native_pg_dropin() {
  printf '%s\n' '# pandora（install.sh 生成）：资源约束一直在，隔离随 PANDORA_SYSTEMD_HARDENING' '[Service]' \
    'MemoryMax=512M' 'MemorySwapMax=0' 'OOMPolicy=continue'
  [ "${NATIVE_HARDENING:-1}" != 1 ] || native_pg_hardening_dropin
}

# Valkey / Redis 的 drop-in。<口味> 是 valkey 或 redis，决定放行写的目录。系统调用用允许清单
# （先清掉发行版单元里的再写，免得两份叠成别的意思）；没有 JIT，可以禁可写可执行内存
#   native_valkey_hardening_dropin <valkey|redis>
native_valkey_hardening_dropin() {
  cat <<UNIT
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
PrivateTmp=yes
PrivateDevices=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=-/var/lib/$1 -/var/log/$1 -/run/$1
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
IPAddressDeny=any
IPAddressAllow=localhost
SystemCallArchitectures=native
SystemCallErrorNumber=EPERM
SystemCallFilter=
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
UNIT
}

# Valkey / Redis 的整份 drop-in：资源约束一直在，隔离随开关
#   native_valkey_dropin <valkey|redis>
native_valkey_dropin() {
  printf '%s\n' '# pandora（install.sh 生成）：资源约束一直在，隔离随 PANDORA_SYSTEMD_HARDENING' '[Service]' \
    'MemoryMax=160M' 'MemorySwapMax=0'
  [ "${NATIVE_HARDENING:-1}" != 1 ] || native_valkey_hardening_dropin "$1"
}

NATIVE_SYSTEMD_DIR=/etc/systemd/system

# 一个单元的 pandora drop-in：读、写、删
native_dropin_file() { printf '%s\n' "$NATIVE_SYSTEMD_DIR/$1.d/$NATIVE_DROPIN"; }
native_write_dropin() {
  local file
  file="$(native_dropin_file "$1")"
  install -d -m 0755 "${file%/*}"
  printf '%s\n' "$2" >"$file.next" && chmod 0644 "$file.next" && mv -f -- "$file.next" "$file"
}
native_remove_dropin() { rm -f -- "$(native_dropin_file "$1")"; }
# 写一个单元的 drop-in；内容没变返回 1（不必重启），写了返回 0
#   native_apply_dropin <单元名> <drop-in 内容>
native_apply_dropin() {
  local file
  file="$(native_dropin_file "$1")"
  if [ -f "$file" ] && [ "$(cat "$file")" = "$2" ]; then return 1; fi
  native_write_dropin "$1" "$2"
}
# 把 drop-in 还原成改之前的样子：<之前有没有> 为 1 时写回 <之前的内容>，否则删掉
native_restore_dropin() {
  if [ "$2" = 1 ]; then native_write_dropin "$1" "$3"; else native_remove_dropin "$1"; fi
}

# 加固开关 PANDORA_SYSTEMD_HARDENING：缺省开（1）；0 明确去掉两份 drop-in 里的隔离段（资源约束 MemoryMax、
# MemorySwapMax=0 照留；Valkey 配置块与 PostgreSQL 调参也不受影响，那是行为口径，不是沙箱）。安装时的环境变量优先，并记进 .env（只改这一行，别的行一个字节都不动），之后的升级沿用；
# 没给就看 .env。结果放在 NATIVE_HARDENING（1 / 0）
#   native_load_hardening_switch <.env>
native_load_hardening_switch() {
  local env_file="$1" v="${PANDORA_SYSTEMD_HARDENING:-}" from_env=1 tmp
  if [ -z "$v" ]; then
    from_env=0
    v="$(pandora_env_file_value "$env_file" PANDORA_SYSTEMD_HARDENING 2>/dev/null || true)"
  fi
  case "${v:-1}" in
    1|on|yes|true) v=1 ;;
    0|off|no|false) v=0 ;;
    *) die "PANDORA_SYSTEMD_HARDENING 只认 1（开，缺省）或 0（关）：$v" ;;
  esac
  if [ "$from_env" = 1 ] && [ -f "$env_file" ] \
      && [ "$(pandora_env_file_value "$env_file" PANDORA_SYSTEMD_HARDENING 2>/dev/null || true)" != "$v" ]; then
    if grep -q '^PANDORA_SYSTEMD_HARDENING=' "$env_file"; then
      tmp="$(mktemp "$env_file.tmp.XXXXXX")"
      chmod 0600 "$tmp"
      NATIVE_SWITCH="$v" awk '/^PANDORA_SYSTEMD_HARDENING=/ { print "PANDORA_SYSTEMD_HARDENING=" ENVIRON["NATIVE_SWITCH"]; next } { print }' \
        "$env_file" >"$tmp" && mv -f -- "$tmp" "$env_file" || { rm -f -- "$tmp"; die "改 $env_file 的 PANDORA_SYSTEMD_HARDENING 失败"; }
    else
      native_env_append_missing "$env_file" "PANDORA_SYSTEMD_HARDENING=$v"
    fi
  fi
  NATIVE_HARDENING="$v"
  [ "$v" = 1 ] || say "  ! systemd 加固已按 PANDORA_SYSTEMD_HARDENING=0 关闭（记在 .env）：PostgreSQL 与 Valkey 没有挂载、进程、网络隔离与 cgroup 内存上限（RUNBOOK「PostgreSQL 与 Valkey 的加固」）"
}

# 单元带着新 drop-in 起不来时给的一句原因与办法：226/NAMESPACE 是主机不支持沙箱要的挂载命名空间
#   native_unit_failure_hint <单元名>
native_unit_failure_hint() {
  if [ "$(systemctl show -p ExecMainStatus --value "$1" 2>/dev/null)" = 226 ] \
      || journalctl -u "$1" -n 80 --no-pager 2>/dev/null | grep -Eq '226/NAMESPACE|step NAMESPACE'; then
    printf '%s\n' "原因是 226/NAMESPACE：这台主机不支持 systemd 沙箱要的挂载命名空间（LXC、OpenVZ 一类容器化 VPS 常见）。确认是这样，就用 PANDORA_SYSTEMD_HARDENING=0 重跑，明确关掉这层加固（记进 .env，之后的升级沿用）"
  else
    printf '%s\n' "原因看 journalctl -u $1 -n 50。确实要关掉这层加固，用 PANDORA_SYSTEMD_HARDENING=0 重跑（记进 .env）"
  fi
}

# 重启 PostgreSQL 前先做一次 CHECKPOINT 并计时：关机检查点因此只剩很少的脏页，计到的秒数就是这台机器
# 刷盘的实际快慢；重启后等它在线的时长按它给（30 秒起，加检查点耗时的三倍），慢盘不会被误判成起不来
#   native_pg_online_budget <端口>
native_pg_online_budget() {
  local started
  started="$(date +%s)"
  native_pg_peer -p "$1" -d postgres -c CHECKPOINT >/dev/null 2>&1 || true
  echo $(( 30 + 3 * ($(date +%s) - started) ))
}

# Pandora 的 PostgreSQL 参数（发布包 deploy/postgresql-pandora.conf：低内存调参、关 JIT、WAL、慢查询日志、UTC）
# 装成集群的 conf.d/pandora.conf。用 conf.d 不用 ALTER SYSTEM：
#   - 这是安装器整份拥有的声明式文件，每次安装与升级整份覆盖，手改不会悄悄留下、和发布包逐字可比；
#   - 放在 /etc 下一眼看得到，写它不需要连库开 SQL 会话；
#   - postgresql.auto.conf 留给运维自己 ALTER SYSTEM（它在 conf.d 之后读，运维的设置照样压过这里）。
# Debian 的 postgresql.conf 缺省有 include_dir = 'conf.d'，写在内置参数之后，这里的值压过上面的缺省值。
# NATIVE_PG_ETC 只给桩测试覆盖
NATIVE_PG_ETC=/etc/postgresql
native_pg_conf_dir() { printf '%s/%s/main\n' "$NATIVE_PG_ETC" "$1"; }
native_pg_tuning_file() { printf '%s/conf.d/pandora.conf\n' "$(native_pg_conf_dir "$1")"; }

# 集群的 postgresql.conf 里要有生效的 include_dir = 'conf.d'（或写成 conf.d 的绝对路径），否则 Pandora 的参数
# 放进去也不生效：停下说清楚。install.sh 在建好集群之后、任何数据与配置改动之前调它
#   native_check_pg_conf_include <版本>
native_check_pg_conf_include() {
  local dir conf
  dir="$(native_pg_conf_dir "$1")"
  conf="$dir/postgresql.conf"
  [ -f "$conf" ] || die "找不到 $conf（PostgreSQL $1/main 的配置文件），Pandora 的参数没处放，停下（什么都还没改）"
  awk -v q="'" -v abs="$dir/conf.d" '
    /^[[:space:]]*include_dir([[:space:]=]|$)/ {
      line = $0
      sub(/^[[:space:]]*include_dir[[:space:]]*(=[[:space:]]*)?/, "", line)
      if (substr(line, 1, 1) != q) next
      line = substr(line, 2)
      end = index(line, q)
      if (end == 0) next
      v = substr(line, 1, end - 1); rest = substr(line, end + 1)
      sub(/\/$/, "", v)
      if ((v == "conf.d" || v == abs) && rest ~ /^[[:space:]]*(#.*)?$/) found = 1
    }
    END { exit !found }' "$conf" \
    || die "$conf 里没有生效的 include_dir = 'conf.d'（Debian 缺省就有这一行，这里被注释或删掉了）：Pandora 的 PostgreSQL 参数放进 $dir/conf.d/pandora.conf 也不会生效。把这一行恢复成 include_dir = 'conf.d' 后重跑本脚本（什么都还没改）"
}

# 写一份 0644 的文件：先写同目录的 .next（PostgreSQL 的 include_dir 只读 .conf 结尾的文件，写了一半的不会被读到）再改名
native_write_file() {
  install -d -m 0755 "${1%/*}"
  cat >"$1.next" && chmod 0644 "$1.next" && mv -f -- "$1.next" "$1"
}

# PostgreSQL 的调参文件与单元 drop-in 一起应用，只重启一次：调参文件与发布包的不同就整份覆盖；加固开关开着写
# drop-in（内容见 native_pg_dropin：资源约束一直在，隔离段随开关）。两样都没变就不重启。
# 重启后在线才算数；起不来就把两样都还原成这次之前的样子（之前有的写回、没有的删掉）、再起、再核在线：
# 在线返回 1（提示照实写），仍不在线就停下。安装器在外来集群检查、迁移、收窄角色都做完之后、起网关之前调它
# （升级时网关已停）
#   native_apply_pg_config <版本> <端口> <调参文件>
native_apply_pg_config() {
  local ver="$1" port="$2" src="$3" unit="postgresql@$1-main.service"
  local conf dropin had_conf=0 prev_conf="" had_dropin=0 prev_dropin="" conf_changed=0 dropin_changed=0
  local what budget hint
  conf="$(native_pg_tuning_file "$ver")"
  dropin="$(native_dropin_file "$unit")"
  [ -f "$src" ] || die "找不到 PostgreSQL 调参文件 $src"
  if [ -f "$conf" ]; then had_conf=1; prev_conf="$(cat "$conf")"; fi
  if [ -f "$dropin" ]; then had_dropin=1; prev_dropin="$(cat "$dropin")"; fi
  if [ "$had_conf" != 1 ] || ! cmp -s "$src" "$conf"; then
    native_write_file "$conf" <"$src" || die "写 $conf 失败"
    conf_changed=1
  fi
  if native_apply_dropin "$unit" "$(native_pg_dropin)"; then dropin_changed=1; fi
  [ "$conf_changed" = 1 ] || [ "$dropin_changed" = 1 ] || return 0
  what=""
  [ "$conf_changed" != 1 ] || what="调参 $conf"
  if [ "$dropin_changed" = 1 ]; then
    [ -z "$what" ] || what+="、"
    what+="drop-in $(native_dropin_file "$unit")"
  fi
  budget="$(native_pg_online_budget "$port")"
  systemctl daemon-reload
  systemctl restart "$unit" 2>/dev/null || true
  if native_wait_pg_online "$ver" "$budget"; then
    say "  PostgreSQL $ver/main：已应用$what，重启后在线"
    return 0
  fi
  if [ "$dropin_changed" = 1 ]; then hint="$(native_unit_failure_hint "$unit")"; else hint="原因看 journalctl -u $unit -n 50。"; fi
  [ "$conf_changed" != 1 ] || hint+="这次也换了 $conf：若是其中某个参数这台机器起不来，日志里的 FATAL 行会写出是哪个"
  if [ "$had_conf" = 1 ]; then
    printf '%s\n' "$prev_conf" | native_write_file "$conf" || die "PostgreSQL $ver/main 起不来，写回之前的 $conf 也失败"
  else
    rm -f -- "$conf"
  fi
  native_restore_dropin "$unit" "$had_dropin" "$prev_dropin"
  systemctl daemon-reload
  systemctl reset-failed "$unit" 2>/dev/null || true
  systemctl restart "$unit" 2>/dev/null || true
  if native_wait_pg_online "$ver" "$budget"; then
    say "  ! PostgreSQL $ver/main 应用$what后起不来；调参文件与 drop-in 已还原成这次之前的样子，核实已在线。$hint" >&2
    return 1
  fi
  die "PostgreSQL $ver/main 应用$what后起不来；调参文件与 drop-in 已还原成这次之前的样子，但 $budget 秒内仍没在线（journalctl -u $unit -n 50）。$hint"
}

# MemorySwapMax=0 要 cgroup v2 且开着 swap 记账才生效；cgroup v1 或没开记账的机器上 systemd 会忽略它（不报错）。
# 安装时看一眼，照实说，不停下
#   native_swap_accounting_note
NATIVE_CGROUP_ROOT=/sys/fs/cgroup
native_swap_accounting_note() {
  if [ -f "$NATIVE_CGROUP_ROOT/cgroup.controllers" ] && [ -e "$NATIVE_CGROUP_ROOT/system.slice/memory.swap.max" ]; then
    say "  面板的服务不换出到硬盘：PostgreSQL、Valkey 与三个网关都带 MemorySwapMax=0（cgroup v2，swap 记账已开）"
    return 0
  fi
  say "  ! 本机 cgroup 不是 v2 或没开 swap 记账：MemorySwapMax=0 被 systemd 忽略，PostgreSQL、Valkey 与网关仍可能被换出到交换区（不影响安装）"
}

# Valkey / Redis 起来并且在跑（Type=notify：restart 返回 0 表示已就绪；再隔一秒核一次没崩）
native_vk_restart_ok() { systemctl restart "$1.service" 2>/dev/null && sleep 1 && systemctl is-active --quiet "$1.service"; }

# 撤回之后再起一次并核实：在跑返回 1（提示照实写），仍没起来就停下
#   native_vk_after_revert <单元名> <改了什么> <原因与办法>
native_vk_after_revert() {
  systemctl daemon-reload
  systemctl reset-failed "$1.service" 2>/dev/null || true
  if native_vk_restart_ok "$1"; then
    say "  ! $1 改了$2之后起不来；这一步已还原，核实 $1 照原样在跑。$3" >&2
    return 1
  fi
  die "$1 改了$2之后起不来；这一步已还原，但 $1 仍没起来（journalctl -u $1.service -n 50）。$3"
}

# 改 Valkey 配置的一步：先把配置存一份，<命令…> 改了（返回 0）就重启核实；起不来把配置原样写回、再起、再核
#   native_vk_conf_step <单元名> <配置文件> <这一步叫什么> <命令…>
native_vk_conf_step() {
  local unit="$1" conf="$2" what="$3" snap hint
  shift 3
  snap="$(mktemp "$conf.pandora.XXXXXX")"   # 0600，里面有口令
  cat "$conf" >"$snap" || { rm -f -- "$snap"; die "存 $conf 的副本失败"; }
  if ! "$@"; then rm -f -- "$snap"; return 0; fi
  if native_vk_restart_ok "$unit"; then rm -f -- "$snap"; return 0; fi
  hint="$(native_unit_failure_hint "$unit.service")"
  cat "$snap" >"$conf" || die "$unit 改了$what之后起不来，写回 $conf 也失败（副本在 $snap）"
  rm -f -- "$snap"
  native_vk_after_revert "$unit" "$what（$conf）" "$hint"
}

# Valkey / Redis：口令、pandora 配置块、单元 drop-in 分三步，每步只在有变化时重启核实，起不来只撤回这一步
# （配置写回原样、drop-in 还原成之前的样子）再核实在跑：在跑返回 1，仍没起来停下
#   native_harden_valkey <单元名，如 valkey-server> <配置文件> <口令>
native_harden_valkey() {
  local unit="$1" conf="$2" file had=0 prev="" what hint
  if [ -f "$conf" ]; then
    native_vk_conf_step "$unit" "$conf" 口令 native_set_valkey_password "$conf" "$3" || return 1
    native_vk_conf_step "$unit" "$conf" "pandora 配置块" \
      native_set_valkey_hardening "$conf" "$(native_valkey_bind_addrs "$unit")" || return 1
  fi
  file="$(native_dropin_file "$unit.service")"
  if [ -f "$file" ]; then had=1; prev="$(cat "$file")"; fi
  native_apply_dropin "$unit.service" "$(native_valkey_dropin "${unit%-server}")" || return 0
  what="drop-in $file"
  systemctl daemon-reload
  native_vk_restart_ok "$unit" && return 0
  hint="$(native_unit_failure_hint "$unit.service")"
  native_restore_dropin "$unit.service" "$had" "$prev"
  native_vk_after_revert "$unit" "$what" "$hint"
}

# 服务端程序的口味与版本：打印「valkey|redis 主版本 次版本」（如 redis 6 0）；认不出返回 1
#   native_valkey_version <服务端程序名，如 valkey-server>
native_valkey_version() {
  local out flavor major minor
  out="$("$1" --version 2>/dev/null | head -1)" || true
  case "$out" in
    Valkey*) flavor=valkey ;;
    Redis*) flavor=redis ;;
    *) return 1 ;;
  esac
  major="$(sed -n 's/.* v=\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1/p' <<<"$out")"
  minor="$(sed -n 's/.* v=\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\2/p' <<<"$out")"
  [ -n "$major" ] && [ -n "$minor" ] || return 1
  printf '%s %s %s\n' "$flavor" "$major" "$minor"
}

# 最低版本：Redis 6.0（Ubuntu 22.04 自带的版本）；Valkey 任何版本（从 Redis 7.2 分出来）。
# 太老或认不出版本就停下：install.sh 在装完包之后、任何数据与配置改动之前调它
#   native_check_valkey_version <服务端程序名>
native_check_valkey_version() {
  local flavor="" major="" minor=""
  read -r flavor major minor <<<"$(native_valkey_version "$1" || true)"
  if [ -z "$flavor" ]; then
    die "认不出 $1 的版本（$1 --version：$("$1" --version 2>&1 | head -1)）。Pandora 要 Valkey（任何版本）或 Redis 6.0 及以上，停下（什么都还没改）"
  fi
  if [ "$flavor" = redis ] && [ "$major" -lt 6 ]; then
    die "$1 是 Redis $major.$minor，太老：Pandora 至少要 Redis 6.0（Ubuntu 22.04 自带的版本）或任何版本的 Valkey。换成 apt-get install valkey-server（或升级 Redis）后重跑（什么都还没改）"
  fi
  case "$flavor" in valkey) say "  $1：Valkey $major.$minor" ;; *) say "  $1：Redis $major.$minor" ;; esac
}

# bind 那一行按装的版本与 IPv6 写：Valkey 与 Redis 6.2 起认「-」前缀（地址不存在也照常起），写 127.0.0.1 -::1；
# 更老的（Ubuntu 22.04 的 Redis 6.0）不认，回环上有 IPv6 写 127.0.0.1 ::1（与它的缺省配置相同），没有只写 127.0.0.1。
# 认不出版本按老的算
#   native_valkey_bind_addrs <服务端程序名，如 valkey-server>
NATIVE_IF_INET6=/proc/net/if_inet6
native_valkey_bind_addrs() {
  local flavor="" major="" minor=""
  read -r flavor major minor <<<"$(native_valkey_version "$1" || true)"
  case "$flavor" in
    valkey) echo '127.0.0.1 -::1'; return 0 ;;
    redis)
      if [ "$major" -gt 6 ] || { [ "$major" -eq 6 ] && [ "$minor" -ge 2 ]; }; then
        echo '127.0.0.1 -::1'; return 0
      fi ;;
  esac
  if [ -r "$NATIVE_IF_INET6" ] && awk '$1 == "00000000000000000000000000000001" && $6 == "lo" { f = 1 } END { exit !f }' "$NATIVE_IF_INET6"; then
    echo '127.0.0.1 ::1'
  else
    echo '127.0.0.1'
  fi
}

# Valkey 配置末尾维护一个 pandora 块：只听回环、保护模式、禁 FLUSHALL / FLUSHDB、不落盘（会话与限流键丢了可以重建）、
# 内存上限与淘汰策略。块在文件最后，单值项以它为准；没变返回 1（不必重启），改了返回 0。
# 不含口令（口令走 native_set_valkey_password），整块经 awk 的环境给，不进命令行参数
#   native_set_valkey_hardening <配置文件> <bind 地址，见 native_valkey_bind_addrs>
# 块从以「# >>> pandora」开头的一行起、到「# <<< pandora」止：认开头不认整行，块头的说明文字改了也认得出旧块
NATIVE_VALKEY_BLOCK_BEGIN='# >>> pandora（install.sh 维护；手改会被下次安装覆盖）'
NATIVE_VALKEY_BLOCK_END='# <<< pandora'
native_valkey_hardening_block() {
  printf '%s\n' "$NATIVE_VALKEY_BLOCK_BEGIN" \
    "bind $1" \
    'protected-mode yes' \
    'rename-command FLUSHALL ""' \
    'rename-command FLUSHDB ""' \
    'save ""' \
    'appendonly no' \
    'maxmemory 96mb' \
    'maxmemory-policy allkeys-lru' \
    "$NATIVE_VALKEY_BLOCK_END"
}
native_set_valkey_hardening() {
  local conf="$1" tmp
  tmp="$(mktemp "$conf.XXXXXX")"
  NATIVE_VK_END="$NATIVE_VALKEY_BLOCK_END" NATIVE_VK_BLOCK="$(native_valkey_hardening_block "$2")" awk '
    index($0, "# >>> pandora") == 1 { skip = 1; next }
    skip && $0 == ENVIRON["NATIVE_VK_END"] { skip = 0; next }
    skip { next }
    { lines[++n] = $0 }
    END {
      while (n > 0 && lines[n] == "") n--
      for (i = 1; i <= n; i++) print lines[i]
      print ""
      print ENVIRON["NATIVE_VK_BLOCK"]
    }
  ' "$conf" >"$tmp" || { rm -f -- "$tmp"; die "改 $conf 的 pandora 配置块失败"; }
  if cmp -s "$tmp" "$conf"; then rm -f -- "$tmp"; return 1; fi
  cat "$tmp" >"$conf" || { rm -f -- "$tmp"; die "写回 $conf 失败"; }
  rm -f -- "$tmp"
  return 0
}

#------------------------------------------------------------------------------
# Valkey 口令与库编码（install-backup_mock_test.sh）
#------------------------------------------------------------------------------
# 系统 Valkey / Redis 的 requirepass 设成 <口令>：已经是它就不动、返回 1（不用重启，升级时网关还连着）；
# 改了返回 0。口令只经标准输入与环境给 grep / awk，不进任何命令行参数
#   native_set_valkey_password <配置文件> <口令>
native_set_valkey_password() {
  local conf="$1" tmp
  printf 'requirepass %s\n' "$2" | grep -qxF -f - "$conf" && return 1
  tmp="$(mktemp "$conf.XXXXXX")"
  NATIVE_VK_PASS="$2" awk '
    /^requirepass[[:space:]]/ { if (!done) print "requirepass " ENVIRON["NATIVE_VK_PASS"]; done = 1; next }
    { print }
    END { if (!done) { print ""; print "requirepass " ENVIRON["NATIVE_VK_PASS"] } }
  ' "$conf" >"$tmp" || { rm -f -- "$tmp"; die "改 $conf 的 requirepass 失败"; }
  # 原地覆盖内容（保留属主与权限：valkey 组要能读）
  cat "$tmp" >"$conf" || { rm -f -- "$tmp"; die "写回 $conf 失败"; }
  rm -f -- "$tmp"
  return 0
}

# 升级时看一眼 aegis 库的编码：更早的安装器用 LC_ALL=C 建集群，库可能是 SQL_ASCII（不校验编码，中文按字节存）。
# 只告警、给办法，不自动改库（换编码要导出重建，得停服、由人决定）
#   native_check_db_encoding <端口>
native_check_db_encoding() {
  local enc
  enc="$(native_pg_peer -p "$1" -d postgres -Atc "SELECT pg_catalog.pg_encoding_to_char(encoding) FROM pg_catalog.pg_database WHERE datname = 'aegis'" 2>/dev/null)" || return 0
  case "$enc" in
    SQL_ASCII)
      printf '\033[1;33m%s\033[0m\n' "  ! 库 aegis 的编码是 SQL_ASCII（更早的安装器用 C locale 建的集群）：不校验编码，中文按字节存，排序与大小写函数对中文无效。" >&2
      printf '%s\n' "    这次升级照常做、不改库；找个停服窗口按 deploy/MIGRATION-RUNBOOK.md「库是 SQL_ASCII」一节导出重建成 UTF8" >&2
      return 2 ;;
  esac
  return 0
}

#------------------------------------------------------------------------------
# 迁移（install-migrate-order_mock_test.sh）
#------------------------------------------------------------------------------
# 升级时一次性库预检（check-migrations.sh：整库克隆 + 在克隆上演练待执行迁移）耗时随库
# 大小线性增长（5k-r4 实测 95 MB 的库约 15 秒，占停服的 2/3）。所以升级分三段：
#   1. 停服之前跑完整预检，按停写口径在克隆上演练（PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER
#      只作用于没有写入者的克隆库，不是「线上写入者已停」的声明），通过后留一张凭据。
#      失败就返回，服务一个都没停；
#   2. 停服；
#   3. migrate.sh up 带凭据：只做只读核对（迁移目录摘要、源库水位、续费闸门、停写口径一致、
#      六小时内），再迁移。PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes 只在这一步、停服之后给。
#      失败就把服务拉回来（新程序还没装，起来的是原来的版本）。
# 停服窗口里只剩：停服、凭据核对（亚秒级）、正式迁移、之后的收窄角色、装程序、重启。
#
# 首装、或升级但库里还没有 goose 记录（全新库）：没有要保护的数据，跳过克隆预检，直接迁移。
# 升级且不是全新库：预检之前先 migrate.sh check-indexes，有 INVALID 索引（上次 CONCURRENTLY
# 失败的半成品，重跑会被 IF NOT EXISTS 跳过）就返回 10，服务不停。
#
#   pandora_run_migrations <install|upgrade> <fresh: yes|no> <deploy 目录> <.env> <migrations 目录> <goose>
# deploy 目录里要有 migrate.sh 与 check-migrations.sh。要停的服务是 SERVICES。迁移 DSN（postgres 超级用户）
# 由 migrate.sh 从 .env 读：不经这里的 env(1) 传，它带着口令，进命令行参数 ps 就看得见。
# 返回 0 成功；10 停服前索引检查或预检失败（服务没停）；11 迁移失败（服务已拉回）；
# 12 迁移失败（首装，没有服务可拉）。
pandora_run_migrations() {
  local mode="$1" fresh="$2" deploy="$3" env_file="$4" migrations="$5" goose="$6"
  local base_env=(env -i "PATH=$PATH" "HOME=${HOME:-/root}" "AEGIS_ENV_FILE=$env_file"
    "AEGIS_MIGRATIONS_DIR=$migrations" "GOOSE_BIN=$goose")
  local attest_dir="" attestation="" log rc started
  local migrate_extra=()

  log="$(mktemp "${TMPDIR:-/tmp}/pandora-migrate.XXXXXX")"
  if [ "$fresh" = yes ]; then
    migrate_extra+=("PANDORA_SKIP_PRECHECK_FRESH_DB=yes-empty-database")
    printf '    %s\n' "全新库，跳过一次性数据库预检"
  elif [ "$mode" = upgrade ]; then
    # 上次 CREATE INDEX CONCURRENTLY 失败留下的 INVALID 索引：停服之前就拦下（migrate.sh up 自己
    # 也会在执行前拒绝，但那时服务已经停了）。只读查询，输出里带清理命令。
    if ! "${base_env[@]}" PANDORA_LOCAL_MIGRATION_APPROVED=yes "$deploy/migrate.sh" check-indexes >"$log" 2>&1; then
      printf '    ---- 索引检查完整输出 ----\n' >&2
      sed 's/^/    /' "$log" >&2
      rm -f -- "$log"
      return 10
    fi
    attest_dir="$(mktemp -d "${TMPDIR:-/tmp}/pandora-precheck.XXXXXX")"
    attestation="$attest_dir/precheck.attestation"
    printf '    %s\n' "停服之前，先在一次性克隆库上演练这次要跑的迁移（库越大越慢，服务照常在跑）"
    started="$(date +%s)"
    if ! "${base_env[@]}" \
        PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER=yes \
        "PANDORA_PRECHECK_ATTESTATION_OUT=$attestation" \
        "$deploy/check-migrations.sh" >"$log" 2>&1 || [ ! -s "$attestation" ]; then
      printf '    ---- 预检完整输出 ----\n' >&2
      sed 's/^/    /' "$log" >&2
      rm -rf -- "$attest_dir" "$log"
      return 10
    fi
    printf '    %s\n' "预检通过（$(( $(date +%s) - started )) 秒），凭据已写好；停服后只核对凭据，不再演练"
    migrate_extra+=("PANDORA_PRECHECK_ATTESTATION=$attestation")
  fi

  if [ "$mode" = upgrade ]; then
    printf '    %s\n' "停止服务后迁移"
    systemctl stop "${SERVICES[@]}" 2>/dev/null || true
  fi

  # 写入者已停（升级：上面刚停；首装：服务还没装起来）——这份声明此刻才成立。
  # 具体的批准值固定在 migrate.sh 内部，这里只递交「已停止」这个事实。
  if "${base_env[@]}" \
      PANDORA_LOCAL_MIGRATION_APPROVED=yes \
      PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes \
      ${migrate_extra[@]+"${migrate_extra[@]}"} \
      "$deploy/migrate.sh" up >"$log" 2>&1; then
    tail -4 "$log" | sed 's/^/    /'
    rm -rf -- "$log" ${attest_dir:+"$attest_dir"}
    return 0
  fi
  rc=$?
  printf '    ---- 迁移完整输出（退出码 %s）----\n' "$rc" >&2
  sed 's/^/    /' "$log" >&2
  rm -rf -- "$log" ${attest_dir:+"$attest_dir"}
  if [ "$mode" = upgrade ]; then
    printf '    %s\n' "迁移失败，正在把服务拉回来" >&2
    systemctl start "${SERVICES[@]}" 2>/dev/null || true
    return 11
  fi
  return 12
}

#------------------------------------------------------------------------------
# 健康检查与首装时交互式建管理员（install-firstrun_mock_test.sh、install-chain_mock_test.sh）
#------------------------------------------------------------------------------
# 三个网关的 /healthz 都是 200（最多等一分钟）
native_gateways_healthy() {
  local _ p ok
  for _ in $(seq 1 30); do
    ok=1
    for p in 9000 9001 9003; do
      [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$p/healthz" 2>/dev/null)" = 200 ] || ok=0
    done
    [ "$ok" = 1 ] && return 0
    sleep 2
  done
  return 1
}

# 只在首装、且标准输入输出都是终端时问；无人值守（PANDORA_ASSUME_YES=1、
# PANDORA_NONINTERACTIVE=1、管道、CI）一律不问，由收尾提示给出手工命令。
pandora_admin_prompt_wanted() {
  [ "$1" = install ] || return 1
  [ "${PANDORA_ASSUME_YES:-}" != 1 ] && [ "${PANDORA_NONINTERACTIVE:-}" != 1 ] || return 1
  [ -t 0 ] && [ -t 1 ]
}

# 交互式建第一个管理员。调用方先把 .env 导出到环境（aegis-adminctl 经 platform/config 读配置）。
#   pandora_bootstrap_admin <aegis-adminctl>
# 密码只经 read -s 读进一个不导出的 shell 变量，再由内建 printf 经管道送到
# aegis-adminctl create --password-stdin 的标准输入：不进命令行参数、不进环境变量、
# 不回显、不写日志。邮箱打印出来没关系。
# 结果写到 PANDORA_ADMIN_STATE：created（建好了）、existing（已有管理员，没问）、
# manual（没建成，要手工建）。总是返回 0：建管理员失败不该让一个已经装好的面板报安装失败。
pandora_bootstrap_admin() {
  local adminctl="$1" email="" pw="" pw2="" attempt has_rc=0 create_rc out
  PANDORA_ADMIN_STATE=manual
  [ -x "$adminctl" ] || { printf '    %s\n' "找不到 $adminctl，管理员留给你手工建" >&2; return 0; }

  "$adminctl" has-admin >/dev/null 2>&1 || has_rc=$?
  case "$has_rc" in
    0) PANDORA_ADMIN_STATE=existing; printf '    %s\n' "库里已有可登录的管理员，不再新建"; return 0 ;;
    3) ;;
    *) printf '    %s\n' "查不到现有管理员（aegis-adminctl has-admin 退出码 $has_rc），管理员留给你手工建" >&2; return 0 ;;
  esac

  printf '    %s\n' "现在创建第一个管理员（直接回车跳过，之后再手工建）。"
  for attempt in 1 2 3; do
    printf '    管理员邮箱：'
    IFS= read -r email || email=""
    email="${email//[[:space:]]/}"
    [ -n "$email" ] || { printf '    %s\n' "跳过，之后手工建"; return 0; }
    if [[ ! "$email" =~ ^[^@]+@[^@]+\.[^@]+$ ]]; then
      printf '    %s\n' "邮箱格式不对：$email" >&2
      continue
    fi
    printf '    密码（不回显）：'
    IFS= read -r -s pw || pw=""
    printf '\n    再输一次：'
    IFS= read -r -s pw2 || pw2=""
    printf '\n'
    if [ -z "$pw" ] || [ "$pw" != "$pw2" ]; then
      pw=""; pw2=""
      printf '    %s\n' "两次密码不一致或为空，重来" >&2
      continue
    fi
    pw2=""
    # 管道最后一段是 aegis-adminctl，命令替换的退出码就是它的，与调用方开没开 pipefail 无关
    create_rc=0
    out="$(printf '%s\n' "$pw" | "$adminctl" create --email "$email" --password-stdin 2>&1)" || create_rc=$?
    pw=""
    printf '%s\n' "$out" | sed 's/^/    /'
    if [ "$create_rc" -eq 0 ]; then
      PANDORA_ADMIN_STATE=created
      PANDORA_ADMIN_EMAIL="$email"
      return 0
    fi
    printf '    %s\n' "没建成（原因见上，常见是密码强度不够），重来" >&2
  done
  printf '    %s\n' "三次都没建成，管理员留给你手工建" >&2
  return 0
}

#------------------------------------------------------------------------------
# HTTPS 边缘：要不要接管 nginx
#------------------------------------------------------------------------------
# 首装一律配（证书与 nginx 交给 edge-tls.sh setup）。升级时：已经在用（有 aegis.conf）就照常
# 重渲染、沿用证书；还没走过 nginx 边缘的面板（以前没申请证书、没渲染 nginx，可能另有自己的
# nginx 站点）不替人接管 80/443，除非显式 PANDORA_ACME=1（旧名 PANDORA_CERTBOT=1），或在终端里答 y。
#   pandora_edge_wanted <install|upgrade> <aegis.conf 路径>
pandora_edge_wanted() {
  local reply
  [ "$1" = install ] && return 0
  [ -f "$2" ] && return 0
  if [ "${PANDORA_ACME:-}" = 1 ] || [ "${PANDORA_CERTBOT:-}" = 1 ]; then return 0; fi
  if [ "${PANDORA_ASSUME_YES:-}" != 1 ] && [ -t 0 ]; then
    printf '    这台面板还没走 nginx 边缘。现在配 HTTPS（接管 80/443、申请 Let'"'"'s Encrypt 证书，即同意其订户协议）？[y/N] '
    read -r reply || reply=""
    case "$reply" in [yY]*) return 0 ;; esac
  fi
  return 1
}

#------------------------------------------------------------------------------
# 收尾提示（install-firstrun_mock_test.sh）
#------------------------------------------------------------------------------
# 按「现在可以做什么 → 还差什么 → 常用操作」三段写，首装与升级分开说，只留新手需要的。
# 后台地址只在首装时打印：升级没改它，也就不必再把入口写进一次终端记录。
# 用到的全局量：MODE、EDGE_STATE（trusted / selfsigned / not-enabled / bad-url / no-nginx / skipped / failed）、
# PANDORA_ADMIN_STATE（created / existing / manual）、PANDORA_ADMIN_EMAIL、MIGRATION_BEFORE / MIGRATION_AFTER
# （迁移版本）、BK（升级前备份）、BACKUP_TIMER（enabled / disabled）、INSTALL_DIR、SERVICES。
print_install_summary() {
  local d="$INSTALL_DIR/deploy" env_file base admin_path url todo=() item
  env_file="$d/.env"
  base="$(pandora_env_file_value "$env_file" AEGIS_PUBLIC_BASE_URL)"; base="${base%/}"
  admin_path="$(pandora_env_file_value "$env_file" AEGIS_ADMIN_PATH)"
  url="$base/$admin_path/"
  local show_url="sudo $d/admin-url.sh" edge="sudo $d/edge-tls.sh"
  local create_cmd="cd $INSTALL_DIR && set -a && . deploy/.env && set +a && read -rsp '密码：' p && echo && printf '%s\n' \"\$p\" | ./bin/aegis-adminctl create --email <你的邮箱> --password-stdin; unset p"

  # 还差什么：只列这台机器上确实还没做的
  case "${EDGE_STATE:-skipped}" in
    trusted) ;;
    selfsigned) todo+=("换上正规证书（现在是自签，浏览器提示不安全、节点用 https 接入会失败）：确认 80/tcp 从公网可达后 $edge issue；续期 timer 每天两次也会自动重试，成功即换上") ;;
    not-enabled) todo+=("切到 HTTPS（接管 nginx 80/443、申请 Let's Encrypt 证书，即同意其订户协议）：$edge setup") ;;
    bad-url) todo+=("把 $env_file 的 AEGIS_PUBLIC_BASE_URL 改成 https://域名 或 https://公网IPv4，systemctl restart ${SERVICES[*]}，再 $edge setup") ;;
    no-nginx) todo+=("装 nginx 才能从公网打开面板：apt-get install -y nginx 后 $edge setup") ;;
    failed) todo+=("HTTPS 边缘没配好（原因见上，nginx 保持原配置）：修好后 $edge setup") ;;
    *) todo+=("这次按 PANDORA_SKIP_NGINX=1 没碰 nginx；要从公网用 HTTPS 打开面板：$edge setup") ;;
  esac
  if [ "$MODE" = install ] && [ "${PANDORA_ADMIN_STATE:-manual}" = manual ]; then
    todo+=("创建管理员（下面这条整行复制；密码不回显，只经标准输入交给 aegis-adminctl）：" "    $create_cmd")
  fi
  if [ "${BACKUP_TIMER:-disabled}" != enabled ]; then
    todo+=("开每日加密备份：先立刻做一份 sudo $d/backup-postgres.sh，再 systemctl enable --now aegis-backup.timer" \
      "    解密私钥 $INSTALL_DIR/secrets/backup-age.key 与备份在同一台机器，另存一份到别处（机器整体丢失时备份才解得开）")
  fi
  [ "$MODE" != install ] \
    || todo+=("站点放在 Cloudflare 后面的话：sudo $d/update-cloudflare-realip.sh（默认不信任任何代理）")

  say ""
  if [ "$MODE" = install ]; then
    say "Pandora 安装完成"
    printf '%s\n' "  现在可以做什么："
    if [ "${EDGE_STATE:-}" = trusted ]; then
      printf '    %s\n' "打开管理后台：$url" "  （这个地址就是后台入口，别外传）"
    elif [ "${EDGE_STATE:-}" = selfsigned ]; then
      printf '    %s\n' "打开管理后台：$url" "  （这个地址就是后台入口，别外传；现在是自签证书，浏览器提示不安全时确认继续即可）"
    else
      printf '    %s\n' "面板已在本机跑起来（三个服务健康检查通过）；HTTPS 边缘配好后从这里打开管理后台：" "  $url"
    fi
    case "${PANDORA_ADMIN_STATE:-manual}" in
      created) printf '    %s\n' "用刚建的管理员 ${PANDORA_ADMIN_EMAIL:-} 登录，然后尽快绑定两步验证" ;;
      existing) printf '    %s\n' "用库里已有的管理员登录" ;;
    esac
  else
    say "Pandora 升级完成"
    printf '%s\n' "  现在可以做什么："
    printf '    %s\n' "面板已升级（迁移版本 ${MIGRATION_BEFORE:-?} → ${MIGRATION_AFTER:-?}），三个服务已重启并通过健康检查" \
      "管理后台地址没变（重看：$show_url）"
    case "${EDGE_STATE:-}" in
      trusted) printf '    %s\n' "HTTPS：Let's Encrypt 证书在用（查看：$edge status）" ;;
      selfsigned) printf '    %s\n' "HTTPS：在用自签证书（查看：$edge status）" ;;
    esac
    [ -z "${BK:-}" ] || printf '    %s\n' "升级前的数据库备份：$BK（未加密的 pg_dump，确认升级没问题后自己删）"
  fi

  printf '\n%s\n' "  还差什么："
  [ "${#todo[@]}" -gt 0 ] || printf '    %s\n' "没有了"
  for item in ${todo[@]+"${todo[@]}"}; do
    case "$item" in
      "    "*) printf '    %s\n' "$item" ;;
      *) printf '    · %s\n' "$item" ;;
    esac
  done

  printf '\n%s\n' "  常用操作："
  printf '    %s\n' \
    "重看后台地址  $show_url" \
    "查看状态      systemctl status ${SERVICES[*]}" \
    "查看日志      tail -f /var/log/aegis/public.log" \
    "重启          systemctl restart ${SERVICES[*]}" \
    "配置文件      $env_file" \
    "数据库        sudo $d/psql.sh" \
    "立刻备份      sudo $d/backup-postgres.sh" \
    "HTTPS 证书    $edge status" \
    "升级          新发布包放到 root 独占目录后：sudo <发布目录>/deploy/install.sh"
}
