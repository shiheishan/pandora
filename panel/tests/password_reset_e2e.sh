#!/usr/bin/env bash
# 自助找回密码端到端（w5account，用户 2026-10-07 定）：
#   - 没配邮件服务时入口关闭：site-config.password_reset=false，接口 403；
#   - 配好 SMTP 后打开；第 1 步对存在与不存在的邮箱回同一句话；
#   - 验证码一次性、错了不认；重置成功后该账号的全部登录失效，旧密码进不去，新密码可以；
#   - 留下请求与重置两条审计。
#
# 会改 SMTP 设置（退出时原样恢复）并留下审计，只许在一次性库上跑：
# PASSWORD_RESET_E2E_DISPOSABLE=YES_DELETE_FIXTURES，库名带独立的 test / e2e 段。
# 冒烟栈不是生产模式，第 1 步会回带开发验证码（dev_code），这里直接用它。
set -uo pipefail

PUB=${PUB:-http://127.0.0.1:9000}
ADM=${ADM:-http://127.0.0.1:9001}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}

pass=0; fail=0
ok()  { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad() { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
sec() { echo; echo "=== $1 ==="; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
code(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }
json(){ curl -s -H 'Content-Type: application/json' "$@"; }

[ "${PASSWORD_RESET_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || { echo '[FATAL] PASSWORD_RESET_E2E_DISPOSABLE=YES_DELETE_FIXTURES is required' >&2; exit 1; }
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] \
  || { echo '[FATAL] ADMIN_EMAIL / ADMIN_PASS must be explicit' >&2; exit 1; }
DB_NAME=$("$PSQL" -X -qAtc 'SELECT current_database()' | tr -d '[:space:]')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] \
  || { echo '[FATAL] database name must contain a standalone test/e2e segment' >&2; exit 1; }

#-------------------------------------------------------------------------------
sec "1. 没配邮件服务时入口关闭"

ATOK=$(json -X POST "$ADM/v1/auth/login" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" | jqr "['access_token']")
[ -n "$ATOK" ] && ok "管理员已登录" || { bad "管理员登录失败" "响应已隐藏"; exit 1; }
AH="Authorization: Bearer $ATOK"
MAIL_ORIG=$(curl -s "$ADM/v1/settings/mail" -H "$AH")
echo "$MAIL_ORIG" | jqr "['smtp_port']" >/dev/null || { bad "读不到邮件设置" ""; exit 1; }

restore_mail(){
  local body
  body=$(echo "$MAIL_ORIG" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(json.dumps({'smtp_host':d['smtp_host'],'smtp_port':d['smtp_port'] or 465,'encryption':d['encryption'] or 'ssl',
  'smtp_username':d['smtp_username'],'smtp_password':'','from_address':d['from_address'],'from_name':d['from_name']}))")
  C=$(code -X POST "$ADM/v1/settings/mail" -H "$AH" -H 'Content-Type: application/json' -d "$body")
  [ "$C" = 200 ] && echo "  邮件设置已恢复" || echo "  [WARN] 邮件设置恢复失败 HTTP $C"
}

HOST_ORIG=$(echo "$MAIL_ORIG" | jqr "['smtp_host']")
if [ -n "$HOST_ORIG" ]; then
  echo "  已配过 SMTP（$HOST_ORIG 不打印细节），跳过「未配置时关闭」这一段"
else
  SC=$(curl -s "$PUB/v1/site-config")
  [ "$(echo "$SC" | jqr "['password_reset']")" = False ] \
    && ok "未配 SMTP：site-config.password_reset=false" || bad "未配 SMTP 时入口没关" "$SC"
  C=$(code -X POST "$PUB/v1/auth/password-reset/start" -H 'Content-Type: application/json' -d '{"email":"nobody@example.test"}')
  [ "$C" = 403 ] && ok "未配 SMTP：发验证码回 403" || bad "未配 SMTP 时发验证码返回 $C" ""
fi

#-------------------------------------------------------------------------------
sec "2. 配好 SMTP 后打开（指向一个收不到信的本机端口，信只留在队列里）"

trap restore_mail EXIT
C=$(code -X POST "$ADM/v1/settings/mail" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"smtp_host":"127.0.0.1","smtp_port":9,"encryption":"none","smtp_username":"","smtp_password":"","from_address":"noreply@example.test","from_name":"E2E"}')
[ "$C" = 200 ] && ok "SMTP 已配置" || { bad "配置 SMTP 失败" "HTTP $C"; exit 1; }
# public 网关的 SMTP 配置有 30 秒缓存
open=""
for _ in $(seq 1 45); do
  [ "$(curl -s "$PUB/v1/site-config" | jqr "['password_reset']")" = True ] && { open=1; break; }
  sleep 1
done
[ -n "$open" ] && ok "site-config.password_reset=true" || { bad "配好 SMTP 后入口仍未打开" ""; exit 1; }

#-------------------------------------------------------------------------------
sec "3. 准备一个门户用户并登录"

UE="pwreset_$(date +%s)_$RANDOM@example.com"
R=$(json -X POST "$PUB/v1/auth/register/start" -d "{\"email\":\"$UE\"}")
RT=$(echo "$R" | jqr "['registration_token']"); CD=$(echo "$R" | jqr "['dev_code']")
C=$(code -X POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
  -d "{\"registration_token\":\"$RT\",\"code\":\"$CD\",\"password\":\"Reset!Old2026\"}")
[ "$C" = 201 ] || { bad "注册失败" "HTTP $C"; exit 1; }
UTOK=$(json -X POST "$PUB/v1/auth/login" -d "{\"email\":\"$UE\",\"password\":\"Reset!Old2026\"}" | jqr "['access_token']")
[ -n "$UTOK" ] && ok "用户已登录（这枚令牌重置后应失效）" || { bad "登录失败" ""; exit 1; }

#-------------------------------------------------------------------------------
sec "4. 发验证码：存在与不存在的邮箱回同一句话"

R1=$(json -X POST "$PUB/v1/auth/password-reset/start" -d "{\"email\":\"$UE\"}")
R2=$(json -X POST "$PUB/v1/auth/password-reset/start" -d '{"email":"nobody-pwreset@example.test"}')
M1=$(echo "$R1" | jqr "['message']"); M2=$(echo "$R2" | jqr "['message']")
[ -n "$M1" ] && [ "$M1" = "$M2" ] && ok "两种邮箱的回应一致：$M1" || bad "回应不一致" "$M1 / $M2"
DEV=$(echo "$R1" | jqr "['dev_code']")
[[ "$DEV" =~ ^[0-9]{6}$ ]] && ok "开发模式拿到验证码" || { bad "没拿到开发验证码" ""; exit 1; }
Q=$("$PSQL" -X -qAtc "SELECT count(*) FROM notification_deliveries
  WHERE template_code='auth.password_reset' AND user_id IS NULL AND created_at > now() - interval '5 minutes'" | tr -d '[:space:]')
[ "${Q:-0}" -ge 1 ] && ok "验证码邮件已入队（$Q 封）" || bad "验证码邮件没入队" "count=$Q"

#-------------------------------------------------------------------------------
sec "5. 填错不认，填对重置；验证码一次性"

WRONG=000000; [ "$DEV" = "$WRONG" ] && WRONG=111111
R=$(json -X POST "$PUB/v1/auth/password-reset/complete" -d "{\"email\":\"$UE\",\"code\":\"$WRONG\",\"new_password\":\"Reset!New2026\"}")
echo "$R" | grep -q '"code"' && ok "错误验证码被拒" || bad "错误验证码没被拒" "$R"
C=$(code -X POST "$PUB/v1/auth/password-reset/complete" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$UE\",\"code\":\"$DEV\",\"new_password\":\"Reset!New2026\"}")
[ "$C" = 200 ] && ok "验证码正确，密码已重置" || bad "重置失败" "HTTP $C"
C=$(code -X POST "$PUB/v1/auth/password-reset/complete" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$UE\",\"code\":\"$DEV\",\"new_password\":\"Reset!Again2026\"}")
[ "$C" = 422 ] && ok "同一枚验证码不能再用" || bad "验证码被重复使用" "HTTP $C"

#-------------------------------------------------------------------------------
sec "6. 重置后吊销全部登录"

C=$(code "$PUB/v1/me" -H "Authorization: Bearer $UTOK")
[ "$C" = 401 ] && ok "重置前的令牌已失效" || bad "旧令牌仍可用" "HTTP $C"
C=$(code -X POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"$UE\",\"password\":\"Reset!Old2026\"}")
[ "$C" = 401 ] && ok "旧密码登不进去" || bad "旧密码仍能登录" "HTTP $C"
C=$(code -X POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"$UE\",\"password\":\"Reset!New2026\"}")
[ "$C" = 200 ] && ok "新密码可以登录" || bad "新密码登录失败" "HTTP $C"
A=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events a JOIN users u ON u.id = a.resource_id
  WHERE u.email = '$UE' AND a.action IN ('user.password_reset_requested','user.password_reset')" | tr -d '[:space:]')
[ "${A:-0}" -ge 2 ] && ok "请求与重置都进了审计（$A 条）" || bad "审计缺失" "count=$A"

echo
echo "通过 $pass 项，失败 $fail 项"
[ "$fail" -eq 0 ]
