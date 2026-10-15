#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf -- "$TEST_DIR"' EXIT

refute() {
  if grep "$@"; then
    printf 'unexpected match: %s\n' "$*" >&2
    exit 1
  fi
}

banned_words() {
  refute -iE 'aegis|AEGIS_|VALKEY_|valkey|/etc/aegispanel' "$1"
}

# 每个含 proxy_pass 的 location 块里必须有 Host 与 X-Real-IP
assert_proxy_headers_per_location() {
  awk '
    function flush() {
      if (!inloc) return
      if (buf ~ /proxy_pass/) {
        if (buf !~ /proxy_set_header[[:space:]]+Host[[:space:]]+\$host;/) {
          printf "location missing Host: %s\n", locline > "/dev/stderr"
          exit 1
        }
        if (buf !~ /proxy_set_header[[:space:]]+X-Real-IP[[:space:]]+\$remote_addr;/) {
          printf "location missing X-Real-IP: %s\n", locline > "/dev/stderr"
          exit 1
        }
      }
      inloc = 0
      buf = ""
    }
    /^[[:space:]]*location / {
      flush()
      inloc = 1
      locline = $0
      buf = $0 "\n"
      next
    }
    inloc { buf = buf $0 "\n" }
    inloc && /^[[:space:]]*\}[[:space:]]*$/ { flush() }
    END { flush() }
  ' "$1"
}

path='ops_0123456789abcdef0123456789abcdef'
domain='panel.example.test'
printf 'public_base_url: https://%s\n' "$domain" >"$TEST_DIR/config.yaml"
printf 'PANDORA_ADMIN_PATH=%s\n' "$path" >"$TEST_DIR/deploy.env"
OUT="$TEST_DIR/pandora.conf"
REALIP="$TEST_DIR/realip.conf"

export PANDORA_NGINX_VERSION=1.26.3
export PANDORA_NGINX_MAIN_CONF=none
render_out="$("$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/config.yaml" "$TEST_DIR/deploy.env" "$OUT" "$REALIP")"
[[ -n "$render_out" ]]
refute -F "$path" <<<"$render_out"

refute -Fq '__PANDORA_' "$OUT"
grep -Fq "server_name $domain;" "$OUT"
grep -Fq "location = /$path/v1/auth/login" "$OUT"
grep -Fq "location = /$path/v1/auth/refresh" "$OUT"
grep -Fq "location = /$path/v1/events" "$OUT"
grep -Fq "return 301 /$path/;" "$OUT"
grep -Fq "location ^~ /$path/" "$OUT"
grep -Fq "proxy_cookie_path /v1/auth /$path/v1/auth;" "$OUT"
grep -Eq '^limit_req_zone \$binary_remote_addr zone=pandora_refresh:' "$OUT"
grep -Fq 'limit_req zone=pandora_refresh burst=20 nodelay;' "$OUT"
[[ "$(grep -c 'limit_req zone=pandora_refresh burst=20 nodelay;' "$OUT")" -eq 2 ]]

events_block() {
  awk -v want="$1" '
    index($0, want) { on = 1 }
    on { print }
    on && /^    }$/ { exit }
  ' "$OUT"
}
pub_events="$(events_block 'location = /v1/events')"
adm_events="$(events_block "location = /$path/v1/events")"
grep -Fq 'proxy_buffering off;' <<<"$pub_events"
grep -Fq 'proxy_read_timeout 1h;' <<<"$pub_events"
grep -Fq 'proxy_buffering off;' <<<"$adm_events"
grep -Fq 'proxy_read_timeout 1h;' <<<"$adm_events"

assert_proxy_headers_per_location "$OUT"

refute -Fq 'UniProxy' "$OUT"
refute -Fq '/v1/nodes/' "$OUT"
refute -Fq '9003' "$OUT"
banned_words "$OUT"

# HTTP/2：1.24.0 走 listen ... http2，不要 http2 on;
PANDORA_NGINX_VERSION=1.24.0 "$SCRIPT_DIR/render-nginx.sh" \
  "$TEST_DIR/config.yaml" "$TEST_DIR/deploy.env" "$TEST_DIR/legacy.conf" "$REALIP" >/dev/null
refute -Eq '^[[:space:]]*http2 on;' "$TEST_DIR/legacy.conf"
grep -Fq 'listen 0.0.0.0:443 ssl http2;' "$TEST_DIR/legacy.conf"
grep -Fq 'listen [::]:443 ssl http2;' "$TEST_DIR/legacy.conf"

reject_render() {
  local cfg="$1" dep="$2" out="$3" rip="$4"
  if "$SCRIPT_DIR/render-nginx.sh" "$cfg" "$dep" "$out" "$rip" >/dev/null 2>&1; then
    printf 'expected render to fail: cfg=%s dep=%s\n' "$cfg" "$dep" >&2
    exit 1
  fi
  [[ ! -e "$out" ]] || { printf 'rejected render left output file %s\n' "$out" >&2; exit 1; }
}

