#!/usr/bin/env bash
# pandora-ops 容器入口：在 PostgreSQL 的网络命名空间里跑迁移、备份与运维脚本。
#
# 子命令：
#   migrate   — 落盘 /etc/pandora 配置后建角色、升级前转储（如需）、执行迁移、收敛 grants
#   backup    — 加密数据库备份（调用 deploy/backup-postgres.sh）
#   verify    — 校验备份归档能否恢复
#   restore   — 恢复到指定库名（正式库 pandora 禁止在容器内直接覆盖）
#   psql      — 以 postgres 超级用户连面板库
#   gen-env   — 生成填好随机秘密的 compose .env（须重定向到文件）
#   help      — 打印子命令说明
#
# 为何挂在 PostgreSQL 的网络命名空间：部署脚本与裸机相同，经 127.0.0.1:5432 连库。
# 秘密经 materialize 写入容器内 root:root 0600 的 deploy.env / secrets.env / backup-age.key，容器退出即消失。
#
# 容器版不支持：WebDAV 异地上传（backend 网络不出网）；正式库直接覆盖恢复（须停 systemd，见 RUNBOOK compose 一章）。
set -Eeuo pipefail
umask 077

die() { printf 'pandora-ops: %s\n' "$*" >&2; exit 1; }
say() { printf 'pandora-ops: %s\n' "$*" >&2; }

DEPLOY=/opt/pandora/deploy
BIN=/opt/pandora/bin/pandora
ETC=/etc/pandora
BACKUP_DIR=/var/backups/pandora
ENV_EXAMPLE=/opt/pandora/docker/env.example

require_pw() {
  # 分两行：同一条 local 里右边的 ${!name} 展开时 name 还没赋值
  local name="$1"
  local val="${!name-}"
  [[ "$val" =~ ^[A-Za-z0-9_-]{32,}$ ]] || die "${name} 不合规：须至少 32 个 URL 安全字符（字母、数字、_、-）"
}

write_root_file() {
  local dest="$1" dir tmp
  dir="$(dirname -- "$dest")"
  install -d -m 0755 "$dir"
  tmp="$(mktemp "$dir/.tmp.XXXXXX")"
  cat >"$tmp"
  chmod 0600 "$tmp"
  chown root:root "$tmp"
  mv -f -- "$tmp" "$dest"
}

materialize() {
  local recipient retention="${PANDORA_BACKUP_RETENTION_DAYS:-14}"
  require_pw POSTGRES_SUPER_PASSWORD
  require_pw PANDORA_OWNER_DB_PASSWORD
  require_pw PANDORA_APP_DB_PASSWORD
  [[ "${PANDORA_BACKUP_AGE_KEY:-}" =~ ^AGE-SECRET-KEY-1[0-9A-Z]{58}$ ]] \
    || die 'PANDORA_BACKUP_AGE_KEY 不合规：须为 AGE-SECRET-KEY-1 开头的 age 私钥一行'
  [[ "$retention" =~ ^[0-9]+$ ]] || die 'PANDORA_BACKUP_RETENTION_DAYS 须为非负整数'

  printf '%s\n' "$PANDORA_BACKUP_AGE_KEY" | write_root_file "$ETC/backup-age.key"
  recipient="$(age-keygen -y "$ETC/backup-age.key")" || die '读不出备份 age 公钥'
  [[ "$recipient" == age1* ]] || die '备份 age 公钥不像 age1 收件人'

  # 容器在 PostgreSQL 网络命名空间里，127.0.0.1:5432 即数据库；裸机 deploy 脚本读这两个文件。
  {
    printf '%s\n' \
      'POSTGRES_PORT=5432' \
      'POSTGRES_DB=pandora' \
      "POSTGRES_SUPER_PASSWORD=${POSTGRES_SUPER_PASSWORD}" \
      "PANDORA_DATABASE_MIGRATION_URL=postgres://pandora_owner:${PANDORA_OWNER_DB_PASSWORD}@127.0.0.1:5432/pandora?sslmode=disable" \
      "PANDORA_BACKUP_RETENTION_DAYS=${retention}" \
      "PANDORA_BACKUP_AGE_RECIPIENT=${recipient}" \
      'PANDORA_BACKUP_AGE_IDENTITY=/etc/pandora/backup-age.key'
  } | write_root_file "$ETC/deploy.env"

  {
    printf '%s\n' \
      "PANDORA_DATABASE_URL=postgres://pandora_app:${PANDORA_APP_DB_PASSWORD}@127.0.0.1:5432/pandora?sslmode=disable"
  } | write_root_file "$ETC/secrets.env"
}

