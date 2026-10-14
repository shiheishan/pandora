#!/usr/bin/env bash
# 压测期间临时放宽 nginx 对指定来源的每 IP 限流与限连（geo/map 把键置空则不计数）。
# 只用于测试机；面板进程侧限流另在配置里放宽（见 README）。不要在生产环境使用。
#
# 用法：
#   nginx-perf-exempt.sh apply 10.0.0.20/32 [10.0.0.21/32 …]
#   nginx-perf-exempt.sh revert
set -euo pipefail

CONF="${PANDORA_NGINX_CONF:-/etc/nginx/conf.d/pandora.conf}"
BAK="${CONF}.perf-bak"

usage() {
  echo "用法: $0 apply <私网 CIDR> [更多 CIDR …] | revert" >&2
  exit 1
}

require_root() {
  if [[ "${EUID:-}" -ne 0 ]]; then
    echo "需要 root 运行" >&2
    exit 1
  fi
}

valid_cidr_syntax() {
  local cidr="$1"
  [[ "$cidr" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}$ ]]
}

octet_ok() {
  local o="$1"
  [[ "$o" =~ ^[0-9]+$ ]] && (( o >= 0 && o <= 255 ))
}

is_private_cidr() {
  local cidr="$1"
  local ip="${cidr%/*}"
  local o1 o2 o3 o4
  IFS=. read -r o1 o2 o3 o4 <<< "$ip" || return 1
  octet_ok "$o1" && octet_ok "$o2" && octet_ok "$o3" && octet_ok "$o4" || return 1
  if [[ "$o1" == "10" ]]; then
    return 0
  fi
  if [[ "$o1" == "172" ]] && (( o2 >= 16 && o2 <= 31 )); then
    return 0
  fi
  if [[ "$o1" == "192" && "$o2" == "168" ]]; then
    return 0
  fi
  return 1
}

build_geo_block() {
  local -a cidrs=("$@")
  local line
  {
    echo "# perf-exempt: 压测期间临时加，revert 会还原"
    echo "geo \$pandora_perf_exempt {"
    echo "    default 0;"
    for line in "${cidrs[@]}"; do
      echo "    ${line} 1;"
    done
    echo "}"
    echo "map \$pandora_perf_exempt \$pandora_limit_key {"
    echo "    0 \$binary_remote_addr;"
    echo "    1 \"\";"
    echo "}"
  }
}

apply_exempt() {
  shift
  if [[ "$#" -lt 1 ]]; then
    echo "apply 至少需要一个 CIDR" >&2
    usage
  fi
  if [[ -f "$BAK" ]]; then
    echo "备份已存在: $BAK，请先执行 revert" >&2
    exit 1
  fi
  if [[ ! -f "$CONF" ]]; then
    echo "找不到配置: $CONF" >&2
    exit 1
  fi

  local cidr
  for cidr in "$@"; do
    if ! valid_cidr_syntax "$cidr"; then
      echo "非法 CIDR: $cidr" >&2
      exit 1
    fi
    if ! is_private_cidr "$cidr"; then
      echo "仅允许私网 CIDR: $cidr" >&2
      exit 1
    fi
  done

  cp -p "$CONF" "$BAK"
  # 备份之后任何一步失败都拷回原文件并删备份
  trap 'cp -p "$BAK" "$CONF"; rm -f "$BAK"; echo "放宽失败，已还原配置" >&2' ERR

  # 原地覆写（cat >）保留文件属主与权限
  { build_geo_block "$@"; cat "$BAK"; } >"$CONF"
  sed -i -E 's/limit_req_zone \$binary_remote_addr/limit_req_zone $pandora_limit_key/g' "$CONF"
  sed -i -E 's/limit_conn_zone \$binary_remote_addr/limit_conn_zone $pandora_limit_key/g' "$CONF"
  grep -q 'limit_req_zone \$pandora_limit_key' "$CONF" || { echo "没找到 limit_req_zone \$binary_remote_addr，配置结构变了" >&2; false; }

  nginx -t
  restart_nginx
  trap - ERR
  echo "已放宽：$*"
}

# restart_nginx 用 restart 而不是 reload：放宽与还原都改了限流区的 key，nginx 不许 reload 时改已有共享区的 key，
# nginx -t 照样通过，reload 时 master 记一条 emerg 后沿用旧配置（10-14 测试机实测，豁免没生效）。
# restart 失败时 systemctl 返回非零，交给调用方的 ERR / set -e 处理。
restart_nginx() {
  systemctl restart nginx
  systemctl is-active --quiet nginx
}

revert_exempt() {
  if [[ ! -f "$BAK" ]]; then
    echo "没有备份，无需还原"
    exit 0
  fi
  cp -p "$BAK" "$CONF"
  if ! nginx -t; then
    echo "nginx -t 失败，已保留 $BAK" >&2
    exit 1
  fi
  restart_nginx
  rm -f "$BAK"
  echo "已还原 nginx 配置"
}

main() {
  require_root
  if [[ "$#" -lt 1 ]]; then
    usage
  fi
  case "$1" in
    apply) apply_exempt "$@" ;;
    revert) revert_exempt ;;
    *) usage ;;
  esac
}

main "$@"
