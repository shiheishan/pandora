#!/usr/bin/env bash
# 潘多拉面板一键安装 / 升级。
#
#   首次安装：  sudo ./install.sh
#   升级：      sudo ./install.sh          （检测到已装会自动走升级）
#   无人值守：  sudo PANDORA_ASSUME_YES=1 ./install.sh
#
# 这个脚本把原先要手工串起来的七八步固化成一条命令：前置检查 → 生成配置
# → 起数据基座 → 迁移 → 收窄数据库角色 → 安装二进制与 systemd 单元 →
# 启动 → 健康检查。每一步都做过实机验证，顺序上的坑（下面注释里逐条记着）
# 都是踩出来的。
#
# 设计上的三条约束：
#
#   1. 幂等。已有 .env 绝不覆盖——那里面是随机生成的密钥，重写一次
#      就再也连不上原来的数据库了。
#   2. 不替用户造管理员。脚本生成并打印管理员密码，等于把它写进终端
#      回滚日志和 CI 输出里。装完只提示该跑哪条命令。
#   3. 失败即停，并说清楚停在哪一步、怎么恢复。
set -Eeuo pipefail
umask 022

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
RELEASE_ROOT="$(cd "$HERE/.." && pwd -P)"
DEST=/opt/aegispanel

step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
info() { printf '    %s\n' "$1"; }
warn() { printf '    \033[33m! %s\033[0m\n' "$1" >&2; }
die() {
  printf '\n\033[31m安装中止：%s\033[0m\n' "$1" >&2
  [ -n "${2:-}" ] && printf '%s\n' "$2" >&2
  exit 1
}

#------------------------------------------------------------------------------
# 1) 前置检查
#------------------------------------------------------------------------------
step "检查运行环境"

[ "$(uname -s)" = Linux ] || die "只支持 Linux"
[ "$(id -u)" -eq 0 ] || die "需要 root 权限" "请用 sudo 运行。"

for cmd in docker openssl sha256sum systemctl install awk sed curl; do
  command -v "$cmd" >/dev/null 2>&1 || die "缺少命令：$cmd"
done
docker compose version >/dev/null 2>&1 || die "缺少 docker compose 插件" \
  "参考 https://docs.docker.com/compose/install/"
docker info >/dev/null 2>&1 || die "docker 守护进程没在运行" "先执行：systemctl start docker"

# age 是备份加密要用的；缺了它安装器自己会拒绝，先在这里给出更清楚的提示。
command -v age >/dev/null 2>&1 || die "缺少 age（备份加密）" \
  "Debian/Ubuntu: apt-get install -y age"

[ -d "$RELEASE_ROOT/bin" ] || die "这不像一个发布目录：找不到 $RELEASE_ROOT/bin"
[ -f "$RELEASE_ROOT/SHA256SUMS" ] || die "发布目录缺少 SHA256SUMS"

# 安装器拒绝从任何人可写的目录安装（防止有人往 /tmp 里塞一份假的）。
# 这条检查在 install-linux-binaries.sh 里，这里提前说清楚，免得走到一半才失败。
probe="$RELEASE_ROOT"
while :; do
  perm="$(stat -c '%a' "$probe")"
  case "$perm" in
    *2|*3|*6|*7)
      die "发布目录的父路径可被非 root 写入：$probe（权限 $perm）" \
          "把发布包放到 root 独占的目录再装，例如：
  install -d -o root -g root -m 0755 /opt/pandora-release
  cp -r <发布目录> /opt/pandora-release/rel && chown -R root:root /opt/pandora-release" ;;
  esac
  [ "$probe" = / ] && break
  probe="$(dirname "$probe")"
done
info "环境检查通过（$(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME") / $(uname -m)）"

#------------------------------------------------------------------------------
# 2) 判断首装还是升级
#------------------------------------------------------------------------------
MODE=install
[ -f "$DEST/deploy/.env" ] && MODE=upgrade
step "运行模式：$([ "$MODE" = install ] && echo '首次安装' || echo '升级现有安装')"