pg_super() {
  PGHOST=127.0.0.1 PGPORT=5432 PGUSER=postgres PGPASSWORD="$POSTGRES_SUPER_PASSWORD" PGSSLMODE=disable "$@"
}

bootstrap() {
  local phase="$1"
  # bootstrap.sh 用 runuser -u postgres -- psql 且不带 -h；PGHOST 走 TCP 到本命名空间 127.0.0.1。
  PGHOST=127.0.0.1 PGPORT=5432 PGPASSWORD="$POSTGRES_SUPER_PASSWORD" PANDORA_ETC_DIR="$ETC" \
    bash "$DEPLOY/bootstrap.sh" "$phase" || die "bootstrap.sh ${phase} 失败"
}

run_pandora() {
  PANDORA_DATABASE_MIGRATION_URL="postgres://pandora_owner:${PANDORA_OWNER_DB_PASSWORD}@127.0.0.1:5432/pandora?sslmode=disable" \
    runuser -u nobody -- "$BIN" "$@"
}

cmd_migrate() {
  local i has_goose pending status_out f
  materialize
  for i in $(seq 1 60); do
    pg_isready -h 127.0.0.1 -p 5432 -q && break
    sleep 2
  done
  pg_isready -h 127.0.0.1 -p 5432 -q || die 'PostgreSQL 在 120 秒内未就绪'

  bootstrap roles

  has_goose="$(pg_super psql -X -d pandora -tAc \
    "SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'goose_db_version'" 2>/dev/null || true)"
  pending=0
  if [[ "$has_goose" == 1 ]]; then
    # status 的输出是 tabwriter 用空格对齐的「版本 名字 已执行」，最后一列是 是/否；查不出状态就停，不能悄悄跳过转储
    status_out="$(run_pandora migrate status)" || die 'pandora migrate status 失败，没有做升级前转储，什么都还没改'
    if awk 'NR > 1 && $NF == "否" { found = 1 } END { exit !found }' <<<"$status_out"; then
      pending=1
    fi
  fi
  if [[ "$has_goose" == 1 && "$pending" == 1 ]]; then
    install -d -m 0750 "$BACKUP_DIR"
    f="$BACKUP_DIR/pre-upgrade-$(date -u +%Y%m%dT%H%M%SZ).dump"
    if ! ( umask 077; pg_super pg_dump -Fc -d pandora >"$f" ); then
      rm -f -- "$f"
      die '升级前转储失败，什么都还没改'
    fi
    if ! pg_restore --list <"$f" >/dev/null 2>&1; then
      rm -f -- "$f"
      die '升级前转储失败，什么都还没改'
    fi
    chmod 0600 "$f"
    chown root:root "$f"
    say "升级前转储：$f"
  fi

  run_pandora migrate up || die 'pandora migrate up 失败'
  bootstrap grants
  say '迁移完成'
}

