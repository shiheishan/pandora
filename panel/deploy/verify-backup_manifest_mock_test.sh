#!/usr/bin/env bash
# verify-backup.sh / restore-postgres.sh 的签名清单闸门（fail-closed）：
#   - 缺签名清单时缺省拒绝；
#   - 没配 WebDAV 的机器本地备份都没有清单，只能经逐个文件的一次性批准
#     （AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY=RESTORE_UNSIGNED:<文件名>）核 sha256、解密与归档目录，
#     不许做临时库演练；
#   - restore-postgres.sh 见到这个批准一律拒绝（它调的 verify-backup.sh 会继承这个变量）。
# 两个脚本要 Linux root（.env 与可执行文件按属主与 /proc/self/fd 绑定），别处只做静态检查。
# PostgreSQL 客户端都是桩：校验路径只用 pg_restore --list，拒绝都发生在连库之前。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [ "$(uname -s)" != Linux ] || [ "$(id -u)" -ne 0 ]; then
  grep -Fq 'load_trusted_env "$PWD/.env"' "$ROOT/deploy/verify-backup.sh"
  grep -Fq '. "/proc/self/fd/$env_fd"' "$ROOT/deploy/verify-backup.sh"
  grep -Fq 'die "signed manifest not found: $manifest"' "$ROOT/deploy/verify-backup.sh"
  grep -Fq 'unsigned legacy backups cannot run full restore rehearsal' "$ROOT/deploy/verify-backup.sh"
  grep -Fq 'unsigned legacy backups cannot be used' "$ROOT/deploy/restore-postgres.sh"
  echo "verify-backup signed-manifest fail-closed mock: PASS static=PASS runtime=NOT_RUN reason=linux_root_required"
  exit 0
fi
WORK="$(mktemp -d /root/aegis-backup-manifest-mock.XXXXXX)"
cleanup() {
  rm -rf -- "$WORK"
}
trap cleanup EXIT

cp "$ROOT/deploy/verify-backup.sh" "$WORK/verify-backup.sh"
cp "$ROOT/deploy/restore-postgres.sh" "$WORK/restore-postgres.sh"
: >"$WORK/.env"
chmod 0600 "$WORK/.env"
chmod 0700 "$WORK/verify-backup.sh" "$WORK/restore-postgres.sh"

archive="$WORK/aegis-postgres-20260801T031700Z.dump.age"
printf 'encrypted' >"$archive"
(
  cd "$WORK"
  sha256sum "$(basename "$archive")" >"$(basename "$archive").sha256"
)

mkdir -p "$WORK/bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$WORK/bin/aegis-backup-webdav"
printf '%s\n' '#!/usr/bin/env bash' "printf 'toc'" >"$WORK/bin/age"
# 本机客户端桩：pg_restore 读完标准输入即成功；连库的客户端被调用就记下（这些路径都不该连库）
printf '%s\n' '#!/usr/bin/env bash' 'cat >/dev/null' 'exit 0' >"$WORK/bin/pg_restore"
for client in psql createdb dropdb; do
  printf '%s\n' '#!/usr/bin/env bash' "printf '%s %s\\n' $client \"\$*\" >>\"$WORK/db.calls\"" 'exit 0' >"$WORK/bin/$client"
done
chmod 0700 "$WORK/bin/aegis-backup-webdav" "$WORK/bin/age" "$WORK/bin/pg_restore" \
  "$WORK/bin/psql" "$WORK/bin/createdb" "$WORK/bin/dropdb"
: >"$WORK/public.key"
: >"$WORK/checkpoint"
: >"$WORK/age.identity"

set +e
PATH="$WORK/bin:$PATH" \
AEGIS_BACKUP_MANIFEST_PUBLIC_KEY="$WORK/public.key" \
AEGIS_BACKUP_TRUSTED_CHECKPOINT="$WORK/checkpoint" \
AEGIS_BACKUP_WEBDAV_BIN="$WORK/bin/aegis-backup-webdav" \
AEGIS_BACKUP_AGE_IDENTITY="$WORK/age.identity" \
  "$WORK/verify-backup.sh" "$archive" >"$WORK/default.out" 2>"$WORK/default.err"
default_status=$?
set -e
[ "$default_status" -ne 0 ] || {
  echo "missing signed manifest was accepted by default" >&2
  exit 1
}
grep -F "signed manifest not found" "$WORK/default.err" >/dev/null

# 批准绑定文件名：批准的是别的文件就不算
set +e
PATH="$WORK/bin:$PATH" \
AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY="RESTORE_UNSIGNED:aegis-postgres-20260731T031700Z.dump.age" \
AEGIS_BACKUP_AGE_IDENTITY="$WORK/age.identity" \
  "$WORK/verify-backup.sh" "$archive" >"$WORK/other.out" 2>"$WORK/other.err"
other_status=$?
set -e
[ "$other_status" -ne 0 ] || {
  echo "an approval for another file unlocked unsigned verification" >&2
  exit 1
}

PATH="$WORK/bin:$PATH" \
AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY="RESTORE_UNSIGNED:$(basename "$archive")" \
AEGIS_BACKUP_AGE_IDENTITY="$WORK/age.identity" \
  "$WORK/verify-backup.sh" "$archive" >"$WORK/legacy.out" 2>"$WORK/legacy.err"
grep -F "WARNING unsigned legacy recovery explicitly enabled" "$WORK/legacy.err" >/dev/null
grep -F "backup verified:" "$WORK/legacy.out" >/dev/null

set +e
PATH="$WORK/bin:$PATH" \
AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY="RESTORE_UNSIGNED:$(basename "$archive")" \
AEGIS_BACKUP_AGE_IDENTITY="$WORK/age.identity" \
AEGIS_VERIFY_RESTORE=1 \
POSTGRES_DB=aegis POSTGRES_PORT=5432 POSTGRES_SUPER_PASSWORD=test \
  "$WORK/verify-backup.sh" "$archive" >"$WORK/legacy-restore.out" 2>"$WORK/legacy-restore.err"
legacy_restore_status=$?
set -e
[ "$legacy_restore_status" -ne 0 ] || {
  echo "unsigned legacy backup was accepted for full restore rehearsal" >&2
  exit 1
}
grep -F "unsigned legacy backups cannot run full restore rehearsal" \
  "$WORK/legacy-restore.err" >/dev/null

set +e
PATH="$WORK/bin:$PATH" \
AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY="RESTORE_UNSIGNED:$(basename "$archive")" \
AEGIS_BACKUP_AGE_IDENTITY="$WORK/age.identity" \
AEGIS_RESTORE_CONFIRM='RESTORE:aegis_recovery' \
POSTGRES_USER=aegis POSTGRES_DB=aegis POSTGRES_PORT=5432 POSTGRES_SUPER_PASSWORD=test \
  "$WORK/restore-postgres.sh" --archive "$archive" --target-db aegis_recovery \
  >"$WORK/restore.out" 2>"$WORK/restore.err"
restore_status=$?
set -e
[ "$restore_status" -ne 0 ] || {
  echo "destructive restore accepted unsigned legacy mode" >&2
  exit 1
}
grep -F "unsigned legacy backups cannot be used" "$WORK/restore.err" >/dev/null

# 以上路径都没有连库
[ ! -s "$WORK/db.calls" ] || {
  echo "a fail-closed path reached the database: $(cat "$WORK/db.calls")" >&2
  exit 1
}

echo "verify-backup signed-manifest fail-closed mock: PASS"
