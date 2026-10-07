#!/usr/bin/env bash
# 潘多拉面板一键安装 / 升级。
#
#   首次安装：  sudo ./install.sh
#   升级：      sudo ./install.sh          （检测到已装会自动走升级）
#   无人值守：  sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://你的域名 ./install.sh
#   顺带申请证书：加 PANDORA_CERTBOT=1（等于同意 Let's Encrypt 的订户协议；
#               PANDORA_CERTBOT_EMAIL=你的邮箱 可选）。不碰 nginx：PANDORA_SKIP_NGINX=1
#
# 发布包装出来的就是生产：首装写 AEGIS_ENV=production。生产模式下网关启动时
# 要求 AEGIS_PUBLIC_BASE_URL 是 https://公网域名（platform/config 的
# CanonicalPublicOrigin），节点接入要求发布物的 SHA-256 与版本（deploy/
# release-artifact.env，随发布包生成）。所以首装必须先拿到
# 域名：PANDORA_PUBLIC_BASE_URL 给出，或在终端里现场问；两者都没有就在动手前停下。
#
# 这个脚本把原先要手工串起来的七八步固化成一条命令：前置检查 → 生成配置
# → 升级前备份 → 起数据基座、网关改走 unix socket → 迁移 → 收窄数据库角色 →
# 安装二进制与 systemd 单元 → 启动 → 健康检查 → nginx 边缘（防火墙、证书、
# 渲染、nginx -t、reload）。每一步都做过实机验证，顺序上的坑（下面注释里逐条记着）
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

# 宿主机上的路径。PANDORA_* 覆盖只给桩测试用（install-chain_mock_test.sh 把它们指到临时目录）
NGINX_DIR="${PANDORA_NGINX_DIR:-/etc/nginx}"
LE_LIVE_DIR="${PANDORA_LE_LIVE_DIR:-/etc/letsencrypt/live}"
ACME_WEBROOT="${PANDORA_ACME_WEBROOT:-/var/www/aegis-acme}"
REALIP_FILE="${PANDORA_REALIP_FILE:-/etc/aegispanel/cloudflare-realip.conf}"
BACKUP_DIR="${PANDORA_BACKUP_DIR:-/var/backups/aegispanel}"
# docker-compose.yml 把两个 socket 挂到 deploy/run/ 下
PG_SOCKET_DIR="$DEST/deploy/run/postgresql"
VK_SOCKET="$DEST/deploy/run/valkey/valkey.sock"

#------------------------------------------------------------------------------
# 步骤函数。install-chain_mock_test.sh 以 PANDORA_INSTALL_LIB=1 source 本文件，
# 只取这些函数，不往下执行任何安装动作
#------------------------------------------------------------------------------

# .env 里还要手工填的项：只看「键=值」行。.env.example 的注释里也写着 CHANGE_ME
# （解释这个占位符的用途），按整份文件数会把注释算成一项，装完误报「还有 1 项要填」
pending_env_lines() {
  grep -nE '^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*=.*CHANGE_ME' "$1" || true
}

