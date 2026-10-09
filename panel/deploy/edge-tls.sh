#!/usr/bin/env bash
# 面板 HTTPS 边缘：证书 → nginx 配置 → 续期，一处管完。install.sh 调它，装完后由
# aegis-tls-renew.timer 每天两次调 renew；出了问题，运维也直接用它补救。
#
#   edge-tls.sh setup  [.env]   放行防火墙 → 确保有证书（没有就先自签）→ 渲染 nginx 并生效
#                               → 申请 Let's Encrypt 正规证书 → 启用续期 timer
#   edge-tls.sh issue  [.env]   只申请正规证书并换上（nginx 已在 80 上提供校验目录）
#   edge-tls.sh renew  [.env]   timer 入口：续期、自签兜底时重试申请、证书换了就 reload、到期告警
#   edge-tls.sh status [.env]   打印当前证书来源、主机、到期时间与上次续期结论
#   edge-tls.sh ensure [.env]   只确保 live 下有一张覆盖面板主机的证书（setup 的第一步）
#   edge-tls.sh apply  [.env]   只渲染 nginx 配置 → nginx -t → reload（setup 的第二步）
#
# 证书按面板对外地址（.env 的 AEGIS_PUBLIC_BASE_URL，不 source，逐键读）的主机选：
#   DNS 域名   → certbot webroot 申请 Let's Encrypt 证书（90 天，Debian 的 certbot.timer 续期）
#   公网 IPv4  → lego webroot 申请 Let's Encrypt IP 证书（shortlived 配置，约 6 天，本脚本续期）。
#               Debian 13 主仓的 certbot 4.0 不支持 IP 证书（--ip-address 要 5.3+），主仓的
#               lego 4.9 不支持 ACME profiles（要 4.22+）；Debian 上自动从官方 backports 装 lego
#   申请不到   → 自签证书兜底（浏览器提示不安全），续期 timer 每次都重试申请，成功即无缝换上
# nginx 只认 /etc/aegispanel/tls/live/{fullchain,privkey}.pem：live 是指向证书目录的链接，
# 换证书只改链接再 reload，不重新渲染配置。
#
# 环境变量（安装器透传，第一次 setup / ensure 时记进 /etc/aegispanel/tls/acme.env，renew 沿用）：
#   PANDORA_ACME=0            不联系任何 CA，只用自签（也认旧名 PANDORA_CERTBOT=0）
#   PANDORA_ACME_EMAIL=邮箱    ACME 账号联系邮箱，可选（也认旧名 PANDORA_CERTBOT_EMAIL）
#   PANDORA_ACME_SERVER=URL   ACME 目录地址，缺省 Let's Encrypt 正式环境；真机演练可指向 staging
# 申请证书即表示同意 Let's Encrypt 的订户协议（https://letsencrypt.org/repository/）。
#
# 退出码：
#   setup / issue：0 在用正规证书（或运维自己指定的证书）；3 在用自签（申请失败或 PANDORA_ACME=0，
#                  nginx 已能用 https 打开）；1 出错，nginx 保持原配置（原因在 stderr）
#   renew：0 正常（含 PANDORA_ACME=0 的自签）；1 续期失败、证书快到期 / 已过期、或该申请却还在自签，
#          续期单元记为 failed，结论写进 /var/lib/aegispanel/tls/status
set -Eeuo pipefail
umask 022

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# 主机上的路径。PANDORA_* 覆盖只给桩测试用（edge-tls_mock_test.sh 指到临时目录）
TLS_DIR="${PANDORA_TLS_DIR:-/etc/aegispanel/tls}"
STATE_DIR="${PANDORA_TLS_STATE_DIR:-/var/lib/aegispanel/tls}"
LE_LIVE_DIR="${PANDORA_LE_LIVE_DIR:-/etc/letsencrypt/live}"
# certbot 的续期配置（<证书名>.conf）与 live 并排
LE_RENEWAL_DIR="${LE_LIVE_DIR%/*}/renewal"
ACME_WEBROOT="${PANDORA_ACME_WEBROOT:-/var/www/aegis-acme}"
NGINX_DIR="${PANDORA_NGINX_DIR:-/etc/nginx}"
REALIP_FILE="${PANDORA_REALIP_FILE:-/etc/aegispanel/cloudflare-realip.conf}"
BACKUP_DIR="${PANDORA_BACKUP_DIR:-/var/backups/pandora}"
OS_RELEASE="${PANDORA_OS_RELEASE:-/etc/os-release}"
APT_SOURCES_DIR="${PANDORA_APT_SOURCES_DIR:-/etc/apt}"
RENEW_TIMER=aegis-tls-renew.timer

LIVE="$TLS_DIR/live"
SELFSIGNED_DIR="$TLS_DIR/selfsigned"
LEGO_PATH="$TLS_DIR/acme"
LEGO_LIVE_DIR="$TLS_DIR/lego"
ACME_CONF="$TLS_DIR/acme.env"
STATUS_FILE="$STATE_DIR/status"
LOADED_FILE="$STATE_DIR/loaded.sha256"
LEGO_MIN_VERSION=4.22.0
SELFSIGNED_DAYS=365

say() { printf 'edge-tls: %s\n' "$*"; }
warn() { printf 'edge-tls: %s\n' "$*" >&2; }
die() { warn "$*"; exit 1; }

#------------------------------------------------------------------------------
# 读配置
#------------------------------------------------------------------------------
# 读 KEY=value 文件的单个键（最后一次出现为准），不 source
file_value() {
  [[ -f "$1" ]] || return 0
  awk -F= -v key="$2" '$1 == key { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$1"
}

