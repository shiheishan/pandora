#!/usr/bin/env bash
# install-native.sh 的常量与函数：PostgreSQL 集群、.env 与加密备份、--from-docker。
# 只定义，不执行任何安装动作；由 install-native.sh 从发布目录 source（与 install-lib.sh 同样的用法），
# 不装到主机上。桩测试直接 source 它：install-native_pgcluster_mock_test.sh、
# install-native_backup_mock_test.sh、install-native_fromdocker_mock_test.sh。

# ── 常量 ──────────────────────────────────────────────
# 潘多拉专属安装目录。systemd 单元里原路径 /opt/aegispanel 会替换为这里
INSTALL_DIR="/opt/pandora"
# Pandora 只用这一个大版本的 main 集群（迁移用到 PG18 的 uuidv7() 等内建函数）
PANDORA_PG_MAJOR=18
SERVICES=(aegis-public aegis-admin aegis-node)

say(){ printf '\033[1;32m%s\033[0m\n' "$*"; }
die(){ printf '\033[1;31m%s\033[0m\n' "$*" >&2; exit 1; }
need(){ command -v "$1" >/dev/null 2>&1 || die "缺少 $1"; }

#------------------------------------------------------------------------------
# PostgreSQL 集群（install-native_pgcluster_mock_test.sh）
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
  for _ in $(seq 1 15); do
    native_pg_cluster_port "$ver" >/dev/null && return 0
    sleep 1
  done
  die "PostgreSQL ${ver}/main 起不来：journalctl -u postgresql@${ver}-main -n 50"
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
#   - 首装（或从 Docker 迁）而 PG18 里还没有 aegis 库：数据多半就在旧集群里，停下；
#   - 升级而 .env 的 POSTGRES_PORT 指着旧集群：这台面板一直跑在旧集群上，停下；
#   - 其余（PG18 已有 aegis 库、.env 指着 PG18）：旧集群是早先的遗留，只提示。
# 查不了旧集群按「有 aegis 库」算。升级时 .env 的 POSTGRES_PORT 不是 PG18 的端口也停下。
# 停下时什么都还没改。
#   native_check_foreign_clusters <PG 大版本> <它的端口> <install|upgrade|from-docker> <.env 的 POSTGRES_PORT，仅升级>
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
# .env 与加密备份。install-native_backup_mock_test.sh 测这几个函数
#------------------------------------------------------------------------------
# 直装的加密备份目录（升级前备份、HTTPS 边缘的备份也在这里）；备份单元的 ReadWritePaths 换成它
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
    printf '\n# install-native.sh 升级时补上的新键（原有的行没动）\n'
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

# 直装 .env 里与布局、加密备份、节点端分发目录有关的键（首装写进去，升级缺了才追加）。recipient 为空时不出那一行
#   native_layout_env_lines <安装目录> <age recipient>
native_layout_env_lines() {
  printf '%s\n' "PANDORA_DB_LAYOUT=native" "AEGIS_BACKUP_DIR=$NATIVE_BACKUP_DIR" "AEGIS_BACKUP_RETENTION_DAYS=14"
  [ -z "$2" ] || printf '%s\n' "AEGIS_BACKUP_AGE_RECIPIENT=$2"
  printf '%s\n' "AEGIS_BACKUP_AGE_IDENTITY=$1/secrets/backup-age.key" "AEGIS_BACKUP_WEBDAV_BIN=$1/bin/aegis-backup-webdav"
  # 节点一键安装分发的二进制目录：网关的缺省值是 docker 布局的 /opt/aegispanel/pdnd-dist
  printf '%s\n' "PANDORA_PDND_DIST_DIR=$1/pdnd-dist"
}

# 发布包里的单元（按 docker 布局写）改成直装：安装目录、备份目录，去掉对 docker 的依赖
#   native_render_unit <单元文件> <安装目录>
native_render_unit() {
  sed -e "s|/opt/aegispanel|$2|g" \
      -e "s|/var/backups/aegispanel|$NATIVE_BACKUP_DIR|g" \
      -e '/^Requires=docker\.service$/d' \
      -e 's|^After=docker\.service$|After=postgresql.service|' \
      "$1"
}

#------------------------------------------------------------------------------
# Valkey 口令与库编码（install-native_backup_mock_test.sh）
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

