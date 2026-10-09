#!/usr/bin/env bash
# 潘多拉面板一键安装 / 升级。
#
#   首次安装：  sudo ./install.sh
#   升级：      sudo ./install.sh          （检测到已装会自动走升级）
#
# 面板默认走 HTTPS（nginx 边缘 + 证书，交给同目录的 edge-tls.sh）：
#   有域名     → Let's Encrypt 域名证书（certbot webroot）
#   只有公网 IP → Let's Encrypt IP 证书（lego，shortlived 约 6 天，aegis-tls-renew.timer 自动续期）
#   申请失败   → 自签证书兜底（浏览器提示不安全），timer 每天两次重试，成功即无缝换上
# 申请证书即表示同意 Let's Encrypt 的订户协议。
#
# 无人值守的环境变量（都可选）：
#   PANDORA_ASSUME_YES=1            不问任何问题
#   PANDORA_PUBLIC_BASE_URL=URL     面板对外地址：https://你的域名 或 https://公网IPv4。首装不给时，
#                                   终端里现场问（回车用本机公网 IPv4），无人值守直接用本机公网 IPv4；
#                                   本机没有公网 IPv4（NAT 后面）就必须给
#   PANDORA_ACME=0                  不联系任何 CA，只用自签证书（旧名 PANDORA_CERTBOT=0 同义）
#   PANDORA_ACME=1                  升级一台还没走 nginx 边缘的旧面板时，同意接管 nginx 并申请证书
#                                   （旧名 PANDORA_CERTBOT=1 同义）
#   PANDORA_ACME_EMAIL=邮箱          ACME 账号联系邮箱（旧名 PANDORA_CERTBOT_EMAIL）
#   PANDORA_ACME_SERVER=URL         ACME 目录地址，缺省 Let's Encrypt 正式环境（演练可用 staging）
#   PANDORA_SKIP_NGINX=1            不碰 nginx 与证书
#   PANDORA_LAYOUT=docker           全新安装也用 docker 布局（缺省交给 install-native.sh 装直装布局；
#                                   已装 docker 布局的机器升级不受影响）
#   例：sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://panel.example.com ./install.sh
#
# 发布包装出来的就是生产：首装写 AEGIS_ENV=production。生产模式下网关启动时
# 要求 AEGIS_PUBLIC_BASE_URL 是 https://域名 或 https://公网IPv4（platform/config 的
# CanonicalPublicOrigin），节点接入要求发布物的 SHA-256 与版本（deploy/
# release-artifact.env，随发布包生成）。所以首装必须先拿到对外地址：
# PANDORA_PUBLIC_BASE_URL 给出、终端里现场问、或用本机公网 IPv4；都没有就在动手前停下。
#
# 这个脚本把原先要手工串起来的七八步固化成一条命令：前置检查 → 生成配置
# → 升级前备份 → 起数据基座、网关改走 unix socket → 迁移 → 收窄数据库角色 →
# 安装二进制与 systemd 单元 → 启动 → 健康检查 → HTTPS 边缘（防火墙、证书、
# 渲染、nginx -t、reload、续期 timer）。每一步都做过实机验证，顺序上的坑（下面注释里逐条记着）
# 都是踩出来的。
#
# 设计上的三条约束：
#
#   1. 幂等。已有 .env 绝不覆盖——那里面是随机生成的密钥，重写一次
#      就再也连不上原来的数据库了。
#   2. 不替用户生成管理员密码：生成再打印出来，等于把它写进终端回滚日志和 CI 输出里。
#      首装且在交互终端里时，健康检查通过后现场问邮箱和密码（不回显、输两次），
#      密码只经标准输入交给 aegis-adminctl；无人值守时只提示该跑哪条命令。
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

# 宿主机上的路径。PANDORA_* 覆盖只给桩测试用（install-chain_mock_test.sh 把它们指到临时目录）；
# 证书、nginx 渲染相关的路径归 edge-tls.sh，它自己读同名的 PANDORA_* 覆盖
NGINX_DIR="${PANDORA_NGINX_DIR:-/etc/nginx}"
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

