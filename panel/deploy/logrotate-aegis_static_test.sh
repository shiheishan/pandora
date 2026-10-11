#!/usr/bin/env bash
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
grep -Fq 'install -m 0644 "$SCRIPT_DIR/logrotate-aegis" /etc/logrotate.d/aegis' "$DEPLOY/install.sh" \
  || fail 'install.sh does not install logrotate-aegis'

printf 'logrotate-aegis static: PASS\n'
