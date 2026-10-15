# shellcheck shell=bash
# e2e 共用：关掉注册 IP 限额。
#
# 注册第 1 步按来源 IP 限额是后台设置 signup.ip_limit_*（产品规则的唯一来源，缺省每 IP 每分钟 10 次），
# 不受 PANDORA_RATELIMIT_* 环境变量影响。CI 里几个脚本连着从 127.0.0.1 注册十几个账号，会撞 429，
# 所以凡是注册账号的脚本，开头都经后台接口把它关掉（照常走登录、重认证、写审计，不直接改库）。
#
# 只许在一次性库上用：调用前脚本自己的 *_DISPOSABLE 守卫必须已经通过。设置会留在库里，不恢复。
# 用法：. "$(dirname "$0")/lib/signup.sh"; relax_signup_ip_limit
# 需要：ADM、ADMIN_EMAIL、ADMIN_PASS；curl、python3。
relax_signup_ip_limit() {
  local adm=${ADM:?ADM is required} tmp code tok
  : "${ADMIN_EMAIL:?ADMIN_EMAIL is required}" "${ADMIN_PASS:?ADMIN_PASS is required}"
  tmp=$(mktemp)
  # 口令只经环境变量交给 python 拼 JSON，不出现在命令行参数里
  code=$(python3 -c 'import json,os; print(json.dumps({"email": os.environ["ADMIN_EMAIL"], "password": os.environ["ADMIN_PASS"]}))' |
    curl -sS -o "$tmp" -w '%{http_code}' -X POST "$adm/v1/auth/login" -H 'Content-Type: application/json' --data-binary @-)
  [ "$code" = 200 ] || { echo "  [FATAL] relax_signup_ip_limit: admin login HTTP $code" >&2; rm -f "$tmp"; return 1; }
  tok=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["access_token"])' "$tmp")
  code=$(python3 -c 'import json,os; print(json.dumps({"password": os.environ["ADMIN_PASS"]}))' |
    curl -sS -o "$tmp" -w '%{http_code}' -X POST "$adm/v1/auth/reauth" -H 'Content-Type: application/json' \
      -H "Authorization: Bearer $tok" --data-binary @-)
  [ "$code" = 200 ] || { echo "  [FATAL] relax_signup_ip_limit: reauth HTTP $code" >&2; rm -f "$tmp"; return 1; }
  tok=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["access_token"])' "$tmp")
  code=$(curl -sS -o "$tmp" -w '%{http_code}' -X POST "$adm/v1/settings/signup" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $tok" -d '{"ip_limit_enabled":false}')
  case "$code" in
    2??) echo "  -- 注册 IP 限额已关闭（一次性库）" ;;
    *) echo "  [FATAL] relax_signup_ip_limit: POST settings/signup HTTP $code: $(head -c 300 "$tmp")" >&2; rm -f "$tmp"; return 1 ;;
  esac
  rm -f "$tmp"
}
