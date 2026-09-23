#!/usr/bin/env bash
set -Eeuo pipefail
umask 022

[ "$(uname -s)" = Linux ] || { echo "installer: Linux is required" >&2; exit 1; }
[ "$(id -u)" -eq 0 ] || { echo "installer: root is required" >&2; exit 1; }
for required_command in sha256sum install find grep awk flock systemctl cp mv rm mkdir sync stat dirname chmod; do
  command -v "$required_command" >/dev/null 2>&1 \
    || { echo "installer: missing dependency: $required_command" >&2; exit 1; }
done

RELEASE_DIR="${1:-}"
[ -n "$RELEASE_DIR" ] \
  || { echo "installer: usage: PANDORA_RELEASE_MANIFEST_SHA256=<digest> $0 /absolute/release-dir" >&2; exit 1; }
[[ "$RELEASE_DIR" = /* ]] || { echo "installer: release directory must be absolute" >&2; exit 1; }
RELEASE_DIR="$(cd "$RELEASE_DIR" && pwd -P)"
[ -d "$RELEASE_DIR/bin" ] || { echo "installer: missing $RELEASE_DIR/bin" >&2; exit 1; }
[ -f "$RELEASE_DIR/SHA256SUMS" ] || { echo "installer: missing $RELEASE_DIR/SHA256SUMS" >&2; exit 1; }

# This digest must be obtained out of band from the release channel. A digest
# found only inside the same archive cannot authenticate a root installation.
expected_manifest="${PANDORA_RELEASE_MANIFEST_SHA256:-}"
[[ "$expected_manifest" =~ ^[A-Fa-f0-9]{64}$ ]] \
  || { echo "installer: PANDORA_RELEASE_MANIFEST_SHA256 must be a 64-hex out-of-band digest" >&2; exit 1; }
actual_manifest="$(sha256sum "$RELEASE_DIR/SHA256SUMS" | awk '{print $1}')"
[ "${actual_manifest,,}" = "${expected_manifest,,}" ] \
  || { echo "installer: release manifest digest mismatch" >&2; exit 1; }

# Reject an extracted tree that an unprivileged account can still replace.
# No package-owned code is sourced before these ownership and digest checks.
secure_path="$RELEASE_DIR"
while :; do
  path_owner="$(stat -c %u -- "$secure_path")"
  path_mode="$(stat -c %a -- "$secure_path")"
  path_mode="${path_mode: -3}"
  if [ "$path_owner" != 0 ] || (( (8#$path_mode & 8#022) != 0 )); then
    echo "installer: release path and all parents must be root-owned and not group/world-writable: $secure_path" >&2
    exit 1
  fi
  [ "$secure_path" = / ] && break
  secure_path="$(dirname "$secure_path")"
done
if find "$RELEASE_DIR" -xdev \( ! -user root -o -perm /022 \) -print -quit | grep -q .; then
  echo "installer: release tree must be root-owned and not group/world-writable" >&2
  exit 1
fi
(cd "$RELEASE_DIR" && sha256sum --strict -c SHA256SUMS)

# shellcheck source=platform.sh
. "$RELEASE_DIR/deploy/platform.sh"
pandora_detect_platform
pandora_require_root

exec 9>/run/lock/pandora-panel-install.lock
flock -n 9 || pandora_die "another Pandora Panel installation is running"

# Fail before writes when the host cannot run the packaged backup service.
"$RELEASE_DIR/deploy/preflight-linux.sh"

timer_was_active=0
if systemctl is-active --quiet aegis-backup.timer 2>/dev/null; then
  timer_was_active=1
fi
resume_timer() {
  if [ "$timer_was_active" -eq 1 ]; then
    systemctl start aegis-backup.timer >/dev/null 2>&1
  fi
}
trap 'resume_timer || true' EXIT

systemctl stop aegis-backup.timer aegis-backup.service 2>/dev/null || true
for unit in aegis-backup.timer aegis-backup.service; do
  load_state="$(systemctl show "$unit" --property=LoadState --value 2>/dev/null || true)"
  [ "$load_state" = not-found ] && continue
  active_state="$(systemctl show "$unit" --property=ActiveState --value 2>/dev/null || true)"
  sub_state="$(systemctl show "$unit" --property=SubState --value 2>/dev/null || true)"
  # .timer 类型没有 MainPID/ControlPID 这两个属性，systemctl show 对它们
  # 返回空串。直接拿空串和 0 比会永远判成「还有进程在跑」——第一次安装时
  # 单元 LoadState=not-found 走了上面的 continue 所以没暴露，装过一次之后
  # 就再也装不上了，升级路径被自己堵死。空即视为无进程。
  main_pid="$(systemctl show "$unit" --property=MainPID --value 2>/dev/null || true)"
  control_pid="$(systemctl show "$unit" --property=ControlPID --value 2>/dev/null || true)"
  [ -n "$main_pid" ] || main_pid=0
  [ -n "$control_pid" ] || control_pid=0
  # failed 也是终止态：单元跑过、挂了、没有进程留下。把它和 inactive 一视同仁。
  #
  # 只认 inactive/dead 的后果是：备份任务失败过一次，之后就再也装不上——
  # 和今天早上那个 .timer 空 PID 的问题是同一个判断的两个面。
  #
  # 真正要确认的是「没有进程还在跑」，那由下面两个 PID 为 0 保证；
  # ActiveState 只需排除还在动的那几种。
  case "$active_state" in
    active|activating|deactivating|reloading)
      pandora_die "$unit is still $active_state; stop it before installing" ;;
  esac
  if [ "$main_pid" != 0 ] || [ "$control_pid" != 0 ]; then
    pandora_die "$unit still has running processes (main=$main_pid control=$control_pid); refusing installation"
  fi
done

if [ -d /opt/aegispanel/install-rollbacks ]; then
  unfinished="$(find /opt/aegispanel/install-rollbacks -mindepth 2 -maxdepth 2 \
    \( -name TRANSACTION -o -name ROLLBACK_FAILED \) -print -quit)"
  [ -z "$unfinished" ] \
    || pandora_die "unfinished prior install transaction requires reviewed recovery: $unfinished"
fi
install -d -o root -g root -m 0700 /opt/aegispanel/install-rollbacks

install -d -o root -g root -m 0755 /opt/aegispanel/bin /opt/aegispanel/deploy /opt/aegispanel/pdnd-dist
install -d -o root -g root -m 0700 /opt/aegispanel/checkpoint-sink
# 三个网关的 unit 都把日志 append 到 /var/log/aegis 下。这个目录不存在时
# systemd 以 209/STDOUT 失败，且错误信息不会提到缺的是目录——建在这里。
install -d -o root -g root -m 0750 /var/log/aegis
install -d -o root -g root -m 0700 /var/backups/aegispanel
install -d -o root -g root -m 0700 /var/lib/aegispanel/backup-webdav

transaction_id="${actual_manifest}-$$"
rollback="/opt/aegispanel/install-rollbacks/$transaction_id"
install -d -o root -g root -m 0700 "$rollback"

sources=()
targets=()
stage_file() {
  source_file="$1"
  target_file="$2"
  target_mode="$3"
  [ -f "$source_file" ] || pandora_die "release is missing $source_file"
  staged_file="${target_file}.next-${transaction_id}"
  install -m "$target_mode" "$source_file" "$staged_file"
  sync -f "$staged_file"
  sync -f "$(dirname "$staged_file")"
  sources+=("$staged_file")
  targets+=("$target_file")
}

for binary in aegis-public aegis-admin aegis-node aegis-agent aegis-payctl aegis-adminctl aegis-backup-webdav goose; do
  stage_file "$RELEASE_DIR/bin/$binary" "/opt/aegispanel/bin/$binary" 0755
done
for node_arch in amd64 arm64; do
  stage_file "$RELEASE_DIR/pdnd-dist/pandora-native-linux-$node_arch" \
    "/opt/aegispanel/pdnd-dist/pandora-native-linux-$node_arch" 0755
done
for script in backup-postgres.sh verify-backup.sh restore-postgres.sh bootstrap.sh psql.sh render-nginx.sh migrate.sh platform.sh check-migrations.sh; do
  stage_file "$RELEASE_DIR/deploy/$script" "/opt/aegispanel/deploy/$script" 0755
done
for data_file in BACKUP.md backup-webdav.example.json .env.example \
                 docker-compose.yml configure-app-role.sql; do
  stage_file "$RELEASE_DIR/deploy/$data_file" "/opt/aegispanel/deploy/$data_file" 0644
done
for unit in aegis-public.service aegis-admin.service aegis-node.service \
             aegis-backup.service aegis-backup.timer; do
  stage_file "$RELEASE_DIR/deploy/systemd/$unit" "/etc/systemd/system/$unit" 0644
done

had_previous=()
for target in "${targets[@]}"; do
  relative="${target#/}"
  install -d -o root -g root -m 0700 "$rollback/$(dirname "$relative")"
  if [ -e "$target" ]; then
    cp -a -- "$target" "$rollback/$relative"
    sync -f "$rollback/$relative"
    sync -f "$(dirname "$rollback/$relative")"
    had_previous+=(1)
  else
    had_previous+=(0)
  fi
done
sync -f "$rollback"

committed=0
sync_target_set() {
  declare -A synced_parents=()
  for sync_target in "${targets[@]}"; do
    if [ -e "$sync_target" ]; then
      sync -f "$sync_target" || return 1
    fi
    sync_parent="$(dirname "$sync_target")"
    if [[ -z "${synced_parents[$sync_parent]+present}" ]]; then
      sync -f "$sync_parent" || return 1
      synced_parents[$sync_parent]=1
    fi
  done
}
persist_transaction_state() {
  state="$1"
  printf '%s\n' "$state" >"$rollback/TRANSACTION.next"
  chmod 0600 "$rollback/TRANSACTION.next"
  sync -f "$rollback/TRANSACTION.next"
  mv -f -- "$rollback/TRANSACTION.next" "$rollback/TRANSACTION"
  sync -f "$rollback"
}
rollback_install() {
  status="${1:-1}"
  trap - ERR INT TERM HUP EXIT
  set +e
  rollback_failed=0
  while [ "$committed" -gt 0 ]; do
    committed=$((committed - 1))
    target="${targets[$committed]}"
    relative="${target#/}"
    if [ "${had_previous[$committed]}" -eq 1 ]; then
      rollback_next="${target}.rollback-${transaction_id}"
      if cp -a -- "$rollback/$relative" "$rollback_next"; then
        mv -f -- "$rollback_next" "$target" || rollback_failed=1
      else
        rollback_failed=1
      fi
    else
      rm -f -- "$target" || rollback_failed=1
    fi
  done
  for staged_file in "${sources[@]}"; do
    rm -f -- "$staged_file" || rollback_failed=1
  done
  sync_target_set || rollback_failed=1
  systemctl daemon-reload >/dev/null 2>&1 || rollback_failed=1
  if [ "$rollback_failed" -ne 0 ]; then
    printf 'ROLLBACK_FAILED original_status=%s\n' "$status" >"$rollback/ROLLBACK_FAILED"
    sync -f "$rollback/ROLLBACK_FAILED" >/dev/null 2>&1 || true
    sync -f "$rollback" >/dev/null 2>&1 || true
    # TRANSACTION intentionally remains. The timer stays stopped and the next
    # installer fails closed until an operator completes reviewed recovery.
    exit 125
  fi
  printf 'ROLLED_BACK status=%s\n' "$status" >"$rollback/ROLLED_BACK"
  sync -f "$rollback/ROLLED_BACK" || exit 125
  rm -f -- "$rollback/TRANSACTION"
  sync -f "$rollback" || exit 125
  resume_timer || exit 126
  exit "$status"
}
trap 'rollback_install $?' ERR
trap 'rollback_install 130' INT
trap 'rollback_install 143' TERM
trap 'rollback_install 129' HUP

persist_transaction_state PREPARED
persist_transaction_state COMMITTING
for index in "${!targets[@]}"; do
  # Count the current item before mv: even a partially failed replacement is
  # reconstructed from the rollback set.
  committed=$((index + 1))
  mv -f -- "${sources[$index]}" "${targets[$index]}"
done
sync_target_set
systemctl daemon-reload
printf 'COMPLETED\n' >"$rollback/COMPLETED"
sync -f "$rollback/COMPLETED"
rm -f -- "$rollback/TRANSACTION"
sync -f "$rollback"
trap - ERR INT TERM HUP
if ! resume_timer; then
  echo "installer: files committed but the previously active backup timer could not be restarted" >&2
  exit 126
fi
trap - EXIT

echo "Installed Pandora Panel linux/$PANDORA_ARCH binaries, backup scripts, and systemd units. Configure secrets and the independent checkpoint hook before enabling the timer."