# 一条连接串的形态：socket（经 unix socket）、tcp（安装器生成的回环原样）、custom（手工改过）
conn_form() {
  case "$1" in
    postgres://*@/*\?host=/*|unix://*) echo socket ;;
    *)
      if [[ "$1" =~ ^postgres://aegis_app:[A-Za-z0-9_-]+@127\.0\.0\.1:5433/aegis\?sslmode=disable$ \
         || "$1" =~ ^redis://:[A-Za-z0-9_-]+@127\.0\.0\.1:6380/0$ ]]; then
        echo tcp
      else
        echo custom
      fi ;;
  esac
}

SOCKET_ROLLBACK_NOTE='# 网关经 unix socket 连 PG 与 Valkey（install.sh 换的，socket 由 docker-compose.yml 挂到 deploy/run/）。'

# 把网关的两条连接串从「回环端口 → docker-proxy → 容器」换成 unix socket。
#   switch_env_to_sockets <.env> <PG socket 目录> <Valkey socket 文件>
# 只改安装器自己生成的回环原样（口令照抄）；已是 socket 或手工改过的值不碰。
# 迁移用的 AEGIS_MIGRATION_DATABASE_URL 不动，仍走回环端口。
switch_env_to_sockets() {
  local env_file="$1" pg_dir="$2" vk_sock="$3" tmp
  [[ "$pg_dir" =~ ^/[A-Za-z0-9/._-]+$ && "$vk_sock" =~ ^/[A-Za-z0-9/._-]+$ ]] \
    || { warn "socket 路径不合规：$pg_dir / $vk_sock"; return 1; }
  tmp="$(mktemp "$env_file.tmp.XXXXXX")"
  sed -E \
    -e "s#^AEGIS_DATABASE_URL=postgres://aegis_app:([A-Za-z0-9_-]+)@127\\.0\\.0\\.1:5433/aegis\\?sslmode=disable\$#AEGIS_DATABASE_URL=postgres://aegis_app:\\1@/aegis?host=${pg_dir}#" \
    -e "s#^AEGIS_REDIS_URL=redis://:([A-Za-z0-9_-]+)@127\\.0\\.0\\.1:6380/0\$#AEGIS_REDIS_URL=unix://:\\1@${vk_sock}?db=0#" \
    "$env_file" >"$tmp"
  if cmp -s "$env_file" "$tmp"; then
    rm -f -- "$tmp"
  else
    if ! grep -Fqx "$SOCKET_ROLLBACK_NOTE" "$tmp"; then
      {
        printf '\n%s\n' "$SOCKET_ROLLBACK_NOTE"
        printf '%s\n' '# 回退到不带 run/ 挂载的旧发布包之前，先把这两条改回回环形式（口令不变）：'
        printf '%s\n' '#   postgres://aegis_app:<AEGIS_DB_APP_PASSWORD>@127.0.0.1:5433/aegis?sslmode=disable'
        printf '%s\n' '#   redis://:<VALKEY_PASSWORD>@127.0.0.1:6380/0'
      } >>"$tmp"
    fi
    chmod 0600 "$tmp"
    mv -f -- "$tmp" "$env_file"
  fi
  local key
  for key in AEGIS_DATABASE_URL AEGIS_REDIS_URL; do
    printf '%s %s\n' "$key" "$(conn_form "$(pandora_env_file_value "$env_file" "$key")")"
  done
}

# ufw 开着才放行 80/443（证书校验与 HTTPS 入口都要）；没装或没开就什么都不做
open_firewall() {
  command -v ufw >/dev/null 2>&1 || return 0
  ufw status 2>/dev/null | grep -q '^Status: active' || return 0
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  info "ufw 已开启：放行了 80/tcp 与 443/tcp（ufw status 可查）"
}

nginx_reload() {
  if systemctl is-active --quiet nginx; then
    systemctl reload nginx
  else
    systemctl enable --now nginx >/dev/null 2>&1 || systemctl start nginx
  fi
}

# Debian/Ubuntu 的 nginx 包自带默认站点，它也 listen 80 default_server，与 aegis.conf
# 抢同一个位置，nginx -t 报 duplicate default server。只停用包里原样的那个链接
# （sites-enabled/default → sites-available/default），原文件留着，随时能链回去；
# 别的冲突不替人处理，交给 nginx -t 报出来
disable_stock_default_site() {
  local link="$NGINX_DIR/sites-enabled/default"
  [ -L "$link" ] || return 0
  case "$(readlink "$link")" in
    */sites-available/default|../sites-available/default) ;;
    *) return 0 ;;
  esac
  grep -Eq '^[[:space:]]*listen[^;#]*default_server' "$link" 2>/dev/null || return 0
  rm -f -- "$link"
  info "停用了 nginx 自带的默认站点（它与面板抢 80 端口的 default_server）"
  info "  原文件还在 $NGINX_DIR/sites-available/default，要恢复：ln -s ../sites-available/default $link"
}

have_certificate() {
  [ -s "$LE_LIVE_DIR/$1/fullchain.pem" ] && [ -s "$LE_LIVE_DIR/$1/privkey.pem" ]
}

