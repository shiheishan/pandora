#!/usr/bin/env bash
# install-native.sh 补齐的运维面（备份、校验、恢复、psql、收窄运行角色），不需要 root：
#   ① 静态：这五个脚本拷进 deploy/；加密备份单元按直装改写后装上、不启用；configure-app-role 走 bootstrap.sh；
#      口令不再拼进命令行（su -c、valkey-cli -a）；首装 .env 带布局与备份键；
#   ② source install-native-lib.sh 取函数：
#      native_env_append_missing 只追加缺的键、原有字节不动、保持 0600；
#      native_layout_env_lines 的键与路径；native_render_unit 把真实的 aegis-backup.service 改成直装
#      （安装目录、备份目录、没有 docker 依赖）；native_ensure_age_key 已有密钥绝不覆盖。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
NATIVE="$DEPLOY/install-native.sh"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-native-backup.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-native backup: %s\n' "$*" >&2; exit 1; }
code="$(grep -v '^[[:space:]]*#' "$NATIVE")"

# --- ① 静态 -----------------------------------------------------------------------
grep -Fq 'for f in backup-postgres.sh verify-backup.sh restore-postgres.sh psql.sh bootstrap.sh; do' "$NATIVE" \
  || fail 'install-native.sh does not install the backup/restore/psql/bootstrap scripts'
grep -Fq 'role_log="$(bash "$INSTALL_DIR/deploy/bootstrap.sh" 2>&1)"' "$NATIVE" \
  || fail 'install-native.sh does not narrow the runtime role through bootstrap.sh'
grep -Fq 'for u in aegis-backup.service aegis-backup.timer; do' "$NATIVE" || fail 'backup units are not installed'
grep -Fq 'native_render_unit "$SCRIPT_DIR/systemd/$u" "$INSTALL_DIR"' "$NATIVE" || fail 'backup units are not rendered for the native layout'
# 与 install.sh 一致：只装不启用（从 Docker 迁过来时原样恢复之前的启用状态，见 --from-docker）
if grep -Eq 'enable --now aegis-backup\.timer' <<<"$(grep -v 'say \|echo ' <<<"$code")"; then
  fail 'install-native.sh enables the backup timer on its own'
fi
# 口令不进命令行参数
if grep -Eq 'su -s /bin/bash postgres -c' <<<"$code"; then fail 'install-native.sh still builds psql commands for su -c'; fi
if grep -Eq "valkey-cli[^|]*-a " <<<"$code"; then fail 'install-native.sh passes the Valkey password with -a'; fi
grep -Fq 'REDISCLI_AUTH="$VK_PASS" valkey-cli' "$NATIVE" || fail 'valkey ping does not use REDISCLI_AUTH'
grep -Fq '$(native_layout_env_lines "$INSTALL_DIR" "$AGE_RECIPIENT")' "$NATIVE" || fail 'fresh .env lacks the layout and backup keys'
grep -Fq 'native_env_append_missing "$ENV_FILE" "${layout_lines[@]}"' "$NATIVE" || fail 'upgrade does not add missing keys'

# --- ② 函数 -----------------------------------------------------------------------
mkdir -p "$T/bin"
# age-keygen 桩：-o 写一份「私钥」，-y 打印固定公钥
cat >"$T/bin/age-keygen" <<'MOCK'
#!/usr/bin/env bash
if [ "$1" = -o ]; then printf 'AGE-SECRET-KEY-1MOCK\n' >"$2"; printf 'generated %s\n' "$2" >>"$(dirname "$0")/../keygen.calls"; exit 0; fi
if [ "$1" = -y ]; then printf 'age1mockrecipient\n'; exit 0; fi
exit 2
MOCK
chmod 0755 "$T/bin/age-keygen"
export PATH="$T/bin:$PATH"
. "$DEPLOY/install-native-lib.sh"
set -euo pipefail

# native_env_append_missing：缺的追加，有的不动（连原来的值也不换）
env_file="$T/.env"
printf 'POSTGRES_PASSWORD=keep-me\nAEGIS_BACKUP_DIR=/srv/custom-backups\n# comment\nNO_TRAILING_NEWLINE=1' >"$env_file"
chmod 0600 "$env_file"
cp "$env_file" "$T/.env.before"
native_env_append_missing "$env_file" PANDORA_DB_LAYOUT=native AEGIS_BACKUP_DIR=/var/backups/pandora AEGIS_BACKUP_RETENTION_DAYS=14
[ "$NATIVE_ENV_ADDED" = 'PANDORA_DB_LAYOUT AEGIS_BACKUP_RETENTION_DAYS' ] || fail "added: $NATIVE_ENV_ADDED"
head -c "$(wc -c <"$T/.env.before")" "$env_file" | cmp -s - "$T/.env.before" || fail 'existing bytes changed'
grep -qx 'NO_TRAILING_NEWLINE=1' "$env_file" || fail 'the last line without newline was glued to the appended keys'
grep -qx 'AEGIS_BACKUP_DIR=/srv/custom-backups' "$env_file" || fail 'an existing value was replaced'
[ "$(grep -c '^AEGIS_BACKUP_DIR=' "$env_file")" -eq 1 ] || fail 'an existing key was appended again'
grep -qx 'PANDORA_DB_LAYOUT=native' "$env_file" || fail 'missing key was not appended'
mode="$(stat -c %a "$env_file" 2>/dev/null || stat -f %Lp "$env_file")"
[ "$mode" = 600 ] || fail ".env mode became $mode"
# 什么都不缺：文件一字不变
cp "$env_file" "$T/.env.second"
native_env_append_missing "$env_file" PANDORA_DB_LAYOUT=native
[ -z "$NATIVE_ENV_ADDED" ] && cmp -s "$env_file" "$T/.env.second" || fail 'a no-op append changed the file'

