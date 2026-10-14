#!/usr/bin/env bash
# Pandora 面板一键安装 / 升级（新栈）：PostgreSQL 18 + Redis + 单二进制 pandora，程序在 /opt/pandora。
#
#   首次安装：  sudo bash <发布目录>/deploy/install.sh
#   升级：      sudo bash <发布目录>/deploy/install.sh   （已有 /etc/pandora/secrets.env 按升级处理）
#   无人值守：  sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://panel.example.test bash <发布目录>/deploy/install.sh
#
# 环境变量（可选）：
#   PANDORA_ASSUME_YES=1              不问任何问题
#   PANDORA_PUBLIC_BASE_URL=URL       首装对外地址（https://域名 或 https://公网 IPv4）
#   PANDORA_GRPC_PUBLIC_HOST=主机     节点连面板的主机，缺省取对外地址的主机
#   PANDORA_ADMIN_EMAIL=邮箱            无人值守建第一个管理员（须从标准输入读一行口令），例如：
#                                     printf '%s\n' "$pw" | sudo PANDORA_ADMIN_EMAIL=a@example.test \
#                                       PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://panel.example.test bash install.sh
#   PANDORA_SKIP_NGINX=1              不碰 nginx 与证书
#   PANDORA_ACME / PANDORA_ACME_EMAIL / PANDORA_ACME_SERVER  交给 edge-tls.sh（与旧版同义）
#   PANDORA_SYSTEMD_HARDENING=0|1     PostgreSQL / Redis 的 systemd 隔离（缺省 1，记在 deploy.env）
#
# 设计要点：幂等（已有 secrets.env 按升级，三个配置文件首装生成、升级不改）；密钥现生成只写文件；
# 迁移失败不起服务；升级 = 停服 → 升级前备份 → 换程序 → migrate up → 收敛权限 → 起服务。
set -euo pipefail
umask 022

PANDORA_PRESET_ADMIN_EMAIL="${PANDORA_ADMIN_EMAIL:-}"
if [ -n "$PANDORA_PRESET_ADMIN_EMAIL" ] && [ ! -t 0 ]; then
  IFS= read -r PANDORA_PRESET_ADMIN_PW || true
  exec </dev/null
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
RELEASE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"

