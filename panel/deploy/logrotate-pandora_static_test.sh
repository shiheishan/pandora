#!/usr/bin/env bash
# logrotate-pandora 的静态检查：
#   - 轮转的正好是 healthcheck.sh 写的 health.log；
#   - 不碰面板服务自己用 lumberjack 滚动的 serve.log / worker.log（两套轮转会互相打架）；
#   - 不用 copytruncate（health.log 每次追加时重新打开，用不着，也免得复制与清空之间丢行）。
# 发布包与安装器有没有带上它，由安装器那边的检查管。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
CONF="$DEPLOY/logrotate-pandora"
fail() { echo "logrotate-pandora: $*" >&2; exit 1; }

[ -f "$CONF" ] || fail 'logrotate-pandora is missing'
# 只有这一份轮转配置（旧名字的那份已经改名过来，不留双份）
others="$(find "$DEPLOY" -maxdepth 1 -name 'logrotate-*' ! -name 'logrotate-pandora' ! -name '*_test.sh' -print)"
[ -z "$others" ] || fail "other logrotate configs next to logrotate-pandora: $others"

globs=()
while IFS= read -r line; do globs+=("${line% \{}"); done \
  < <(grep -E '^/[^[:space:]]+ \{$' "$CONF")
[ "${#globs[@]}" -gt 0 ] || fail 'no rotated path found'

# 巡检日志：healthcheck.sh 的缺省日志目录 + health.log
log_dir="$(sed -n 's/^LOG_DIR="\${HEALTHCHECK_LOG_DIR:-\(.*\)}"$/\1/p' "$DEPLOY/healthcheck.sh")"
[ -n "$log_dir" ] || fail 'cannot read the log directory from healthcheck.sh'
health="$log_dir/health.log"
matches() { # <路径> → 有一条 glob 匹配就返回 0
  local g
  for g in "${globs[@]}"; do
    # shellcheck disable=SC2053
    [[ "$1" == $g ]] && return 0
  done
  return 1
}
matches "$health" || fail "healthcheck.sh writes $health, which logrotate-pandora does not rotate"
for own in "$log_dir/serve.log" "$log_dir/worker.log" "$log_dir/serve-2026-10-13T00-00-00.000.log.gz"; do
  if matches "$own"; then fail "logrotate-pandora also rotates $own, which lumberjack already rotates"; fi
done
if grep -Eq '^[[:space:]]*copytruncate' "$CONF"; then fail 'copytruncate is not needed for health.log'; fi
# 轮转的路径都在 /var/log/pandora 下
for g in "${globs[@]}"; do [[ "$g" == /var/log/pandora/* ]] || fail "logrotate-pandora rotates outside /var/log/pandora: $g"; done

printf 'logrotate-pandora static: PASS\n'