# 升级时看一眼 aegis 库的编码：更早的直装用 LC_ALL=C 建集群，库可能是 SQL_ASCII（不校验编码，中文按字节存）。
# 只告警、给办法，不自动改库（换编码要导出重建，得停服、由人决定）
#   native_check_db_encoding <端口>
native_check_db_encoding() {
  local enc
  enc="$(native_pg_peer -p "$1" -d postgres -Atc "SELECT pg_catalog.pg_encoding_to_char(encoding) FROM pg_catalog.pg_database WHERE datname = 'aegis'" 2>/dev/null)" || return 0
  case "$enc" in
    SQL_ASCII)
      printf '\033[1;33m%s\033[0m\n' "  ! 库 aegis 的编码是 SQL_ASCII（更早的直装用 C locale 建的集群）：不校验编码，中文按字节存，排序与大小写函数对中文无效。" >&2
      printf '%s\n' "    这次升级照常做、不改库；找个停服窗口按 deploy/MIGRATION-RUNBOOK.md「直装库是 SQL_ASCII」一节导出重建成 UTF8" >&2
      return 2 ;;
  esac
  return 0
}

# 只换一个库里的对象属主：REASSIGN OWNED 会顺带改集群级对象（别的库）的属主，所以先记下这个角色名下的
# 别的库，换完原样改回；目标库本身给 <库属主>。以 postgres 经本地 socket 跑
#   native_reassign_in_db <端口> <库> <原角色> <新角色> <库属主>
native_reassign_in_db() {
  local port="$1" db="$2" from="$3" to="$4" owner="$5" others o
  others="$(native_pg_peer -p "$port" -d postgres -Atc "SELECT datname FROM pg_catalog.pg_database WHERE datdba = (SELECT oid FROM pg_catalog.pg_roles WHERE rolname = '$from') AND datname <> '$db'")" || return 1
  native_pg_peer -p "$port" -d "$db" -c "REASSIGN OWNED BY \"$from\" TO \"$to\"" -c "ALTER DATABASE \"$db\" OWNER TO \"$owner\"" >/dev/null || return 1
  while IFS= read -r o; do
    [ -n "$o" ] || continue
    native_pg_peer -p "$port" -d postgres -c "ALTER DATABASE \"$o\" OWNER TO \"$from\"" >/dev/null || return 1
  done <<<"$others"
}

#------------------------------------------------------------------------------
# --from-docker：把 docker 布局（install.sh，/opt/aegispanel）迁到直装。
# 备份 → 装直装 → 恢复 → 核对 → 切换 → 停 Docker 容器；删卷不做，只打印命令。任何一步失败（切换之前）
# 都把 docker 布局的服务拉回来，docker 那边的库与卷全程只读。install-native_fromdocker_mock_test.sh 测这些函数
#------------------------------------------------------------------------------
DOCKER_DIR=/opt/aegispanel
# 切换时会被直装覆盖的单元（同名）：先存一份，失败时原样放回
FD_UNITS=(aegis-public.service aegis-admin.service aegis-node.service aegis-health.service aegis-health.timer
  aegis-backup.service aegis-backup.timer aegis-tls-renew.service aegis-tls-renew.timer)
FD_STATE_FILE=""            # $INSTALL_DIR/deploy/from-docker.state
FD_WRITERS_STOPPED=0        # docker 布局的写入者已被本脚本停下
FD_UNITS_SWAPPED=0          # 单元已换成直装的
FD_CUTOVER=0                # 直装已接管（健康检查通过）
FD_BACKUP_TIMER_WAS_ACTIVE=0
FD_UNITS_BACKUP=""
FD_SYSTEMD_DIR=/etc/systemd/system

# 状态文件：重跑时据此判断能不能重来（切换之前的任何一步失败都能）
fd_state_get() {
  [ -f "$FD_STATE_FILE" ] || return 0
  awk -F= -v k="$1" '$1 == k { sub(/^[^=]*=/, ""); print; exit }' "$FD_STATE_FILE"
}
fd_state_set() {
  local tmp line key
  tmp="$(mktemp "$FD_STATE_FILE.XXXXXX")"
  [ ! -f "$FD_STATE_FILE" ] || cat "$FD_STATE_FILE" >"$tmp"
  for line in "$@"; do
    key="${line%%=*}"
    grep -v "^${key}=" "$tmp" >"$tmp.n" || true
    printf '%s\n' "$line" >>"$tmp.n"
    mv -f -- "$tmp.n" "$tmp"
  done
  chmod 0600 "$tmp"
  mv -f -- "$tmp" "$FD_STATE_FILE"
  fd_log "  状态：$*"
}

# docker 布局的库：容器 aegis-postgres 里的客户端，以 docker .env 的 POSTGRES_USER（容器里的超级用户）连；
# 口令按名字经 -e PGPASSWORD 透传，不进命令行参数
fd_docker_pg() {
  local tool="$1"; shift
  PGPASSWORD="$FD_DOCKER_PG_PASSWORD" docker exec -i -e PGPASSWORD aegis-postgres "$tool" -U "$FD_DOCKER_PG_USER" "$@"
}