pandora_verify_release_tree() {
  local root="$1" probe owner perm
  [ -f "$root/bin/pandora" ] || { echo "这不像发布目录：找不到 $root/bin/pandora" >&2; return 1; }
  [ -f "$root/SHA256SUMS" ] || { echo "发布目录缺少 SHA256SUMS" >&2; return 1; }
  [ -f "$root/deploy/release-artifact.env" ] \
    || { echo "发布目录缺少 deploy/release-artifact.env" >&2; return 1; }
  if [ -z "${PANDORA_INSTALL_SANDBOX:-}" ]; then
    probe="$root"
    while :; do
      owner="$(stat -c %u -- "$probe")" && perm="$(stat -c %a -- "$probe")" \
        || { echo "读不出 $probe 的属主与权限" >&2; return 1; }
      if [ "$owner" != 0 ] || (( (8#$perm & 8#022) != 0 )); then
        printf '%s\n' "发布目录或父路径不归 root 或可被他人写入：$probe。放到 root 独占目录再装。" >&2
        return 1
      fi
      [ "$probe" = / ] && break
      probe="$(dirname -- "$probe")"
    done
    if find "$root" -xdev \( ! -user root -o -perm /022 \) -print -quit | grep -q .; then
      echo "发布目录里有不归 root 或可被他人写入的文件" >&2
      return 1
    fi
  fi
  (cd "$root" && sha256sum --quiet --strict -c SHA256SUMS) \
    || { echo "SHA256SUMS 校验失败" >&2; return 1; }
}

pandora_valid_grpc_host() {
  local host="${1,,}"
  pandora_public_ipv4 "$host" && return 0
  [[ ${#host} -le 253 ]] || return 1
  [[ "$host" != *.localhost ]] || return 1
  [[ "$host" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$ ]]
}

for arg in "$@"; do
  case "$arg" in
    -h|--help)
      printf '%s\n' "用法: sudo bash install.sh    首装或升级；环境变量见本脚本头注释"
      exit 0 ;;
    *) echo "不认识的参数：$arg（只认 --help；选项用环境变量，见头注释）" >&2; exit 1 ;;
  esac
done

if [ -z "${PANDORA_INSTALL_SANDBOX:-}" ]; then
  [[ $EUID -eq 0 ]] || { echo "请用 root 运行: sudo bash install.sh" >&2; exit 1; }
fi
command -v sha256sum >/dev/null 2>&1 || { echo "缺少 sha256sum" >&2; exit 1; }
pandora_verify_release_tree "$RELEASE_ROOT" || exit 1

# shellcheck source=install-lib.sh
. "$SCRIPT_DIR/install-lib.sh"
[[ -f "$SCRIPT_DIR/public-base-url.sh" ]] || die "发布目录缺少 deploy/public-base-url.sh"
# shellcheck source=public-base-url.sh
. "$SCRIPT_DIR/public-base-url.sh"
[[ -f "$SCRIPT_DIR/platform.sh" ]] || die "发布目录缺少 deploy/platform.sh"
# shellcheck source=platform.sh
. "$SCRIPT_DIR/platform.sh"
if ! pandora_sandboxed; then
  pandora_detect_platform
fi
need openssl; need curl; need systemctl

[[ -f "$SCRIPT_DIR/postgresql-pandora.conf" ]] || die "发布目录缺少 deploy/postgresql-pandora.conf"

OLD_VERSION=""
if [ -f "$SECRETS_FILE" ]; then
  MODE=upgrade
  pandora_require_config_files
  OLD_VERSION="$(pandora_release_version "$INSTALL_DIR/release.env")"
else
  MODE=install
fi

NEW_VERSION="$(pandora_release_version "$RELEASE_ROOT/deploy/release-artifact.env")"
if [ "$MODE" = install ]; then
  PUBLIC_BASE_URL="$(pandora_resolve_public_base_url 'bash install.sh')" || die "首装需要合规的对外地址（原因见上）"
  GRPC_HOST="${PANDORA_GRPC_PUBLIC_HOST:-$(pandora_url_host "$PUBLIC_BASE_URL")}"
  pandora_valid_grpc_host "$GRPC_HOST" \
    || die "节点 gRPC 公网主机不合规：$GRPC_HOST（须为公网 IPv4 或 DNS 域名）"
  say "首装 → $NEW_VERSION（对外 $PUBLIC_BASE_URL，gRPC 主机 $GRPC_HOST）"
else
  say "升级 ${OLD_VERSION:-?} → $NEW_VERSION"
fi

# ── [1/8] 系统包 ──────────────────────────────────────
say "[1/8] 安装系统依赖"
pandora_install_packages
pandora_check_redis_version

# ── [2/8] 用户与目录 ──────────────────────────────────
say "[2/8] 用户与目录"
pandora_ensure_user
pandora_ensure_dirs

# ── [3/8] PostgreSQL 集群 ─────────────────────────────
say "[3/8] PostgreSQL 集群"
pandora_ensure_pg_cluster "$PG_MAJOR"
PG_PORT="$(pandora_pg_cluster_port "$PG_MAJOR")" || die "读不出 PostgreSQL ${PG_MAJOR}/main 端口"
pandora_check_pg_conf_include "$PG_MAJOR"
if [ "$MODE" = upgrade ]; then
  deploy_pg_port="$(pandora_env_value "$DEPLOY_ENV" POSTGRES_PORT)"
  [[ "$deploy_pg_port" == "$PG_PORT" ]] || die "deploy.env 的 POSTGRES_PORT=$deploy_pg_port 与 PG${PG_MAJOR}/main 端口 $PG_PORT 不一致，什么都还没改"
fi

# ── [4/8] 停服与备份 ──────────────────────────────────
say "[4/8] 停服与升级前备份"
if [ "$MODE" = upgrade ]; then
  pandora_stop_services
  pandora_pre_upgrade_dump "$PG_PORT"
else
  say "  首装，跳过"
fi

# ── [5/8] 配置与数据服务 ──────────────────────────────
say "[5/8] 配置与数据服务"
pandora_install_host_files "$RELEASE_ROOT"
pandora_ensure_backup_key
if [ "$MODE" = install ]; then
  pandora_write_new_config "$PUBLIC_BASE_URL" "$GRPC_HOST" "$PG_PORT"
else
  pandora_ensure_backup_env
fi
pandora_load_hardening_switch
HARDEN_FAILED=""
pandora_apply_pg_config "$PG_MAJOR" "$PG_PORT" "$SCRIPT_DIR/postgresql-pandora.conf" || HARDEN_FAILED+=" PostgreSQL"
systemctl start "$REDIS_UNIT" 2>/dev/null || true
redis_url="$(pandora_env_value "$SECRETS_FILE" PANDORA_REDIS_URL)"
redis_pw=""
if [[ "$redis_url" =~ ^redis://:([^@]+)@ ]]; then redis_pw="${BASH_REMATCH[1]}"; fi
[ -n "$redis_pw" ] || die "secrets.env 的 PANDORA_REDIS_URL 里读不出口令（须形如 redis://:<口令>@127.0.0.1:6379/0）"
pandora_harden_redis "$REDIS_CONF" "$redis_pw" || HARDEN_FAILED+=" Redis"
pandora_redis_ping "$redis_pw" || die "Redis 连不上（口令或配置有误，见 journalctl -u $REDIS_UNIT -n 30）"

# ── [6/8] 程序与数据库 ──────────────────────────────
say "[6/8] 程序与数据库"
pandora_bootstrap_db roles || die "建库角色失败，服务没有启动"
pandora_install_program "$RELEASE_ROOT" "$MODE"
if ! pandora_migrate; then
  die "迁移失败（输出见上），服务没有启动。升级时上一版程序在 $INSTALL_DIR/bin/pandora.prev；升级前备份在 ${PANDORA_PRE_UPGRADE_DUMP:-（无）}。修好后重跑本脚本（按升级处理，幂等）"
fi
pandora_bootstrap_db grants || die "收敛运行角色权限失败，服务没有启动。修好后重跑本脚本（按升级处理，幂等）"

# ── [7/8] systemd 与服务 ─────────────────────────────
say "[7/8] systemd 与服务"
pandora_install_units "$RELEASE_ROOT"
pandora_swap_accounting_note
pandora_start_services
systemctl enable --now pandora-health.timer 2>/dev/null || say "  ! 未能启用 pandora-health.timer：可稍后 systemctl enable --now pandora-health.timer"
HEALTH_OK=1
pandora_services_healthy || HEALTH_OK=0
for s in "${SERVICES[@]}"; do
  printf '  %s: %s\n' "$s" "$(systemctl is-active "$s" 2>/dev/null || echo inactive)"
done
printf '  公开 readyz :9000 → %s\n' "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://127.0.0.1:9000/readyz 2>/dev/null || echo 000)"
printf '  管理 readyz :9001 → %s\n' "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://127.0.0.1:9001/readyz 2>/dev/null || echo 000)"
pandora_open_firewall
if [ -n "$HARDEN_FAILED" ]; then
  die "程序已装好，服务已按新版本起来，但 ${HARDEN_FAILED# } 的加固或调参没生效（已撤回、核实在跑；原因见上）。用 deploy.env 的 PANDORA_SYSTEMD_HARDENING=0 关掉隔离后重跑，或修好主机能力后重跑本脚本（按升级处理）"
fi

# ── [8/8] 管理员与 HTTPS ──────────────────────────────
say "[8/8] 管理员与 HTTPS"
if [ "$HEALTH_OK" = 1 ]; then
  pandora_admin_bootstrap
else
  PANDORA_ADMIN_STATE=manual
fi
pandora_setup_edge
if [ "$HEALTH_OK" != 1 ]; then
  die "服务起来了但健康检查没通过（公开/管理 readyz 或 worker 未就绪）。看 journalctl -u pandora-serve -n 50 --no-pager 与 /var/log/pandora/serve.log；修好后重跑本脚本（按升级处理，幂等）"
fi

BACKUP_TIMER=disabled
systemctl is-enabled --quiet pandora-backup.timer 2>/dev/null && BACKUP_TIMER=enabled
pandora_print_summary

PANDORA_PRESET_ADMIN_PW=""
