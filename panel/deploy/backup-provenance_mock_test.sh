#!/usr/bin/env bash
# 备份来源核验与往返（真跑 backup-postgres.sh、verify-backup.sh、restore-postgres.sh，封条与核验用真的 pandora backup）：
#   ① 默认安装（没配 WebDAV）的本地备份：backup-postgres.sh 写出归档、校验文件与封条 <归档>.seal（0600），
#      verify-backup.sh 核过，restore-postgres.sh 恢复到别名库成功（备份 → 校验 → 恢复往返）；
#   ② 被换过的归档（校验文件一起改成对得上）、换了 age 私钥的机器、封条被改：核验拒绝，恢复在连库建库之前停下；
#   ③ 既没有封条也没有签名清单：拒绝；有签名清单却没配公钥与可信检查点：拒绝。以上拒绝都不建库、不删库；
#   ④ 配了 WebDAV：封条之后调 pandora backup webdav-upload --webdav-config <配置> <归档> <校验文件>；
#      上传失败时备份记为失败，但已封条的本地这份留着。
# PostgreSQL 客户端与 age 是桩（age 原样透传）；WebDAV 上传由 pandora 的包装脚本记账代答（没有 WebDAV 服务），
# 其余子命令（dir、seal-local、verify-local-seal）交给真的二进制。
# 三个脚本把 deploy.env 与可执行文件按属主与 /proc/self/fd 绑定，要 Linux root：CI 的 deploy-root-mock-tests job 里跑，
# 由它先构建 ./cmd/pandora 并以 PANDORA_TEST_BACKUP_TOOL 传进来。别处只做静态检查。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
fail() { printf 'backup provenance: %s\n' "$*" >&2; exit 1; }
if [ "$(uname -s)" != Linux ] || [ "$(id -u)" -ne 0 ]; then
  grep -Fq 'run_trusted_executable "$pandora_bin" backup seal-local "$archive" "$checksum" "$PANDORA_BACKUP_AGE_IDENTITY" "$PANDORA_BACKUP_AGE_RECIPIENT"' "$ROOT/deploy/backup-postgres.sh" \
    || fail 'backup-postgres.sh does not seal local backups'
  grep -Fq 'run_trusted_executable "$pandora_bin" backup webdav-upload --webdav-config "$webdav_config" "$archive" "$checksum"' "$ROOT/deploy/backup-postgres.sh" \
    || fail 'backup-postgres.sh does not upload through pandora backup webdav-upload'
  grep -Fq 'backup_dir="$(run_trusted_executable "$pandora_bin" backup dir)"' "$ROOT/deploy/backup-postgres.sh" \
    || fail 'backup-postgres.sh does not take the backup directory from backup.dir'
  grep -Fq 'backup verify-local-seal "$archive" "$checksum" "$seal" "$PANDORA_BACKUP_AGE_IDENTITY"' "$ROOT/deploy/verify-backup.sh" \
    || fail 'verify-backup.sh does not check the local seal'
  grep -Fq 'refusing an archive of unknown origin' "$ROOT/deploy/verify-backup.sh" || fail 'verify-backup.sh accepts archives of unknown origin'
  grep -Fq 'rm -f -- "$archive" "$checksum" "${archive}.seal"' "$ROOT/deploy/backup-postgres.sh" \
    || fail 'backup-postgres.sh leaves an unsealed archive behind when sealing fails'
  if grep -l 'BACKUP_ALLOW_UNSIGNED' "$ROOT"/deploy/*.sh | grep -vq '_test\.sh$'; then fail 'the unsigned approval path is back'; fi
  echo "backup provenance mock: PASS static=PASS runtime=NOT_RUN reason=linux_root_required"
  exit 0
fi
TOOL="${PANDORA_TEST_BACKUP_TOOL:?PANDORA_TEST_BACKUP_TOOL must point at a built pandora binary}"
WORK="$(mktemp -d /root/pandora-backup-provenance.XXXXXX)"
trap 'rm -rf -- "$WORK"' EXIT
chmod 0700 "$WORK"
mkdir -m 0700 "$WORK/deploy" "$WORK/bin" "$WORK/secrets" "$WORK/backups" "$WORK/stub" "$WORK/etc"
for s in backup-postgres.sh verify-backup.sh restore-postgres.sh; do
  install -m 0700 "$ROOT/deploy/$s" "$WORK/deploy/$s"
done
install -m 0755 "$TOOL" "$WORK/bin/pandora-real"
# pandora 的包装：每次调用记一行到 tool.calls；backup webdav-upload 只记账（webdav.fail 在时以 1 退出），
# 其余原样交给真的二进制。脚本按 root 所有、单链接、不可他写核它，所以同样装成 root 0755
cat >"$WORK/bin/pandora" <<WRAP
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$WORK/tool.calls"
if [ "\${1:-} \${2:-}" = "backup webdav-upload" ]; then
  [ -f "$WORK/webdav.fail" ] && { echo "webdav backup upload failed: probe_failed" >&2; exit 1; }
  echo "webdav backup upload complete: archive=stub bytes=1 sha256=stub"
  exit 0
fi
exec "$WORK/bin/pandora-real" "\$@"
WRAP
chmod 0755 "$WORK/bin/pandora"
# backup.dir：真的 pandora backup dir 读 PANDORA_BACKUP_DIR（测试机上没有 /etc/pandora/config.yaml）
export PANDORA_BACKUP_DIR="$WORK/backups"
# 虚构的 age 私钥（X25519 私钥字节 0x01…0x20 与 0x21…0x40 的 Bech32 编码）与推出的收件人；拆开写免得被当成真私钥
KEY1="AGE-SECRET-KEY-""1QYPQXPQ9QCRSSZG2PVXQ6RS0ZQG3YYC5Z5TPWXQERGD3C8G7RUSQGPQYEE"
RCP1=age1q73he0q5yzfu3d64msd3p6rvksnrwjk3d2598mgtmlqt9wrdr37q2vrn72
KEY2="AGE-SECRET-KEY-""1YY3ZXFP9YCNJS2F29VKZ6T30XQCNYVE5X5MRWWPE8GANC0F78AQQ2X9KSF"
printf '# public key: %s\n%s\n' "$RCP1" "$KEY1" >"$WORK/secrets/backup-age.key"
chmod 0600 "$WORK/secrets/backup-age.key"
# 部署脚本的变量文件（生产上是 /etc/pandora/deploy.env）；夹带一行 PATH= 与一行非白名单键，确认解析器不收
cat >"$WORK/etc/deploy.env" <<ENV
POSTGRES_DB=pandora
POSTGRES_PORT=5432
POSTGRES_SUPER_PASSWORD=provenance-fixture
PANDORA_BACKUP_RETENTION_DAYS=7
PANDORA_BACKUP_AGE_RECIPIENT=$RCP1
PANDORA_BACKUP_AGE_IDENTITY=$WORK/secrets/backup-age.key
PANDORA_BACKUP_BIN=$WORK/bin/pandora
PANDORA_BACKUP_WEBDAV_CONFIG=$WORK/etc/backup-webdav.json
PATH=/nonexistent
UNRELATED_KEY=\$(touch $WORK/env-executed)
ENV
chmod 0600 "$WORK/etc/deploy.env"
export PANDORA_DEPLOY_ENV="$WORK/etc/deploy.env"

# 桩：age 原样透传；pg_dump 吐一段固定内容；pg_restore：--list 读完即可，--schema-only 打印一份本安装的角色计划，
# -d 读完即可；psql 答「角色在」「库不在」；createdb、dropdb 只记账
# age 桩：原样透传；$WORK/age.fail 在时，解密先吐 4 MiB 再以 1 退出（上游在输出之后失败：读完剩余流的写法
# 不许把它吞掉，pipefail 要照样抓到）
cat >"$WORK/stub/age" <<STUB
#!/usr/bin/env bash
decrypt=0
while [ "\$#" -gt 0 ]; do
  case "\$1" in
    --decrypt) decrypt=1; shift ;;
    --recipient|--identity) shift 2 ;;
    -*) shift ;;
    *) if [ "\$decrypt" = 1 ] && [ -f "$WORK/age.fail" ]; then head -c 4194304 "\$1"; exit 1; fi; exec cat "\$1" ;;
  esac
done
exec cat
STUB
# 归档要比管道缓冲（64 KiB）大得多：pg_restore 只读目录就退出时，age 会被 SIGPIPE 打断（panel2 演练撞上的问题）
printf '#!/usr/bin/env bash\nprintf "PGDMP-provenance-fixture-%%s\\n" "$*"\nhead -c 4194304 /dev/zero\n' >"$WORK/stub/pg_dump"
cat >"$WORK/stub/pg_restore" <<STUB
#!/usr/bin/env bash
printf 'pg_restore %s\n' "\$*" >>"$WORK/db.calls"
case " \$* " in
  # --list 与 --schema-only 像真的 pg_restore 一样读完开头就退出，不读到末尾
  *" --schema-only "*) head -c 16 >/dev/null; printf 'ALTER TABLE public.users OWNER TO pandora_owner;\nGRANT SELECT ON TABLE public.users TO pandora_app;\n' ;;
  *" --list"*) head -c 16 >/dev/null ;;
  *) cat >/dev/null ;;
esac
STUB
cat >"$WORK/stub/psql" <<STUB
#!/usr/bin/env bash
printf 'psql %s\n' "\$*" >>"$WORK/db.calls"
case "\$*" in
  *"FROM pg_catalog.pg_roles"*) echo 1 ;;
  *"SELECT 1 FROM pg_database"*) ;;
  *"SELECT 1"*) echo 1 ;;
esac
exit 0
STUB
for c in createdb dropdb; do
  printf '#!/usr/bin/env bash\nprintf "%s %%s\\n" "$*" >>"%s/db.calls"\n' "$c" "$WORK" >"$WORK/stub/$c"
done
chmod 0755 "$WORK/stub/"*
export PATH="$WORK/stub:$PATH"

run() { ( cd "$WORK/deploy" && "$@" ); }
created_db() { grep -E '^createdb .*pandora_recovery|^dropdb ' "$WORK/db.calls" 2>/dev/null || true; }

# --- ① 默认安装的本地备份：写、核、恢复 ---------------------------------------------------------
run ./backup-postgres.sh >"$WORK/backup.out" 2>"$WORK/backup.err" || fail "backup failed: $(cat "$WORK/backup.err")"
archive="$(sed -n 's/^backup complete: //p' "$WORK/backup.out")"
[ -f "$archive" ] && [ -f "$archive.sha256" ] && [ -f "$archive.seal" ] || fail "backup did not write archive, checksum and seal: $(ls -la "$WORK/backups")"
[ "$(stat -c %a "$archive.seal")" = 600 ] || fail "seal mode $(stat -c %a "$archive.seal")"
grep -q '^PANDORA-LOCAL-SEAL-V1 [0-9a-f]\{64\}$' "$archive.seal" || fail "seal format: $(cat "$archive.seal")"
run ./verify-backup.sh "$archive" >"$WORK/verify.out" 2>"$WORK/verify.err" || fail "own backup not verified: $(cat "$WORK/verify.err")"
: >"$WORK/db.calls"
run env PANDORA_RESTORE_CONFIRM=RESTORE:pandora_recovery ./restore-postgres.sh --archive "$archive" --target-db pandora_recovery \
  >"$WORK/restore.out" 2>"$WORK/restore.err" || fail "own backup not restored: $(cat "$WORK/restore.err")"
grep -Fq "restore complete: $archive -> database pandora_recovery" "$WORK/restore.out" || fail "restore output: $(cat "$WORK/restore.out")"
grep -q '^createdb .*--owner=pandora_owner pandora_recovery$' "$WORK/db.calls" || fail "restore did not create the target: $(cat "$WORK/db.calls")"
# 演练库与正式恢复同参数：带同一个库属主
grep -q '^createdb --template=template0 --encoding=UTF8 --owner pandora_owner pandora_verify_' "$WORK/db.calls" \
  || fail "rehearsal database without the same owner: $(cat "$WORK/db.calls")"

# --- ② 被换过、换了私钥、封条被改：拒绝，且不建库不删库 --------------------------------------------
refuse() { # <说明> <期望的提示> <命令…>
  local what="$1" want="$2"; shift 2
  : >"$WORK/db.calls"
  if run "$@" >"$WORK/r.out" 2>"$WORK/r.err"; then fail "$what was accepted"; fi
  grep -Fq "$want" "$WORK/r.err" || fail "$what: unexpected refusal: $(cat "$WORK/r.err")"
  [ -z "$(created_db)" ] || fail "$what reached createdb/dropdb: $(created_db)"
}
cp -p "$archive" "$WORK/archive.orig"; cp -p "$archive.sha256" "$WORK/sha.orig"; cp -p "$archive.seal" "$WORK/seal.orig"
printf 'PGDMP-swapped-archive\n' >"$archive"
( cd "$WORK/backups" && sha256sum "$(basename "$archive")" >"$(basename "$archive").sha256" )
refuse 'a swapped archive (with a matching checksum)' 'local backup seal verification failed' ./verify-backup.sh "$archive"
refuse 'restoring a swapped archive' 'local backup seal verification failed' \
  env PANDORA_RESTORE_CONFIRM=RESTORE:pandora_recovery ./restore-postgres.sh --archive "$archive" --target-db pandora_recovery
cp -p "$WORK/archive.orig" "$archive"; cp -p "$WORK/sha.orig" "$archive.sha256"
cp -p "$WORK/secrets/backup-age.key" "$WORK/key.orig"
printf '%s\n' "$KEY2" >"$WORK/secrets/backup-age.key"
refuse 'a backup sealed by another identity' 'local backup seal verification failed' ./verify-backup.sh "$archive"
cp -p "$WORK/key.orig" "$WORK/secrets/backup-age.key"
sed -i 's/ \(.\)/ f\1/; s/^\(PANDORA-LOCAL-SEAL-V1 .\{64\}\).*/\1/' "$archive.seal"
refuse 'a tampered seal' 'local backup seal verification failed' ./verify-backup.sh "$archive"
cp -p "$WORK/seal.orig" "$archive.seal"
run ./verify-backup.sh "$archive" >/dev/null 2>&1 || fail 'the restored original no longer verifies'