# 收尾提示：按「现在可以做什么 → 还差什么 → 常用操作」三段写，首装与升级分开说，
# 只留新手需要的。用到的全局量：MODE、EDGE_STATE（trusted / selfsigned / not-enabled / bad-url / skipped）、
# PANDORA_ADMIN_STATE（created / existing / manual）、PANDORA_ADMIN_EMAIL、PENDING_COUNT、
# before / after（迁移版本）、BK（升级前备份）、DEST、RELEASE_ROOT。
# 后台地址只在首装时打印：升级没改它，也就不必再把入口写进一次终端记录。
print_install_summary() {
  local env_file="$DEST/deploy/.env" base admin_path url todo=()
  base="$(pandora_env_file_value "$env_file" AEGIS_PUBLIC_BASE_URL)"; base="${base%/}"
  admin_path="$(pandora_env_file_value "$env_file" AEGIS_ADMIN_PATH)"
  url="$base/$admin_path/"
  local show_url="sudo $DEST/deploy/admin-url.sh"
  local create_cmd="cd $DEST && set -a && . deploy/.env && set +a && read -rsp '密码：' p && echo && printf '%s\n' \"\$p\" | ./bin/aegis-adminctl create --email <你的邮箱> --password-stdin; unset p"
  local rerun="sudo PANDORA_ACME=1 $RELEASE_ROOT/deploy/install.sh"

  # 还差什么：只列这台机器上确实还没做的
  case "${EDGE_STATE:-skipped}" in
    trusted) ;;
    selfsigned) todo+=("换上正规证书（现在是自签，浏览器提示不安全、节点用 https 接入会失败）：确认 80/tcp 从公网可达后 sudo $DEST/deploy/edge-tls.sh issue；续期 timer 每天两次也会自动重试，成功即换上") ;;
    not-enabled) todo+=("切到 HTTPS（接管 nginx 80/443、申请 Let's Encrypt 证书，会先备份再走升级）：$rerun") ;;
    bad-url) todo+=("把 $env_file 的 AEGIS_PUBLIC_BASE_URL 改成 https://域名 或 https://公网IPv4，再 $rerun") ;;
    *) todo+=("配好 nginx 才能从公网打开面板：装好 nginx 后重跑 $RELEASE_ROOT/deploy/install.sh") ;;
  esac
  if [ "$MODE" = install ] && [ "${PANDORA_ADMIN_STATE:-manual}" = manual ]; then
    todo+=("创建管理员（下面这条整行复制；密码不回显，只经标准输入交给 aegis-adminctl）：" "    $create_cmd")
  fi
  if [ "${PENDING_COUNT:-0}" -gt 0 ]; then
    todo+=("填好 $env_file 里还是 CHANGE_ME 的 $PENDING_COUNT 项，再 systemctl restart aegis-public aegis-admin aegis-node")
  fi
  [ "$MODE" != install ] \
    || todo+=("站点放在 Cloudflare 后面的话：sudo $DEST/deploy/update-cloudflare-realip.sh（默认不信任任何代理）")

  if [ "$MODE" = install ]; then
    step "安装完成"
    printf '%s\n' "  现在可以做什么："
    if [ "${EDGE_STATE:-}" = trusted ]; then
      printf '    %s\n' "打开管理后台：$url" "  （这个地址就是后台入口，别外传）"
    elif [ "${EDGE_STATE:-}" = selfsigned ]; then
      printf '    %s\n' "打开管理后台：$url" "  （这个地址就是后台入口，别外传；现在是自签证书，浏览器提示不安全时确认继续即可）"
    else
      printf '    %s\n' "面板已在本机跑起来（三个服务健康检查通过）；nginx 配好后从这里打开管理后台：" "  $url"
    fi
    case "${PANDORA_ADMIN_STATE:-manual}" in
      created) printf '    %s\n' "用刚建的管理员 ${PANDORA_ADMIN_EMAIL:-} 登录，然后尽快绑定两步验证" ;;
      existing) printf '    %s\n' "用库里已有的管理员登录" ;;
    esac
  else
    step "升级完成"
    printf '%s\n' "  现在可以做什么："
    printf '    %s\n' "面板已升级（迁移版本 ${before:-?} → ${after:-?}），三个服务已重启并通过健康检查" \
      "管理后台地址没变（重看：$show_url）"
    case "${EDGE_STATE:-}" in
      trusted) printf '    %s\n' "HTTPS：Let's Encrypt 证书在用（查看：sudo $DEST/deploy/edge-tls.sh status）" ;;
      selfsigned) printf '    %s\n' "HTTPS：在用自签证书（查看：sudo $DEST/deploy/edge-tls.sh status）" ;;
    esac
    [ -z "${BK:-}" ] || printf '    %s\n' "升级前的数据库备份：$BK"
  fi

  printf '\n%s\n' "  还差什么："
  local item
  [ "${#todo[@]}" -gt 0 ] || printf '    %s\n' "没有了"
  for item in ${todo[@]+"${todo[@]}"}; do
    case "$item" in
      "    "*) printf '    %s\n' "$item" ;;
      *) printf '    · %s\n' "$item" ;;
    esac
  done

  printf '\n%s\n' "  常用操作："
  printf '    %s\n' \
    "重看后台地址  $show_url" \
    "查看状态      systemctl status aegis-public aegis-admin aegis-node" \
    "查看日志      tail -f /var/log/aegis/public.log" \
    "重启          systemctl restart aegis-public aegis-admin aegis-node" \
    "数据库        cd $DEST/deploy && ./psql.sh" \
    "HTTPS 证书    sudo $DEST/deploy/edge-tls.sh status" \
    "升级          新发布包放到 root 独占目录后：sudo <发布目录>/deploy/install.sh"
}

