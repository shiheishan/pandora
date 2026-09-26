#!/usr/bin/env bash
# [INPUT]: 依赖 /opt/aegis-migrate 下的 Age 密文备份与代码包、新主机的 deploy/.env（须含 AEGIS_PUBLIC_BASE_URL）
# [OUTPUT]: 在新主机恢复数据、重建 aegis_app、校验账本、编译启动并渲染 nginx
# [POS]: deploy 的整机迁移编排，复用 restore-postgres.sh / bootstrap.sh / render-nginx.sh，不接收未校验的额外模板
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
# 在**新服务器**上执行的一键迁移脚本。
#
# 前提：/opt/aegis-migrate/ 下已放好从旧机器带来的四件套
#   aegis-postgres-*.dump.age  Age 加密的 PostgreSQL custom-format 备份
#   同名 .sha256       密文校验文件
#   aegis-code.tgz         仅代码，明确不含 deploy/.env 或任何密钥
# systemd 与 nginx 模板随代码包提供，不再从迁移目录接收未校验的额外副本。
#
# deploy/.env（包括 AEGIS_MASTER_KEY）和 Age identity 必须通过 Vault/KMS/离线介质
# 独立供应，不得与代码包或数据库密文放在同一传输包中。
set -euo pipefail
umask 077

MIG=/opt/aegis-migrate
APP=/opt/aegispanel
NEW_IP="${NEW_IP:-}"
AEGIS_SECRET_ENV_FILE="${AEGIS_SECRET_ENV_FILE:-}"
AEGIS_MIGRATION_BACKUP="${AEGIS_MIGRATION_BACKUP:-}"
AEGIS_RELEASE_DIR="${AEGIS_RELEASE_DIR:-}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=platform.sh
. "$SCRIPT_DIR/platform.sh"

say(){ echo; echo "════════ $* ════════"; }
die(){ echo "错误: $*" >&2; exit 1; }