# 面板主机与种类（domain / ip）。规则与 render-nginx.sh 相同；不合规就停
read_host() {
  local url
  [[ -f "$ENV_FILE" ]] || die "找不到 $ENV_FILE"
  url="$(file_value "$ENV_FILE" AEGIS_PUBLIC_BASE_URL)"
  [[ "$url" =~ ^https://([^/:]+)/?$ ]] \
    || die "AEGIS_PUBLIC_BASE_URL 不是 https://域名 或 https://公网IPv4（当前：${url:-空}），没法按它配证书"
  HOST="${BASH_REMATCH[1],,}"
  if public_ipv4 "$HOST"; then
    HOST_KIND=ip
  elif [[ ${#HOST} -le 253 && "$HOST" != *.localhost \
          && "$HOST" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$ ]]; then
    HOST_KIND=domain
  else
    die "AEGIS_PUBLIC_BASE_URL 的主机 $HOST 不是 DNS 域名或公网 IPv4"
  fi
}

# 与 public-base-url.sh、render-nginx.sh 同一份公网 IPv4 判断（本脚本装在主机上单独运行）
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

# ACME 设置：环境变量优先，其次 acme.env，最后缺省。renew 没有安装器的环境，只看 acme.env
load_acme_config() {
  local enabled email server
  enabled="${PANDORA_ACME:-}"
  [[ -n "$enabled" || "${PANDORA_CERTBOT:-}" != 0 ]] || enabled=0
  [[ -n "$enabled" ]] || enabled="$(file_value "$ACME_CONF" ACME_ENABLED)"
  [[ "$enabled" = 0 ]] && ACME_ENABLED=0 || ACME_ENABLED=1
  email="${PANDORA_ACME_EMAIL:-${PANDORA_CERTBOT_EMAIL:-}}"
  [[ -n "$email" ]] || email="$(file_value "$ACME_CONF" ACME_EMAIL)"
  server="${PANDORA_ACME_SERVER:-}"
  [[ -n "$server" ]] || server="$(file_value "$ACME_CONF" ACME_SERVER)"
  [[ -z "$email" || "$email" =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]+$ ]] || die "ACME 邮箱不合规：$email"
  [[ -z "$server" || "$server" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._~/-]*)?$ ]] || die "ACME 目录地址不合规：$server"
  ACME_EMAIL="$email" ACME_SERVER="$server"
}

save_acme_config() {
  local tmp
  install -d -m 0755 "$TLS_DIR"
  tmp="$(mktemp "$ACME_CONF.tmp.XXXXXX")"
  {
    printf '# edge-tls.sh 的 ACME 设置（安装时记下，续期 timer 沿用）。改完执行 edge-tls.sh issue\n'
    printf 'ACME_ENABLED=%s\nACME_EMAIL=%s\nACME_SERVER=%s\n' "$ACME_ENABLED" "$ACME_EMAIL" "$ACME_SERVER"
  } >"$tmp"
  chmod 0644 "$tmp"
  mv -f -- "$tmp" "$ACME_CONF"
}

#------------------------------------------------------------------------------
# 证书检查
#------------------------------------------------------------------------------
cert_file() { printf '%s/fullchain.pem' "$1"; }
key_file() { printf '%s/privkey.pem' "$1"; }

# 目录里有一对可读的证书与私钥，证书覆盖面板主机，且还没过期
cert_usable() {
  local dir="$1" cert key
  cert="$(cert_file "$dir")" key="$(key_file "$dir")"
  [[ -s "$cert" && -s "$key" ]] || return 1
  openssl x509 -in "$cert" -noout -checkend 0 >/dev/null 2>&1 || return 1
  cert_covers_host "$cert"
}

cert_covers_host() {
  local out
  if [[ "$HOST_KIND" = ip ]]; then
    out="$(openssl x509 -in "$1" -noout -checkip "$HOST" 2>/dev/null)" || return 1
  else
    out="$(openssl x509 -in "$1" -noout -checkhost "$HOST" 2>/dev/null)" || return 1
  fi
  [[ "$out" == *"does match"* && "$out" != *"NOT match"* ]]
}

# openssl 打的日期（notAfter=Oct  8 12:00:00 2026 GMT）转成 epoch 秒；GNU date 与 BSD date 都认
date_epoch() {
  date -u -d "$1" +%s 2>/dev/null || date -u -j -f '%b %e %T %Y %Z' "$1" +%s 2>/dev/null
}
cert_not_after() { openssl x509 -in "$1" -noout -enddate 2>/dev/null | sed 's/^notAfter=//'; }
cert_not_before() { openssl x509 -in "$1" -noout -startdate 2>/dev/null | sed 's/^notBefore=//'; }