# native_layout_env_lines：路径跟安装目录走，recipient 为空时不出那一行
lines="$(native_layout_env_lines /opt/pandora age1abc)"
for want in PANDORA_DB_LAYOUT=native AEGIS_BACKUP_DIR=/var/backups/pandora AEGIS_BACKUP_RETENTION_DAYS=14 \
    AEGIS_BACKUP_AGE_RECIPIENT=age1abc AEGIS_BACKUP_AGE_IDENTITY=/opt/pandora/secrets/backup-age.key \
    AEGIS_BACKUP_WEBDAV_BIN=/opt/pandora/bin/aegis-backup-webdav; do
  grep -qxF "$want" <<<"$lines" || fail "layout env lacks $want"
done
if native_layout_env_lines /opt/pandora '' | grep -q '^AEGIS_BACKUP_AGE_RECIPIENT='; then fail 'empty recipient was written'; fi

# native_render_unit：真实的备份单元改成直装
unit="$(native_render_unit "$DEPLOY/systemd/aegis-backup.service" /opt/pandora)"
if grep -Eq 'docker|/opt/aegispanel|/var/backups/aegispanel' <<<"$unit"; then fail "rendered unit still references docker layout: $(grep -E 'docker|aegispanel' <<<"$unit")"; fi
grep -qx 'ExecStart=/opt/pandora/deploy/backup-postgres.sh' <<<"$unit" || fail 'ExecStart not rewritten'
grep -qx 'WorkingDirectory=/opt/pandora/deploy' <<<"$unit" || fail 'WorkingDirectory not rewritten'
grep -qx 'ReadWritePaths=/var/backups/pandora /var/lib/aegispanel/backup-webdav' <<<"$unit" || fail 'ReadWritePaths not rewritten'
grep -qx 'After=postgresql.service' <<<"$unit" || fail 'not ordered after PostgreSQL'
# 加固项原样保留
for keep in 'NoNewPrivileges=true' 'ProtectSystem=strict' 'SystemCallFilter=~@privileged @resources @mount @reboot @swap @obsolete'; do
  grep -qxF "$keep" <<<"$unit" || fail "hardening line lost: $keep"
done
# 直装客户端走回环 TCP：单元必须放行 AF_INET（不用 runuser，@privileged 禁了切换用户）
grep -qx 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6' <<<"$unit" || fail 'the unit no longer allows loopback TCP'
cmp -s <(native_render_unit "$DEPLOY/systemd/aegis-backup.timer" /opt/pandora) "$DEPLOY/systemd/aegis-backup.timer" \
  || fail 'the timer should not need rewriting'

# native_ensure_age_key：没有就生成（0600），已有的绝不覆盖
key="$T/secrets/backup-age.key"
[ "$(native_ensure_age_key "$key")" = age1mockrecipient ] || fail 'recipient not printed'
[ -f "$key" ] || fail 'key not generated'
mode="$(stat -c %a "$key" 2>/dev/null || stat -f %Lp "$key")"; [ "$mode" = 600 ] || fail "key mode $mode"
printf 'AGE-SECRET-KEY-1EXISTING\n' >"$key"
: >"$T/keygen.calls"
native_ensure_age_key "$key" >/dev/null
grep -qx 'AGE-SECRET-KEY-1EXISTING' "$key" || fail 'an existing key was overwritten'
[ ! -s "$T/keygen.calls" ] || fail 'age-keygen -o ran over an existing key'

# native_check_release_tree：缺件、不归 root 的发布目录在动手之前就拒绝（与 install.sh 同样的检查）
rel="$T/rel"; mkdir -p "$rel/deploy"
if ( native_check_release_tree "$rel" ) >"$T/rt.out" 2>&1; then fail 'a release without bin/ was accepted'; fi
grep -Fq '找不到' "$T/rt.out" || fail "release check message: $(cat "$T/rt.out")"
mkdir -p "$rel/bin"
if ( native_check_release_tree "$rel" ) >"$T/rt.out" 2>&1; then fail 'a release without SHA256SUMS was accepted'; fi
: >"$rel/SHA256SUMS"
if ( native_check_release_tree "$rel" ) >"$T/rt.out" 2>&1; then fail 'a release without release-artifact.env was accepted'; fi
grep -Fq 'release-artifact.env' "$T/rt.out" || fail "release-artifact message: $(cat "$T/rt.out")"
: >"$rel/deploy/release-artifact.env"
if stat -c %u / >/dev/null 2>&1 && [ "$(id -u)" -ne 0 ]; then
  if ( native_check_release_tree "$rel" ) >"$T/rt.out" 2>&1; then fail 'a release owned by a normal user was accepted'; fi
  grep -Fq '不归 root' "$T/rt.out" || fail "ownership message: $(cat "$T/rt.out")"
fi
n_check="$(grep -n 'native_check_release_tree "$RELEASE_ROOT"' "$NATIVE" | head -1 | cut -d: -f1)"
n_first="$(grep -n 'say "\[0/6\]' "$NATIVE" | head -1 | cut -d: -f1)"
[ -n "$n_check" ] && [ -n "$n_first" ] && [ "$n_check" -lt "$n_first" ] || fail 'install-native.sh does not verify the release before changing anything'
grep -Fq 'RELEASE_BIN="$RELEASE_ROOT/bin"' "$NATIVE" || fail 'binaries are not taken from the verified release root'

printf 'install-native backup mock: PASS\n'
