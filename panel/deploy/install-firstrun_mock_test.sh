#!/usr/bin/env bash
# 部署上手三件事的桩测试：不需要 root 或数据库。
#   ① admin-url.sh：读 .env 打印完整后台地址，不 source、不打印别的值；
#   ② 首装交互式建管理员（install-lib.sh 的 pandora_bootstrap_admin）：密码不进命令行参数、
#      环境变量、输出；已有管理员跳过；无人值守不问；
#   ③ 收尾提示（install-lib.sh 的 print_install_summary）：「现在可以做什么 → 还差什么 → 常用操作」，
#      首装与升级分开说；升级不再打印后台地址本身；
# 再静态核对发布包与安装器都带上了 admin-url.sh、install-lib.sh，建管理员在健康检查之后、HTTPS 边缘之前。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-firstrun: %s\n' "$*" >&2; exit 1; }
refute() { if grep "$@"; then fail "unexpected match: $*"; fi; }

ADMIN_PATH_VALUE='ops_0123456789abcdef0123456789abcdef'
SECRET_VALUE='Sup3r-Secret-Master-Key-Value'

# --- ① admin-url.sh ---------------------------------------------------------------
mkdir -p "$T/opt/deploy"
cp "$DEPLOY/admin-url.sh" "$T/opt/deploy/admin-url.sh"
chmod 0755 "$T/opt/deploy/admin-url.sh"
cat >"$T/opt/deploy/.env" <<EOF
AEGIS_MASTER_KEY=$SECRET_VALUE
AEGIS_ADMIN_PATH=old_value_overridden_below_000000000
AEGIS_PUBLIC_BASE_URL=https://panel.example.test/
AEGIS_ADMIN_PATH=$ADMIN_PATH_VALUE
\$(touch $T/sourced)
EOF
out="$(bash "$T/opt/deploy/admin-url.sh")" || fail 'admin-url.sh failed on a complete .env'
[ "$out" = "https://panel.example.test/$ADMIN_PATH_VALUE/" ] || fail "full URL: $out"
[ ! -e "$T/sourced" ] || fail 'admin-url.sh executed the .env'
# 指定别的 .env；没有对外地址时只给路径并说明
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=CHANGE_ME\n' "$ADMIN_PATH_VALUE" >"$T/other.env"
out="$(bash "$T/opt/deploy/admin-url.sh" "$T/other.env" 2>"$T/err")" || fail 'path-only case failed'
[ "$out" = "/$ADMIN_PATH_VALUE/" ] || fail "path-only: $out"
grep -q 'AEGIS_PUBLIC_BASE_URL' "$T/err" || fail 'path-only case does not explain itself'
# 缺后台前缀、读不到文件：非零退出，不打印半截地址
printf 'AEGIS_PUBLIC_BASE_URL=https://panel.example.test\n' >"$T/nopath.env"
if bash "$T/opt/deploy/admin-url.sh" "$T/nopath.env" >"$T/out" 2>/dev/null; then fail 'missing admin path accepted'; fi
[ ! -s "$T/out" ] || fail 'printed something without an admin path'
if bash "$T/opt/deploy/admin-url.sh" "$T/missing.env" >/dev/null 2>&1; then fail 'missing .env accepted'; fi
printf 'AEGIS_ADMIN_PATH=../etc\n' >"$T/bad.env"
if bash "$T/opt/deploy/admin-url.sh" "$T/bad.env" >/dev/null 2>&1; then fail 'unsafe admin path accepted'; fi
bash "$T/opt/deploy/admin-url.sh" >"$T/url.out" 2>&1; refute -q "$SECRET_VALUE" "$T/url.out"

# --- ② 交互式建管理员 ---------------------------------------------------------------
# shellcheck source=install-lib.sh
. "$DEPLOY/install-lib.sh"

# 只在首装 + 终端里问；测试的标准输入不是终端，所以这里总是 false
if pandora_admin_prompt_wanted upgrade </dev/null; then fail 'upgrade asked for an administrator'; fi
if pandora_admin_prompt_wanted install </dev/null; then fail 'a non-terminal stdin asked for an administrator'; fi
if PANDORA_ASSUME_YES=1 pandora_admin_prompt_wanted install; then fail 'PANDORA_ASSUME_YES=1 asked'; fi
if PANDORA_NONINTERACTIVE=1 pandora_admin_prompt_wanted install; then fail 'PANDORA_NONINTERACTIVE=1 asked'; fi