if [ "$MODE" = upgrade ]; then
  info "检测到 $DEST/deploy/.env，将保留现有配置与数据"
else
  info "全新安装到 $DEST"
fi

if [ "${PANDORA_ASSUME_YES:-}" != 1 ] && [ -t 0 ]; then
  printf '    继续？[y/N] '
  read -r reply
  case "$reply" in [yY]*) ;; *) die "已取消" ;; esac
fi

#------------------------------------------------------------------------------
# 3) 生成配置（仅首装）
#------------------------------------------------------------------------------
install -d -o root -g root -m 0755 "$DEST" "$DEST/deploy"

if [ "$MODE" = install ]; then
  step "生成配置"
  rand() { openssl rand -base64 48 | tr -dc 'A-Za-z0-9_-' | head -c 44; }

  PGPW="$(rand)"; APPPW="$(rand)"; VKPW="$(rand)"
  ADMIN_PATH="ops_$(openssl rand -hex 24)"

  install -d -o root -g root -m 0700 "$DEST/secrets"
  [ -f "$DEST/secrets/backup-age.key" ] || age-keygen -o "$DEST/secrets/backup-age.key" 2>/dev/null
  AGE_RECIPIENT="$(age-keygen -y "$DEST/secrets/backup-age.key")"

  sed \
    -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$PGPW|" \
    -e "s|^AEGIS_MIGRATION_DATABASE_URL=.*|AEGIS_MIGRATION_DATABASE_URL=postgres://aegis:$PGPW@127.0.0.1:5433/aegis?sslmode=disable|" \
    -e "s|^AEGIS_DB_APP_PASSWORD=.*|AEGIS_DB_APP_PASSWORD=$APPPW|" \
    -e "s|^VALKEY_PASSWORD=.*|VALKEY_PASSWORD=$VKPW|" \
    -e "s|^AEGIS_DATABASE_URL=.*|AEGIS_DATABASE_URL=postgres://aegis_app:$APPPW@127.0.0.1:5433/aegis?sslmode=disable|" \
    -e "s|^AEGIS_REDIS_URL=.*|AEGIS_REDIS_URL=redis://:$VKPW@127.0.0.1:6380/0|" \
    -e "s|^AEGIS_ADMIN_PATH=.*|AEGIS_ADMIN_PATH=$ADMIN_PATH|" \
    -e "s|^AEGIS_MASTER_KEY=.*|AEGIS_MASTER_KEY=$(openssl rand -base64 32)|" \
    -e "s|^AEGIS_JWT_PUBLIC_SECRET=.*|AEGIS_JWT_PUBLIC_SECRET=$(rand)|" \
    -e "s|^AEGIS_JWT_ADMIN_SECRET=.*|AEGIS_JWT_ADMIN_SECRET=$(rand)|" \
    -e "s|^AEGIS_JWT_CLIENT_SECRET=.*|AEGIS_JWT_CLIENT_SECRET=$(rand)|" \
    -e "s|^AEGIS_CONFIG_SIGNING_SEED=.*|AEGIS_CONFIG_SIGNING_SEED=$(openssl rand -base64 32)|" \
    -e "s|^AEGIS_BACKUP_AGE_RECIPIENT=.*|AEGIS_BACKUP_AGE_RECIPIENT=$AGE_RECIPIENT|" \
    -e "s|^AEGIS_BACKUP_AGE_IDENTITY=.*|AEGIS_BACKUP_AGE_IDENTITY=$DEST/secrets/backup-age.key|" \
    "$RELEASE_ROOT/deploy/.env.example" > "$DEST/deploy/.env"
  chmod 600 "$DEST/deploy/.env"

  remaining="$(grep -c 'CHANGE_ME' "$DEST/deploy/.env" || true)"
  if [ "$remaining" -gt 0 ]; then
    warn "还有 $remaining 项需要手工填写（装完后编辑 $DEST/deploy/.env）："
    grep -n 'CHANGE_ME' "$DEST/deploy/.env" | sed 's/=.*/=…/' | sed 's/^/      /' >&2
  fi
  info "已生成 $DEST/deploy/.env（密钥随机，未打印）"