# live 当前指向哪种证书：certbot / lego / selfsigned / custom（运维自己放的）/ none
live_mode() {
  local target
  [[ -L "$LIVE" ]] || { [[ -e "$LIVE" ]] && echo custom || echo none; return 0; }
  target="$(readlink "$LIVE")"
  case "$target" in
    "$SELFSIGNED_DIR") echo selfsigned ;;
    "$LEGO_LIVE_DIR") echo lego ;;
    "$LE_LIVE_DIR"/*) echo certbot ;;
    *) echo custom ;;
  esac
}

# 原子地把 live 指到某个证书目录（GNU mv -T；别处退回 ln -sfn）
point_live() {
  local target="$1" tmp="$TLS_DIR/.live.next"
  # 运维把 live 换成了真目录：不动它（ln -sfn 会把链接建进目录里），说清楚怎么交回给本脚本
  [[ ! -d "$LIVE" || -L "$LIVE" ]] \
    || die "$LIVE 是运维自建的目录，不替换；要交给 edge-tls.sh 管理，先把它移走再执行 setup"
  install -d -m 0755 "$TLS_DIR"
  rm -f -- "$tmp"
  ln -s -- "$target" "$tmp"
  if ! mv -Tf -- "$tmp" "$LIVE" 2>/dev/null; then
    rm -f -- "$tmp"
    ln -sfn -- "$target" "$LIVE"
  fi
}

make_selfsigned() {
  local tmp san
  if [[ "$HOST_KIND" = ip ]]; then san="IP:$HOST"; else san="DNS:$HOST"; fi
  install -d -m 0700 "$SELFSIGNED_DIR"
  tmp="$(mktemp -d "$TLS_DIR/.selfsigned.XXXXXX")"
  if ! openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
        -keyout "$tmp/privkey.pem" -out "$tmp/fullchain.pem" -days "$SELFSIGNED_DAYS" \
        -subj "/CN=$HOST" -addext "subjectAltName=$san" \
        -addext "basicConstraints=critical,CA:FALSE" -addext "keyUsage=critical,digitalSignature" \
        -addext "extendedKeyUsage=serverAuth" >/dev/null 2>&1; then
    rm -rf -- "$tmp"
    die "openssl 生成自签证书失败（要 OpenSSL 1.1.1 以上）"
  fi
  chmod 0600 "$tmp/privkey.pem"
  chmod 0644 "$tmp/fullchain.pem"
  mv -f -- "$tmp/privkey.pem" "$SELFSIGNED_DIR/privkey.pem"
  mv -f -- "$tmp/fullchain.pem" "$SELFSIGNED_DIR/fullchain.pem"
  rm -rf -- "$tmp"
  say "已生成覆盖 $HOST 的自签证书（$SELFSIGNED_DAYS 天，浏览器会提示不安全）"
}

#------------------------------------------------------------------------------
# certbot 续期配置：接管来的证书，续期也要走本脚本的 nginx 提供的校验目录
#------------------------------------------------------------------------------
# 运维手工（或更早的安装器）用 certbot 申请的证书，续期配置（renewal/<证书名>.conf）里的 webroot 常是
# /var/www/html；nginx 模板只从 ACME_WEBROOT 提供 /.well-known/acme-challenge/，不改的话
# certbot.timer 与 renew 的补救续期都拿不到校验文件（404），要到快过期才发现。
certbot_lineage() { basename -- "$(readlink "$LIVE")"; }

# 续期配置 [renewalparams] 段（不含子段）里某个键的值
renewal_param() {
  awk -v want="$2" '
    /^[ \t]*\[\[/ { insub = 1; next }
    /^[ \t]*\[/ { top = $0; gsub(/[][ \t\r]/, "", top); insub = 0; next }
    top == "renewalparams" && !insub && index($0, "=") {
      k = $0; sub(/^[ \t]+/, "", k); sub(/[ \t]*=.*$/, "", k)
      if (k == want) { v = $0; sub(/^[^=]*=[ \t]*/, "", v); sub(/[ \t\r]+$/, "", v); gsub(/^["\047]|["\047]$/, "", v); print v; exit }
    }' "$1"
}

