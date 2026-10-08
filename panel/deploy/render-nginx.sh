#!/usr/bin/env bash
# Render the unified edge config without sourcing the secret environment file.
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${1:-$SCRIPT_DIR/.env}"
OUTPUT_FILE="${2:-/etc/nginx/conf.d/aegis.conf}"
# 第三个参数只给测试用：模板里 include 的路径是写死的，生产上永远是这个默认值
REALIP_FILE="${3:-/etc/aegispanel/cloudflare-realip.conf}"
TEMPLATE_FILE="$SCRIPT_DIR/nginx-aegis.conf"

die() { printf 'render-nginx: %s\n' "$*" >&2; exit 1; }

[[ -f "$ENV_FILE" ]] || die "environment file not found"
[[ -f "$TEMPLATE_FILE" ]] || die "nginx template not found"
[[ "$OUTPUT_FILE" = /* ]] || die "output path must be absolute"
[[ "$REALIP_FILE" = /* ]] || die "real-IP include path must be absolute"

# Read KEY=value lines with awk instead of sourcing: the file holds secrets and
# must never be executed. Each key must appear exactly once.
env_value() {
  local key="$1" values
  mapfile -t values < <(awk -F= -v key="$key" '
    $1 == key {
      sub(/^[^=]*=/, "")
      sub(/\r$/, "")
      print
    }
  ' "$ENV_FILE")
  [[ ${#values[@]} -eq 1 ]] || die "$key must occur exactly once"
  printf '%s' "${values[0]}"
}

admin_path="$(env_value AEGIS_ADMIN_PATH)"

# One high-entropy URL segment keeps the management surface out of commodity
# scans. It is a discovery-control layer, never a replacement for admin auth.
[[ ${#admin_path} -ge 20 && ${#admin_path} -le 64 ]] || \
  die "AEGIS_ADMIN_PATH must be 20-64 characters"
[[ "$admin_path" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || \
  die "AEGIS_ADMIN_PATH must be one URL-safe path segment"
[[ "$admin_path" != "__AEGIS_ADMIN_PATH__" ]] || die "placeholder value is forbidden"
[[ "$admin_path" != CHANGE_ME* ]] || die "example/default value is forbidden"

# The edge serves the same origin the panel builds install commands, payment
# callbacks and subscription links from, so the host is taken from that URL
# rather than configured twice. HTTPS on the default port only: the template
# listens on 443 and the certificate sits behind /etc/aegispanel/tls/live
# (edge-tls.sh keeps it there: Let's Encrypt for a domain or a public IPv4,
# self-signed as the fallback).
#
# 与 public-base-url.sh 的 pandora_valid_public_base_url、platform/config 的
# CanonicalPublicOrigin（生产）同一规则，用例表 fixtures/public-base-url-cases.txt：
# https://<DNS 域名或公网 IPv4>。本脚本装在主机上单独运行，不 source 安装器的库，所以抄一份。
public_ipv4() {
  local o='(0|[1-9][0-9]{0,2})' a b c d
  [[ "$1" =~ ^$o\.$o\.$o\.$o$ ]] || return 1
  a="${BASH_REMATCH[1]}" b="${BASH_REMATCH[2]}" c="${BASH_REMATCH[3]}" d="${BASH_REMATCH[4]}"
  (( a <= 255 && b <= 255 && c <= 255 && d <= 255 )) || return 1
  (( a != 0 && a != 10 && a != 127 && a < 224 )) || return 1
  (( !(a == 100 && b >= 64 && b <= 127) )) || return 1
  (( !(a == 169 && b == 254) )) || return 1
  (( !(a == 172 && b >= 16 && b <= 31) )) || return 1
  (( !(a == 192 && b == 168) )) || return 1
  (( !(a == 192 && b == 0 && c == 0) )) || return 1
  (( !(a == 198 && (b == 18 || b == 19)) ))
}
base_url="$(env_value AEGIS_PUBLIC_BASE_URL)"
[[ "$base_url" != *CHANGE_ME* ]] || die "AEGIS_PUBLIC_BASE_URL still holds the example value"
[[ "$base_url" =~ ^https://([^/:]+)/?$ ]] || \
  die "AEGIS_PUBLIC_BASE_URL must be https://<domain or public IPv4> with no port or path"
domain="${BASH_REMATCH[1],,}"
if ! public_ipv4 "$domain"; then
  [[ ${#domain} -le 253 ]] || die "domain is too long"
  [[ "$domain" != *.localhost ]] || die "AEGIS_PUBLIC_BASE_URL must not be a localhost name"
  [[ "$domain" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$ ]] || \
    die "AEGIS_PUBLIC_BASE_URL must name a DNS domain or a public IPv4 address (not a private, reserved or IPv6 address)"
fi

for placeholder in __AEGIS_ADMIN_PATH__ __AEGIS_DOMAIN__; do
  grep -q "$placeholder" "$TEMPLATE_FILE" || die "template placeholder $placeholder is missing"
done

# ---------------------------------------------------------------------------
# HTTP/2 的写法随 nginx 版本：1.25.1 起是独立的 http2 on;（listen 的 http2 参数已弃用、只告警），
# 更老的版本不认识 http2 指令，nginx -t 直接失败。模板按新写法写，这里按本机 nginx 版本取舍：
# 探不到版本（没装 nginx、在别处渲染）时用旧写法——它在新版本上也能用，只多一条弃用告警。
# PANDORA_NGINX_VERSION 可显式指定（测试与异机渲染用），形如 1.26.3
# ---------------------------------------------------------------------------
nginx_version="${PANDORA_NGINX_VERSION:-}"
if [[ -z "$nginx_version" ]] && command -v nginx >/dev/null 2>&1; then
  nginx_version="$(nginx -v 2>&1 || true)"
fi
http2_directive=0
if [[ "$nginx_version" =~ ([0-9]+)\.([0-9]+)\.([0-9]+) ]]; then
  v_major="${BASH_REMATCH[1]}" v_minor="${BASH_REMATCH[2]}" v_patch="${BASH_REMATCH[3]}"
  if (( v_major > 1 || (v_major == 1 && (v_minor > 25 || (v_minor == 25 && v_patch >= 1))) )); then
    http2_directive=1
  fi
fi
[[ "$(grep -cE '^[[:space:]]*http2 on;' "$TEMPLATE_FILE")" -eq 1 ]] || die "template must hold exactly one 'http2 on;' line"

# ---------------------------------------------------------------------------
# 真实来源 IP 的信任表：模板在 server 块里 include 它，文件缺了 nginx -t 就失败
# ---------------------------------------------------------------------------
# 两个安装脚本都不生成它，全新安装后第一次渲染就会撞上。这里只在它不存在时写一份
# 「不信任任何代理」的默认文件：没有 set_real_ip_from，nginx 就只认 TCP 对端，
# 客户端自己填的 CF-Connecting-IP / X-Real-IP 一律不采信——站点不在 Cloudflare
# 后面时，信任 Cloudflare 的头等于让任何人伪造来源 IP，绕过按 IP 的限流与风控。
# 已存在就不碰：在 Cloudflare 后面的站点由 update-cloudflare-realip.sh 写入网段，
# 升级时重新渲染不能把它冲掉。
ensure_realip_default() {
  [[ -e "$REALIP_FILE" ]] && return 0
  local dir tmp
  dir="$(dirname -- "$REALIP_FILE")"
  install -d -m 0755 "$dir"
  tmp="$(mktemp "${REALIP_FILE}.tmp.XXXXXX")"
  cat >"$tmp" <<'REALIP'
# Pandora edge real-IP trust list, created by render-nginx.sh because none existed.
#
# Empty on purpose: no proxy is trusted, so nginx keeps the TCP peer as the
# client address and ignores any CF-Connecting-IP or X-Real-IP a client sends.
#
# Behind Cloudflare, replace this file with Cloudflare's published networks:
#   /opt/aegispanel/deploy/update-cloudflare-realip.sh
#   nginx -t && systemctl reload nginx
#
# render-nginx.sh never overwrites this file; upgrades keep whatever is here.
REALIP
  chmod 0644 "$tmp"
  mv -f -- "$tmp" "$REALIP_FILE"
  printf 'render-nginx: created %s (trusts no proxy; run update-cloudflare-realip.sh if the site is behind Cloudflare)\n' "$REALIP_FILE"
}
ensure_realip_default

# ---------------------------------------------------------------------------
# 主配置的连接上限：worker_connections 与 worker_rlimit_nofile 只能写在 nginx.conf 的
# events / main 段，conf.d 里的站点文件管不到。Debian 缺省 768 × worker_processes auto（2 核
# 两个 worker）：每条 SSE、每条节点事件流各占一个客户端连接加一个上游连接，约一千条就顶满，
# 新连接（含节点请求）一律被拒（5k-r4 复测：1000 人 SSE 时节点请求报 500）。这里把每个 worker
# 抬到至少 8192 条，文件描述符上限配套抬到 65536（一条代理连接两个 fd）；已经更高的不动。
# 主配置的位置：PANDORA_NGINX_MAIN_CONF 显式指定（none 表示不碰），否则只在输出文件位于
# <nginx 目录>/conf.d/ 下时取 <nginx 目录>/nginx.conf；文件不存在就跳过。
# 改动只涉及这两个数，结构不认识（events 写在一行里、没有 events 段）时不改、只提示。
# ---------------------------------------------------------------------------
NGINX_WORKER_CONNECTIONS=8192
NGINX_RLIMIT_NOFILE=65536

nginx_main_conf() {
  if [[ -n "${PANDORA_NGINX_MAIN_CONF:-}" ]]; then
    [[ "$PANDORA_NGINX_MAIN_CONF" = none ]] || printf '%s' "$PANDORA_NGINX_MAIN_CONF"
    return 0
  fi
  local dir
  dir="$(dirname -- "$OUTPUT_FILE")"
  [[ "$(basename -- "$dir")" = conf.d ]] && printf '%s/nginx.conf' "$(dirname -- "$dir")"
  return 0
}

tune_nginx_main() {
  local conf="$1" tmp rc=0
  [[ -f "$conf" ]] || { printf 'render-nginx: %s not found; worker_connections left as is\n' "$conf"; return 0; }
  tmp="$(mktemp "${conf}.tmp.XXXXXX")"
  # 两遍：第一遍只看主段有没有 worker_rlimit_nofile，第二遍改写
  awk -v wc="$NGINX_WORKER_CONNECTIONS" -v nofile="$NGINX_RLIMIT_NOFILE" '
    function lead(s) { match(s, /^[[:space:]]*/); return substr(s, 1, RLENGTH) }
    function num(s) { sub(/^[^0-9]*/, "", s); sub(/[^0-9].*$/, "", s); return s + 0 }
    {
      code = $0; sub(/#.*/, "", code)
      opens = gsub(/\{/, "{", code); closes = gsub(/\}/, "}", code)
    }
    NR == FNR {
      if (depth == 0 && code ~ /^[[:space:]]*worker_rlimit_nofile[[:space:]]+[0-9]+[[:space:]]*;/) have_rlimit = 1
      depth += opens - closes
      next
    }
    FNR == 1 { depth = 0 }
    {
      line = $0
      if (depth == 0 && code ~ /^[[:space:]]*worker_rlimit_nofile[[:space:]]+[0-9]+[[:space:]]*;/ && num(code) < nofile)
        line = lead($0) "worker_rlimit_nofile " nofile ";"
      if (depth == 0 && code ~ /^[[:space:]]*events[[:space:]]*\{/) {
        if (opens != closes + 1) exit 4
        events = 1; in_events = 1
        if (!have_rlimit) { print "worker_rlimit_nofile " nofile ";"; have_rlimit = 1 }
      }
      if (in_events && depth == 1 && code ~ /^[[:space:]]*worker_connections[[:space:]]+[0-9]+[[:space:]]*;/) {
        saw_wc = 1
        if (num(code) < wc) line = lead($0) "worker_connections " wc ";"
      }
      if (in_events && depth == 1 && opens == 0 && closes == 1 && code ~ /^[[:space:]]*\}[[:space:]]*$/) {
        if (!saw_wc) print "    worker_connections " wc ";"
        in_events = 0
      }
      depth += opens - closes
      print line
    }
    END { if (!events) exit 3 }
  ' "$conf" "$conf" >"$tmp" || rc=$?
  if [[ "$rc" -ne 0 ]]; then
    rm -f -- "$tmp"
    printf 'render-nginx: %s has no events block this script understands; set worker_connections >= %s by hand\n' \
      "$conf" "$NGINX_WORKER_CONNECTIONS" >&2
    return 0
  fi
  if cmp -s -- "$tmp" "$conf"; then
    rm -f -- "$tmp"
    return 0
  fi
  chmod --reference="$conf" "$tmp" 2>/dev/null || chmod 0644 "$tmp"
  mv -f -- "$tmp" "$conf"
  printf 'render-nginx: %s now has worker_connections >= %s and worker_rlimit_nofile >= %s\n' \
    "$conf" "$NGINX_WORKER_CONNECTIONS" "$NGINX_RLIMIT_NOFILE"
}