[ -n "$AEGIS_MIGRATION_BACKUP" ] || die "必须显式设置 AEGIS_MIGRATION_BACKUP"
[[ "$AEGIS_MIGRATION_BACKUP" = /* ]] || die "AEGIS_MIGRATION_BACKUP 必须是绝对路径"
[ -f "$AEGIS_MIGRATION_BACKUP" ] || die "缺少加密备份 $AEGIS_MIGRATION_BACKUP"
[ -f "${AEGIS_MIGRATION_BACKUP}.sha256" ] || die "缺少 ${AEGIS_MIGRATION_BACKUP}.sha256"
[ -f "$MIG/aegis-code.tgz" ] || die "缺少 $MIG/aegis-code.tgz"
[ -n "$AEGIS_SECRET_ENV_FILE" ] || die "必须通过独立密钥通道设置 AEGIS_SECRET_ENV_FILE"
[[ "$AEGIS_SECRET_ENV_FILE" = /* ]] || die "AEGIS_SECRET_ENV_FILE 必须是绝对路径"
[ -f "$AEGIS_SECRET_ENV_FILE" ] || die "找不到独立供应的环境文件"
[ "$(stat -c '%a' "$AEGIS_SECRET_ENV_FILE")" = "600" ] || die "AEGIS_SECRET_ENV_FILE 权限必须是 0600"
[ -n "$NEW_IP" ] || die "无法确定本机公网 IP，请用 NEW_IP=x.x.x.x 显式指定"

pandora_detect_platform

say "0. 目标环境"
echo "  公网 IP : $NEW_IP"
echo "  系统    : $(. /etc/os-release; echo "$PRETTY_NAME")"
echo "  架构    : linux/$PANDORA_ARCH"
echo "  CPU/内存: $(nproc) 核 / $(free -h|awk '/^Mem:/{print $2}')"
echo "  磁盘    : $(df -h / | awk 'NR==2{print $4" 可用 ("$5" 已用)"}')"

#-------------------------------------------------------------------------------
say "1. 依赖"

# 迁移脚本以 root 运行，因此绝不在线下载并执行未校验产物。基础镜像或
# 配置管理系统必须预装并锁定这些依赖；缺少任何一项就 fail-closed。
for command_name in curl git make python3 nginx age xxd docker sha256sum systemctl; do
  command -v "$command_name" >/dev/null 2>&1 || \
    die "缺少依赖 $command_name；请通过受控镜像/包仓预装并验证签名或摘要"
done
docker compose version >/dev/null 2>&1 || die "缺少已验证的 Docker Compose 插件"
if [ -n "$AEGIS_RELEASE_DIR" ]; then
  [ -d "$AEGIS_RELEASE_DIR/bin" ] || die "AEGIS_RELEASE_DIR 缺少 bin 目录"
  [ -f "$AEGIS_RELEASE_DIR/SHA256SUMS" ] || die "AEGIS_RELEASE_DIR 缺少 SHA256SUMS"
else
  pandora_go_version_ok || die "源码编译需要 Go 1.26+；也可用 AEGIS_RELEASE_DIR 指向预编译的 linux/$PANDORA_ARCH 发布包"
fi
echo "  docker: $(docker --version)"
echo "  compose: $(docker compose version | head -1)"
[ -n "$AEGIS_RELEASE_DIR" ] && echo "  binaries: $AEGIS_RELEASE_DIR (linux/$PANDORA_ARCH)" || echo "  go: $(go version)"

#-------------------------------------------------------------------------------
say "2. 释放代码"

[ "$(realpath "$AEGIS_SECRET_ENV_FILE")" != "$(realpath -m "$MIG/aegis-code.tgz")" ] \
  || die "密钥环境文件不得与代码包复用同一文件"
if tar -tzf "$MIG/aegis-code.tgz" | grep -Eq '(^|/)\.env($|\.)|(^|/)deploy/\.env$'; then
  die "代码包含有 .env；必须重新打包并通过独立密钥通道供应"
fi
[ -d "$APP" ] && { echo "  已存在 $APP，备份为 $APP.bak.$(date +%s)"; mv "$APP" "$APP.bak.$(date +%s)"; }
tar xzf "$MIG/aegis-code.tgz" -C /opt
mkdir -p "$APP/logs" "$APP/bin"
install -m 0600 "$AEGIS_SECRET_ENV_FILE" "$APP/deploy/.env"
echo "  代码已释放到 $APP"

#-------------------------------------------------------------------------------
say "3. 数据基座"

cd "$APP/deploy"
docker compose up -d
echo "  等待 PostgreSQL 健康…"
for i in $(seq 1 60); do
  s=$(docker inspect -f '{{.State.Health.Status}}' aegis-postgres 2>/dev/null || echo starting)
  [ "$s" = healthy ] && break
  sleep 3
done
[ "$s" = healthy ] || die "PostgreSQL 未能就绪（当前 $s），查看 docker logs aegis-postgres"
echo "  postgres: healthy"
echo "  valkey  : $(docker inspect -f '{{.State.Health.Status}}' aegis-valkey)"

#-------------------------------------------------------------------------------
say "4. 恢复数据库"

. "$APP/deploy/.env"

# 恢复密文备份。目标库、已存在库和配置主库的三道确认只对本次调用生效。
AEGIS_RESTORE_CONFIRM="RESTORE:${POSTGRES_DB}" \
AEGIS_RESTORE_EXISTING_CONFIRM="OVERWRITE_EXISTING:${POSTGRES_DB}" \
AEGIS_RESTORE_PRODUCTION_CONFIRM="OVERWRITE_CONFIGURED_DATABASE:${POSTGRES_DB}" \
  "$APP/deploy/restore-postgres.sh" \
    --archive "$AEGIS_MIGRATION_BACKUP" --target-db "$POSTGRES_DB"
echo "  恢复完成"

# pg_dump --no-owner/--no-acl 不携带集群角色与授权；在启动任何应用服务前幂等重建。
"$APP/deploy/bootstrap.sh"
echo "  aegis_app 角色与授权已重建"

psqlq(){ PGPASSWORD="$POSTGRES_PASSWORD" docker exec -i -e PGPASSWORD aegis-postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "$1" | tr -d '[:space:]'; }

echo "  用户 $(psqlq 'SELECT count(*) FROM users') 人 · 订单 $(psqlq 'SELECT count(*) FROM orders') 单 · 订阅 $(psqlq 'SELECT count(*) FROM subscriptions') 份"
echo "  账本分录 $(psqlq 'SELECT count(*) FROM ledger_entries') 条 · 审计 $(psqlq 'SELECT count(*) FROM audit_events') 条"

DRIFT=$(psqlq 'SELECT count(*) FROM app.verify_ledger_all()')
[ "$DRIFT" = 0 ] && echo "  账本一致性: 无漂移 ✓" || die "账本漂移 $DRIFT 个账户，恢复不完整"

#-------------------------------------------------------------------------------
say "5. 外部地址确认"

# 公网基址属于应用配置，不写进 systemd 单元。第 8 步渲染 nginx 时 server_name
# 与证书路径都从它取域名，缺了就会在服务已启动之后才失败，所以在这里提前拦下。
# 完整格式校验在 render-nginx.sh（https://<DNS 域名>，不带端口与路径）。
case "${AEGIS_PUBLIC_BASE_URL:-}" in
  https://*) echo "  AEGIS_PUBLIC_BASE_URL=$AEGIS_PUBLIC_BASE_URL" ;;
  *) die "deploy/.env 需要 AEGIS_PUBLIC_BASE_URL=https://<你的域名>：nginx 的 server_name 与证书路径从它生成" ;;
esac
echo "  检测到公网 IP=$NEW_IP（未自动覆盖域名配置）"

#-------------------------------------------------------------------------------
say "6. 编译"

cd "$APP"
if [ -n "$AEGIS_RELEASE_DIR" ]; then
  (cd "$AEGIS_RELEASE_DIR" && sha256sum -c SHA256SUMS) || die "发布包摘要校验失败"
  for b in aegis-public aegis-admin aegis-node aegis-payctl aegis-adminctl aegis-backup-webdav; do
    [ -f "$AEGIS_RELEASE_DIR/bin/$b" ] || die "发布包缺少 $b"
    install -m 0755 "$AEGIS_RELEASE_DIR/bin/$b" "bin/$b"
    echo "  ✓ $b (linux/$PANDORA_ARCH release)"
  done
else
  for b in aegis-public aegis-admin aegis-node aegis-payctl aegis-adminctl aegis-backup-webdav; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$PANDORA_ARCH" \
      go build -trimpath -ldflags="-s -w" -o "bin/$b" "./cmd/$b" \
      && echo "  ✓ $b (linux/$PANDORA_ARCH)" || die "编译 $b 失败"
  done
fi

#-------------------------------------------------------------------------------
say "7. 服务"

install -d -m 0755 /var/log/aegis
install -m 0644 \
  "$APP/deploy/systemd/aegis-public.service" \
  "$APP/deploy/systemd/aegis-admin.service" \
  "$APP/deploy/systemd/aegis-node.service" \
  /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now aegis-public aegis-admin aegis-node
sleep 4
for s in aegis-public aegis-admin aegis-node; do
  st=$(systemctl is-active $s)
  [ "$st" = active ] && echo "  ✓ $s" || { journalctl -u $s -n 20 --no-pager; die "$s 未启动"; }
done

#-------------------------------------------------------------------------------
say "8. 边缘入口"

"$APP/deploy/render-nginx.sh" "$APP/deploy/.env" /etc/nginx/conf.d/aegis.conf

# 9080 保持本机回环运维入口；公网测试入口使用 7001。生产公网入口应由
# HTTPS/零信任边缘显式提供。
nginx -t || die "nginx 配置校验失败"
systemctl reload nginx || systemctl restart nginx
echo "  nginx 已加载"

#-------------------------------------------------------------------------------
say "9. 验证"

fail=0
chk(){ c=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 "$2");
       [ "$c" = "$3" ] && echo "  ✓ $1 ($c)" || { echo "  ✗ $1 期望 $3 实际 $c"; fail=$((fail+1)); }; }

chk "用户门户"     "http://127.0.0.1:9080/"        200
chk "管理控制台"   "http://127.0.0.1:9080/${AEGIS_ADMIN_PATH}/" 200
chk "套餐接口"     "http://127.0.0.1:9080/v1/plans" 200
chk "readyz 应拒"  "http://127.0.0.1:9080/readyz"  403

# 主密钥是否真的能解开旧数据 —— 迁移成败的关键判据
if $APP/bin/aegis-payctl list --tenant 00000000-0000-7000-8000-000000000001 2>/dev/null | grep -q epay; then
  echo "  ✓ 支付渠道凭据可解密（主密钥迁移正确）"
else
  echo "  ✗ 支付渠道读取失败，检查 AEGIS_MASTER_KEY 是否与旧机器一致"; fail=$((fail+1))
fi

say "结果"
if [ "$fail" = 0 ]; then
  echo "迁移完成 ✓"
  echo
  echo "  用户门户    http://127.0.0.1:9080（仅经 SSH 隧道/VPN/零信任访问）"
  echo "  管理控制台  http://127.0.0.1:9080/<已配置管理路径>/（仅经 SSH 隧道/VPN/零信任访问）"
  echo
  echo "后续必做："
  echo "  1. 更新易支付商户后台的回调地址为 http://${NEW_IP}:9080/v1/webhooks/payments/epay"
  echo "  2. 确认新机器无误后再停旧机器的 aegis-public / aegis-admin"
  echo "  3. 旧机器的 /opt/aegis-migrate 含数据库全量备份，清理前先确认已不需要"
else
  echo "有 $fail 项未通过，请先排查再切流量"
  exit 1
fi