# 改写续期配置的 awk 程序（文件读两遍：第一遍收集，第二遍输出）。-v host= 面板主机，-v new= ACME_WEBROOT。
# 只改面板主机那一项：[[webroot_map]] 里它的目录（没有这一项就补上）；webroot_path 里同一个旧目录
# 没被别的域名共用时一并改（certbot 拿它给没列进 webroot_map 的域名兜底）。别的域名一律不动：
# 面板的 80 端口只替面板主机应答校验，它们要靠本机别的站点。
# 退出码：0 已改写（新内容在 stdout）；10 已指向 new，不用改；3 不是 webroot 方式；4 认不出
renewal_rewrite_awk() {
  cat <<'AWK'
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t\r]+$/, "", s); return s }
function unq(s,   c) {
  s = trim(s)
  c = substr(s, 1, 1)
  if (length(s) >= 2 && (c == "\"" || c == "\047") && substr(s, length(s), 1) == c) s = substr(s, 2, length(s) - 2)
  return s
}
function norm(p) { p = unq(p); while (length(p) > 1 && p ~ /\/$/) p = substr(p, 1, length(p) - 1); return p }
# 段头：一级段更新 s1 并清 s2，二级段（[[...]]）更新 s2
function header(l,   n) {
  if (l !~ /^[ \t]*\[/) return 0
  n = l; gsub(/[][ \t\r]/, "", n)
  if (l ~ /^[ \t]*\[\[/) s2 = n; else { s1 = n; s2 = "" }
  return 1
}
function keyof(l) { l = trim(l); if (l ~ /^#/ || index(l, "=") == 0) return ""; sub(/[ \t]*=.*$/, "", l); return tolower(l) }
function valof(l) { sub(/^[^=]*=/, "", l); return trim(l) }
# webroot_path 是逗号分隔的列表：等于 old 的项换成 new；单项列表末尾的逗号保留（configobj 靠它认列表）
function fixlist(v,   n, items, i, out, item, tail) {
  tail = (trim(v) ~ /,$/)
  n = split(v, items, ",")
  out = ""
  for (i = 1; i <= n; i++) {
    item = trim(items[i])
    if (item == "") continue
    if (norm(item) == old) item = new
    out = out (out == "" ? "" : ", ") item
  }
  return out (tail ? "," : "")
}
NR == FNR {
  if (header($0)) { if (s1 == "renewalparams") { seenrp = 1; if (s2 == "webroot_map") hasmap = 1 }; next }
  k = keyof($0)
  if (k == "") next
  if (s1 == "renewalparams" && s2 == "") {
    if (k == "authenticator") auth = unq(valof($0))
    else if (k == "webroot_path") wp = valof($0)
  } else if (s1 == "renewalparams" && s2 == "webroot_map") {
    map[k] = norm(valof($0))
  }
  next
}
FNR == 1 {
  rplast = (s1 == "renewalparams"); s1 = ""; s2 = ""
  new = norm(new)
  if (!seenrp) { code = 4; exit }
  if (auth != "webroot") { code = 3; exit }
  if (host in map) old = map[host]
  else {
    n = split(wp, parts, ","); old = ""
    for (i = n; i >= 1; i--) { p = norm(parts[i]); if (p != "") { old = p; break } }
  }
  if (old == new) { code = 10; exit }
  shared = 0
  for (k in map) if (k != host && map[k] == old) shared = 1
  addhost = !(host in map)
  # 要补 [[webroot_map]] 段而 [renewalparams] 又不是最后一段：追加到文件末尾会落错段，不冒险
  if (addhost && !hasmap && !rplast) { code = 4; exit }
  started = 1
}
{
  if (header($0)) {
    print
    if (addhost && s1 == "renewalparams" && s2 == "webroot_map") { print host " = " new; addhost = 0 }
    next
  }
  k = keyof($0)
  if (s1 == "renewalparams" && s2 == "" && k == "webroot_path" && old != "" && !shared) { print "webroot_path = " fixlist(valof($0)); next }
  if (s1 == "renewalparams" && s2 == "webroot_map" && k == host) { print host " = " new; next }
  print
}
END {
  if (code) exit code
  if (!started) exit 4
  if (addhost) { print "[[webroot_map]]"; print host " = " new }
}
AWK
}

# 把 live 指向的 certbot 证书的续期配置对齐到 ACME_WEBROOT：改前备份到 BACKUP_DIR，换文件是原子的；
# 已经对齐就不动；不是 webroot 方式（dns 插件等，运维自己的选择）不碰。
# 返回 0 正常；1 续期配置缺失、认不出或写不进（原因在 REASON，调用方记进 status / 告警）
align_certbot_renewal() {
  local lineage conf auth tmp backup rc=0 others
  lineage="$(certbot_lineage)"
  conf="$LE_RENEWAL_DIR/$lineage.conf"
  if [[ ! -f "$conf" ]]; then
    REASON="找不到 certbot 的续期配置 $conf，这张证书不会自动续期：执行 certbot certonly --webroot -w $ACME_WEBROOT -d $HOST --cert-name $lineage 重新申请"
    return 1
  fi
  auth="$(renewal_param "$conf" authenticator)"
  if [[ "$auth" != webroot ]]; then
    say "certbot 续期用的是 ${auth:-未知} 方式校验，不是 webroot，续期配置不动（$conf）"
    return 0
  fi
  tmp="$(mktemp "$conf.pandora.XXXXXX")" || { REASON="没法在 $LE_RENEWAL_DIR 写临时文件，续期配置没改"; return 1; }
  # 先复制一份让临时文件继承原文件的权限与属主，再整体覆写内容
  cp -p -- "$conf" "$tmp" || { rm -f -- "$tmp"; REASON="复制 $conf 失败，续期配置没改"; return 1; }
  awk -v host="$HOST" -v new="$ACME_WEBROOT" "$(renewal_rewrite_awk)" "$conf" "$conf" >"$tmp" || rc=$?
  case "$rc" in
    0) ;;
    10) rm -f -- "$tmp"; return 0 ;;
    *)
      rm -f -- "$tmp"
      REASON="认不出 certbot 续期配置 $conf 的 webroot 设置，没有改：手工把 [[webroot_map]] 里 $HOST 的目录改成 $ACME_WEBROOT 后执行 certbot renew --dry-run --cert-name $lineage"
      return 1 ;;
  esac
  backup="$BACKUP_DIR/letsencrypt-renewal-$lineage.conf.$(date +%Y%m%d-%H%M%S)"
  if ! install -d -m 0700 "$BACKUP_DIR" || ! cp -p -- "$conf" "$backup"; then
    rm -f -- "$tmp"
    REASON="备份 $conf 失败，续期配置没改（续期校验仍会失败）"
    return 1
  fi
  mv -f -- "$tmp" "$conf" || { rm -f -- "$tmp"; REASON="替换 $conf 失败，续期配置没改"; return 1; }
  say "certbot 续期改走 $ACME_WEBROOT 校验（$conf；原文件备份在 $backup）"
  others="$(awk -v host="$HOST" '
    /^[ \t]*\[\[/ { m = ($0 ~ /webroot_map/); next }
    /^[ \t]*\[/ { m = 0; next }
    m && index($0, "=") { k = $0; sub(/^[ \t]+/, "", k); sub(/[ \t]*=.*$/, "", k); if (tolower(k) != host) printf "%s%s", (n++ ? " " : ""), k }' "$conf")"
  [[ -z "$others" ]] || say "  证书还包含 $others：面板的 nginx 只替 $HOST 应答校验，它们的续期要靠本机别的站点提供 /.well-known/acme-challenge/"
}

# status 用：只读地说明续期配置现在是否走 ACME_WEBROOT
renewal_alignment() {
  local conf rc=0
  conf="$LE_RENEWAL_DIR/$(certbot_lineage).conf"
  [[ -f "$conf" ]] || { echo "找不到续期配置 $conf，证书不会自动续期"; return 0; }
  awk -v host="$HOST" -v new="$ACME_WEBROOT" "$(renewal_rewrite_awk)" "$conf" "$conf" >/dev/null 2>&1 || rc=$?
  case "$rc" in
    10) echo "webroot $ACME_WEBROOT（$conf）" ;;
    0) echo "webroot 还指着别处，下次 renew 会改到 $ACME_WEBROOT（$conf）" ;;
    3) echo "$(renewal_param "$conf" authenticator) 方式，不经本机 nginx（$conf）" ;;
    *) echo "认不出 $conf 的 webroot 设置" ;;
  esac
}

#------------------------------------------------------------------------------
# ensure：live 下要有一张覆盖面板主机的证书，nginx 才能起来
#------------------------------------------------------------------------------
cmd_ensure() {
  read_host
  load_acme_config
  save_acme_config
  install -d -m 0755 "$TLS_DIR"
  if [[ -e "$LIVE" ]] && cert_usable "$LIVE"; then
    say "沿用现有证书（$(live_mode)，覆盖 $HOST）"
    # 以前接管过、续期配置还没改过来的（老版本 edge-tls.sh 接管的），这次补上
    [[ "$(live_mode)" != certbot ]] || align_certbot_renewal || warn "$REASON"
    return 0
  fi
  # 本机已有 certbot 申请过的这个域名的证书（运维手工或更早的安装器申请的），直接接管
  if [[ "$HOST_KIND" = domain ]] && cert_usable "$LE_LIVE_DIR/$HOST"; then
    point_live "$LE_LIVE_DIR/$HOST"
    say "接管已有的 Let's Encrypt 证书 $LE_LIVE_DIR/$HOST"
    align_certbot_renewal || warn "$REASON"
    return 0
  fi
  if [[ "$HOST_KIND" = ip ]] && cert_usable "$LEGO_LIVE_DIR"; then
    point_live "$LEGO_LIVE_DIR"
    say "换回已有的 Let's Encrypt IP 证书"
    return 0
  fi
  cert_usable "$SELFSIGNED_DIR" || make_selfsigned
  point_live "$SELFSIGNED_DIR"
  say "先用自签证书让 nginx 起来，随后申请正规证书"
}

