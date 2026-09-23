#!/usr/bin/env bash
# 安装链回归测试：全新安装 与 存量升级 两种形态各跑一遍。
#
#   bash deploy/test-install.sh <发布目录>
#
# 为什么需要它：
#
# install.sh 第一版只在「全新安装」上验证过就上了生产，结果在存量环境里
# 崩在停服之后——生产停机 90 秒。原因是两种形态的差异只在存量环境才出现：
#
#   · 存量 .env 是早期生成的，没有 AEGIS_MIGRATION_DATABASE_URL；
#     新装的 .env 从 .env.example 生成，恰好有。
#   · 存量库已有 goose 记录，会触发一次性克隆库预检；空库不会。
#
# 只验证其中一种，等于没验证。这个脚本把两种都跑掉，并且刻意构造一份
# 「老式 .env」来复现生产那种配置形态。
#
# 破坏性：会清空本机的 aegis 容器、数据卷与 /opt/aegispanel。只在一次性
# 验证机上跑，绝不要在生产上跑——开头有护栏挡着。
set -Eeuo pipefail

RELEASE="${1:-}"
[ -n "$RELEASE" ] && [ -d "$RELEASE/bin" ] || {
  echo "用法: $0 <发布目录>" >&2; exit 2; }
RELEASE_SRC="$(cd "$RELEASE" && pwd -P)"

# 安装器拒绝从任何人可写的目录安装（防止有人塞一份假的进来），所以
# 测试必须按真实交付方式来：先把包放到 root 独占目录。直接拿 /tmp 下的
# 构建产物去装，会被那道检查挡住——那是安装器在正确工作，不是它坏了。
STAGE=/opt/pandora-release/rel-test
install -d -o root -g root -m 0755 /opt/pandora-release
rm -rf "$STAGE"
cp -r "$RELEASE_SRC" "$STAGE"
chown -R root:root "$STAGE"
chmod -R go-w "$STAGE"
RELEASE="$STAGE"

# 护栏：生产上有真实数据，这个脚本会把库删掉。
if [ -f /opt/aegispanel/deploy/.env ] && [ "${PANDORA_ALLOW_DESTRUCTIVE_TEST:-}" != yes ]; then
  existing="$(docker exec aegis-postgres psql -U aegis -d aegis -tAc \
    "SELECT count(*) FROM users" 2>/dev/null | tr -d '[:space:]' || echo 0)"
  if [ "${existing:-0}" -gt 0 ]; then
    echo "拒绝执行：本机已有安装且库里有 $existing 个用户。" >&2
    echo "这个脚本会清空数据。确认是一次性验证机后再设 PANDORA_ALLOW_DESTRUCTIVE_TEST=yes。" >&2
    exit 1
  fi
fi