# 没有证书时按需用 certbot 的 webroot 方式申请。要显式同意：PANDORA_CERTBOT=1，
# 或在终端里答 y（等于同意 Let's Encrypt 的订户协议）。
# 申请期间临时放一个只回答该域名 ACME 校验的 80 端口站点，申请完就删掉；
# 不依赖发行版默认站点，也不碰 aegis.conf（它要证书在才能通过 nginx -t）。
# 返回 0 有证书；1 申请失败；2 没同意申请（不是错误，只是还不能渲染）
obtain_certificate() {
  local domain="$1" acme_conf rc=0 reply
  have_certificate "$domain" && return 0
  if [ "${PANDORA_CERTBOT:-}" != 1 ]; then
    if [ "${PANDORA_ASSUME_YES:-}" != 1 ] && [ -t 0 ]; then
      printf '    %s 还没有证书。现在用 certbot 申请 Let'"'"'s Encrypt 证书（同意其订户协议）？[y/N] ' "$domain"
      read -r reply
      case "$reply" in [yY]*) ;; *) return 2 ;; esac
    else
      return 2
    fi
  fi
  command -v certbot >/dev/null 2>&1 || { warn "没有 certbot：apt-get install -y certbot 后重跑"; return 1; }
  acme_conf="$NGINX_DIR/conf.d/aegis-acme.conf"
  install -d -m 0755 "$ACME_WEBROOT"
  cat >"$acme_conf" <<ACME
# install.sh 申请证书时临时放的站点，申请完即删除
server {
    listen 80;
    listen [::]:80;
    server_name $domain;
    location ^~ /.well-known/acme-challenge/ { root $ACME_WEBROOT; }
    location / { return 404; }
}
ACME
  if ! nginx -t >/dev/null 2>&1; then
    rm -f -- "$acme_conf"
    warn "放进 ACME 校验站点后 nginx -t 不通过，先修好 nginx 现有配置：nginx -t"
    return 1
  fi
  nginx_reload
  local email_args=(--register-unsafely-without-email)
  [ -n "${PANDORA_CERTBOT_EMAIL:-}" ] && email_args=(--email "$PANDORA_CERTBOT_EMAIL")
  certbot certonly --webroot -w "$ACME_WEBROOT" -d "$domain" \
    --non-interactive --agree-tos "${email_args[@]}" || rc=$?
  rm -f -- "$acme_conf"
  nginx_reload || true
  [ "$rc" -eq 0 ] && have_certificate "$domain" && return 0
  warn "certbot 没有拿到证书（退出码 $rc）：确认 $domain 解析到本机、80 端口从公网可达"
  return 1
}

# 渲染 aegis.conf → nginx -t → reload。原有的 aegis.conf 先备份；nginx -t 不过就原样
# 换回去（没有原文件就删掉新的），nginx 继续跑旧配置，不会因为这一步停摆。
#   apply_edge_config <render-nginx.sh> <.env>
apply_edge_config() {
  local render="$1" env_file="$2" out="$NGINX_DIR/conf.d/aegis.conf" prev="" log
  install -d -m 0755 "$NGINX_DIR/conf.d"
  if [ -f "$out" ]; then
    install -d -m 0700 "$BACKUP_DIR" || return 1
    prev="$BACKUP_DIR/nginx-aegis.conf.$(date +%Y%m%d-%H%M%S)"
    cp -p -- "$out" "$prev" || { warn "备份原 nginx 配置失败，没有改动"; return 1; }
  fi
  bash "$render" "$env_file" "$out" "$REALIP_FILE" | sed 's/^/    /' \
    || { warn "render-nginx.sh 拒绝渲染（原因见上），nginx 配置没动"; return 1; }
  log="$(nginx -t 2>&1)" || {
    if [ -n "$prev" ]; then cp -p -- "$prev" "$out"; else rm -f -- "$out"; fi
    printf '%s\n' "$log" | sed 's/^/    /' >&2
    warn "新渲染的 nginx 配置没通过 nginx -t，已换回原来的（${prev:-原来没有 aegis.conf}）"
    return 1
  }
  nginx_reload
  info "nginx 配置已渲染并生效：$out${prev:+（原配置备份在 $prev）}"
}

# 可单测的部分到此为止
if [ "${PANDORA_INSTALL_LIB:-}" = 1 ]; then
  return 0 2>/dev/null || exit 0
fi

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
[ -f "$RELEASE_ROOT/deploy/release-artifact.env" ] || die "发布目录缺少 deploy/release-artifact.env" \
  "它由 build-release.sh 生成，记着节点端二进制的 SHA-256 与版本；没有它生产模式下节点接入全部被拒。"
info "环境检查通过（$(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME") / $(uname -m)）"