#------------------------------------------------------------------------------
# apply：渲染 → nginx -t → reload。原配置先备份；nginx -t 不过就原样换回，nginx 继续跑旧配置
#------------------------------------------------------------------------------
nginx_reload() {
  if systemctl is-active --quiet nginx; then
    systemctl reload nginx
  else
    systemctl enable --now nginx >/dev/null 2>&1 || systemctl start nginx
  fi
}

# Debian/Ubuntu 的 nginx 包自带默认站点，它也 listen 80 default_server，与 aegis.conf 抢同一个
# 位置，nginx -t 报 duplicate default server。只停用包里原样的那个链接（sites-enabled/default →
# sites-available/default），原文件留着，随时能链回去；别的冲突不替人处理，交给 nginx -t 报出来
disable_stock_default_site() {
  local link="$NGINX_DIR/sites-enabled/default"
  [[ -L "$link" ]] || return 0
  case "$(readlink "$link")" in
    */sites-available/default|../sites-available/default) ;;
    *) return 0 ;;
  esac
  grep -Eq '^[[:space:]]*listen[^;#]*default_server' "$link" 2>/dev/null || return 0
  rm -f -- "$link"
  say "停用了 nginx 自带的默认站点（它与面板抢 80 端口的 default_server）"
  say "  原文件还在 $NGINX_DIR/sites-available/default，要恢复：ln -s ../sites-available/default $link"
}

cmd_apply() {
  local render="$SCRIPT_DIR/render-nginx.sh" out="$NGINX_DIR/conf.d/aegis.conf" prev="" log
  # render-nginx.sh 顺带把主配置 nginx.conf 的 worker_connections / worker_rlimit_nofile 抬上去，
  # 它同样先备份、nginx -t 不过就一起换回
  local main_conf="$NGINX_DIR/nginx.conf" main_prev=""
  [[ -f "$render" ]] || die "找不到 $render"
  disable_stock_default_site
  install -d -m 0755 "$NGINX_DIR/conf.d" "$ACME_WEBROOT"
  if [[ -f "$out" || -f "$main_conf" ]]; then
    install -d -m 0700 "$BACKUP_DIR" || return 1
  fi
  if [[ -f "$out" ]]; then
    prev="$BACKUP_DIR/nginx-aegis.conf.$(date +%Y%m%d-%H%M%S)"
    cp -p -- "$out" "$prev" || { warn "备份原 nginx 配置失败，没有改动"; return 1; }
  fi
  if [[ -f "$main_conf" ]]; then
    main_prev="$BACKUP_DIR/nginx-main.conf.$(date +%Y%m%d-%H%M%S)"
    cp -p -- "$main_conf" "$main_prev" || { warn "备份 nginx.conf 失败，没有改动"; return 1; }
  fi
  if ! PANDORA_NGINX_MAIN_CONF="$main_conf" bash "$render" "$ENV_FILE" "$out" "$REALIP_FILE"; then
    warn "render-nginx.sh 拒绝渲染（原因见上），nginx 配置没动"
    return 1
  fi
  log="$(nginx -t 2>&1)" || {
    if [[ -n "$prev" ]]; then cp -p -- "$prev" "$out"; else rm -f -- "$out"; fi
    if [[ -n "$main_prev" ]]; then cp -p -- "$main_prev" "$main_conf"; fi
    printf '%s\n' "$log" | sed 's/^/    /' >&2
    warn "新渲染的 nginx 配置没通过 nginx -t，已换回原来的（${prev:-原来没有 aegis.conf}${main_prev:+；nginx.conf 也已换回}）"
    return 1
  }
  nginx_reload
  record_loaded
  say "nginx 配置已渲染并生效：$out${prev:+（原配置备份在 $prev）}"
}

# 记下 nginx 当前加载的证书，renew 据此判断要不要 reload
record_loaded() {
  local sum
  [[ -s "$LIVE/fullchain.pem" ]] || return 0
  install -d -m 0755 "$STATE_DIR"
  sum="$(sha256sum <"$LIVE/fullchain.pem" | awk '{print $1}')"
  printf '%s\n' "$sum" >"$LOADED_FILE"
}

# 证书文件变了（certbot.timer 续期、lego 续期、换了 live）就 nginx -t 后 reload
reload_if_changed() {
  local sum loaded
  [[ -s "$LIVE/fullchain.pem" ]] || return 0
  sum="$(sha256sum <"$LIVE/fullchain.pem" | awk '{print $1}')"
  loaded="$(cat "$LOADED_FILE" 2>/dev/null || true)"
  [[ "$sum" != "$loaded" ]] || return 0
  if ! nginx -t >/dev/null 2>&1; then
    warn "证书换了，但 nginx -t 不通过，没有 reload：nginx -t"
    return 1
  fi
  nginx_reload
  record_loaded
  say "证书已更新，nginx 已 reload"
}