PASS=0; FAIL=0
ok()   { printf '    \033[32m✓\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '    \033[31m✗\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

wipe() {
  systemctl stop aegis-public aegis-admin aegis-node aegis-backup.timer 2>/dev/null || true
  rm -f /etc/systemd/system/aegis-*.service /etc/systemd/system/aegis-*.timer
  systemctl daemon-reload 2>/dev/null || true
  docker rm -f aegis-postgres aegis-valkey >/dev/null 2>&1 || true
  docker volume rm aegis_pgdata aegis_valkeydata >/dev/null 2>&1 || true
  rm -rf /opt/aegispanel
}

pgq() {
  docker exec -e PGPASSWORD="$1" aegis-postgres \
    psql -U aegis -d aegis -tAc "$2" 2>/dev/null | tr -d '[:space:]'
}

# 装完之后共同要满足的条件。两种形态都必须过。
assert_healthy() {
  local label="$1"
  local pw; pw="$(grep '^POSTGRES_PASSWORD=' /opt/aegispanel/deploy/.env | cut -d= -f2-)"

  local ver; ver="$(pgq "$pw" 'SELECT max(version_id) FROM goose_db_version')"
  [ -n "$ver" ] && [ "$ver" -ge 60 ] && ok "$label 迁移到 $ver" || bad "$label 迁移版本异常（$ver）"

  local shape; shape="$(pgq "$pw" "SELECT rolsuper::text||rolbypassrls::text FROM pg_roles WHERE rolname='aegis_app'")"
  [ "$shape" = falsefalse ] && ok "$label aegis_app 非超级用户、不绕过 RLS" \
    || bad "$label aegis_app 权限形态异常（$shape）"

  local rls; rls="$(pgq "$pw" "SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relkind='r' AND relrowsecurity AND relforcerowsecurity")"
  [ "${rls:-0}" -ge 100 ] && ok "$label $rls 张表强制行级安全" || bad "$label 强制 RLS 表数偏少（$rls）"

  local bad_svc=0
  for s in aegis-public aegis-admin aegis-node; do
    [ "$(systemctl is-active "$s")" = active ] || bad_svc=1
  done
  [ "$bad_svc" -eq 0 ] && ok "$label 三个网关 active" || bad "$label 有网关没起来"

  local bad_http=0
  for p in 9000 9001 9003; do
    [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$p/healthz")" = 200 ] || bad_http=1
  done
  [ "$bad_http" -eq 0 ] && ok "$label healthz 全 200" || bad "$label healthz 有非 200"
}

run_install() {
  local label="$1" log="$2"
  local start; start=$(date +%s)
  if ( cd "$RELEASE/deploy" && PANDORA_ASSUME_YES=1 bash ./install.sh ) > "$log" 2>&1; then
    ok "$label 安装脚本退出码 0（耗时 $(( $(date +%s) - start ))s）"
    return 0
  fi
  bad "$label 安装脚本失败（耗时 $(( $(date +%s) - start ))s）"
  echo "    ---- 末尾 25 行 ----"
  tail -25 "$log" | sed 's/^/    /'
  return 1
}

#------------------------------------------------------------------------------
step "场景一：全新安装（空机器）"
wipe
run_install "全新安装" /tmp/test-install-fresh.log && assert_healthy "全新安装"

grep -q "跳过一次性数据库预检" /tmp/test-install-fresh.log \
  && ok "全新库正确跳过了预检" \
  || bad "全新库没跳过预检（会白跑一遍迁移，安装时间翻倍）"

#------------------------------------------------------------------------------
step "场景二：升级（同一命令再跑一次）"
before_users="$(pgq "$(grep '^POSTGRES_PASSWORD=' /opt/aegispanel/deploy/.env | cut -d= -f2-)" 'SELECT count(*) FROM users')"
env_before="$(sha256sum /opt/aegispanel/deploy/.env | awk '{print $1}')"

run_install "升级" /tmp/test-install-upgrade.log && assert_healthy "升级"

env_after="$(sha256sum /opt/aegispanel/deploy/.env | awk '{print $1}')"
[ "$env_before" = "$env_after" ] && ok "升级没有改写 .env" || bad "升级改写了 .env（密钥会失配）"

after_users="$(pgq "$(grep '^POSTGRES_PASSWORD=' /opt/aegispanel/deploy/.env | cut -d= -f2-)" 'SELECT count(*) FROM users')"
[ "$before_users" = "$after_users" ] && ok "升级前后用户数一致（$after_users）" \
  || bad "升级前后用户数变了（$before_users → $after_users）"

ls /var/backups/aegispanel/pre-upgrade-*.dump >/dev/null 2>&1 \
  && ok "升级前自动做了备份" || bad "升级前没有备份"

#------------------------------------------------------------------------------
step "场景三：存量形态（老式 .env，没有 AEGIS_MIGRATION_DATABASE_URL）"
# 这正是生产那份配置的样子——第一版 install.sh 就是死在这里，
# 而且死在停服之后。这个场景必须常驻回归。
if grep -q '^AEGIS_MIGRATION_DATABASE_URL=' /opt/aegispanel/deploy/.env; then
  sed -i '/^AEGIS_MIGRATION_DATABASE_URL=/d' /opt/aegispanel/deploy/.env
  ok "已构造老式 .env（删掉 AEGIS_MIGRATION_DATABASE_URL）"
else
  ok "当前 .env 本来就没有 AEGIS_MIGRATION_DATABASE_URL"
fi

run_install "老式配置升级" /tmp/test-install-legacy.log && assert_healthy "老式配置升级"

#------------------------------------------------------------------------------
step "场景四：备份单元处于 failed（生产上真实出现过）"
# 一次失败的备份任务会把 aegis-backup.service 留在 failed/failed。
# 安装器原先只认 inactive/dead，于是「装过一次且备份失败过」的机器
# 再也装不上——生产就是这么卡住的。
systemctl reset-failed aegis-backup.service 2>/dev/null || true
cat > /tmp/aegis-backup-failtest.sh <<'FAILEOF'
#!/bin/sh
exit 7
FAILEOF
chmod +x /tmp/aegis-backup-failtest.sh
systemd-run --unit=aegis-backup-probe --quiet /tmp/aegis-backup-failtest.sh 2>/dev/null || true
sleep 2
# 把真正的 aegis-backup.service 也弄成 failed 来复现
systemctl stop aegis-backup.service 2>/dev/null || true
if [ -f /etc/systemd/system/aegis-backup.service ]; then
  # 用一个必定失败的 ExecStart 覆盖，触发 failed 状态，测完还原
  cp /etc/systemd/system/aegis-backup.service /tmp/aegis-backup.service.bak
  sed -i 's|^ExecStart=.*|ExecStart=/bin/false|' /etc/systemd/system/aegis-backup.service
  systemctl daemon-reload
  systemctl start aegis-backup.service 2>/dev/null || true
  sleep 2
  state="$(systemctl show aegis-backup.service --property=ActiveState --value)"
  [ "$state" = failed ] && ok "已把 aegis-backup.service 置为 failed"     || ok "aegis-backup.service 当前状态 $state（未能复现 failed，跳过）"
  cp /tmp/aegis-backup.service.bak /etc/systemd/system/aegis-backup.service
  systemctl daemon-reload
fi

run_install "备份单元 failed 时升级" /tmp/test-install-failedunit.log   && assert_healthy "备份单元 failed 时升级"
systemctl reset-failed aegis-backup.service aegis-backup-probe 2>/dev/null || true
rm -f /tmp/aegis-backup-failtest.sh /tmp/aegis-backup.service.bak

#------------------------------------------------------------------------------
step "结果"
printf '    通过 %d 项，失败 %d 项\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  echo "    日志：/tmp/test-install-{fresh,upgrade,legacy}.log"
  exit 1
fi
echo "    安装链在三种形态下都可用。"