# 这台机器该走哪种布局（入口判断，在任何前置检查之前）。打印 native 或 docker；该停下时把原因写到
# 标准错误并返回 1。
#   pandora_entry_layout <docker 布局目录> <直装目录> <PANDORA_LAYOUT 的值>
# 口径与 install-native.sh 的 native_plain_mode_guard、release-stop-the-world.sh 的 default_app_dir 一致：
#   - 迁完的直装（state=done、直装 .env 在、docker 的已改名），或本来就只有直装 → native；这时要 docker 布局一律拒绝；
#   - state=done 而两个 .env 都在（收尾改名没成）→ 停下，要人改名；
#   - state=done 而直装 .env 不在、docker 的在：退回过 Docker（退回步骤漏了把状态文件改名）→ 只有
#     显式 PANDORA_LAYOUT=docker 才按 docker 升级，否则停下说清楚；
#   - 迁移切换了还没收尾 → 停下，先 install-native.sh --from-docker 收尾；
#   - 两种都在、却没有迁移记录 → 停下，要人清掉不用的那一套。不接受 PANDORA_LAYOUT 明说：两套的 systemd
#     单元同名，升错一套就把服务切到另一份库上；
#   - docker 布局在服务、直装是没迁完的副本 → 只有显式 PANDORA_LAYOUT=docker 才按 docker 布局升级；
#   - 只有 docker 布局 → docker；什么都没有 → 全新安装，缺省 native。
pandora_entry_layout() {
  local docker_env="$1/deploy/.env" native_env="$2/deploy/.env" want="$3" state=""
  case "$want" in
    ''|native|docker) ;;
    *) echo "PANDORA_LAYOUT 只认 native 或 docker（现在是 $want）" >&2; return 1 ;;
  esac
  [ ! -f "$2/deploy/from-docker.state" ] \
    || state="$(awk -F= '$1 == "state" { sub(/^[^=]*=/, ""); print; exit }' "$2/deploy/from-docker.state")"
  if [ "$state" = done ]; then
    if [ -f "$native_env" ] && [ -f "$docker_env" ]; then
      echo "记录说已经从 Docker 迁完，可直装（$2）与 docker 布局（$1）的 .env 都在（收尾时改名没成？）：确认哪套在服务（systemctl cat aegis-public 看 ExecStart），把不用的那套的 deploy/.env 改名（不删），再重跑" >&2
      return 1
    fi
    if [ -f "$native_env" ]; then
      if [ "$want" = docker ]; then
        echo "这台已经从 Docker 迁到直装（$2），不再按 docker 布局装；升级直接跑 install-native.sh" >&2
        return 1
      fi
      echo native
      return 0
    fi
    if [ -f "$docker_env" ]; then
      if [ "$want" = docker ]; then echo docker; return 0; fi
      echo "这台从 Docker 迁到过直装、之后又退回了 Docker（直装的 .env 不在，$2/deploy/from-docker.state 还记着 done）：按 docker 布局升级加 PANDORA_LAYOUT=docker；以后想再迁到直装，先把 from-docker.state 改名为 from-docker.state.retired，再跑 install-native.sh --from-docker" >&2
      return 1
    fi
    echo "记录说已经从 Docker 迁完（$2/deploy/from-docker.state），却找不到直装或 docker 布局的 .env，不知道数据在哪，停下" >&2
    return 1
  fi
  if [ "$state" = cutover ]; then
    echo "上次从 Docker 迁到直装已经切换、还没收尾：先 sudo bash <发布目录>/deploy/install-native.sh --from-docker 收尾，再升级" >&2
    return 1
  fi
  if [ -f "$native_env" ] && [ -z "$state" ]; then
    if [ ! -f "$docker_env" ]; then
      if [ "$want" = docker ]; then
        echo "这台已经是直装布局（$2），不再装 docker 布局；升级直接跑 install-native.sh" >&2
        return 1
      fi
      echo native
      return 0
    fi
    echo "这台同时装着 docker 布局（$1）与直装（$2），又没有迁移记录，不知道哪个是正主。确认哪套在服务（systemctl cat aegis-public 看 ExecStart 指向哪个目录），把不用的那套的 deploy/.env 改名（不删），再重跑。不接受 PANDORA_LAYOUT 明说：两套的 systemd 单元同名，升错一套就把服务切到另一份库上" >&2
    return 1
  fi
  if [ -f "$docker_env" ]; then
    if [ -n "$state" ] && [ "$want" != docker ]; then
      echo "上次从 Docker 迁到直装没完成（状态 $state），现在服务的还是 docker 布局：继续迁移跑 install-native.sh --from-docker；要先按 docker 布局升级，加 PANDORA_LAYOUT=docker" >&2
      return 1
    fi
    echo docker
    return 0
  fi
  if [ -n "$state" ]; then
    echo "有从 Docker 迁移的记录（$state），却找不到 docker 布局的 $docker_env，不知道数据在哪，停下" >&2
    return 1
  fi
  if [ "$want" = docker ]; then echo docker; else echo native; fi
}