#------------------------------------------------------------------------------
# issue：申请 Let's Encrypt 正规证书，换上，reload
#------------------------------------------------------------------------------
version_at_least() {
  [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" = "$2" ]]
}

lego_version() { lego --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1; }

# IP 证书要 lego 4.22+（ACME profiles）。没有就在 Debian 上从官方 backports 装：
# backports 默认优先级 100，只有点名 -t <版本>-backports 才从那里装，不会顺带升级别的包。
# 续期 timer 不装包（$1=no-install），装不上只记结论
ensure_lego() {
  local v codename id sources
  v="$(lego_version || true)"
  if [[ -n "$v" ]] && version_at_least "$v" "$LEGO_MIN_VERSION"; then return 0; fi
  if [[ "${1:-}" = no-install ]]; then
    REASON="没有 $LEGO_MIN_VERSION 以上的 lego（现在：${v:-没装}）"
    return 1
  fi
  id="$(file_value "$OS_RELEASE" ID)"; id="${id//\"/}"
  codename="$(file_value "$OS_RELEASE" VERSION_CODENAME)"; codename="${codename//\"/}"
  if [[ "$id" != debian || -z "$codename" ]] || ! command -v apt-get >/dev/null 2>&1; then
    REASON="IP 证书要 $LEGO_MIN_VERSION 以上的 lego（现在：${v:-没装}），本机不是 Debian，没法自动装；装好放进 PATH 后执行 $SCRIPT_DIR/edge-tls.sh issue"
    return 1
  fi
  if ! grep -rqsE "^[^#]*[[:space:]]${codename}-backports([[:space:]]|$)" "$APT_SOURCES_DIR/sources.list" "$APT_SOURCES_DIR/sources.list.d" \
     && ! grep -rqsE "^Suites:.*[[:space:]]${codename}-backports([[:space:]]|$)" "$APT_SOURCES_DIR/sources.list.d"; then
    sources="$APT_SOURCES_DIR/sources.list.d/pandora-${codename}-backports.sources"
    say "IP 证书要 lego $LEGO_MIN_VERSION+，启用 Debian 官方 backports 源：$sources"
    printf 'Types: deb\nURIs: http://deb.debian.org/debian\nSuites: %s-backports\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' \
      "$codename" >"$sources"
    chmod 0644 "$sources"
  fi
  say "从 ${codename}-backports 安装 lego"
  # 别的第三方源坏了也会让 update 报错，不因此放弃：装不上再说
  apt-get update -qq >/dev/null 2>&1 || true
  if ! DEBIAN_FRONTEND=noninteractive apt-get install -y -qq -t "${codename}-backports" lego >/dev/null 2>&1; then
    REASON="apt-get 从 ${codename}-backports 安装 lego 失败（软件源不可达？）"
    return 1
  fi
  v="$(lego_version || true)"
  if [[ -z "$v" ]] || ! version_at_least "$v" "$LEGO_MIN_VERSION"; then
    REASON="装上的 lego 版本是 ${v:-未知}，低于 $LEGO_MIN_VERSION"
    return 1
  fi
}

lego_args() {
  LEGO_ARGS=(--path "$LEGO_PATH" --accept-tos --key-type ec256 --disable-cn
    --http --http.webroot "$ACME_WEBROOT" --domains "$HOST")
  [[ -z "$ACME_EMAIL" ]] || LEGO_ARGS+=(--email "$ACME_EMAIL")
  [[ -z "$ACME_SERVER" ]] || LEGO_ARGS+=(--server "$ACME_SERVER")
}

# lego 把证书写成 certificates/<主机>.crt / .key；lego/ 下放两条固定名字的链接给 live 用
link_lego_files() {
  install -d -m 0755 "$LEGO_LIVE_DIR"
  ln -sfn -- "$LEGO_PATH/certificates/$HOST.crt" "$LEGO_LIVE_DIR/fullchain.pem"
  ln -sfn -- "$LEGO_PATH/certificates/$HOST.key" "$LEGO_LIVE_DIR/privkey.pem"
}

obtain_ip_certificate() {
  local install_mode="${1:-}"
  ensure_lego "$install_mode" || return 1
  lego_args
  install -d -m 0700 "$LEGO_PATH"
  if ! lego "${LEGO_ARGS[@]}" run --profile shortlived; then
    REASON="Let's Encrypt 没有签出 $HOST 的 IP 证书（输出见上）：确认 80 端口从公网可达、这个 IP 确实是本机的公网地址"
    return 1
  fi
  link_lego_files
  cert_usable "$LEGO_LIVE_DIR" || { REASON="lego 报告成功，但 $LEGO_PATH/certificates/$HOST.crt 不可用"; return 1; }
}

obtain_domain_certificate() {
  local args
  if ! command -v certbot >/dev/null 2>&1; then
    if [[ "${1:-}" != no-install ]] && command -v apt-get >/dev/null 2>&1; then
      say "没有 certbot，从发行版主仓安装"
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq certbot >/dev/null 2>&1 || true
    fi
    command -v certbot >/dev/null 2>&1 || { REASON="没有 certbot：apt-get install -y certbot 后执行 $SCRIPT_DIR/edge-tls.sh issue"; return 1; }
  fi
  args=(certonly --webroot -w "$ACME_WEBROOT" -d "$HOST" --non-interactive --agree-tos)
  if [[ -n "$ACME_EMAIL" ]]; then args+=(--email "$ACME_EMAIL"); else args+=(--register-unsafely-without-email); fi
  [[ -z "$ACME_SERVER" ]] || args+=(--server "$ACME_SERVER")
  if ! certbot "${args[@]}"; then
    REASON="certbot 没有拿到 $HOST 的证书（输出见上）：确认域名解析到本机、80 端口从公网可达"
    return 1
  fi
  cert_usable "$LE_LIVE_DIR/$HOST" || { REASON="certbot 报告成功，但 $LE_LIVE_DIR/$HOST 下没有可用证书"; return 1; }
}

# 申请并换上。$1=no-install 时不装任何软件包（续期 timer 用）
issue_and_switch() {
  local target prev
  REASON=""
  if [[ "$ACME_ENABLED" != 1 ]]; then
    REASON="PANDORA_ACME=0：按设置不联系 CA（改 $ACME_CONF 的 ACME_ENABLED=1 后执行 edge-tls.sh issue）"
    return 3
  fi
  install -d -m 0755 "$ACME_WEBROOT"
  if [[ "$HOST_KIND" = ip ]]; then
    say "申请 Let's Encrypt IP 证书（$HOST，shortlived 约 6 天，自动续期；即表示同意 Let's Encrypt 订户协议）"
    obtain_ip_certificate "${1:-}" || return 1
    target="$LEGO_LIVE_DIR"
  else
    say "申请 Let's Encrypt 证书（$HOST；即表示同意 Let's Encrypt 订户协议）"
    obtain_domain_certificate "${1:-}" || return 1
    target="$LE_LIVE_DIR/$HOST"
  fi
  prev="$(readlink "$LIVE" 2>/dev/null || true)"
  point_live "$target"
  if ! reload_if_changed; then
    [[ -z "$prev" ]] || point_live "$prev"
    REASON="新证书已签出，但 nginx -t 不通过，已换回原来的证书"
    return 1
  fi
  say "已换上 Let's Encrypt 证书：https://$HOST"
}

cmd_issue() {
  local rc=0
  read_host
  load_acme_config
  [[ -e "$LIVE" ]] || die "还没有配好 nginx 边缘：先执行 $SCRIPT_DIR/edge-tls.sh setup"
  if cert_usable "$LIVE"; then
    case "$(live_mode)" in
      certbot|lego)
        say "已经在用覆盖 $HOST 的 Let's Encrypt 证书，不用再申请"
        write_status ok "Let's Encrypt 证书在用"
        return 0 ;;
      custom)
        # 运维自己放的证书（live 指向别处）：不替换。要改用 Let's Encrypt，先删掉 live 链接再 setup
        say "在用运维自己指定的证书（$LIVE → $(readlink "$LIVE" 2>/dev/null || echo 普通文件)），不替换"
        write_status ok "在用运维指定的证书"
        return 0 ;;
    esac
  fi
  issue_and_switch || rc=$?
  if [[ "$rc" -eq 0 ]]; then
    write_status ok "Let's Encrypt 证书在用"
    return 0
  fi
  warn "$REASON"
  explain_fallback
  write_status warn "$REASON"
  return 3
}