# 对外地址的校验与取值、.env 单键读取，与 install-native.sh 共用一份
[ -f "$HERE/public-base-url.sh" ] || die "发布目录缺少 deploy/public-base-url.sh"
. "$HERE/public-base-url.sh"

#------------------------------------------------------------------------------
# 2) 判断首装还是升级
#------------------------------------------------------------------------------
MODE=install
[ -f "$DEST/deploy/.env" ] && MODE=upgrade
step "运行模式：$([ "$MODE" = install ] && echo '首次安装' || echo '升级现有安装')"

if [ "$MODE" = upgrade ]; then
  info "检测到 $DEST/deploy/.env，将保留现有配置与数据"
  # 升级不改运行模式：把别人的 development 悄悄改成 production，可能因为域名
  # 不合规让三个网关起不来。只提示，由管理员自己决定。
  current_env="$(pandora_env_file_value "$DEST/deploy/.env" AEGIS_ENV)"
  if [ "${current_env,,}" != production ]; then
    warn "现有 .env 的 AEGIS_ENV=${current_env:-（未设置，按 development）}，不是 production："
    warn "  节点接入不会强制校验发布物的 SHA-256 与版本，支付回调、插件钩子也按开发模式放宽。"
    warn "  这次升级不改它。要切到生产：确认 AEGIS_PUBLIC_BASE_URL 是 https://公网域名，"
    warn "  把 AEGIS_ENV 改成 production，再 systemctl restart aegis-public aegis-admin aegis-node"
  fi
else
  info "全新安装到 $DEST"
  PUBLIC_BASE_URL="$(pandora_resolve_public_base_url ./install.sh)" \
    || die "首装需要合规的面板对外地址（原因见上）"
  info "面板对外地址：$PUBLIC_BASE_URL（运行模式 production）"
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
    -e "s|^AEGIS_ENV=.*|AEGIS_ENV=production|" \
    -e "s|^AEGIS_PUBLIC_BASE_URL=.*|AEGIS_PUBLIC_BASE_URL=$PUBLIC_BASE_URL|" \
    -e "s|^AEGIS_MASTER_KEY=.*|AEGIS_MASTER_KEY=$(openssl rand -base64 32)|" \
    -e "s|^AEGIS_JWT_PUBLIC_SECRET=.*|AEGIS_JWT_PUBLIC_SECRET=$(rand)|" \
    -e "s|^AEGIS_JWT_ADMIN_SECRET=.*|AEGIS_JWT_ADMIN_SECRET=$(rand)|" \
    -e "s|^AEGIS_CONFIG_SIGNING_SEED=.*|AEGIS_CONFIG_SIGNING_SEED=$(openssl rand -base64 32)|" \
    -e "s|^AEGIS_BACKUP_AGE_RECIPIENT=.*|AEGIS_BACKUP_AGE_RECIPIENT=$AGE_RECIPIENT|" \
    -e "s|^AEGIS_BACKUP_AGE_IDENTITY=.*|AEGIS_BACKUP_AGE_IDENTITY=$DEST/secrets/backup-age.key|" \
    "$RELEASE_ROOT/deploy/.env.example" > "$DEST/deploy/.env"
  chmod 600 "$DEST/deploy/.env"
  [ "$(pandora_env_file_value "$DEST/deploy/.env" AEGIS_ENV)" = production ] \
    && [ "$(pandora_env_file_value "$DEST/deploy/.env" AEGIS_PUBLIC_BASE_URL)" = "$PUBLIC_BASE_URL" ] \
    || die "生成的 .env 缺少 AEGIS_ENV=production 或 AEGIS_PUBLIC_BASE_URL" "检查发布包里的 deploy/.env.example 是否被改过。"

  pending="$(pending_env_lines "$DEST/deploy/.env")"
  if [ -n "$pending" ]; then
    warn "还有 $(printf '%s\n' "$pending" | wc -l | tr -d ' ') 项需要手工填写（装完后编辑 $DEST/deploy/.env）："
    printf '%s\n' "$pending" | sed 's/=.*/=…/' | sed 's/^/      /' >&2
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

# pg_isready 在 initdb 阶段就会说「就绪」，那时业务库还没建出来，
# 紧接着的第一条 SQL 会撞 database does not exist。等到真能连上业务库为止。
wait_database() {
  local _
  for _ in $(seq 1 150); do
    if docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
         psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc 'SELECT 1' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "数据库 150 秒内没有就绪" "看日志：docker logs --tail 50 aegis-postgres"
}