# 由 docker 布局的 .env 改写出直装的 .env（打印到标准输出）。应用密钥（主密钥、JWT、签名种子、后台前缀、
# 对外地址与其余设置）原样沿用：换了它们就解不开信封加密的字段、全部会话失效、节点验签失败。
# 只换连库连缓存的几项、加 postgres 超级用户口令与布局，路径里的 /opt/aegispanel、/var/backups/aegispanel
# 换成直装的；install.sh 留下的「网关经 unix socket」说明注释去掉（直装不经 docker 挂载）。
# 用到的全局量：INSTALL_DIR NATIVE_BACKUP_DIR PG_PORT VK_PORT PG_SUPER_PASS DB_PASS APP_PASS VK_PASS
# 口令经 awk 的环境（ENVIRON）给，不用 -v：-v 的值在 awk 的命令行参数里，ps 看得见
fd_render_env() {
  FD_ENV_SUPER="$PG_SUPER_PASS" FD_ENV_OWNER="$DB_PASS" FD_ENV_APP="$APP_PASS" FD_ENV_VK="$VK_PASS" \
  awk -v inst="$INSTALL_DIR" -v bdir="$NATIVE_BACKUP_DIR" -v pg_port="$PG_PORT" -v vk_port="$VK_PORT" '
    BEGIN {
      super = ENVIRON["FD_ENV_SUPER"]; owner = ENVIRON["FD_ENV_OWNER"]; app = ENVIRON["FD_ENV_APP"]; vk = ENVIRON["FD_ENV_VK"]
      order = "POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB POSTGRES_PORT POSTGRES_SUPER_PASSWORD AEGIS_MIGRATION_DATABASE_URL AEGIS_DB_APP_PASSWORD VALKEY_PASSWORD VALKEY_PORT AEGIS_DATABASE_URL AEGIS_REDIS_URL PANDORA_DB_LAYOUT"
      repl["POSTGRES_USER"] = "aegis"; repl["POSTGRES_PASSWORD"] = owner; repl["POSTGRES_DB"] = "aegis"
      repl["POSTGRES_PORT"] = pg_port; repl["POSTGRES_SUPER_PASSWORD"] = super
      repl["AEGIS_MIGRATION_DATABASE_URL"] = "postgres://postgres:" super "@127.0.0.1:" pg_port "/aegis?sslmode=disable"
      repl["AEGIS_DB_APP_PASSWORD"] = app; repl["VALKEY_PASSWORD"] = vk; repl["VALKEY_PORT"] = vk_port
      repl["AEGIS_DATABASE_URL"] = "postgres://aegis_app:" app "@127.0.0.1:" pg_port "/aegis?sslmode=disable"
      repl["AEGIS_REDIS_URL"] = "redis://:" vk "@127.0.0.1:" vk_port "/0"
      repl["PANDORA_DB_LAYOUT"] = "native"
      print "# Pandora Panel 配置：install-native.sh --from-docker 由 docker 布局的 .env 改写（应用密钥原样沿用）"
    }
    /^# 网关经 unix socket 连 PG 与 Valkey/ || /^# 回退到不带 run\/ 挂载的旧发布包之前/ { next }
    /^#   postgres:\/\/aegis_app:</ || /^#   redis:\/\/:</ { next }
    match($0, /^[A-Za-z_][A-Za-z0-9_]*=/) {
      key = substr($0, 1, RLENGTH - 1)
      if (key in repl) {
        if (!(key in seen)) print key "=" repl[key]
        seen[key] = 1
        next
      }
    }
    { gsub("/opt/aegispanel", inst); gsub("/var/backups/aegispanel", bdir); print }
    END {
      n = split(order, keys, " ")
      for (i = 1; i <= n; i++) if (!(keys[i] in seen)) print keys[i] "=" repl[keys[i]]
    }
  ' "$1"
}

