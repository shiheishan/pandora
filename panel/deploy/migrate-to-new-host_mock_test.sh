#!/usr/bin/env bash
# [INPUT]: 依赖同目录 migrate-to-new-host.sh（及它 source 的 platform.sh、public-base-url.sh）；备份、代码包、密钥 .env 都是临时目录里的虚构文件
# [OUTPUT]: 迁新主机的生产闸门契约：目标 .env 为 production 且没给 AEGIS_RELEASE_DIR 时，在动手之前以中文原因拒绝源码模式；development 或给了发布包则放过这一关
# [POS]: deploy 的桩测试，CI panel-deploy.yml 必跑；不需要 root，只跑到闸门为止，之后的步骤（平台探测、依赖）自然失败，不碰 /opt
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-migrate-host.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT
fail() { echo "migrate-to-new-host: $*" >&2; exit 1; }

mkdir -p "$TMP/mig" "$TMP/bin" "$TMP/release/bin"
: >"$TMP/backup.dump.age"
: >"$TMP/backup.dump.age.sha256"
: >"$TMP/mig/aegis-code.tgz"
: >"$TMP/release/SHA256SUMS"
# 脚本用 GNU stat -c 查 0600；macOS 上用 BSD stat 代答，Linux 上直通
cat >"$TMP/bin/stat" <<'MOCK'
#!/usr/bin/env bash
if ! /usr/bin/stat --version >/dev/null 2>&1 && [ "$1" = -c ] && [ "$2" = '%a' ]; then
  exec /usr/bin/stat -f '%Lp' "$3"
fi
exec /usr/bin/stat "$@"
MOCK
# 硬性止点：放过闸门的用例绝不能走到第 2 步（它会挪动 /opt/aegispanel）。
# docker compose 桩必定失败，依赖检查在任何写入之前就停下。
cat >"$TMP/bin/docker" <<'MOCK'
#!/usr/bin/env bash
exit 1
MOCK
chmod 0755 "$TMP/bin/stat" "$TMP/bin/docker"

GATE='不能用源码模式迁移'
run_case() {
  local label=$1 env_value=$2 release=$3
  printf 'AEGIS_ENV=%s\nAEGIS_PUBLIC_BASE_URL=https://panel.example.test\n' "$env_value" >"$TMP/secret.env"
  chmod 0600 "$TMP/secret.env"
  set +e
  PATH="$TMP/bin:$PATH" AEGIS_MIGRATE_DIR="$TMP/mig" \
    AEGIS_MIGRATION_BACKUP="$TMP/backup.dump.age" AEGIS_SECRET_ENV_FILE="$TMP/secret.env" \
    AEGIS_RELEASE_DIR="$release" NEW_IP=203.0.113.10 \
    bash "$DEPLOY/migrate-to-new-host.sh" >"$TMP/$label.out" 2>&1 </dev/null
  CASE_STATUS=$?
  set -e
}

# production + 源码模式：拒绝，说清原因与改法
run_case prod-source production ''
[ "$CASE_STATUS" -ne 0 ] || fail 'production source mode was accepted'
for want in "$GATE" 'AEGIS_ENV=production' 'pdnd-dist' 'release-artifact.env' 'AEGIS_RELEASE_DIR'; do
  grep -Fq "$want" "$TMP/prod-source.out" || fail "production refusal lacks '$want': $(cat "$TMP/prod-source.out")"
done
# 大小写与首尾空白同 platform/config 一样宽容
run_case prod-upper ' Production ' ''
grep -Fq "$GATE" "$TMP/prod-upper.out" || fail 'AEGIS_ENV= Production  slipped past the gate'

# production + 发布包、development + 源码：都过这一关（之后在平台探测或依赖检查上停下）
run_case prod-release production "$TMP/release"
if grep -Fq "$GATE" "$TMP/prod-release.out"; then fail 'release mode was refused'; fi
run_case dev-source development ''
if grep -Fq "$GATE" "$TMP/dev-source.out"; then fail 'development source mode was refused'; fi
# 闸门之后的失败（平台、依赖或 docker compose 桩）证明脚本确实往下走了，而不是在更早的输入校验上停下
for label in prod-release dev-source; do
  grep -Eq '仅支持 Linux|缺少依赖|Docker Compose' "$TMP/$label.out" \
    || fail "$label did not get past the input checks: $(cat "$TMP/$label.out")"
done

printf 'migrate-to-new-host mock: PASS\n'