explain_fallback() {
  warn "现在用的是自签证书：https://$HOST 能打开，但浏览器会提示不安全；节点用 https 接入面板会校验证书失败。"
  warn "  补救：确认 80/tcp 从公网可达（云厂商安全组、防火墙），然后执行"
  warn "    sudo $SCRIPT_DIR/edge-tls.sh issue"
  warn "  不用手动：续期 timer（$RENEW_TIMER）每天两次自动重试，申请成功就无缝换上正规证书。"
}

#------------------------------------------------------------------------------
# 状态与告警
#------------------------------------------------------------------------------
# status 文件：renew / issue 每次的结论，KEY=value，运维与巡检都能读
write_status() {
  local result="$1" message="$2" tmp not_after=""
  install -d -m 0755 "$STATE_DIR"
  [[ ! -s "$LIVE/fullchain.pem" ]] || not_after="$(cert_not_after "$LIVE/fullchain.pem")"
  tmp="$(mktemp "$STATUS_FILE.tmp.XXXXXX")"
  {
    printf 'RESULT=%s\nMODE=%s\nHOST=%s\nNOT_AFTER=%s\nCHECKED_AT=%s\nMESSAGE=%s\n' \
      "$result" "$(live_mode)" "${HOST:-}" "$not_after" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${message//$'\n'/ }"
  } >"$tmp"
  chmod 0644 "$tmp"
  mv -f -- "$tmp" "$STATUS_FILE"
}

# 走巡检同一个告警渠道（.env 的 AEGIS_ALERT_TG_TOKEN / AEGIS_ALERT_TG_CHAT，可选）。
# 只在结论变了、或正规证书快到期 / 已过期时推，不每次都推
alert() {
  local text="$1" token chat
  token="$(file_value "$ENV_FILE" AEGIS_ALERT_TG_TOKEN)"
  chat="$(file_value "$ENV_FILE" AEGIS_ALERT_TG_CHAT)"
  [[ -n "$token" && -n "$chat" ]] || return 0
  curl -sS -m 15 -X POST "https://api.telegram.org/bot${token}/sendMessage" \
    --data-urlencode "chat_id=${chat}" --data-urlencode "text=${text}" >/dev/null 2>&1 \
    || warn "告警推送失败"
}

#------------------------------------------------------------------------------
# renew：timer 入口
#------------------------------------------------------------------------------
cmd_renew() {
  local mode result=ok message="证书正常" now end start left life threshold urgent=0 prev_result prev_message
  if [[ ! -e "$LIVE" ]]; then
    say "还没有配 nginx 边缘（$LIVE 不存在），没有要续期的证书"
    return 0
  fi
  read_host
  load_acme_config
  prev_result="$(file_value "$STATUS_FILE" RESULT)"
  prev_message="$(file_value "$STATUS_FILE" MESSAGE)"
  REASON=""
  case "$(live_mode)" in
    lego)
      if [[ "$ACME_ENABLED" = 1 ]] && ensure_lego no-install; then
        lego_args
        # --dynamic：短证书过半即续（约剩 3 天），ARI 给出的续期窗口优先；随机延迟交给 timer
        lego "${LEGO_ARGS[@]}" renew --dynamic --profile shortlived --no-random-sleep \
          || REASON="lego 续期失败（输出见上）：确认 80 端口从公网可达"
      fi ;;
    certbot)
      # Debian 的 certbot.timer 负责续期，这里只负责换了就 reload；快到期时下面再补一次。
      # 续期配置的校验目录每次核一遍：老版本接管的证书在这里自愈；修不好的原因留在 REASON，下面记进 status
      align_certbot_renewal || : ;;
    selfsigned)
      # 兜底期间每次都重试申请；成功就无缝换上（不装软件包，装包只在安装器与手动 issue 里做）
      if [[ "$ACME_ENABLED" = 1 ]] && ! issue_and_switch no-install; then
        REASON="仍在用自签证书：$REASON"
      fi ;;
  esac
  mode="$(live_mode)"
  [[ -z "$REASON" ]] || { result=error; message="$REASON"; warn "$REASON"; }
  reload_if_changed || { result=error; message="证书已更新但 nginx -t 不通过，没有 reload"; }

  if [[ -s "$LIVE/fullchain.pem" ]]; then
    now="$(date -u +%s)"
    end="$(date_epoch "$(cert_not_after "$LIVE/fullchain.pem")" || true)"
    start="$(date_epoch "$(cert_not_before "$LIVE/fullchain.pem")" || true)"
    if [[ -n "$end" && -n "$start" ]]; then
      left=$(( end - now )); life=$(( end - start ))
      if [[ "$mode" = selfsigned ]]; then
        # 自签证书自己续：剩不到 30 天就重签一张
        if (( left < 30 * 86400 )); then make_selfsigned; reload_if_changed || true; fi
      else
        # 剩余 < min(14 天, 寿命 / 3) 告警（与 healthcheck.sh 同一阈值）：6 天的 IP 证书过半就续，
        # 约剩 2.2 天才报；90 天的域名证书 certbot 剩 30 天续，按 14 天报
        threshold=$(( life / 3 )); (( threshold < 14 * 86400 )) || threshold=$(( 14 * 86400 ))
        if (( left < threshold )) && [[ "$mode" = certbot && "$ACME_ENABLED" = 1 ]] && command -v certbot >/dev/null 2>&1; then
          # 失败不能吞掉：原因记进 status 并告警（certbot 的报错在 stderr，进续期单元的日志）
          if ! certbot renew --cert-name "$(certbot_lineage)" --non-interactive -q; then
            REASON="${REASON:+$REASON；}certbot 续期失败（报错见 journalctl -u aegis-tls-renew 与 /var/log/letsencrypt/letsencrypt.log）"
            result=error message="$REASON"
            warn "$REASON"
          fi
          reload_if_changed || true
          end="$(date_epoch "$(cert_not_after "$LIVE/fullchain.pem")" || true)"
          left=$(( ${end:-0} - now ))
        fi
        if (( left <= 0 )); then
          result=error urgent=1 message="证书已过期（$(cert_not_after "$LIVE/fullchain.pem")）${REASON:+：$REASON}"
        elif (( left < threshold )); then
          result=error urgent=1 message="证书还剩 $(( left / 3600 )) 小时到期，续期没成功${REASON:+：$REASON}"
        fi
      fi
    fi
    if ! cert_covers_host "$LIVE/fullchain.pem"; then
      result=error urgent=1
      message="在用的证书不覆盖面板地址 $HOST（对外地址改过？）：重跑安装脚本或 $SCRIPT_DIR/edge-tls.sh setup"
    fi
  fi
  if [[ "$result" = ok && "$mode" = selfsigned ]]; then
    result=warn message="按设置只用自签证书（PANDORA_ACME=0）"
  fi
  write_status "$result" "$message"
  say "$result：$message"
  # 告警只在结论变了、或正规证书快到期 / 已过期时推；首次就正常不推
  if [[ "$urgent" = 1 || "$result" != "$prev_result" || ( "$result" != ok && "$message" != "$prev_message" ) ]]; then
    [[ "$result" = ok && -z "$prev_result" ]] || alert "潘多拉面板 HTTPS 证书（$HOST）：$message"
  fi
  case "$result" in
    ok) return 0 ;;
    warn) return 0 ;;
    *) return 1 ;;
  esac
}

