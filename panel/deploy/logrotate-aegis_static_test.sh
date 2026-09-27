#!/usr/bin/env bash
# [INPUT]: 依赖同目录 logrotate-aegis、systemd/ 下三个网关单元、build-release.sh、install-linux-binaries.sh
# [OUTPUT]: 日志轮转的静态契约：轮转的 glob 覆盖三个网关单元 append: 写的每个日志文件，规则随发布包分发并装到 /etc/logrotate.d/aegis
# [POS]: deploy 的静态测试，CI panel-deploy.yml 必跑；只读源码，不需要 root；曾经轮转 /opt/aegispanel/logs 而单元写 /var/log/aegis，谁也没发现
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { echo "logrotate-aegis: $*" >&2; exit 1; }

globs=()
while IFS= read -r line; do globs+=("${line% \{}"); done \
  < <(grep -E '^/[^[:space:]]+ \{$' "$DEPLOY/logrotate-aegis")
[ "${#globs[@]}" -gt 0 ] || fail 'no rotated path found'

logs=0
for unit in aegis-public aegis-admin aegis-node; do
  while IFS= read -r path; do
    logs=$((logs + 1))
    covered=0
    for glob in "${globs[@]}"; do
      # shellcheck disable=SC2053
      [[ "$path" == $glob ]] && covered=1
    done
    [ "$covered" -eq 1 ] || fail "$unit writes $path, which logrotate-aegis does not rotate"
  done < <(sed -nE 's/^Standard(Output|Error)=append:(.*)$/\2/p' "$DEPLOY/systemd/$unit.service")
done
[ "$logs" -eq 6 ] || fail "expected stdout+stderr append paths on three gateway units, found $logs"

grep -Fq 'cp "$ROOT/deploy/logrotate-aegis" "$target/deploy/logrotate-aegis"' "$DEPLOY/build-release.sh" \
  || fail 'build-release.sh does not ship logrotate-aegis'
grep -Fq '"$target_base/deploy/logrotate-aegis"' "$DEPLOY/build-release.sh" \
  || fail 'build-release.sh does not archive logrotate-aegis'
grep -Fq 'stage_file "$RELEASE_DIR/deploy/logrotate-aegis" /etc/logrotate.d/aegis 0644' "$DEPLOY/install-linux-binaries.sh" \
  || fail 'install-linux-binaries.sh does not install logrotate-aegis'

printf 'logrotate-aegis static: PASS\n'