printf 'public_base_url: https://10.0.0.1\n' >"$TEST_DIR/bad-private.yaml"
reject_render "$TEST_DIR/bad-private.yaml" "$TEST_DIR/deploy.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'public_base_url: https://%s:8443\n' "$domain" >"$TEST_DIR/bad-port.yaml"
reject_render "$TEST_DIR/bad-port.yaml" "$TEST_DIR/deploy.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'public_base_url: https://%s/extra\n' "$domain" >"$TEST_DIR/bad-path.yaml"
reject_render "$TEST_DIR/bad-path.yaml" "$TEST_DIR/deploy.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'public_base_url: http://%s\n' "$domain" >"$TEST_DIR/bad-http.yaml"
reject_render "$TEST_DIR/bad-http.yaml" "$TEST_DIR/deploy.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'public_base_url: https://localhost\n' >"$TEST_DIR/bad-localhost.yaml"
reject_render "$TEST_DIR/bad-localhost.yaml" "$TEST_DIR/deploy.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'PANDORA_ADMIN_PATH=short\n' >"$TEST_DIR/bad-admin.env"
reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/bad-admin.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'PANDORA_ADMIN_PATH=__PANDORA_ADMIN_PATH__\n' >"$TEST_DIR/placeholder-admin.env"
reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/placeholder-admin.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'PANDORA_ADMIN_PATH=%s\nPANDORA_ADMIN_PATH=%s\n' "$path" "$path" >"$TEST_DIR/dup-admin.env"
reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/dup-admin.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf '# no public_base_url\nenv: production\n' >"$TEST_DIR/no-base.yaml"
reject_render "$TEST_DIR/no-base.yaml" "$TEST_DIR/deploy.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'public_base_url: https://203.0.113.10\n' >"$TEST_DIR/ip.yaml"
printf 'PANDORA_ADMIN_PATH=%s\n' "$path" >"$TEST_DIR/ip-deploy.env"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/ip.yaml" "$TEST_DIR/ip-deploy.env" "$TEST_DIR/ip.conf" "$REALIP" >/dev/null
[[ "$(grep -c 'server_name 203.0.113.10;' "$TEST_DIR/ip.conf")" -eq 2 ]]

# 真实 IP 信任表：不存在时创建默认内容；已存在则字节不变
[[ -e "$REALIP" ]] && rm -f "$REALIP"
NEW_REALIP="$TEST_DIR/fresh-realip.conf"
PANDORA_NGINX_MAIN_CONF=none "$SCRIPT_DIR/render-nginx.sh" \
  "$TEST_DIR/config.yaml" "$TEST_DIR/deploy.env" "$OUT" "$NEW_REALIP" >/dev/null
[[ -f "$NEW_REALIP" ]]
refute -Fq 'set_real_ip_from' "$NEW_REALIP"
existing='set_real_ip_from 203.0.113.0/24;'
printf '%s\n' "$existing" >"$TEST_DIR/keep-realip.conf"
cp "$TEST_DIR/keep-realip.conf" "$TEST_DIR/keep-realip.before"
PANDORA_NGINX_MAIN_CONF=none "$SCRIPT_DIR/render-nginx.sh" \
  "$TEST_DIR/config.yaml" "$TEST_DIR/deploy.env" "$OUT" "$TEST_DIR/keep-realip.conf" >/dev/null
cmp -s "$TEST_DIR/keep-realip.before" "$TEST_DIR/keep-realip.conf" \
  || { printf 'existing real-IP file was modified\n' >&2; exit 1; }

# 主配置调参
NGX="$TEST_DIR/etc-nginx"
mkdir -p "$NGX/conf.d"
printf 'user www-data;\nworker_processes auto;\npid /run/nginx.pid;\n\nevents {\n\tworker_connections 768;\n}\n\nhttp {\n\tinclude /etc/nginx/conf.d/*.conf;\n}\n' >"$NGX/nginx.conf"
export PANDORA_NGINX_MAIN_CONF="$NGX/nginx.conf"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/config.yaml" "$TEST_DIR/deploy.env" "$NGX/conf.d/pandora.conf" "$REALIP" >/dev/null
grep -Eq '^[[:space:]]*worker_connections 16384;' "$NGX/nginx.conf"
grep -Fq 'worker_processes 1;' "$NGX/nginx.conf"
[[ "$(grep -c 'worker_rlimit_nofile 65536;' "$NGX/nginx.conf")" -eq 1 ]]
cp "$NGX/nginx.conf" "$TEST_DIR/nginx.once"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/config.yaml" "$TEST_DIR/deploy.env" "$NGX/conf.d/pandora.conf" "$REALIP" >/dev/null
cmp -s "$TEST_DIR/nginx.once" "$NGX/nginx.conf" \
  || { printf 'second render changed nginx.conf\n' >&2; exit 1; }