cmd_status() {
  local f="$LIVE/fullchain.pem"
  read_host
  printf '面板主机   %s（%s）\n' "$HOST" "$([[ "$HOST_KIND" = ip ]] && echo 公网 IPv4 || echo 域名)"
  printf '证书来源   %s\n' "$(live_mode)"
  if [[ "$(live_mode)" = certbot ]]; then
    printf '续期校验   %s\n' "$(renewal_alignment)"
  fi
  if [[ -s "$f" ]]; then
    printf '到期时间   %s\n' "$(cert_not_after "$f")"
    printf '签发者     %s\n' "$(openssl x509 -in "$f" -noout -issuer 2>/dev/null | sed 's/^issuer=[[:space:]]*//')"
  fi
  if [[ -f "$STATUS_FILE" ]]; then
    printf '上次检查   %s %s：%s\n' "$(file_value "$STATUS_FILE" CHECKED_AT)" \
      "$(file_value "$STATUS_FILE" RESULT)" "$(file_value "$STATUS_FILE" MESSAGE)"
  fi
}

#------------------------------------------------------------------------------
# setup：首装与补救的一条命令
#------------------------------------------------------------------------------
open_firewall() {
  command -v ufw >/dev/null 2>&1 || return 0
  ufw status 2>/dev/null | grep -q '^Status: active' || return 0
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  say "ufw 已开启：放行了 80/tcp（证书校验）与 443/tcp（HTTPS）"
}

enable_renew_timer() {
  if systemctl cat "$RENEW_TIMER" >/dev/null 2>&1; then
    systemctl enable --now "$RENEW_TIMER" >/dev/null 2>&1 \
      && say "续期 timer 已启用：$RENEW_TIMER（每天两次）" \
      || warn "启用 $RENEW_TIMER 失败：systemctl enable --now $RENEW_TIMER"
  else
    warn "没有找到 $RENEW_TIMER 单元，证书不会自动续期"
  fi
}

cmd_setup() {
  open_firewall
  cmd_ensure
  cmd_apply || return 1
  enable_renew_timer
  # 已在用正规证书（或运维自己的）就不再联系 CA；自签兜底时申请
  cmd_issue
}

#------------------------------------------------------------------------------
cmd="${1:-}"
ENV_FILE="${2:-$SCRIPT_DIR/.env}"
HOST="" HOST_KIND="" REASON=""

# 安装器与 timer 可能同时动 live：串行化
if [[ -n "$cmd" && "$cmd" != status ]] && command -v flock >/dev/null 2>&1; then
  install -d -m 0755 "$STATE_DIR"
  exec 8>"$STATE_DIR/lock"
  flock -w 600 8 || die "等了 10 分钟仍有另一个 edge-tls.sh 在跑"
fi

case "$cmd" in
  setup) cmd_setup ;;
  ensure) cmd_ensure ;;
  apply) read_host; cmd_apply ;;
  issue) cmd_issue ;;
  renew) cmd_renew ;;
  status) cmd_status ;;
  *) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
esac