# 库的指纹：迁移水位、每个模式与对象的属主（跑迁移的超级用户记作 <m>：docker 那边是 POSTGRES_USER，
# 直装是 postgres）、行级安全开关、表与列与函数的权限、每张表的行数与内容摘要、序列当前值、策略、触发器、
# 扩展。两边逐行相同才切换。超级用户不受行级安全限制，数到、摘到的是全部行。
# 内容摘要：每行的文本形式取 md5，前后两半各当一个 64 位整数，分别求和（numeric，不溢出），连同行数输出。
# 与行的顺序无关、内存固定：不受 1GB 单值上限和容器内存限制（以前用 string_agg 拼串，约 3300 万行就超限）。
# 两边的行集合（含重复行）一样，摘要就一样。时区、日期与浮点的输出格式先钉死，同一行在两边打印出来一字不差。
# 分区表的父表只数行，内容由各分区摘。耗时与整库扫一遍相当（每行一次 md5）：约 100 MB 的库几秒；
# 千万行级的大表（如 node_user_traffic_hourly）估半分钟到一分钟（估计，以测试机实测为准）。只在迁移时跑一次
fd_fingerprint_sql() {
  cat <<'SQL'
\set ON_ERROR_STOP on
SET TimeZone = 'UTC';
SET DateStyle = 'ISO, YMD';
SET IntervalStyle = 'postgres';
SET extra_float_digits = 1;
SET bytea_output = 'hex';
SELECT 'goose ' || coalesce(max(version_id) FILTER (WHERE is_applied), 0) FROM public.goose_db_version;
SELECT 'ext ' || extname || ' ' || extversion FROM pg_catalog.pg_extension ORDER BY 1;
SELECT 'schema ' || n.nspname || ' ' || CASE WHEN r.rolname = :'migrator' THEN '<m>' ELSE r.rolname END
  FROM pg_catalog.pg_namespace n JOIN pg_catalog.pg_roles r ON r.oid = n.nspowner
 WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' ORDER BY 1;
SELECT 'rel ' || n.nspname || '.' || c.relname || ' ' || c.relkind || ' '
       || CASE WHEN r.rolname = :'migrator' THEN '<m>' ELSE r.rolname END
       || ' rls=' || c.relrowsecurity || '/' || c.relforcerowsecurity
       || ' acl=' || coalesce(regexp_replace(c.relacl::text, '(^|[{,/])' || :'migrator' || '([=/,}])', '\1<m>\2', 'g'), '-')
       || CASE WHEN c.relkind IN ('r', 'p') THEN ' rows=' || (xpath('/row/c/text()',
            query_to_xml(format('SELECT count(*) AS c FROM %I.%I', n.nspname, c.relname), false, true, '')))[1]::text
          ELSE '' END
       || CASE WHEN c.relkind = 'r' THEN ' sum=' || (xpath('/row/c/text()',
            query_to_xml(format('SELECT count(*) || '':'' || coalesce(sum((''x'' || left(h, 16))::bit(64)::bigint::numeric), 0) || '':'' || coalesce(sum((''x'' || right(h, 16))::bit(64)::bigint::numeric), 0) AS c FROM (SELECT md5(t::text) AS h FROM %I.%I t) s',
              n.nspname, c.relname), false, true, '')))[1]::text
          ELSE '' END
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_catalog.pg_roles r ON r.oid = c.relowner
 WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
 ORDER BY 1;
SELECT 'col ' || n.nspname || '.' || c.relname || '.' || a.attname || ' '
       || regexp_replace(a.attacl::text, '(^|[{,/])' || :'migrator' || '([=/,}])', '\1<m>\2', 'g')
  FROM pg_catalog.pg_attribute a
  JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE a.attacl IS NOT NULL AND n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' ORDER BY 1;
SELECT 'fn ' || n.nspname || '.' || p.proname || '(' || pg_catalog.pg_get_function_identity_arguments(p.oid) || ') '
       || CASE WHEN r.rolname = :'migrator' THEN '<m>' ELSE r.rolname END || ' definer=' || p.prosecdef
       || ' acl=' || coalesce(regexp_replace(p.proacl::text, '(^|[{,/])' || :'migrator' || '([=/,}])', '\1<m>\2', 'g'), '-')
  FROM pg_catalog.pg_proc p
  JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
  JOIN pg_catalog.pg_roles r ON r.oid = p.proowner
 WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' ORDER BY 1;
SELECT 'seq ' || schemaname || '.' || sequencename || ' ' || coalesce(last_value::text, '-')
  FROM pg_catalog.pg_sequences WHERE schemaname !~ '^pg_' ORDER BY 1;
SELECT 'policy ' || schemaname || '.' || tablename || '.' || policyname || ' ' || cmd || ' ' || permissive
       || ' ' || array_to_string(roles, ',')
  FROM pg_catalog.pg_policies ORDER BY 1;
SELECT 'trigger ' || n.nspname || '.' || c.relname || '.' || t.tgname || ' ' || t.tgenabled
  FROM pg_catalog.pg_trigger t
  JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE NOT t.tgisinternal ORDER BY 1;
SQL
}
# fd_fingerprint <docker|native>：打印该侧的库指纹
fd_fingerprint() {
  case "$1" in
    docker) fd_fingerprint_sql | fd_docker_pg psql -X -q -At -d "$FD_DOCKER_PG_DB" -v migrator="$FD_DOCKER_PG_USER" ;;
    native) fd_fingerprint_sql | native_pg_peer -p "$PG_PORT" -d aegis -At -v migrator=postgres ;;
  esac
}