#------------------------------------------------------------------------------
# 4) 升级前先备份：没有备份不升级
#------------------------------------------------------------------------------
# 以前容器没在跑就不备份、照样升级，等于在最需要回退点的时候没有回退点。
# 现在容器不在就先把库拉起来再备份；拉不起来、导出失败、导出的文件读不出目录，都停在这里，
# 这时还什么都没改（服务没停、迁移没跑）
if [ "$MODE" = upgrade ]; then
  step "备份数据库"
  if ! docker ps --format '{{.Names}}' | grep -qx aegis-postgres; then
    info "数据库容器没在运行，先把它拉起来再备份"
    ( cd "$DEST/deploy" && docker compose up -d postgres ) 2>&1 | sed 's/^/    /' \
      || die "拉不起数据库容器，无法做升级前备份" "看日志：docker logs --tail 50 aegis-postgres"
    wait_database
  fi
  BK="$BACKUP_DIR/pre-upgrade-$(date +%Y%m%d-%H%M%S).dump"
  install -d -o root -g root -m 0700 "$BACKUP_DIR"
  docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
    pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f /tmp/pre-upgrade.dump \
    || die "升级前备份失败（pg_dump），没有改动任何东西" "看日志：docker logs --tail 50 aegis-postgres"
  docker exec aegis-postgres pg_restore --list /tmp/pre-upgrade.dump >/dev/null \
    || die "升级前备份读不出目录（pg_restore --list），没有改动任何东西"
  docker cp aegis-postgres:/tmp/pre-upgrade.dump "$BK" >/dev/null \
    || die "升级前备份拷不出容器，没有改动任何东西"
  docker exec aegis-postgres rm -f /tmp/pre-upgrade.dump
  [ -s "$BK" ] || die "升级前备份是空文件：$BK"
  chmod 0600 "$BK"
  info "已备份到 $BK（$(du -h "$BK" | cut -f1)）"
fi

#------------------------------------------------------------------------------
# 5) 数据基座
#------------------------------------------------------------------------------
step "启动数据基座（PostgreSQL 18 + Valkey）"
( cd "$DEST/deploy" && docker compose up -d ) 2>&1 | sed 's/^/    /'

info "等待数据库就绪"
wait_database
info "$(docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" aegis-postgres \
        psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc 'SELECT version()' | cut -c1-48)"

#------------------------------------------------------------------------------
# 5b) 网关改走 unix socket
#------------------------------------------------------------------------------
# 网关原先经 127.0.0.1:5433 / 6380 连库与缓存，中间是 Docker 的 userland docker-proxy：
# 每个往返多一次用户态拷贝和两次上下文切换（5k 复测里 docker-proxy 的 CPU 排第 3）。
# compose 把两个 socket 挂到 deploy/run/ 下；这里确认两个都真能用，再把 .env 的两条连接串
# 换成 socket 形式（首装与升级同一条路，网关在第 9 步重启后生效）。socket 不可用就保持
# 回环端口，照旧能跑；.env 已是 socket 形式而 socket 不可用，网关起不来，直接停下
step "网关改走 unix socket（去掉 docker-proxy 一跳）"
sockets_ok=0
for _ in $(seq 1 30); do
  if [ -S "$PG_SOCKET_DIR/.s.PGSQL.5432" ] && [ -S "$VK_SOCKET" ] \
     && REDISCLI_AUTH="$VALKEY_PASSWORD" docker exec -e REDISCLI_AUTH aegis-valkey \
          valkey-cli -s /data/sock/valkey.sock ping 2>/dev/null | grep -q PONG; then
    sockets_ok=1; break
  fi
  sleep 1
done
if [ "$sockets_ok" = 1 ]; then
  forms="$(switch_env_to_sockets "$DEST/deploy/.env" "$PG_SOCKET_DIR" "$VK_SOCKET")" \
    || die "改写 $DEST/deploy/.env 的连接串失败"
  while read -r key form; do
    case "$form" in
      socket) info "$key 经 unix socket" ;;
      *) warn "$key 是手工改过的值，保持不变（仍经回环端口）" ;;
    esac
  done <<<"$forms"