# aegis-adminctl 桩：记下参数、标准输入、环境里有没有密码；HAS_ADMIN_RC 决定 has-admin 的退出码；
# 密码不是 Strong-Pass-1 就按强度不够拒绝
cat >"$T/adminctl" <<'MOCK'
#!/usr/bin/env bash
log="$(dirname "$0")/adminctl.log"
case "$1" in
  has-admin) printf 'has-admin\n' >>"$log"; exit "${HAS_ADMIN_RC:-3}" ;;
  create)
    stdin="$(cat)"
    printf 'argv=%s\n' "$*" >>"$log"
    printf 'stdin=%s\n' "$stdin" >>"$log"
    if env | grep -q -e 'Strong-Pass-1' -e 'weak'; then printf 'env-leak\n' >>"$log"; fi
    [ "$stdin" = Strong-Pass-1 ] || { echo '错误: 密码强度不够' >&2; exit 1; }
    echo '管理员已就绪'
    ;;
esac
MOCK
chmod 0755 "$T/adminctl"
ALOG="$T/adminctl.log"
bootstrap() { : >"$ALOG"; PANDORA_ADMIN_STATE=""; PANDORA_ADMIN_EMAIL=""; pandora_bootstrap_admin "$T/adminctl" >"$T/boot.out" 2>&1; }

# 两次一致：建成；密码只经标准输入
printf 'admin@example.test\nStrong-Pass-1\nStrong-Pass-1\n' | { bootstrap; printf '%s %s\n' "$PANDORA_ADMIN_STATE" "$PANDORA_ADMIN_EMAIL" >"$T/state"; }
[ "$(cat "$T/state")" = 'created admin@example.test' ] || fail "state: $(cat "$T/state"); $(cat "$T/boot.out")"
grep -qx 'argv=create --email admin@example.test --password-stdin' "$ALOG" || fail "argv: $(cat "$ALOG")"
grep -qx 'stdin=Strong-Pass-1' "$ALOG" || fail 'password did not arrive on stdin'
refute -q 'env-leak' "$ALOG"
refute -q 'Strong-Pass-1' "$T/boot.out"
grep -q '管理员已就绪' "$T/boot.out" || fail 'adminctl output not shown'

# 两次不一致 → 重来；强度不够 → 重来；第三次成功
printf 'a@example.test\nStrong-Pass-1\nTypo-Pass\na@example.test\nweak\nweak\na@example.test\nStrong-Pass-1\nStrong-Pass-1\n' \
  | { bootstrap; printf '%s\n' "$PANDORA_ADMIN_STATE" >"$T/state"; }
[ "$(cat "$T/state")" = created ] || fail "retry flow: $(cat "$T/boot.out")"
[ "$(grep -c '^argv=' "$ALOG")" = 2 ] || fail 'mismatched passwords were sent to adminctl'
grep -q '不一致' "$T/boot.out" || fail 'mismatch not explained'
refute -q -e 'Strong-Pass-1' -e 'Typo-Pass' -e 'weak$' "$T/boot.out"
refute -q 'env-leak' "$ALOG"

# 三次都不成：留给手工，安装不算失败
printf 'a@example.test\nweak\nweak\na@example.test\nweak\nweak\na@example.test\nweak\nweak\n' \
  | { bootstrap || fail 'bootstrap failure must not fail the install'; printf '%s\n' "$PANDORA_ADMIN_STATE" >"$T/state"; }
[ "$(cat "$T/state")" = manual ] || fail 'three failures did not fall back to manual'

# 直接回车：跳过，不调 create
printf '\n' | { bootstrap; printf '%s\n' "$PANDORA_ADMIN_STATE" >"$T/state"; }
[ "$(cat "$T/state")" = manual ] && ! grep -q '^argv=' "$ALOG" || fail 'empty email did not skip'

# 已有管理员：不问、不建
printf 'x@example.test\nStrong-Pass-1\nStrong-Pass-1\n' | { HAS_ADMIN_RC=0 bootstrap; printf '%s\n' "$PANDORA_ADMIN_STATE" >"$T/state"; }
[ "$(cat "$T/state")" = existing ] && ! grep -q '^argv=' "$ALOG" || fail "existing admin was not skipped: $(cat "$ALOG")"
# 查不了（连不上库等）：不问，留给手工
printf 'x@example.test\nStrong-Pass-1\nStrong-Pass-1\n' | { HAS_ADMIN_RC=1 bootstrap; printf '%s\n' "$PANDORA_ADMIN_STATE" >"$T/state"; }
[ "$(cat "$T/state")" = manual ] && ! grep -q '^argv=' "$ALOG" || fail 'has-admin failure still created an account'