else
  info "沿用现有 $DEST/deploy/.env"
fi

# 这几个是后面几步就要用的，不能等到安装器那一步才落地：
# bootstrap.sh 会 cd 到自己所在目录再读 ./.env，必须先在 $DEST/deploy 里就位。
cp "$RELEASE_ROOT/deploy/docker-compose.yml" "$DEST/deploy/"
cp "$RELEASE_ROOT/deploy/configure-app-role.sql" "$DEST/deploy/"
cp "$RELEASE_ROOT/deploy/bootstrap.sh" "$DEST/deploy/"
cp "$RELEASE_ROOT/deploy/psql.sh" "$DEST/deploy/" 2>/dev/null || true
chmod 0755 "$DEST/deploy/bootstrap.sh" "$DEST/deploy/psql.sh" 2>/dev/null || true
install -d -o root -g root -m 0755 "$DEST/migrations"
cp "$RELEASE_ROOT"/migrations/*.sql "$DEST/migrations/"

set -a; . "$DEST/deploy/.env"; set +a

#------------------------------------------------------------------------------
# 4) 升级前先备份
#------------------------------------------------------------------------------
if [ "$MODE" = upgrade ]; then
  step "备份数据库"
  if docker ps --format '{{.Names}}' | grep -qx aegis-postgres; then
    BK="/var/backups/aegispanel/pre-upgrade-$(date +%Y%m%d-%H%M%S).dump"
    install -d -o root -g root -m 0700 /var/backups/aegispanel
    docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
      pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f /tmp/pre-upgrade.dump
    docker cp aegis-postgres:/tmp/pre-upgrade.dump "$BK" >/dev/null
    docker exec aegis-postgres rm -f /tmp/pre-upgrade.dump
    info "已备份到 $BK（$(du -h "$BK" | cut -f1)）"
  else
    warn "数据库容器没在运行，跳过备份"
  fi
fi

#------------------------------------------------------------------------------
# 5) 数据基座
#------------------------------------------------------------------------------
step "启动数据基座（PostgreSQL 18 + Valkey）"
( cd "$DEST/deploy" && docker compose up -d ) 2>&1 | sed 's/^/    /'

# pg_isready 在 initdb 阶段就会说「就绪」，那时业务库还没建出来，
# 紧接着的第一条 SQL 会撞 database does not exist。等到真能连上业务库为止。
info "等待数据库就绪"
ready=0
for _ in $(seq 1 150); do
  if docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
       psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc 'SELECT 1' >/dev/null 2>&1; then
    ready=1; break
  fi
  sleep 1
done
[ "$ready" = 1 ] || die "数据库 150 秒内没有就绪" \
  "看日志：docker logs --tail 50 aegis-postgres"
info "$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
        psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc 'SELECT version()' | cut -c1-48)"

#------------------------------------------------------------------------------
# 6) 迁移
#------------------------------------------------------------------------------
step "执行数据库迁移"

# 几个涉及幂等键与订单释放的迁移是 fail-closed 的：必须显式声明「写入者
# 已停止」才肯执行。首装时本来就没有写入者，升级时前面已经停服，两种
# 情况下这份声明都成立。
#
# 具体批准值固定在 migrate.sh 内部，这里只递交「已停止」这个事实——
# 那是有意的设计：不让调用方通过特权迁移入口注入任意 libpq 选项。

# 迁移走 migrate.sh，不自己拼 goose 命令。
#
# 它比这里周全：AEGIS_MIGRATION_DATABASE_URL 缺失时从 POSTGRES_* 回退，
# 密码只走环境不进 DSN，回退固定 loopback，并要求显式批准。
#
# 第一版这里直接引用那个变量，结果在只有 POSTGRES_* 的存量 .env 上崩在
# 停服之后，把生产撂在停机状态。教训有两条：能复用的运维脚本别另写一套；
# 新装环境与存量环境的配置形态不同，只验证新装那种是不够的。
# migrate.sh 会先在一次性数据库上演练一遍迁移（check-migrations.sh），
# 通过了才碰真库——这道预检值得留着，但它的依赖必须一起就位，
# 否则报的是「precheck failed」，看着像迁移本身有问题。
for f in migrate.sh platform.sh check-migrations.sh; do
  cp "$RELEASE_ROOT/deploy/$f" "$DEST/deploy/" 2>/dev/null || true
  chmod 0755 "$DEST/deploy/$f" 2>/dev/null || true
done

# 查询失败要返回空串，而不是让整个脚本死掉。
#
# 空库上查 goose_db_version 必然报「表不存在」，set -e + pipefail 会把
# 这个非零退出变成脚本终止——而且是在赋值那一行静默退出，屏幕上连
# 错误都没有。第一版就是这么挂的。
pgq() {
  docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "$1" 2>/dev/null | tr -d '[:space:]' || true
}
before="$(pgq 'SELECT coalesce(max(version_id),0) FROM goose_db_version')" || before=""
[ -n "$before" ] || before=0

restore_services() {
  warn "迁移失败，正在把服务拉回来"
  systemctl start aegis-public aegis-admin aegis-node 2>/dev/null || true
}

if [ "$MODE" = upgrade ]; then
  info "停止服务后再迁移"
  systemctl stop aegis-public aegis-admin aegis-node 2>/dev/null || true
  # 迁移失败不能把生产撂在停机状态。
  trap restore_services ERR
fi

# 发布包自带 goose；migrate.sh 通过 GOOSE_BIN 认它，免得用到目标机器上
# 碰巧装着的其它版本。
GOOSE_FOR_MIGRATE="$RELEASE_ROOT/bin/goose"
if [ ! -x "$GOOSE_FOR_MIGRATE" ]; then
  GOOSE_FOR_MIGRATE="$(command -v goose || echo /root/go/bin/goose)"
  warn "发布包里没有 goose，改用 $GOOSE_FOR_MIGRATE"
fi

# 迁移输出落盘再择要显示：失败时要能看到完整原因。
# 第一版直接 | tail -4，把真正的报错吞掉了，日志上只剩「执行数据库迁移」
# 一行，排障时完全看不出发生了什么。
MIGRATE_LOG="$(mktemp)"
# 库里没有 goose 记录 = 从没跑过迁移 = 预检没有可保护的东西。
# 这种情况下跳过它，省掉一遍完整迁移（单核机器上是十分钟量级的差别）。
SKIP_PRECHECK=""
if [ "$(pgq "SELECT (pg_catalog.to_regclass('public.goose_db_version') IS NOT NULL)::text")" = false ]; then
  SKIP_PRECHECK="PANDORA_SKIP_PRECHECK_FRESH_DB=yes-empty-database"
  info "全新库，跳过一次性数据库预检"
else
  info "已有迁移记录，先在一次性克隆库上演练（这一步比较慢）"
fi

if ( cd "$DEST/deploy" && env \
      "GOOSE_BIN=$GOOSE_FOR_MIGRATE" \
      "PANDORA_LOCAL_MIGRATION_APPROVED=yes" \
      "PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes" \
      ${SKIP_PRECHECK:+"$SKIP_PRECHECK"} \
      bash ./migrate.sh up ) > "$MIGRATE_LOG" 2>&1; then
  tail -4 "$MIGRATE_LOG" | sed 's/^/    /'
  rm -f "$MIGRATE_LOG"
else
  rc=$?
  echo "    ---- 迁移完整输出 ----" >&2
  sed 's/^/    /' "$MIGRATE_LOG" >&2
  rm -f "$MIGRATE_LOG"
  die "迁移失败（退出码 $rc）" "上面是完整输出。预检失败时数据库未被改动。"
fi

if [ "$MODE" = upgrade ]; then
  trap - ERR
fi

after="$(pgq 'SELECT max(version_id) FROM goose_db_version')"
info "迁移版本 $before → ${after:-未知}"


#------------------------------------------------------------------------------
# 7) 收窄数据库角色
#------------------------------------------------------------------------------
step "收窄运行时数据库角色"
( cd "$DEST/deploy" && bash ./bootstrap.sh ) 2>&1 | tail -2 | sed 's/^/    /'

shape="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc \
  "SELECT rolsuper::text||' '||rolbypassrls::text FROM pg_roles WHERE rolname='aegis_app'")"
[ "$(echo "$shape" | tr -d ' ')" = "falsefalse" ] \
  || die "aegis_app 不该是超级用户或绕过 RLS（当前：$shape）"
rls="$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc \
  "SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace
     AND relkind='r' AND relrowsecurity AND relforcerowsecurity" | tr -d ' ')"
info "aegis_app 非超级用户、不绕过 RLS；$rls 张表启用强制行级安全"

#------------------------------------------------------------------------------
# 8) 安装二进制与 systemd 单元
#------------------------------------------------------------------------------
step "安装程序与服务单元"
digest="$(sha256sum "$RELEASE_ROOT/SHA256SUMS" | awk '{print $1}')"
PANDORA_RELEASE_MANIFEST_SHA256="$digest" \
  bash "$RELEASE_ROOT/deploy/install-linux-binaries.sh" "$RELEASE_ROOT" 2>&1 | tail -3 | sed 's/^/    /'

#------------------------------------------------------------------------------
# 9) 启动并检查
#------------------------------------------------------------------------------
step "启动服务"
systemctl daemon-reload
for s in aegis-public aegis-admin aegis-node; do
  systemctl enable "$s" >/dev/null 2>&1 || true
  systemctl restart "$s"
done

ok=1
for _ in $(seq 1 30); do
  ok=1
  for p in 9000 9001 9003; do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$p/healthz" || echo 000)"
    [ "$code" = 200 ] || ok=0
  done
  [ "$ok" = 1 ] && break
  sleep 2
done

for s in aegis-public aegis-admin aegis-node; do
  info "$(printf '%-14s %s' "$s" "$(systemctl is-active "$s")")"
done
for p in 9000:公开 9001:管理 9003:节点; do
  info "$(printf '%-14s %s' "${p#*:}接口 :${p%%:*}" \
    "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:${p%%:*}/healthz" || echo 000)")"
done

if [ "$ok" != 1 ]; then
  die "服务起来了但健康检查没通过" \
      "看日志：journalctl -u aegis-public -n 50 --no-pager
      或：tail -50 /var/log/aegis/public.log"
fi

#------------------------------------------------------------------------------
# 10) 收尾提示
#------------------------------------------------------------------------------
step "安装完成"
cat <<EOF
    三个网关只监听 127.0.0.1，公网访问需要在前面放一个反向代理并配好 TLS。
    渲染 nginx 配置：先把 $DEST/deploy/.env 的 AEGIS_PUBLIC_BASE_URL 改成 https://你的域名，
    再执行 $DEST/deploy/render-nginx.sh（server_name 与证书路径都从这个域名生成）

    管理后台路径（高熵，泄露等同暴露入口）：
      /$AEGIS_ADMIN_PATH/

$([ "$MODE" = install ] && cat <<'FIRST'
    还差最后一步——创建管理员。脚本不替你生成密码，避免它出现在终端
    记录和日志里。自己跑：

      cd /opt/aegispanel
      set -a; . deploy/.env; set +a
      ./bin/aegis-adminctl create --email <你的邮箱> --password-stdin
FIRST
)
    常用操作：
      查看状态    systemctl status aegis-public aegis-admin aegis-node
      查看日志    tail -f /var/log/aegis/public.log
      重启        systemctl restart aegis-public aegis-admin aegis-node
      数据库      cd $DEST/deploy && ./psql.sh
EOF
