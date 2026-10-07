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
# callbacks and subscription links from, so the domain is taken from that URL
# rather than configured twice. HTTPS on the default port only: the template
# listens on 443 and looks up the certificate by this name.
base_url="$(env_value AEGIS_PUBLIC_BASE_URL)"
[[ "$base_url" != *CHANGE_ME* ]] || die "AEGIS_PUBLIC_BASE_URL still holds the example value"
[[ "$base_url" =~ ^https://([^/:]+)/?$ ]] || \
  die "AEGIS_PUBLIC_BASE_URL must be https://<domain> with no port or path"
domain="${BASH_REMATCH[1],,}"
[[ ${#domain} -le 253 ]] || die "domain is too long"
[[ "$domain" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$ ]] || \
  die "AEGIS_PUBLIC_BASE_URL must name a DNS domain, not an IP address"

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