# 订阅域名
mkdir -p "$TEST_DIR/subtls/sub-a.example.test"
printf 'cert-dummy\n' >"$TEST_DIR/subtls/sub-a.example.test/fullchain.pem"
printf 'key-dummy\n' >"$TEST_DIR/subtls/sub-a.example.test/privkey.pem"

printf 'PANDORA_ADMIN_PATH=%s\nPANDORA_SUB_DOMAINS= sub-a.example.test , SUB-B.example.test\n' "$path" >"$TEST_DIR/sub-deploy.env"
sub_err="$TEST_DIR/sub.err"
PANDORA_SUB_TLS_DIR="$TEST_DIR/subtls" "$SCRIPT_DIR/render-nginx.sh" \
  "$TEST_DIR/config.yaml" "$TEST_DIR/sub-deploy.env" "$TEST_DIR/sub.conf" "$REALIP" 2>"$sub_err"

[[ "$(grep -c 'server_name sub-a.example.test;' "$TEST_DIR/sub.conf")" -eq 2 ]]
[[ "$(grep -c 'server_name sub-b.example.test;' "$TEST_DIR/sub.conf")" -eq 1 ]]
grep -Fq 'no certificate for subscription domain sub-b.example.test' "$sub_err"
grep -Fq "ssl_certificate $TEST_DIR/subtls/sub-a.example.test/fullchain.pem;" "$TEST_DIR/sub.conf"
refute -Fq 'sub-b.example.test/fullchain.pem' "$TEST_DIR/sub.conf"
refute -Fq 'sub-b.example.test/privkey.pem' "$TEST_DIR/sub.conf"
[[ "$(grep -c 'location = /v1/sub-probe' "$TEST_DIR/sub.conf")" -eq 1 ]]
# 探测页、票据、上报只走门户主域名，订阅域名上不放行
refute -Fq 'sub-probe/' "$TEST_DIR/sub.conf"
[[ "$(grep -cE 'location ~ \^/\[0-9a-f\]\{12\}/' "$TEST_DIR/sub.conf")" -eq 1 ]]

[[ "$(grep -c "$path" "$TEST_DIR/sub.conf")" -eq "$(grep -c "$path" "$OUT")" ]]
refute -Fq '__PANDORA_' "$TEST_DIR/sub.conf"
assert_proxy_headers_per_location "$TEST_DIR/sub.conf"
banned_words "$TEST_DIR/sub.conf"
[[ "$(grep -c -F 'return 308 https://$host$request_uri;' "$TEST_DIR/sub.conf")" -ge 2 ]]

printf 'PANDORA_ADMIN_PATH=%s\nPANDORA_SUB_DOMAINS=\n' "$path" >"$TEST_DIR/sub-empty-deploy.env"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/config.yaml" "$TEST_DIR/sub-empty-deploy.env" "$TEST_DIR/sub-empty.conf" "$REALIP" >/dev/null
cmp -s "$OUT" "$TEST_DIR/sub-empty.conf" || { printf 'empty PANDORA_SUB_DOMAINS output differed from no sub domains\n' >&2; exit 1; }

PANDORA_NGINX_VERSION=1.24.0 PANDORA_SUB_TLS_DIR="$TEST_DIR/subtls" "$SCRIPT_DIR/render-nginx.sh" \
  "$TEST_DIR/config.yaml" "$TEST_DIR/sub-deploy.env" "$TEST_DIR/sub-legacy.conf" "$REALIP" >/dev/null 2>&1
[[ "$(grep -c 'listen 0.0.0.0:443 ssl http2;' "$TEST_DIR/sub-legacy.conf")" -eq 2 ]]
refute -Eq '^[[:space:]]*http2 on;' "$TEST_DIR/sub-legacy.conf"

printf 'PANDORA_ADMIN_PATH=%s\nPANDORA_SUB_DOMAINS=panel.example.test\n' "$path" >"$TEST_DIR/sub-rej-panel.env"
PANDORA_SUB_TLS_DIR="$TEST_DIR/subtls" reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/sub-rej-panel.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'PANDORA_ADMIN_PATH=%s\nPANDORA_SUB_DOMAINS=203.0.113.9\n' "$path" >"$TEST_DIR/sub-rej-ip.env"
PANDORA_SUB_TLS_DIR="$TEST_DIR/subtls" reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/sub-rej-ip.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'PANDORA_ADMIN_PATH=%s\nPANDORA_SUB_DOMAINS=bad_domain.example.test\n' "$path" >"$TEST_DIR/sub-rej-underscore.env"
PANDORA_SUB_TLS_DIR="$TEST_DIR/subtls" reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/sub-rej-underscore.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'PANDORA_ADMIN_PATH=%s\nPANDORA_SUB_DOMAINS=a.example.test,a.example.test\n' "$path" >"$TEST_DIR/sub-rej-dup.env"
PANDORA_SUB_TLS_DIR="$TEST_DIR/subtls" reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/sub-rej-dup.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'PANDORA_ADMIN_PATH=%s\nPANDORA_SUB_DOMAINS=foo.localhost\n' "$path" >"$TEST_DIR/sub-rej-localhost.env"
PANDORA_SUB_TLS_DIR="$TEST_DIR/subtls" reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/sub-rej-localhost.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