# docker 那边的非系统角色在直装集群里补齐（属性照搬；跑迁移的 POSTGRES_USER 本身对应直装的 postgres，不搬）。
# 角色属性里有 SUPERUSER / REPLICATION / BYPASSRLS、或有角色成员关系的，不替人决定，停下
fd_roles_plan() {
  fd_docker_pg psql -X -q -At -F ' ' -d "$FD_DOCKER_PG_DB" -v ON_ERROR_STOP=1 -c "
    SELECT rolname, rolsuper, rolreplication, rolbypassrls, rolcanlogin, rolinherit, rolcreatedb, rolcreaterole
      FROM pg_catalog.pg_roles
     WHERE rolname !~ '^pg_' AND rolname NOT IN (current_user, 'postgres') ORDER BY 1"
}
fd_roles_memberships() {
  fd_docker_pg psql -X -q -At -d "$FD_DOCKER_PG_DB" -v ON_ERROR_STOP=1 -c "
    SELECT r.rolname || ' -> ' || m.rolname FROM pg_catalog.pg_auth_members a
      JOIN pg_catalog.pg_roles r ON r.oid = a.roleid JOIN pg_catalog.pg_roles m ON m.oid = a.member
     WHERE r.rolname !~ '^pg_' AND m.rolname !~ '^pg_' ORDER BY 1"
}
# 读 fd_roles_plan 的输出，打印要在直装集群里执行的 CREATE ROLE（已有的跳过由 SQL 里的 NOT EXISTS 管）；
# 有不该搬的特权角色时返回 1
fd_roles_sql() {
  local name super repl bypass login inherit createdb createrole flags
  while read -r name super repl bypass login inherit createdb createrole; do
    [ -n "$name" ] || continue
    [[ "$name" =~ ^[a-z_][a-z0-9_]*$ ]] || { echo "role name not supported: $name" >&2; return 1; }
    if [ "$super" = t ] || [ "$repl" = t ] || [ "$bypass" = t ]; then
      echo "privileged role: $name" >&2; return 1
    fi
    flags="NOSUPERUSER NOREPLICATION NOBYPASSRLS"
    [ "$login" = t ] && flags+=" LOGIN" || flags+=" NOLOGIN"
    [ "$inherit" = t ] && flags+=" INHERIT" || flags+=" NOINHERIT"
    [ "$createdb" = t ] && flags+=" CREATEDB" || flags+=" NOCREATEDB"
    [ "$createrole" = t ] && flags+=" CREATEROLE" || flags+=" NOCREATEROLE"
    printf "SELECT 'CREATE ROLE %s %s' WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = '%s') \\\\gexec\n" \
      "$name" "$flags" "$name"
  done
}

# 停 / 起 docker 布局的写入者（三个网关；备份 timer 在跑就一起停，回滚或切换后照原样起）
fd_stop_docker_writers() {
  if systemctl is-active --quiet aegis-backup.timer 2>/dev/null; then FD_BACKUP_TIMER_WAS_ACTIVE=1; fi
  systemctl stop aegis-backup.timer aegis-backup.service 2>/dev/null || true
  FD_WRITERS_STOPPED=1
  systemctl stop "${SERVICES[@]}" || die "停不下 docker 布局的网关（systemctl stop ${SERVICES[*]}）"
}
fd_start_old_writers() {
  systemctl start "${SERVICES[@]}" 2>/dev/null || fd_log "  ! 网关没能全部起来：systemctl status ${SERVICES[*]}"
  [ "$FD_BACKUP_TIMER_WAS_ACTIVE" != 1 ] || systemctl start aegis-backup.timer 2>/dev/null \
    || fd_log "  ! 每日备份 timer 没能起来：systemctl start aegis-backup.timer"
}

# 迁移日志：每一行同时写日志文件（与状态文件同目录，0600）和标准错误。终端断了（ssh 掉线时写标准错误
# 返回 EIO）也照样落到日志里，调用方不会因为写失败而中断
fd_log() {
  local log="${FD_LOG:-}"
  [ -n "$log" ] || { [ -n "$FD_STATE_FILE" ] && log="$(dirname -- "$FD_STATE_FILE")/from-docker.log"; }
  if [ -n "$log" ] && [ -d "$(dirname -- "$log")" ]; then
    { (umask 077 && printf '%s %s\n' "$(date '+%F %T')" "$*" >>"$log"); } 2>/dev/null || true
  fi
  { printf '%s\n' "$*" >&2; } 2>/dev/null || true
}