main_conf="$(nginx_main_conf)"
[[ -z "$main_conf" ]] || tune_nginx_main "$main_conf"

output_dir="$(dirname -- "$OUTPUT_FILE")"
[[ -d "$output_dir" ]] || die "output directory does not exist"
tmp_file="$(mktemp "${OUTPUT_FILE}.tmp.XXXXXX")"
trap 'rm -f -- "$tmp_file"' EXIT

http2_edit=()
if [[ "$http2_directive" -eq 0 ]]; then
  http2_edit=(-e '/^[[:space:]]*http2 on;/d' -e 's/^\([[:space:]]*listen [^;]*:443 ssl\);/\1 http2;/')
fi
sed -e "s/__AEGIS_ADMIN_PATH__/${admin_path}/g" -e "s/__AEGIS_DOMAIN__/${domain}/g" \
  ${http2_edit[@]+"${http2_edit[@]}"} "$TEMPLATE_FILE" >"$tmp_file"
chmod 0644 "$tmp_file"
mv -f -- "$tmp_file" "$OUTPUT_FILE"
trap - EXIT

if [[ "$http2_directive" -eq 1 ]]; then http2_form='http2 on'; else http2_form='listen ... http2'; fi
printf 'render-nginx: unified edge config rendered for %s (admin path redacted, HTTP/2 via %s)\n' "$domain" "$http2_form"
