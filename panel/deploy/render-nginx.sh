#!/usr/bin/env bash
# 渲染面板的 nginx 站点（模板 nginx-pandora.conf）。edge-tls.sh 的 apply / setup 调它，运维也可以直接跑。
#
#   render-nginx.sh [config.yaml] [deploy.env] [输出文件] [真实IP信任表]
#   缺省依次 /etc/pandora/config.yaml、/etc/pandora/deploy.env、/etc/nginx/conf.d/pandora.conf、
#   /etc/pandora/cloudflare-realip.conf（后两个参数只给测试用：模板里 include 的路径写死，生产上永远是缺省值）
#
# 域名取 config.yaml 顶层 public_base_url 那一行，后台前缀取 deploy.env 的 PANDORA_ADMIN_PATH；
# deploy.env 可选键 PANDORA_SUB_DOMAINS（逗号分隔的订阅域名，可带空格；不写或留空 = 没有订阅域名）；
# 证书自备在 /etc/pandora/tls/sub/<域名>/fullchain.pem 与 privkey.pem，缺证书的域名只渲染 80（ACME 校验 + 跳 https）并在 stderr 告警；
# 环境变量 PANDORA_SUB_TLS_DIR 只给测试换证书目录（必须是绝对路径）。
# 两个文件都逐键用 awk 读，绝不 source（deploy.env 里有迁移 DSN 与超级用户口令）。输出里不打印后台前缀。
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${1:-/etc/pandora/config.yaml}"
DEPLOY_ENV="${2:-/etc/pandora/deploy.env}"
OUTPUT_FILE="${3:-/etc/nginx/conf.d/pandora.conf}"
REALIP_FILE="${4:-/etc/pandora/cloudflare-realip.conf}"
TEMPLATE_FILE="$SCRIPT_DIR/nginx-pandora.conf"

die() { printf 'render-nginx: %s\n' "$*" >&2; exit 1; }

