#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [ "$(uname -s)" != Linux ] || [ "$(id -u)" -ne 0 ]; then
  grep -Fq 'load_trusted_env "$PWD/.env"' "$ROOT/deploy/verify-backup.sh"
  grep -Fq '. "/proc/self/fd/$env_fd"' "$ROOT/deploy/verify-backup.sh"
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
printf '%s\n' '#!/usr/bin/env bash' 'while IFS= read -r _; do :; done' 'exit 0' >"$WORK/bin/docker"
chmod 0700 "$WORK/bin/aegis-backup-webdav" "$WORK/bin/age" "$WORK/bin/docker"
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
POSTGRES_USER=aegis POSTGRES_PASSWORD=test POSTGRES_DB=aegis \
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
AEGIS_BACKUP_ALLOW_UNSIGNED_LEGACY="RESTORE_UNSIGNED:$(basename "$archive")" \
AEGIS_BACKUP_AGE_IDENTITY="$WORK/age.identity" \
AEGIS_RESTORE_CONFIRM='RESTORE:aegis_recovery' \
POSTGRES_USER=aegis POSTGRES_PASSWORD=test POSTGRES_DB=aegis \
  "$WORK/restore-postgres.sh" --archive "$archive" --target-db aegis_recovery \
  >"$WORK/restore.out" 2>"$WORK/restore.err"
restore_status=$?
set -e
[ "$restore_status" -ne 0 ] || {
  echo "destructive restore accepted unsigned legacy mode" >&2
  exit 1
}
grep -F "unsigned legacy backups cannot be used" "$WORK/restore.err" >/dev/null

echo "verify-backup signed-manifest fail-closed mock: PASS"