# 可单测的部分到此为止
if [ "${PANDORA_INSTALL_LIB:-}" = 1 ]; then
  return 0 2>/dev/null || exit 0
fi

#------------------------------------------------------------------------------
# 0) 缺省布局是直装（install-native.sh：系统 PostgreSQL 18 + Valkey，不要 Docker）
#------------------------------------------------------------------------------
# 全新安装、以及已经是直装（含从 Docker 迁完）的机器交给 install-native.sh（同一个发布包、同一套
# 环境变量）；要 docker 布局显式给 PANDORA_LAYOUT=docker。只装着 docker 布局的机器照旧在这里升级，
# 收尾提示怎么迁到直装。判断见 pandora_entry_layout
# 两个目录的 PANDORA_ENTRY_* 覆盖只给桩测试用（install-native_fromdocker_mock_test.sh 拿临时目录造场景），
# 而且要同时开着测试开关 PANDORA_ENTRY_TEST=1 才认：生产环境里误设了其中一个不起作用
NATIVE_DEST=/opt/pandora
ENTRY_DOCKER_DIR="$DEST"
if [ "${PANDORA_ENTRY_TEST:-}" = 1 ]; then
  NATIVE_DEST="${PANDORA_ENTRY_NATIVE_DIR:-$NATIVE_DEST}"
  ENTRY_DOCKER_DIR="${PANDORA_ENTRY_DOCKER_DIR:-$ENTRY_DOCKER_DIR}"
fi
ENTRY_LAYOUT="$(pandora_entry_layout "$ENTRY_DOCKER_DIR" "$NATIVE_DEST" "${PANDORA_LAYOUT:-}")" \
  || die "没动手：按上面的提示处理后重跑"
if [ "$ENTRY_LAYOUT" = native ]; then
  [ -f "$HERE/install-native.sh" ] || die "发布目录缺少 deploy/install-native.sh"
  if [ -f "$NATIVE_DEST/deploy/.env" ]; then
    printf '    %s\n' "这台是直装布局（$NATIVE_DEST），交给 install-native.sh 升级" >&2
  else
    printf '    %s\n' "全新安装走直装布局（install-native.sh，不需要 Docker）；要 docker 布局：PANDORA_LAYOUT=docker $0" >&2
  fi
  exec bash "$HERE/install-native.sh" "$@"
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
# 升级迁移的停服顺序、首装交互式建管理员，与 install-native.sh 共用一份
[ -f "$HERE/install-lib.sh" ] || die "发布目录缺少 deploy/install-lib.sh"
. "$HERE/install-lib.sh"

#------------------------------------------------------------------------------
# 2) 判断首装还是升级
#------------------------------------------------------------------------------
MODE=install
[ -f "$DEST/deploy/.env" ] && MODE=upgrade
step "运行模式：$([ "$MODE" = install ] && echo '首次安装' || echo '升级现有安装')"