cmd_gen_env() {
  local url host version out line key
  declare -A GEN

  [[ -t 1 ]] && die '输出里是秘密，重定向到文件：(umask 077 && docker run --rm -e PANDORA_PUBLIC_BASE_URL=https://panel.example.test <镜像仓库>/pandora-ops:<版本> gen-env > .env)'

  url="${PANDORA_PUBLIC_BASE_URL:-}"
  [[ -n "$url" ]] || die '请设置 PANDORA_PUBLIC_BASE_URL'
  url="${url%/}"
  # shellcheck source=/opt/pandora/deploy/public-base-url.sh
  source "$DEPLOY/public-base-url.sh"
  pandora_valid_public_base_url "$url" || die "PANDORA_PUBLIC_BASE_URL 不合规：$url"

  if [[ "$url" =~ ^https://([^/:]+) ]]; then
    host="${BASH_REMATCH[1],,}"
  else
    die '无法从 PANDORA_PUBLIC_BASE_URL 解析主机'
  fi

  version="${PANDORA_VERSION:-${PANDORA_IMAGE_VERSION:-}}"
  [[ -n "$version" ]] || die '缺 PANDORA_VERSION 且镜像未设 PANDORA_IMAGE_VERSION'

  GEN[PANDORA_PUBLIC_BASE_URL]="$url"
  GEN[PANDORA_GRPC_PUBLIC_HOST]="$host"
  GEN[PANDORA_VERSION]="$version"
  GEN[POSTGRES_SUPER_PASSWORD]="$(openssl rand -hex 24)"
  GEN[PANDORA_OWNER_DB_PASSWORD]="$(openssl rand -hex 24)"
  GEN[PANDORA_APP_DB_PASSWORD]="$(openssl rand -hex 24)"
  GEN[PANDORA_REDIS_PASSWORD]="$(openssl rand -hex 24)"
  GEN[PANDORA_SECRETS_MASTER_KEY]="$(openssl rand -base64 32)"
  GEN[PANDORA_AUTH_PUBLIC_JWT_SECRET]="$(openssl rand -hex 32)"
  GEN[PANDORA_AUTH_ADMIN_JWT_SECRET]="$(openssl rand -hex 32)"
  GEN[PANDORA_ADMIN_PATH]="ops_$(openssl rand -hex 12)"
  GEN[PANDORA_BACKUP_AGE_KEY]="$(age-keygen 2>/dev/null | awk '/^AGE-SECRET-KEY-1/ { print; exit }')"
  [[ -n "${GEN[PANDORA_BACKUP_AGE_KEY]}" ]] || die '生成 PANDORA_BACKUP_AGE_KEY 失败（age-keygen）'

  for key in PANDORA_IMAGE_REPO PANDORA_HTTP_PUBLISH PANDORA_HTTPS_PUBLISH PANDORA_LOOPBACK_PUBLISH \
    PANDORA_GRPC_PUBLISH PANDORA_GRPC_PUBLIC_PORT PANDORA_REALIP_FROM PANDORA_REALIP_HEADER \
    PANDORA_TLS_DIR PANDORA_ACME_DIR PANDORA_BACKUP_RETENTION_DAYS; do
    if [[ -n "${!key-}" ]]; then
      GEN[$key]="${!key}"
    fi
  done

  [[ -r "$ENV_EXAMPLE" ]] || die "读不了 $ENV_EXAMPLE"
  out=''
  left=''
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" =~ ^([A-Z][A-Z0-9_]*)= ]]; then
      key="${BASH_REMATCH[1]}"
      if [[ -n "${GEN[$key]+x}" ]]; then
        line="${key}=${GEN[$key]}"
      fi
      # 只查赋值行：模板的注释里本来就写着 CHANGE_ME
      [[ "$line" != *CHANGE_ME* ]] || left+=" $key"
    fi
    out+="$line"$'\n'
  done <"$ENV_EXAMPLE"

  [[ -z "$left" ]] || die "生成结果仍含 CHANGE_ME：${left# }，请检查 env.example 与生成逻辑"
  printf '%s' "$out"
  say '已生成 .env 内容；请 chmod 600 .env，并把 .env 另存到机器之外（含主密钥与备份私钥）。'
}

cmd_help() {
  cat <<'EOF'
migrate  — 建库角色、按需升级前转储、执行 pandora migrate up、收敛 pandora_app 权限
backup   — 加密备份到 /var/backups/pandora（与裸机 backup-postgres.sh 相同）
verify   — 校验备份归档：verify <归档绝对路径>
restore  — 恢复到新库：restore --archive <路径> --target-db <库名>（须 docker compose run -e 传确认变量）
psql     — 超级用户进库，参数同 psql
gen-env  — 生成 compose 用的 .env（须重定向到文件，并设 PANDORA_PUBLIC_BASE_URL）
help     — 本说明
EOF
}

cmd_restore() {
  local -a rest=()
  local target_db='' i
  materialize
  shift
  rest=("$@")
  for i in "${!rest[@]}"; do
    if [[ "${rest[i]}" == --target-db && -n "${rest[i+1]:-}" ]]; then
      target_db="${rest[i+1]}"
    fi
  done
  if [[ "$target_db" == pandora ]]; then
    die '正式库不能在容器里直接覆盖（restore-postgres.sh 恢复正式库要停 systemd 单元）：先恢复到新库名（如 pandora_restore），再按 RUNBOOK 的 compose 一节停服务、改名、重跑 migrate'
  fi
  exec bash "$DEPLOY/restore-postgres.sh" "${rest[@]}"
}

main() {
  case "${1:-help}" in
    migrate) cmd_migrate ;;
    backup)
      materialize
      exec bash "$DEPLOY/backup-postgres.sh"
      ;;
    verify)
      materialize
      exec bash "$DEPLOY/verify-backup.sh" "${@:2}"
      ;;
    restore) cmd_restore "$@" ;;
    psql)
      materialize
      exec bash "$DEPLOY/psql.sh" "${@:2}"
      ;;
    gen-env) cmd_gen_env ;;
    help)
      cmd_help
      ;;
    *)
      cmd_help
      exit 2
      ;;
  esac
}

main "$@"
