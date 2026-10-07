#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf -- "$TEST_DIR"' EXIT

# 「不应出现」的断言不能写成取反的 grep：set -e 对取反的命令不生效，失败也不会退出。
refute() {
  if grep "$@"; then
    printf 'unexpected match: %s\n' "$*" >&2
    exit 1
  fi
}

path='ops_0123456789abcdef0123456789abcdef'
domain='panel.example.test'
base="AEGIS_PUBLIC_BASE_URL=https://$domain"
printf 'AEGIS_ADMIN_PATH=%s\n%s\n' "$path" "$base" >"$TEST_DIR/valid.env"
# 渲染结果随本机 nginx 版本变（HTTP/2 写法），测试一律显式指定版本，不受 runner 上装没装 nginx 影响
export PANDORA_NGINX_VERSION=1.26.3
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/valid.env" "$TEST_DIR/aegis.conf" "$TEST_DIR/realip.conf" >/dev/null

grep -Fq "location = /$path" "$TEST_DIR/aegis.conf"
# 不带尾斜杠只能重定向：入口页的相对路径要以 /$path/ 为基准，直接下发会把请求打到公开网关
grep -Fq "return 301 /$path/;" "$TEST_DIR/aegis.conf"
grep -Fq "location ^~ /$path/" "$TEST_DIR/aegis.conf"
grep -Fq "rewrite ^/$path(/.*)\$ \$1 break;" "$TEST_DIR/aegis.conf"
refute -Fq '__AEGIS_ADMIN_PATH__' "$TEST_DIR/aegis.conf"
refute -Fq 'listen 127.0.0.1:9081' "$TEST_DIR/aegis.conf"
# 公网明文「测试入口」已删除：除 80/443 与回环 9080 外不得再有别的 listen
grep -Fq 'listen 127.0.0.1:9080;' "$TEST_DIR/aegis.conf"
refute -Fq '7001' "$TEST_DIR/aegis.conf"
stray_listen="$(grep -vE '^[[:space:]]*#' "$TEST_DIR/aegis.conf" | grep -oE 'listen[[:space:]]+[^;]*;' \
  | grep -vxE 'listen[[:space:]]+(127\.0\.0\.1:9080|0\.0\.0\.0:80( default_server)?|\[::\]:80( default_server)?|0\.0\.0\.0:443 ssl( http2)?|\[::\]:443 ssl( http2)?);' || true)"