printf 'PANDORA_ADMIN_PATH=%s\nPANDORA_SUB_DOMAINS=a.example.test\nPANDORA_SUB_DOMAINS=b.example.test\n' "$path" >"$TEST_DIR/sub-rej-multi.env"
PANDORA_SUB_TLS_DIR="$TEST_DIR/subtls" reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/sub-rej-multi.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"

# 说明：deploy.env 按行读（awk + mapfile），真换行进不了同一个值；
# 第二行另起一个键会被忽略（上面 sub-rej-multi 已覆盖同名键出现两次会 die 的情况）。
# 下面是 PANDORA_SUB_DOMAINS 的非法值反例，全部必须被拒绝。
sub_rej() {
  local value_file="$1"
  printf 'PANDORA_ADMIN_PATH=%s\n' "$path" >"$TEST_DIR/sub-rej-common.env"
  cat "$value_file" "$TEST_DIR/sub-rej-common.env" >"$TEST_DIR/sub-rej-case.env"
  PANDORA_SUB_TLS_DIR="$TEST_DIR/subtls" reject_render "$TEST_DIR/config.yaml" "$TEST_DIR/sub-rej-case.env" "$TEST_DIR/rej.conf" "$TEST_DIR/rej-realip.conf"
}

printf 'PANDORA_SUB_DOMAINS=a.example.test;\n' >"$TEST_DIR/sub-rej-semicolon.value"
sub_rej "$TEST_DIR/sub-rej-semicolon.value"

printf 'PANDORA_SUB_DOMAINS=a{b}.example.test\n' >"$TEST_DIR/sub-rej-brace.value"
sub_rej "$TEST_DIR/sub-rej-brace.value"

printf 'PANDORA_SUB_DOMAINS=a}.example.test\n' >"$TEST_DIR/sub-rej-rbrace.value"
sub_rej "$TEST_DIR/sub-rej-rbrace.value"

# 单引号里不展开，文件里是字面的 $host
printf 'PANDORA_SUB_DOMAINS=$host.example.test\n' >"$TEST_DIR/sub-rej-dollar.value"
sub_rej "$TEST_DIR/sub-rej-dollar.value"

printf 'PANDORA_SUB_DOMAINS=a b.example.test\n' >"$TEST_DIR/sub-rej-space.value"
sub_rej "$TEST_DIR/sub-rej-space.value"

# 反斜杠加 n 两个字符（不是真换行）
printf 'PANDORA_SUB_DOMAINS=a.example.test\\nserver\n' >"$TEST_DIR/sub-rej-literal-n.value"
sub_rej "$TEST_DIR/sub-rej-literal-n.value"

{
  printf 'PANDORA_SUB_DOMAINS='
  for i in $(seq 1 51); do
    [[ "$i" -eq 1 ]] || printf ','
    printf 's%d.example.test' "$i"
  done
  printf '\n'
} >"$TEST_DIR/sub-rej-51.value"
sub_rej "$TEST_DIR/sub-rej-51.value"

printf 'PANDORA_SUB_DOMAINS=localhost\n' >"$TEST_DIR/sub-rej-bare-localhost.value"
sub_rej "$TEST_DIR/sub-rej-bare-localhost.value"

# 模板：仅两种占位符、恰好一行 http2 on;
[[ "$(grep -cE '__PANDORA_(ADMIN_PATH|DOMAIN)__' "$SCRIPT_DIR/nginx-pandora.conf")" -ge 2 ]]
if grep -oE '__[A-Z0-9_]+__' "$SCRIPT_DIR/nginx-pandora.conf" | grep -vxE '__PANDORA_ADMIN_PATH__|__PANDORA_DOMAIN__'; then
  printf 'template has unexpected placeholders\n' >&2
  exit 1
fi
[[ "$(grep -cE '^[[:space:]]*http2 on;' "$SCRIPT_DIR/nginx-pandora.conf")" -eq 1 ]]
banned_words "$SCRIPT_DIR/nginx-pandora.conf"

printf 'render-nginx tests passed\n'