# age 在吐出数据之后失败：核验与恢复都拒绝，不建库
: >"$WORK/age.fail"
refuse 'an age failure after output (verify)' 'age decryption or pg_restore TOC verification failed' ./verify-backup.sh "$archive"
refuse 'an age failure after output (restore)' 'age decryption or pg_restore TOC verification failed' \
  env PANDORA_RESTORE_CONFIRM=RESTORE:pandora_recovery ./restore-postgres.sh --archive "$archive" --target-db pandora_recovery
rm -f "$WORK/age.fail"

# 封不上（私钥权限不对）：备份失败，刚发布的归档与校验文件撤掉，不留一份恢复不了的「备份」
before="$(ls "$WORK/backups")"
chmod 0644 "$WORK/secrets/backup-age.key"
if run ./backup-postgres.sh >"$WORK/b2.out" 2>"$WORK/b2.err"; then fail 'a backup that could not be sealed was reported as complete'; fi
grep -Fq 'sealing the local backup failed' "$WORK/b2.err" || fail "unexpected backup failure: $(cat "$WORK/b2.err")"
[ "$(ls "$WORK/backups")" = "$before" ] || fail "an unsealed backup was left behind: $(ls -la "$WORK/backups")"
chmod 0600 "$WORK/secrets/backup-age.key"
# 私钥和 .env 的收件人对不上（另存后放回了别的私钥）：同样不封、撤掉
before="$(ls "$WORK/backups")"
cp -p "$WORK/secrets/backup-age.key" "$WORK/key.keep"
printf '%s\n' "$KEY2" >"$WORK/secrets/backup-age.key"
if run ./backup-postgres.sh >"$WORK/b3.out" 2>"$WORK/b3.err"; then fail 'a backup sealed with a key that does not match the recipient'; fi
grep -Fq 'sealing the local backup failed' "$WORK/b3.err" || fail "unexpected backup failure: $(cat "$WORK/b3.err")"
[ "$(ls "$WORK/backups")" = "$before" ] || fail "a backup with a mismatched key was left behind: $(ls -la "$WORK/backups")"
cp -p "$WORK/key.keep" "$WORK/secrets/backup-age.key"