if [ "$MODE" = upgrade ]; then
  info "检测到 $DEST/deploy/.env，将保留现有配置与数据"
  # 升级不改运行模式：把别人的 development 悄悄改成 production，可能因为对外地址
  # 不合规让三个网关起不来。只提示，由管理员自己决定。
  current_env="$(pandora_env_file_value "$DEST/deploy/.env" AEGIS_ENV)"
  if [ "${current_env,,}" != production ]; then
    warn "现有 .env 的 AEGIS_ENV=${current_env:-（未设置，按 development）}，不是 production："
    warn "  节点接入不会强制校验发布物的 SHA-256 与版本，支付回调、插件钩子也按开发模式放宽。"
    warn "  这次升级不改它。要切到生产：确认 AEGIS_PUBLIC_BASE_URL 是 https://域名 或 https://公网IPv4，"
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
    PENDING_COUNT="$(printf '%s\n' "$pending" | wc -l | tr -d ' ')"
    warn "还有 $PENDING_COUNT 项需要手工填写（装完后编辑 $DEST/deploy/.env）："
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
# 随时重看后台地址的小脚本，收尾提示里告诉用户
cp "$RELEASE_ROOT/deploy/admin-url.sh" "$DEST/deploy/" || die "发布目录缺少 deploy/admin-url.sh"
chmod 0755 "$DEST/deploy/admin-url.sh"
# 迁移失败、要回滚时照着做的手册，放在机器上随手可查
cp "$RELEASE_ROOT/deploy/MIGRATION-RUNBOOK.md" "$DEST/deploy/" || die "发布目录缺少 deploy/MIGRATION-RUNBOOK.md"
chmod 0644 "$DEST/deploy/MIGRATION-RUNBOOK.md"
install -d -o root -g root -m 0755 "$DEST/migrations"
cp "$RELEASE_ROOT"/migrations/*.sql "$DEST/migrations/"
# 迁移走 migrate.sh，预检走 check-migrations.sh（它们的依赖 platform.sh 一起）：升级时预检在
# 停服之前就要跑，所以和迁移文件一起先就位
for f in migrate.sh platform.sh check-migrations.sh; do
  cp "$RELEASE_ROOT/deploy/$f" "$DEST/deploy/" || die "发布目录缺少 deploy/$f"
  chmod 0755 "$DEST/deploy/$f"
done

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

# 迁移只走 migrate.sh，不自己拼 goose 命令：它在 AEGIS_MIGRATION_DATABASE_URL 缺失时从
# POSTGRES_* 回退，密码只走环境不进 DSN，回退固定 loopback，并要求显式批准。第一版这里
# 直接引用那个变量，结果在只有 POSTGRES_* 的存量 .env 上崩在停服之后，把生产撂在停机状态。
#
# 几个涉及幂等键与订单释放的迁移是 fail-closed 的：必须显式声明「写入者已停止」才肯执行。
# 这份声明只在真的停了之后才给（首装本来就没有写入者）；升级的一次性库预检挪到了停服
# 之前，停服窗口里只核对预检凭据。顺序与失败处置见 install-lib.sh 的 pandora_run_migrations。

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

# 发布包自带 goose；migrate.sh 通过 GOOSE_BIN 认它，免得用到目标机器上
# 碰巧装着的其它版本。预检与正式迁移必须用同一个 goose。
GOOSE_FOR_MIGRATE="$RELEASE_ROOT/bin/goose"
if [ ! -x "$GOOSE_FOR_MIGRATE" ]; then
  GOOSE_FOR_MIGRATE="$(command -v goose || echo /root/go/bin/goose)"
  warn "发布包里没有 goose，改用 $GOOSE_FOR_MIGRATE"
fi

# 库里没有 goose 记录 = 从没跑过迁移 = 预检没有可保护的东西，跳过它，省掉一遍完整迁移
# （单核机器上是十分钟量级的差别）。用 goose_db_version 在不在判断，比「表数为 0」严谨。
FRESH_DB=no
[ "$(pgq "SELECT (pg_catalog.to_regclass('public.goose_db_version') IS NOT NULL)::text")" != false ] || FRESH_DB=yes

migrate_rc=0
pandora_run_migrations "$MODE" "$FRESH_DB" "$DEST/deploy" "$DEST/deploy/.env" "$DEST/migrations" "$GOOSE_FOR_MIGRATE" \
  || migrate_rc=$?
case "$migrate_rc" in
  0) ;;
  10) die "停服前的迁移预检没通过：服务没停，数据库没动" \
        "上面是预检的完整输出。按提示处理后重跑本脚本（走升级，会再备份一次）。" ;;
  11) die "迁移失败，服务已拉回原来的版本" \
        "上面是完整输出。迁移逐个事务提交，失败的那个已回滚，之前跑完的留在库里；
      怎么处置（前滚、rollback-to、从升级前备份恢复）见 $DEST/deploy/MIGRATION-RUNBOOK.md" ;;
  *) die "迁移失败（返回 $migrate_rc）" "上面是完整输出。" ;;
esac

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
# 健康巡检 timer（healthcheck.sh 每 10 分钟一次，首跑在启用后 10 分钟）：首装与升级都启用，
# 已启用的 enable --now 不会重置计时。单元由 install-linux-binaries.sh 随包装好
systemctl enable --now aegis-health.timer >/dev/null 2>&1 \
  || warn "没能启用健康巡检：systemctl enable --now aegis-health.timer"

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
# 9b) 首装：在交互终端里现场建第一个管理员
#------------------------------------------------------------------------------
# 放在 HTTPS 边缘之前：证书或 nginx 那一步停下时，管理员已经建好，重跑会走升级、不再问。
# aegis-adminctl 经 platform/config 读配置，用网关此刻用的同一份 .env（含改成 socket 的连接串）。
PANDORA_ADMIN_STATE=manual
if pandora_admin_prompt_wanted "$MODE"; then
  step "创建管理员"
  set -a; . "$DEST/deploy/.env"; set +a
  pandora_bootstrap_admin "$DEST/bin/aegis-adminctl"
fi

#------------------------------------------------------------------------------
# 10) HTTPS 边缘：防火墙 → 证书（没有先自签）→ 渲染 nginx → nginx -t → reload
#     → 申请 Let's Encrypt 证书 → 续期 timer。全在 edge-tls.sh setup 里
#------------------------------------------------------------------------------
# 面板此时已装好并通过健康检查，这一步失败也只停在这里，nginx 保持原来的配置继续跑。
# 证书申请失败不算安装失败：自签兜底，浏览器提示不安全，续期 timer 每天两次重试
step "配置 HTTPS（nginx 边缘与证书）"
EDGE_STATE=skipped
EDGE_URL="$(pandora_env_file_value "$DEST/deploy/.env" AEGIS_PUBLIC_BASE_URL)"
if [ "${PANDORA_SKIP_NGINX:-}" = 1 ]; then
  info "PANDORA_SKIP_NGINX=1：不碰 nginx 与证书"
elif ! command -v nginx >/dev/null 2>&1; then
  warn "本机没有 nginx，跳过：apt-get install -y nginx 后重跑本脚本，或手工跑 $DEST/deploy/edge-tls.sh setup"
elif ! pandora_valid_public_base_url "$EDGE_URL"; then
  EDGE_STATE=bad-url
  warn "AEGIS_PUBLIC_BASE_URL（${EDGE_URL:-空}）不是 https://域名 或 https://公网IPv4，没法按它配 HTTPS，跳过"
elif ! pandora_edge_wanted "$MODE" "$NGINX_DIR/conf.d/aegis.conf"; then
  EDGE_STATE=not-enabled
  warn "这台面板还没走 nginx 边缘（没有 $NGINX_DIR/conf.d/aegis.conf），升级不替你接管 80/443。"
  warn "  切到 HTTPS：sudo PANDORA_ACME=1 $RELEASE_ROOT/deploy/install.sh（会先备份再走升级）"
else
  edge_rc=0
  PANDORA_BACKUP_DIR="$BACKUP_DIR" bash "$DEST/deploy/edge-tls.sh" setup "$DEST/deploy/.env" 2>&1 \
    | sed 's/^edge-tls: /    /' || edge_rc=$?
  case "$edge_rc" in
    0) EDGE_STATE=trusted ;;
    3) EDGE_STATE=selfsigned ;;
    *) die "HTTPS 边缘没有配好（面板本身已装好并通过健康检查；nginx 保持原配置）" \
         "按上面的输出修好后执行：sudo $DEST/deploy/edge-tls.sh setup" ;;
  esac
fi

#------------------------------------------------------------------------------
# 11) 收尾提示
#------------------------------------------------------------------------------
print_install_summary
if [ "$MODE" = upgrade ]; then
  printf '\n%s\n' "  这台还是 docker 布局。缺省布局已换成直装（省掉 Docker 守护进程与两个容器的常驻开销），方便停服几分钟时迁过去："
  printf '    %s\n' "sudo $RELEASE_ROOT/deploy/install-native.sh --from-docker" \
    "（核对 → 导出 → 恢复 → 逐项核对 → 切换；失败自动回到 Docker，卷不删。步骤与回退见 deploy/RUNBOOK.md 第 13 章）"
fi