# --- ③ 收尾提示 ---------------------------------------------------------------------
# shellcheck source=public-base-url.sh
. "$DEPLOY/public-base-url.sh"
declare -F print_install_summary >/dev/null || fail 'install-lib.sh does not define print_install_summary'

INSTALL_DIR="$T/opt-pandora"; D="$INSTALL_DIR/deploy"
mkdir -p "$D"
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=https://panel.example.test\nAEGIS_MASTER_KEY=%s\n' \
  "$ADMIN_PATH_VALUE" "$SECRET_VALUE" >"$D/.env"
url="https://panel.example.test/$ADMIN_PATH_VALUE/"
summary() { print_install_summary >"$T/summary" 2>&1; }
todo() { awk '/还差什么/{t=1} /常用操作/{t=0} t' "$T/summary"; }
order_ok() {
  local a b c
  a="$(grep -n '现在可以做什么' "$T/summary" | cut -d: -f1)"
  b="$(grep -n '还差什么' "$T/summary" | cut -d: -f1)"
  c="$(grep -n '常用操作' "$T/summary" | cut -d: -f1)"
  [ -n "$a" ] && [ -n "$b" ] && [ -n "$c" ] && [ "$a" -lt "$b" ] && [ "$b" -lt "$c" ]
}

# 首装、HTTPS 已配好（正规证书）、管理员已建；每日备份还没开：还差什么里有开备份与另存私钥
MODE=install EDGE_STATE=trusted PANDORA_ADMIN_STATE=created PANDORA_ADMIN_EMAIL=admin@example.test BACKUP_TIMER=disabled
MIGRATION_BEFORE=0 MIGRATION_AFTER=134 BK=""
summary
order_ok || fail "install sections out of order: $(cat "$T/summary")"
grep -q '安装完成' "$T/summary" || fail 'install summary title'
grep -Fq "打开管理后台：$url" "$T/summary" || fail 'install summary lacks the full admin URL'
grep -q 'admin@example.test' "$T/summary" || fail 'install summary does not name the new administrator'
grep -Fq "sudo $D/admin-url.sh" "$T/summary" || fail 'install summary does not mention admin-url.sh'
refute -q 'aegis-adminctl create' "$T/summary"
refute -q "$SECRET_VALUE" "$T/summary"
todo | grep -Fq 'systemctl enable --now aegis-backup.timer' && todo | grep -Fq "$INSTALL_DIR/secrets/backup-age.key" \
  || fail "install summary does not ask to enable backups and keep the key elsewhere: $(cat "$T/summary")"
todo | grep -Fq "sudo $D/update-cloudflare-realip.sh" || fail 'install summary lacks the Cloudflare step'
# 每日备份已开：不再列
BACKUP_TIMER=enabled
summary
if todo | grep -q 'aegis-backup.timer'; then fail 'enabled backups are still listed as missing'; fi

# 首装、证书没申请下来（自签兜底）、管理员没建：照样给后台地址并说明自签；还差什么里有换证书的命令
# 与建管理员命令（密码不回显、走标准输入）
MODE=install EDGE_STATE=selfsigned PANDORA_ADMIN_STATE=manual
summary
order_ok || fail 'manual install sections out of order'
grep -Fq "打开管理后台：$url" "$T/summary" && grep -q '自签证书' "$T/summary" || fail 'self-signed install summary lacks the URL or the warning'
todo | grep -Fq "sudo $D/edge-tls.sh issue" || fail 'self-signed summary lacks the issue command under 还差什么'
grep -Fq "read -rsp" "$T/summary" && grep -Fq -- '--password-stdin' "$T/summary" \
  || fail "manual admin command must read the password silently and pass it on stdin: $(cat "$T/summary")"
refute -q -- '--password [^-]' "$T/summary"
todo | grep -q 'aegis-adminctl create' || fail 'the create command is not under 还差什么'

# 首装、HTTPS 边缘没配上（没装 nginx / setup 失败 / PANDORA_SKIP_NGINX=1）：给地址，说清楚补哪一步
for state in no-nginx failed skipped; do
  MODE=install EDGE_STATE=$state PANDORA_ADMIN_STATE=created
  summary
  order_ok || fail "$state sections out of order"
  grep -Fq "$url" "$T/summary" || fail "$state summary lacks the admin URL"
  refute -Fq "打开管理后台：$url" "$T/summary"
  todo | grep -Fq "sudo $D/edge-tls.sh setup" || fail "$state summary lacks the edge setup command: $(cat "$T/summary")"