else
  for key in AEGIS_DATABASE_URL AEGIS_REDIS_URL; do
    [ "$(conn_form "$(pandora_env_file_value "$DEST/deploy/.env" "$key")")" != socket ] \
      || die "$key 走 unix socket，但 $DEST/deploy/run/ 下的 socket 30 秒内不可用" \
             "看 docker logs aegis-postgres / aegis-valkey；或按 .env 末尾的说明改回回环形式再重跑"
  done
  warn "socket 不可用，网关继续走回环端口（docker-proxy）：ls -la $DEST/deploy/run/*"
fi

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
# 10) nginx 边缘：防火墙 → 证书 → 停用抢 80 的默认站点 → 渲染 → nginx -t → reload
#------------------------------------------------------------------------------
# 以前这几步全靠手工：装完要自己 render-nginx.sh、nginx -t、reload、ufw 放行、申请证书，
# 还要先删发行版默认站点。面板此时已装好并通过健康检查，这一步失败也只停在这里，
# nginx 保持原来的配置继续跑
step "配置 nginx 边缘"
EDGE_STATE=skipped
DOMAIN="$(pandora_env_file_value "$DEST/deploy/.env" AEGIS_PUBLIC_BASE_URL)"
DOMAIN="${DOMAIN#https://}"; DOMAIN="${DOMAIN%/}"; DOMAIN="${DOMAIN,,}"
if [ "${PANDORA_SKIP_NGINX:-}" = 1 ]; then
  info "PANDORA_SKIP_NGINX=1：不碰 nginx"
elif ! command -v nginx >/dev/null 2>&1; then
  warn "本机没有 nginx，跳过：apt-get install -y nginx 后重跑本脚本，或手工跑 $DEST/deploy/render-nginx.sh"
elif ! pandora_valid_public_base_url "https://$DOMAIN"; then
  warn "AEGIS_PUBLIC_BASE_URL 不是 https://公网域名，nginx 没法按它渲染，跳过"
else
  open_firewall
  cert_rc=0
  obtain_certificate "$DOMAIN" || cert_rc=$?
  case "$cert_rc" in
    0)
      disable_stock_default_site
      apply_edge_config "$DEST/deploy/render-nginx.sh" "$DEST/deploy/.env" || die "nginx 配置没有生效（面板本身已装好并通过健康检查）" \
        "按上面 nginx -t 的输出修好后，手工跑：$DEST/deploy/render-nginx.sh && nginx -t && systemctl reload nginx"
      EDGE_STATE=applied ;;
    2)
      EDGE_STATE=no-cert
      warn "$DOMAIN 还没有证书，nginx 配置先不渲染（它按 Let's Encrypt 的证书路径找证书）。"
      warn "  申请并渲染：sudo PANDORA_CERTBOT=1 $RELEASE_ROOT/deploy/install.sh（走升级，会先备份）"
      warn "  或自己申请到 $LE_LIVE_DIR/$DOMAIN/ 后：$DEST/deploy/render-nginx.sh && nginx -t && systemctl reload nginx" ;;
    *)
      die "证书申请失败（面板本身已装好并通过健康检查）" \
        "确认域名解析与 80 端口后重跑：sudo PANDORA_CERTBOT=1 $RELEASE_ROOT/deploy/install.sh" ;;
  esac
fi

#------------------------------------------------------------------------------
# 11) 收尾提示
#------------------------------------------------------------------------------
step "安装完成"
cat <<EOF
    三个网关只监听 127.0.0.1，公网经 nginx（443，HTTP/2）进来。
    nginx：$(case "$EDGE_STATE" in
      applied) echo "已按 .env 的 AEGIS_PUBLIC_BASE_URL（${AEGIS_PUBLIC_BASE_URL:-未设置}）渲染并 reload" ;;
      no-cert) echo "等证书，见上面的提示" ;;
      *) echo "未配置；之后跑 $DEST/deploy/render-nginx.sh && nginx -t && systemctl reload nginx" ;;
    esac)
    站点在 Cloudflare 后面时，再跑 $DEST/deploy/update-cloudflare-realip.sh
    写入 Cloudflare 网段（默认的信任表不信任任何代理，升级不会覆盖它）
    运行模式 AEGIS_ENV=${AEGIS_ENV:-development}；节点端发布物绑定在 $DEST/deploy/release-artifact.env，随每次升级覆盖

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