# 切换前把会被覆盖的同名单元存一份；失败时原样放回（之前没有的删掉）
# 眼下的单元已经指向直装目录（上次硬中断：SIGKILL、断电，换了单元却没来得及放回），就沿用状态文件里第一次
# 存的原件，不拿直装单元当原件；没有记录就停下。眼下的单元还是 docker 的，就重新存一份：中间可能按 docker
# 布局升级过（单元更新了），再回滚要放回的是现在这份
fd_save_units() {
  local u prev swapped=0
  for u in "${FD_UNITS[@]}"; do
    if [ -f "$FD_SYSTEMD_DIR/$u" ] && grep -qF "$INSTALL_DIR/" "$FD_SYSTEMD_DIR/$u"; then swapped=1; fi
  done
  if [ "$swapped" = 1 ]; then
    prev="$(fd_state_get units_backup)"
    if [ -n "$prev" ] && [ -d "$prev" ]; then
      FD_UNITS_BACKUP="$prev"
      fd_log "  眼下的单元已是直装的（上次中断了），沿用上次存下的原单元：$prev"
      return 0
    fi
    die "$FD_SYSTEMD_DIR 里的单元已经指向直装目录 $INSTALL_DIR，又没有存下的 docker 原单元，不覆盖；从 docker 布局的发布包重装单元后再迁"
  fi
  install -d -m 0700 "$FD_UNITS_BACKUP"
  for u in "${FD_UNITS[@]}"; do
    if [ -f "$FD_SYSTEMD_DIR/$u" ]; then cp -a "$FD_SYSTEMD_DIR/$u" "$FD_UNITS_BACKUP/$u"; else : >"$FD_UNITS_BACKUP/.absent.$u"; fi
  done
}
fd_restore_units() {
  local u rc=0
  for u in "${FD_UNITS[@]}"; do
    if [ -f "$FD_UNITS_BACKUP/$u" ]; then
      cp -a "$FD_UNITS_BACKUP/$u" "$FD_SYSTEMD_DIR/$u" || rc=1
    elif [ -f "$FD_UNITS_BACKUP/.absent.$u" ]; then
      rm -f "$FD_SYSTEMD_DIR/$u" || rc=1
    fi
  done
  systemctl daemon-reload || rc=1
  return "$rc"
}

# 三个网关的 /healthz 都是 200（最多等一分钟）
fd_gateways_healthy() {
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

# 退出时（EXIT trap）：docker 的写入者已停、直装还没接管，就把 docker 布局拉回来。直装这边的库留着，
# 重跑 --from-docker 时改名放一边再重来；docker 那边的库与卷一直没动过
# 回滚本身不能半途而废：关掉 errexit（终端断了写标准错误会失败）、屏蔽 HUP/INT/TERM（再来一个信号不打断），
# 每一步的结果写进日志文件。它是 EXIT trap：信号处理里的 exit 到这里时 $? 已经复位，退出码取信号 trap
# 记下的 FD_SIGNAL_RC；最后 exit（不是 return），脚本的退出码就是它
fd_abort() {
  local rc=$?
  set +e
  trap '' HUP INT TERM
  [ "$rc" -ne 0 ] || rc="${FD_SIGNAL_RC:-0}"
  [ "$FD_WRITERS_STOPPED" = 1 ] && [ "$FD_CUTOVER" != 1 ] || exit "$rc"
  fd_log "从 Docker 迁移没完成（退出码 $rc），正在把 docker 布局的服务拉回来"
  if [ "$FD_UNITS_SWAPPED" = 1 ]; then
    systemctl stop "${SERVICES[@]}" 2>/dev/null
    if fd_restore_units; then
      fd_log "  原来的 systemd 单元已放回（$FD_UNITS_BACKUP）"
    else
      fd_log "  ! 单元没能全部放回，手工从 $FD_UNITS_BACKUP 拷回 /etc/systemd/system/ 后 systemctl daemon-reload"
    fi
  fi
  fd_start_old_writers
  [ -z "$FD_STATE_FILE" ] || [ ! -d "$(dirname -- "$FD_STATE_FILE")" ] || fd_state_set state=rolled-back
  fd_log "  docker 布局已恢复服务（数据没动）。直装这边的库 aegis 留着，修好原因后重跑 --from-docker 会把它改名放一边再来"
  [ "$rc" -ne 0 ] || rc=1
  exit "$rc"
}

# 直装接管之后：停 docker 的两个容器（restart: unless-stopped，手工停下后重启也不会自己起来），
# 备份 timer 之前在跑就照样起。删卷、停 docker 守护进程都不做，收尾打印命令
fd_finalize() {
  if ( cd "$DOCKER_DIR/deploy" && docker compose stop ) >/dev/null 2>&1 \
     || docker stop aegis-postgres aegis-valkey >/dev/null 2>&1; then
    say "  Docker 容器 aegis-postgres、aegis-valkey 已停（卷还在）"
  else
    printf '%s\n' "  ! 没能停下 Docker 容器：cd $DOCKER_DIR/deploy && docker compose stop" >&2
  fi
  # docker 布局的 .env 改名（不删）：之后旧布局的工具（install.sh 升级、/opt/aegispanel/deploy 下的脚本）
  # 一律因为找不到 .env 而停下，不会再把服务悄悄切回 docker 那份旧库。退回 Docker 时先改回来
  if [ -f "$DOCKER_DIR/deploy/.env" ]; then
    local parked="$DOCKER_DIR/deploy/.env.migrated-to-native"
    [ ! -e "$parked" ] || parked="$parked.$(date +%Y%m%d%H%M%S)"
    if mv -- "$DOCKER_DIR/deploy/.env" "$parked"; then
      fd_log "  docker 布局的 .env 改名为 $parked（旧布局的工具从此找不到它）"
    else
      fd_log "  ! 没能把 $DOCKER_DIR/deploy/.env 改名，手工 mv 成 .env.migrated-to-native，免得再按 docker 布局升级"
    fi
  fi
  [ "$FD_BACKUP_TIMER_WAS_ACTIVE" != 1 ] || systemctl start aegis-backup.timer 2>/dev/null \
    || fd_log "  ! 每日备份 timer 之前在跑，现在没起来：systemctl enable --now aegis-backup.timer"
  fd_state_set state=done
}

# 普通模式（不带 --from-docker）动手之前：docker 布局还在服务就不碰。以前直接跑会按首装新建口令与空库、
# 盖掉 docker 的单元；--from-docker 回滚后再跑会在回滚留下的副本上「升级」；照 RUNBOOK 退回 Docker 之后
# （直装 .env 改名、docker 的改回），状态文件若还记着 done，会把这台当首装、换掉主密钥。
#   native_plain_mode_guard <docker 布局目录> <直装目录> <from-docker.state 里的 state>
native_plain_mode_guard() {
  local docker_env="$1/deploy/.env" native_env="$2/deploy/.env"
  case "$3" in
    done)
      # 迁完的直装：直装 .env 在、docker 的已改名。别的组合都不是迁完的样子
      [ -f "$native_env" ] && [ ! -f "$docker_env" ] && return 0
      if [ -f "$docker_env" ] && [ ! -f "$native_env" ]; then
        die "这台从 Docker 迁到过直装、之后又退回了 Docker（直装的 .env 不在，$2/deploy/from-docker.state 还记着 done）：现在服务的是 docker 布局。照旧升级用 PANDORA_LAYOUT=docker install.sh；想再迁到直装，先把 from-docker.state 改名为 from-docker.state.retired，再跑 install-native.sh --from-docker"
      fi
      die "记录说已经从 Docker 迁完（done），可直装的 .env $([ -f "$native_env" ] && echo 在 || echo 不在)、docker 布局的 .env $([ -f "$docker_env" ] && echo 也在 || echo 不在)，不像迁完的样子，停下：确认哪套在服务后把不用的那套的 deploy/.env 改名（不删）"
      ;;
    cutover) die "上次从 Docker 迁到直装已切换、还没收尾：先 sudo bash install-native.sh --from-docker 收尾" ;;
  esac
  if [ ! -f "$docker_env" ]; then
    [ -n "$3" ] || return 0
    die "有从 Docker 迁移的记录（$3），却找不到 docker 布局的 $docker_env，不知道数据在哪，停下"
  fi
  if [ -n "$3" ]; then
    die "上次从 Docker 迁到直装没完成（状态 $3），现在服务的还是 docker 布局（$1）：继续迁移跑 install-native.sh --from-docker；照旧升级 docker 布局用 PANDORA_LAYOUT=docker install.sh"
  fi
  die "这台装着 docker 布局（$1）：迁到直装跑 install-native.sh --from-docker（停服几分钟）；照旧升级 docker 布局用 install.sh"
}