done
MODE=install EDGE_STATE=no-nginx; summary
todo | grep -Fq 'apt-get install -y nginx' || fail 'no-nginx summary does not say to install nginx'

# 升级一台没走 nginx 边缘的面板：给出切到 HTTPS 的命令（edge-tls.sh setup，不用整个重装一遍）
MODE=upgrade EDGE_STATE=not-enabled PANDORA_ADMIN_STATE=manual
summary
todo | grep -Fq "sudo $D/edge-tls.sh setup" || fail 'not-enabled summary lacks the switch command'
# 对外地址不合规：说清楚改哪里，改完重启网关再配边缘
MODE=upgrade EDGE_STATE=bad-url
summary
todo | grep -q 'https://域名 或 https://公网IPv4' && todo | grep -Fq 'systemctl restart aegis-public aegis-admin aegis-node' \
  || fail "bad-url summary does not say what to change: $(cat "$T/summary")"

# 升级：标题不同；不再打印后台地址本身，只提示怎么重看；给出迁移范围与备份位置；不提建管理员
MODE=upgrade EDGE_STATE=trusted PANDORA_ADMIN_STATE=manual BACKUP_TIMER=enabled
MIGRATION_BEFORE=121 MIGRATION_AFTER=134 BK="$T/backups/pre-upgrade-20261007.dump"
summary
order_ok || fail 'upgrade sections out of order'
grep -q '升级完成' "$T/summary" && ! grep -q '安装完成' "$T/summary" || fail 'upgrade summary title'
grep -q '121 → 134' "$T/summary" || fail 'upgrade summary lacks the migration range'
grep -Fq "$BK" "$T/summary" || fail 'upgrade summary lacks the backup path'
refute -q "$ADMIN_PATH_VALUE" "$T/summary"
grep -Fq "sudo $D/admin-url.sh" "$T/summary" || fail 'upgrade summary does not say how to see the admin URL'
grep -q "Let's Encrypt 证书在用" "$T/summary" || fail 'upgrade summary does not report the certificate state'
refute -q 'aegis-adminctl create' "$T/summary"
todo | grep -qx '    没有了' || fail "nothing left to do, but 还差什么 is not empty: $(cat "$T/summary")"

# --- ④ 静态：发布包与安装器都带上新文件；顺序 ------------------------------------------------
build="$DEPLOY/build-release.sh"
for script in admin-url.sh install-lib.sh; do
  n="$(grep -c "for script in .* $script .*; do" "$build" || true)"
  [ "$n" = 2 ] || fail "build-release.sh lists $script in $n of 2 script loops (copy and archive)"
done
inst="$DEPLOY/install.sh"
grep -Fq '"$SCRIPT_DIR/admin-url.sh"' "$inst" || fail 'install.sh does not install admin-url.sh'
# 建管理员在健康检查之后、HTTPS 边缘之前（那一步出问题时管理员已建好）；收尾提示在最后
line() { grep -nF "$1" "$inst" | head -1 | cut -d: -f1; }
[ "$(line 'native_gateways_healthy || HEALTH_OK=0')" -lt "$(line 'pandora_bootstrap_admin "$INSTALL_DIR/bin/aegis-adminctl"')" ] \
  && [ "$(line 'pandora_bootstrap_admin "$INSTALL_DIR/bin/aegis-adminctl"')" -lt "$(line 'bash "$INSTALL_DIR/deploy/edge-tls.sh" setup "$ENV_FILE"')" ] \
  && [ "$(line 'bash "$INSTALL_DIR/deploy/edge-tls.sh" setup "$ENV_FILE"')" -lt "$(line 'print_install_summary')" ] \
  || fail 'install.sh must create the administrator after the health check and before the HTTPS edge, and summarise last'
grep -q 'if \[\[ "\$HEALTH_OK" == 1 \]\] && pandora_admin_prompt_wanted "\$MODE"; then' "$inst" || fail 'install.sh does not gate the prompt'
# 健康检查没过不打印「装好了」的收尾，停下说怎么查
[ "$(line '[[ "$HEALTH_OK" == 1 ]] || die')" -lt "$(line 'print_install_summary')" ] || fail 'install.sh summarises an unhealthy install'
# 收尾提示按状态说话：边缘的每种结果都有对应的 EDGE_STATE
for state in trusted selfsigned failed no-nginx bad-url not-enabled; do
  grep -Eq "EDGE_STATE=$state( |;|\$)" "$inst" || fail "install.sh never sets EDGE_STATE=$state"
done

printf 'install-firstrun mock: PASS\n'