[[ -z "$stray_listen" ]] || { printf 'unexpected listen: %s\n' "$stray_listen" >&2; exit 1; }
grep -Fq 'listen 0.0.0.0:80 default_server' "$TEST_DIR/aegis.conf"
grep -Fq "server_name $domain;" "$TEST_DIR/aegis.conf"
grep -Fq 'listen 0.0.0.0:443 ssl' "$TEST_DIR/aegis.conf"
grep -Fq "/etc/letsencrypt/live/$domain/fullchain.pem" "$TEST_DIR/aegis.conf"
grep -Fq "/etc/letsencrypt/live/$domain/privkey.pem" "$TEST_DIR/aegis.conf"
refute -Fq '__AEGIS_DOMAIN__' "$TEST_DIR/aegis.conf"
# 模板属于产品，不能带任何一套具体部署的域名：server_name 只能是兜底的 _ 或 .env 给的域名
stray="$(grep -vE '^[[:space:]]*#' "$TEST_DIR/aegis.conf" | grep -oE 'server_name[[:space:]]+[^;]*;' \
  | grep -vxE "server_name[[:space:]]+(_|${domain//./\\.});" || true)"
[[ -z "$stray" ]] || { printf 'unexpected server_name: %s\n' "$stray" >&2; exit 1; }
grep -Fq 'include /etc/aegispanel/cloudflare-realip.conf' "$TEST_DIR/aegis.conf"

# 压缩：网关给 JS 发 text/javascript，必须在 gzip_types 里；级别 5
gzip_types="$(grep -E '^[[:space:]]*gzip_types ' "$TEST_DIR/aegis.conf")"
for type in text/css text/javascript application/javascript application/json image/svg+xml; do
  grep -Fqw "$type" <<<"$gzip_types" || { printf 'gzip_types misses %s\n' "$type" >&2; exit 1; }
done
grep -Eq '^[[:space:]]*gzip_comp_level 5;' "$TEST_DIR/aegis.conf"
# HTTP/2 下每个并发请求都计入 limit_conn，24 会把冷加载的资源 503 掉
grep -Eq '^[[:space:]]*limit_conn aegis_conn 64;' "$TEST_DIR/aegis.conf"

# HTTP/2 按 nginx 版本渲染：1.25.1 起 http2 on;，更老或探不到版本用 listen ... ssl http2
render_http2() {
  PANDORA_NGINX_VERSION="$1" "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/valid.env" "$TEST_DIR/h2.raw" "$TEST_DIR/realip.conf" >/dev/null
  # 只看指令，不看注释
  grep -vE '^[[:space:]]*#' "$TEST_DIR/h2.raw" >"$TEST_DIR/h2.conf" || true
}
expect_modern() {
  [[ "$(grep -cE '^[[:space:]]*http2 on;' "$TEST_DIR/h2.conf")" -eq 1 ]] || { printf 'nginx %s: want one http2 on;\n' "$1" >&2; exit 1; }
  refute -Eq 'listen[^;]*http2' "$TEST_DIR/h2.conf"
}
expect_legacy() {
  refute -Eq '^[[:space:]]*http2 on;' "$TEST_DIR/h2.conf"
  grep -Fq 'listen 0.0.0.0:443 ssl http2;' "$TEST_DIR/h2.conf"
  grep -Fq 'listen [::]:443 ssl http2;' "$TEST_DIR/h2.conf"
  # 只有 443 走 HTTP/2：80 与回环 9080 不带
  [[ "$(grep -cE 'listen[^;]*http2' "$TEST_DIR/h2.conf")" -eq 2 ]] || { printf 'nginx %s: http2 leaked onto a non-443 listen\n' "$1" >&2; exit 1; }
}
for v in 1.25.1 1.26.3 1.27.4 'nginx version: nginx/1.29.0 (Debian)' 2.0.0; do render_http2 "$v"; expect_modern "$v"; done
for v in 1.25.0 1.24.0 1.22.1 1.18.0 'nginx version: openresty/1.21.4.1' garbage; do render_http2 "$v"; expect_legacy "$v"; done

for invalid in short '../escape-path-0123456789' 'slash/path-0123456789abcdef' 'CHANGE_ME_TO_A_RANDOM_48_CHAR_PATH'; do
  printf 'AEGIS_ADMIN_PATH=%s\n%s\n' "$invalid" "$base" >"$TEST_DIR/invalid.env"
  if "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/invalid.env" "$TEST_DIR/rejected.conf" "$TEST_DIR/realip-rejected.conf" >/dev/null 2>&1; then
    printf 'expected invalid path to be rejected\n' >&2
    exit 1
  fi
done

# 域名只接受 https://<DNS 域名>：缺失、重复、示例值、http、带端口或路径、IP 都拒绝
reject_base() {
  printf 'AEGIS_ADMIN_PATH=%s\n' "$path" >"$TEST_DIR/invalid.env"
  printf '%s' "$1" >>"$TEST_DIR/invalid.env"
  if "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/invalid.env" "$TEST_DIR/rejected.conf" "$TEST_DIR/realip-rejected.conf" >/dev/null 2>&1; then
    printf 'expected base URL case to be rejected: %q\n' "$1" >&2
    exit 1
  fi
}
reject_base ''
reject_base $'AEGIS_PUBLIC_BASE_URL=https://a.example.test\nAEGIS_PUBLIC_BASE_URL=https://b.example.test\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://CHANGE_ME_TO_YOUR_PANEL_DOMAIN\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=http://panel.example.test\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://panel.example.test:8443\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://panel.example.test/sub\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://203.0.113.9\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://localhost\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://evil.test;include/x\n'

# 大写与结尾斜杠按同一个域名处理
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=https://Panel.Example.TEST/\n' "$path" >"$TEST_DIR/upper.env"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/upper.env" "$TEST_DIR/upper.conf" "$TEST_DIR/realip.conf" >/dev/null
grep -Fq "server_name $domain;" "$TEST_DIR/upper.conf"

# 拒绝渲染时不留下任何东西（信任表也不建）
[[ ! -e "$TEST_DIR/realip-rejected.conf" ]] || { printf 'rejected render created the real-IP file\n' >&2; exit 1; }

printf 'render-nginx tests passed\n'