# 动手之前核对 docker 布局这一侧（只读）：容器在跑、库连得上、有迁移记录且不比发布包新、角色能搬、
# .env 里的口令与密钥齐全、磁盘够（库要导出一份、恢复一份、迁移预检再克隆一份）。
# 结果放在 FD_DOCKER_ENV / FD_DOCKER_PG_USER / FD_DOCKER_PG_PASSWORD / FD_DOCKER_PG_DB
fd_preflight() {
  local env="$DOCKER_DIR/deploy/.env" key value src max_release plan memberships size_kb
  local pg_path bk_path pg_avail bk_avail
  FD_DOCKER_ENV="$env"
  command -v docker >/dev/null 2>&1 || die "--from-docker 要 docker 命令，这台机器上没有"
  [ -f "$env" ] || die "--from-docker：找不到 docker 布局的 $env（这台不是 install.sh 装的？）"
  FD_DOCKER_PG_USER="$(pandora_env_file_value "$env" POSTGRES_USER)"
  FD_DOCKER_PG_PASSWORD="$(pandora_env_file_value "$env" POSTGRES_PASSWORD)"
  FD_DOCKER_PG_DB="$(pandora_env_file_value "$env" POSTGRES_DB)"
  [[ "$FD_DOCKER_PG_USER" =~ ^[a-z_][a-z0-9_]*$ && "$FD_DOCKER_PG_DB" =~ ^[a-z_][a-z0-9_]*$ ]] \
    || die "docker 布局的 .env 里 POSTGRES_USER / POSTGRES_DB 不是简单的标识符，不替你搬"
  # 口令会进直装的连接串与 SQL 字面量：只认安装器生成的 URL 安全字符
  for key in POSTGRES_PASSWORD AEGIS_DB_APP_PASSWORD VALKEY_PASSWORD; do
    value="$(pandora_env_file_value "$env" "$key")"
    [[ "$value" =~ ^[A-Za-z0-9_-]{16,}$ ]] || die "docker 布局的 .env 里 $key 缺失或含 URL 不安全的字符，不替你搬"
  done
  for key in AEGIS_MASTER_KEY AEGIS_JWT_PUBLIC_SECRET AEGIS_JWT_ADMIN_SECRET AEGIS_CONFIG_SIGNING_SEED AEGIS_ADMIN_PATH AEGIS_PUBLIC_BASE_URL; do
    value="$(pandora_env_file_value "$env" "$key")"
    [ -n "$value" ] && [[ "$value" != *CHANGE_ME* ]] || die "docker 布局的 .env 里 $key 没填，这台面板本身就跑不起来，先修好它"
  done
  [ "$(docker inspect -f '{{.State.Running}}' aegis-postgres 2>/dev/null)" = true ] \
    || die "容器 aegis-postgres 没在跑：cd $DOCKER_DIR/deploy && docker compose up -d postgres，再重跑"
  src="$(fd_docker_pg psql -X -q -At -d "$FD_DOCKER_PG_DB" -v ON_ERROR_STOP=1 \
    -c "SELECT coalesce(max(version_id) FILTER (WHERE is_applied), 0) FROM public.goose_db_version")" \
    || die "连不上 docker 布局的库，或库里没有迁移记录（docker exec aegis-postgres psql …）"
  [[ "$src" =~ ^[0-9]+$ ]] || die "读不出 docker 布局的库的迁移版本：$src"
  max_release="$(find "$SCRIPT_DIR/../migrations" -maxdepth 1 -name '[0-9][0-9][0-9][0-9][0-9]_*.sql' 2>/dev/null \
    | sed 's|.*/||' | sort | tail -n1 | cut -c1-5)"
  [[ "$max_release" =~ ^[0-9]{5}$ ]] || die "发布包里没有迁移文件"
  [ "$src" -le "$((10#$max_release))" ] \
    || die "这个发布包比 docker 布局的库还旧（库在 $src，包里最大 $((10#$max_release))），换新的发布包再迁"
  plan="$(fd_roles_plan)" || die "读不出 docker 布局的库里的角色"
  fd_roles_sql <<<"$plan" >/dev/null \
    || die "docker 布局的库里有带 SUPERUSER / REPLICATION / BYPASSRLS 的角色或名字特殊的角色（见上），不替你决定怎么搬"
  memberships="$(fd_roles_memberships)" || die "读不出 docker 布局的角色成员关系"
  [ -z "$memberships" ] || die "docker 布局的库里有角色成员关系（${memberships//$'\n'/; }），安装器不认识它们，不替你搬"
  size_kb="$(fd_docker_pg psql -X -q -At -d "$FD_DOCKER_PG_DB" -c 'SELECT pg_catalog.pg_database_size(current_database()) / 1024')" \
    || die "读不出 docker 布局的库有多大"
  [[ "$size_kb" =~ ^[0-9]+$ ]] || die "读不出 docker 布局的库有多大：$size_kb"
  pg_path=/var/lib/postgresql; [ -d "$pg_path" ] || pg_path=/var/lib
  bk_path="$NATIVE_BACKUP_DIR"; [ -d "$bk_path" ] || bk_path=/var/backups; [ -d "$bk_path" ] || bk_path=/var
  pg_avail="$(df -Pk "$pg_path" | awk 'NR == 2 { print $4 }')"
  bk_avail="$(df -Pk "$bk_path" | awk 'NR == 2 { print $4 }')"
  if [ "$(stat -c %d "$pg_path")" = "$(stat -c %d "$bk_path")" ]; then
    [ "$pg_avail" -ge $(( size_kb * 4 )) ] \
      || die "磁盘不够：库 $(( size_kb / 1024 )) MB，导出、恢复、迁移预检各要一份，$pg_path 至少要 $(( size_kb * 4 / 1024 )) MB 空闲，现在 $(( pg_avail / 1024 )) MB"
  else
    [ "$pg_avail" -ge $(( size_kb * 3 )) ] && [ "$bk_avail" -ge "$size_kb" ] \
      || die "磁盘不够：库 $(( size_kb / 1024 )) MB；$pg_path 要 $(( size_kb * 3 / 1024 )) MB（现在 $(( pg_avail / 1024 ))），$bk_path 要 $(( size_kb / 1024 )) MB（现在 $(( bk_avail / 1024 ))）"
  fi
  say "  docker 布局：库 $FD_DOCKER_PG_DB 在迁移版本 $src，约 $(( size_kb / 1024 )) MB；发布包到 $((10#$max_release))"
}