# --- ③ 来路不明：没有封条也没有签名清单；有清单没配公钥 ------------------------------------------------
rm -f "$archive.seal"
refuse 'an archive with neither seal nor manifest' 'refusing an archive of unknown origin' ./verify-backup.sh "$archive"
printf '{}' >"${archive%.dump.age}.manifest.json"; chmod 0600 "${archive%.dump.age}.manifest.json"
refuse 'a manifest without the public key' 'PANDORA_BACKUP_MANIFEST_PUBLIC_KEY is required' ./verify-backup.sh "$archive"
if grep -l 'BACKUP_ALLOW_UNSIGNED' "$ROOT"/deploy/*.sh | grep -vq '_test\.sh$'; then fail 'the unsigned approval path is back'; fi
rm -f "${archive%.dump.age}.manifest.json"
cp -p "$WORK/seal.orig" "$archive.seal"

# --- ④ 配了 WebDAV：封条之后上传；上传失败记为失败，已封条的本地这份留着 ----------------------------
grep -q '^backup webdav-upload' "$WORK/tool.calls" && fail 'webdav-upload ran without a WebDAV config'
[ ! -e "$WORK/env-executed" ] || fail 'deploy.env was executed instead of parsed'
printf '{}\n' >"$WORK/etc/backup-webdav.json"; chmod 0600 "$WORK/etc/backup-webdav.json"
: >"$WORK/tool.calls"
run ./backup-postgres.sh >"$WORK/w.out" 2>"$WORK/w.err" || fail "backup with WebDAV failed: $(cat "$WORK/w.err")"
up="$(sed -n 's/^backup complete: //p' "$WORK/w.out")"
[ -f "$up.seal" ] || fail 'the uploaded backup was not sealed'
grep -qx "backup webdav-upload --webdav-config $WORK/etc/backup-webdav.json $up $up.sha256" "$WORK/tool.calls" \
  || fail "webdav-upload arguments: $(cat "$WORK/tool.calls")"
# 封条在上传之前：没封条的归档不出这台机器
[ "$(grep -n '^backup seal-local' "$WORK/tool.calls" | cut -d: -f1)" -lt "$(grep -n '^backup webdav-upload' "$WORK/tool.calls" | cut -d: -f1)" ] \
  || fail "upload ran before the seal: $(cat "$WORK/tool.calls")"
: >"$WORK/webdav.fail"
if run ./backup-postgres.sh >"$WORK/wf.out" 2>"$WORK/wf.err"; then fail 'a failed WebDAV upload was reported as a complete backup'; fi
grep -Fq 'offsite WebDAV upload failed; the sealed local backup' "$WORK/wf.err" || fail "unexpected upload failure: $(cat "$WORK/wf.err")"
kept="$(sed -n 's/.*the sealed local backup \([^ ]*\) is kept.*/\1/p' "$WORK/wf.err")"
[ -f "$kept" ] && [ -f "$kept.sha256" ] && [ -f "$kept.seal" ] || fail "the local backup was not kept after the upload failed: $kept"
run ./verify-backup.sh "$kept" >/dev/null 2>&1 || fail 'the kept local backup does not verify'
rm -f "$WORK/webdav.fail" "$WORK/etc/backup-webdav.json"
echo "backup provenance mock: PASS"