[[ -f "$CONFIG_FILE" ]] || die "config file not found"
[[ -f "$DEPLOY_ENV" ]] || die "deploy.env not found"
[[ -f "$TEMPLATE_FILE" ]] || die "nginx template not found"
[[ "$OUTPUT_FILE" = /* ]] || die "output path must be absolute"
[[ "$REALIP_FILE" = /* ]] || die "real-IP include path must be absolute"

# deploy.env 用 awk 逐键读，绝不 source。
deploy_value() {
  local key="$1" values
  mapfile -t values < <(awk -F= -v key="$key" '
    $1 == key {
      sub(/^[^=]*=/, "")
      sub(/\r$/, "")
      print
    }
  ' "$DEPLOY_ENV")
  [[ ${#values[@]} -eq 1 ]] || die "$key must occur exactly once"
  printf '%s' "${values[0]}"
}

deploy_optional_value() {
  local key="$1" values
  mapfile -t values < <(awk -F= -v key="$key" '
    $1 == key {
      sub(/^[^=]*=/, "")
      sub(/\r$/, "")
      print
    }
  ' "$DEPLOY_ENV")
  if [[ ${#values[@]} -eq 0 ]]; then
    printf ''
    return 0
  fi
  [[ ${#values[@]} -eq 1 ]] || die "$key must occur at most once"
  printf '%s' "${values[0]}"
}

admin_path="$(deploy_value PANDORA_ADMIN_PATH)"

[[ ${#admin_path} -ge 20 && ${#admin_path} -le 64 ]] || \
  die "PANDORA_ADMIN_PATH must be 20-64 characters"
[[ "$admin_path" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || \
  die "PANDORA_ADMIN_PATH must be one URL-safe path segment"
[[ "$admin_path" != "__PANDORA_ADMIN_PATH__" ]] || die "placeholder value is forbidden"
[[ "$admin_path" != CHANGE_ME* ]] || die "example/default value is forbidden"

# 边缘服务的就是面板拼接入命令、支付回调、订阅链接用的那个地址，所以主机从 public_base_url 取，不另配一遍。
# 只认 https、缺省端口：模板听 443，证书在 /etc/pandora/tls/live（edge-tls.sh 维护：域名或公网 IPv4 的
# Let's Encrypt 证书，申请不到时自签兜底）。规则与 public-base-url.sh 的 pandora_valid_public_base_url 相同
# （https://<DNS 域名或公网 IPv4>）；本脚本装在主机上单独运行，不 source 安装器的库，所以抄一份。
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

config_public_base_url() {
  local values
  mapfile -t values < <(awk '
    /^[^[:space:]]/ && /^public_base_url:[[:space:]]*/ {
      line = $0
      sub(/^public_base_url:[[:space:]]*/, "", line)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
      if (line ~ /^".*"$/) { sub(/^"/, "", line); sub(/"$/, "", line) }
      else if (line ~ /^\047.*\047$/) { sub(/^\047/, "", line); sub(/\047$/, "", line) }
      print line
    }
  ' "$CONFIG_FILE")
  [[ ${#values[@]} -eq 1 ]] || die "public_base_url must occur exactly once in config"
  printf '%s' "${values[0]}"
}

base_url="$(config_public_base_url)"
[[ -n "$base_url" ]] || die "public_base_url is empty"
[[ "$base_url" != *CHANGE_ME* ]] || die "public_base_url still holds the example value"
[[ "$base_url" =~ ^https://([^/:]+)/?$ ]] || \
  die "public_base_url must be https://<domain or public IPv4> with no port or path"
domain="${BASH_REMATCH[1],,}"
if ! public_ipv4 "$domain"; then
  [[ ${#domain} -le 253 ]] || die "domain is too long"
  [[ "$domain" != *.localhost ]] || die "public_base_url must not be a localhost name"
  [[ "$domain" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$ ]] || \
    die "public_base_url must name a DNS domain or a public IPv4 address (not a private, reserved or IPv6 address)"
fi

# 解析与校验订阅域名 PANDORA_SUB_DOMAINS
raw_sub_domains="$(deploy_optional_value PANDORA_SUB_DOMAINS)"
sub_domains=()
declare -A seen_sub_domains=()

if [[ -n "$raw_sub_domains" ]]; then
  IFS=',' read -r -a raw_sub_arr <<<"$raw_sub_domains"
  for item in "${raw_sub_arr[@]}"; do
    # 去首尾空白
    item="$(printf '%s' "$item" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
    [[ -n "$item" ]] || continue
    d="${item,,}"
    [[ ${#d} -le 253 ]] || die "subscription domain is too long"
    # 不能是 IPv4（只要是四段数字就拒）
    if [[ "$d" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      die "subscription domain must be a DNS name"
    fi
    [[ "$d" != *.localhost && "$d" != localhost ]] || die "subscription domain must not be a localhost name"
    [[ "$d" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$ ]] || \
      die "subscription domain must name a valid DNS domain"
    [[ "$d" != "$domain" ]] || die "subscription domain equals the panel domain"
    [[ -z "${seen_sub_domains[$d]:-}" ]] || die "duplicate subscription domain"
    seen_sub_domains["$d"]=1
    sub_domains+=("$d")
  done
  [[ ${#sub_domains[@]} -le 50 ]] || die "too many subscription domains (max 50)"
fi

sub_tls_dir="${PANDORA_SUB_TLS_DIR:-/etc/pandora/tls/sub}"
[[ "$sub_tls_dir" = /* ]] || die "PANDORA_SUB_TLS_DIR must be an absolute path"

sub_server_blocks() {
  local d="$1" has_cert="$2"
  cat <<EOF
# 订阅域名 $d 的 80：只有 ACME HTTP-01 校验目录（运维自己用 certbot/lego 的 webroot 申请证书）与 308 跳 https
server {
    listen 0.0.0.0:80;
    listen [::]:80;
    server_name $d;
    server_tokens off;
    access_log off;
    error_log /var/log/nginx/pandora-error.log crit;

    location ^~ /.well-known/acme-challenge/ {
        root /var/www/pandora-acme;
        default_type text/plain;
        try_files \$uri =404;
    }

    location / {
        return 308 https://\$host\$request_uri;
    }
}
EOF

  if [[ "$has_cert" -eq 1 ]]; then
    cat <<EOF

# 订阅域名 $d 的 443：只放行订阅拉取（GET /<12 位十六进制前缀>/<令牌>）与门户探测接口 /v1/sub-probe（GET/HEAD 回 204），
# 门户、后台、其余 API 一律 404（订阅域名不暴露后台前缀与登录入口）
server {
    listen 0.0.0.0:443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name $d;
    server_tokens off;
    include /etc/pandora/cloudflare-realip.conf;

    ssl_certificate $sub_tls_dir/$d/fullchain.pem;
    ssl_certificate_key $sub_tls_dir/$d/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:PandoraTLS:2m;
    ssl_session_timeout 1d;
    ssl_session_tickets off;

    client_max_body_size 16k;
    client_body_timeout 15s;
    client_header_timeout 15s;

    access_log /var/log/nginx/pandora-access.log pandora_safe;
    error_log /var/log/nginx/pandora-error.log crit;

    limit_conn pandora_conn 64;
    # 限速与连接数超限回 429，与主域名同口径（JSON 错误页只在主域名）
    limit_req_status 429;
    limit_conn_status 429;

    gzip on;
    gzip_types text/css text/javascript application/javascript application/json text/yaml text/plain image/svg+xml;
    gzip_comp_level 5;
    gzip_min_length 1024;
    gzip_vary on;

    location = /v1/sub-probe {
        # limit_except GET 会同时放行 HEAD 请求（nginx 缺省语义）；探测页、票据与上报接口只走门户主域名
        limit_except GET { deny all; }
        limit_req zone=pandora_api burst=60 nodelay;
        proxy_pass http://pandora_public;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host              \$host;
        proxy_set_header X-Real-IP         \$remote_addr;
        proxy_set_header X-Forwarded-For   \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_connect_timeout 5s;
        proxy_read_timeout 10s;
    }

    location ~ ^/[0-9a-f]{12}/[A-Za-z0-9_-]{16,}(\.[a-z]{2,4})?$ {
        # limit_except GET 会同时放行 HEAD 请求（nginx 缺省语义）
        limit_except GET { deny all; }
        limit_req zone=pandora_api burst=60 nodelay;
        proxy_pass http://pandora_public;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host              \$host;
        proxy_set_header X-Real-IP         \$remote_addr;
        proxy_set_header X-Forwarded-For   \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_connect_timeout 5s;
        proxy_send_timeout 30s;
        proxy_read_timeout 30s;
        proxy_buffering on;
    }

    location / {
        return 404;
    }
}
EOF
  fi
}

for placeholder in __PANDORA_ADMIN_PATH__ __PANDORA_DOMAIN__; do
  grep -q "$placeholder" "$TEMPLATE_FILE" || die "template placeholder $placeholder is missing"
done

# HTTP/2 的写法随 nginx 版本：1.25.1 起是独立的 http2 on;（listen 的 http2 参数已弃用、只告警），
# 更老的版本不认识 http2 指令，nginx -t 直接失败。模板按新写法写，这里按本机 nginx 版本取舍：
# 探不到版本（没装 nginx、在别处渲染）时用旧写法——它在新版本上也能用，只多一条弃用告警。
# PANDORA_NGINX_VERSION 可显式指定（测试与异机渲染用），形如 1.26.3
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

# 真实来源 IP 的信任表：模板在 server 块里 include 它，文件缺了 nginx -t 就失败。
# 只在它不存在时写一份「不信任任何代理」的默认文件：没有 set_real_ip_from，nginx 只认 TCP 对端，
# 客户端自己填的 CF-Connecting-IP / X-Real-IP 一律不采信（不在 Cloudflare 后面时信任它的头，
# 等于让任何人伪造来源 IP，绕过按 IP 的限流与风控）。已存在就不碰：在 Cloudflare 后面的站点由
# update-cloudflare-realip.sh 写入网段，升级时重新渲染不能把它冲掉。
ensure_realip_default() {
  [[ -e "$REALIP_FILE" ]] && return 0
  local dir tmp
  dir="$(dirname -- "$REALIP_FILE")"
  # 目录已在（/etc/pandora 是 root:pandora 0750）就不动它的权限
  [[ -d "$dir" ]] || install -d -m 0755 "$dir"
  tmp="$(mktemp "${REALIP_FILE}.tmp.XXXXXX")"
  cat >"$tmp" <<'REALIP'
# Pandora edge real-IP trust list, created by render-nginx.sh because none existed.
#
# Empty on purpose: no proxy is trusted, so nginx keeps the TCP peer as the
# client address and ignores any CF-Connecting-IP or X-Real-IP a client sends.
#
# Behind Cloudflare, replace this file with Cloudflare's published networks:
#   /opt/pandora/deploy/update-cloudflare-realip.sh
#   nginx -t && systemctl reload nginx
#
# render-nginx.sh never overwrites this file; upgrades keep whatever is here.
REALIP
  chmod 0644 "$tmp"
  mv -f -- "$tmp" "$REALIP_FILE"
  printf 'render-nginx: created %s (trusts no proxy; run update-cloudflare-realip.sh if the site is behind Cloudflare)\n' "$REALIP_FILE"
}
ensure_realip_default

# 主配置的 worker 与连接上限只能写在 nginx.conf 的 main / events 段，站点文件管不到。
# Debian 缺省 768 × worker_processes auto：每条 SSE 占一个客户端连接加一个上游连接，约一千条就顶满（5k-r4）。
# 这里把 worker_processes auto 改成 1（运维显式写的数字不动；一个 worker 足够，省下每个 worker 常驻的
# TLS 会话索引与缓冲），每个 worker 的 worker_connections 抬到至少 16384、worker_rlimit_nofile 至少 65536，
# 已经更高的不动。主配置位置：PANDORA_NGINX_MAIN_CONF 显式指定（none 表示不碰），否则只在输出文件位于
# <nginx 目录>/conf.d/ 下时取 <nginx 目录>/nginx.conf；不存在就跳过；结构不认识时不改、只提示。
NGINX_WORKER_CONNECTIONS=16384
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
      if (depth == 0 && code ~ /^[[:space:]]*worker_processes[[:space:]]+auto[[:space:]]*;/)
        line = lead($0) "worker_processes 1;"
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
  printf 'render-nginx: %s tuned (worker_processes 1 when it was auto, worker_connections >= %s, worker_rlimit_nofile >= %s)\n' \
    "$conf" "$NGINX_WORKER_CONNECTIONS" "$NGINX_RLIMIT_NOFILE"
}

main_conf="$(nginx_main_conf)"
[[ -z "$main_conf" ]] || tune_nginx_main "$main_conf"

output_dir="$(dirname -- "$OUTPUT_FILE")"
[[ -d "$output_dir" ]] || die "output directory does not exist"
tmp_file="$(mktemp "${OUTPUT_FILE}.tmp.XXXXXX")"
trap 'rm -f -- "$tmp_file"' EXIT

sub_blocks=""
sub_no_cert_count=0
if [[ ${#sub_domains[@]} -gt 0 ]]; then
  for d in "${sub_domains[@]}"; do
    has_cert=0
    if [[ -s "$sub_tls_dir/$d/fullchain.pem" && -s "$sub_tls_dir/$d/privkey.pem" ]]; then
      has_cert=1
    else
      (( sub_no_cert_count++ )) || true
      printf 'render-nginx: warning: no certificate for subscription domain %s in %s/%s; only port 80 rendered\n' \
        "$d" "$sub_tls_dir" "$d" >&2
    fi
    block="$(sub_server_blocks "$d" "$has_cert")"
    if [[ -z "$sub_blocks" ]]; then
      sub_blocks="$block"
    else
      sub_blocks="$(printf '%s\n\n%s' "$sub_blocks" "$block")"
    fi
  done
fi

http2_edit=()
if [[ "$http2_directive" -eq 0 ]]; then
  http2_edit=(-e '/^[[:space:]]*http2 on;/d' -e 's/^\([[:space:]]*listen [^;]*:443 ssl\);/\1 http2;/')
fi

if [[ -n "$sub_blocks" ]]; then
  { cat "$TEMPLATE_FILE"; printf '\n%s\n' "$sub_blocks"; } | sed -e "s/__PANDORA_ADMIN_PATH__/${admin_path}/g" -e "s/__PANDORA_DOMAIN__/${domain}/g" \
    ${http2_edit[@]+"${http2_edit[@]}"} >"$tmp_file"
else
  sed -e "s/__PANDORA_ADMIN_PATH__/${admin_path}/g" -e "s/__PANDORA_DOMAIN__/${domain}/g" \
    ${http2_edit[@]+"${http2_edit[@]}"} "$TEMPLATE_FILE" >"$tmp_file"
fi
chmod 0644 "$tmp_file"
mv -f -- "$tmp_file" "$OUTPUT_FILE"
trap - EXIT

if [[ "$http2_directive" -eq 1 ]]; then http2_form='http2 on'; else http2_form='listen ... http2'; fi
if [[ ${#sub_domains[@]} -gt 0 ]]; then
  if [[ "$sub_no_cert_count" -gt 0 ]]; then
    sub_summary=", ${#sub_domains[@]} subscription domain(s), ${sub_no_cert_count} without certificate"
  else
    sub_summary=", ${#sub_domains[@]} subscription domain(s)"
  fi
else
  sub_summary=""
fi
printf 'render-nginx: unified edge config rendered for %s (admin path redacted, HTTP/2 via %s%s)\n' "$domain" "$http2_form" "$sub_summary"
