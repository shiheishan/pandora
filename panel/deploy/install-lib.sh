#!/usr/bin/env bash
# install.sh 与 install-native.sh 共用的步骤：升级迁移的停服顺序、首装时交互式建管理员、
# 要不要配 HTTPS 边缘。
# 只定义函数，由安装器从发布目录 source（与 public-base-url.sh 同样的用法），不装到主机上。
# 桩测试：install-migrate-order_mock_test.sh、install-firstrun_mock_test.sh、install-chain_mock_test.sh。

#------------------------------------------------------------------------------
# 迁移
#------------------------------------------------------------------------------
# 在干净的环境里跑一个命令：只带 PATH、HOME 与参数里开头连续给出的 NAME=VALUE，之后是命令。
# 变量经本进程内的函数参数传，在子 shell 里清掉全部继承的环境后 export 再 exec，不经 env(1)：
# `env -i NAME=VALUE cmd` 会把 NAME=VALUE 写进 env 自己的命令行参数，迁移 DSN 里的超级用户或
# 迁移角色口令就能被同机其他账号用 ps 看到；这里口令只存在于子进程的环境（/proc/<pid>/environ，
# 仅属主与 root 可读），不进任何 argv。
pandora_scrubbed_run() (
  local p="$PATH" h="${HOME:-/root}" name kv vars=()
  while [ "$#" -gt 0 ] && [[ "$1" =~ ^[A-Za-z_][A-Za-z0-9_]*= ]]; do vars+=("$1"); shift; done
  while IFS= read -r name; do unset "$name" 2>/dev/null || true; done < <(compgen -e)
  while read -r _ _ name; do unset -f "$name" 2>/dev/null || true; done < <(declare -Fx)
  export PATH="$p" HOME="$h"
  for kv in ${vars[@]+"${vars[@]}"}; do export "${kv%%=*}=${kv#*=}"; done
  exec "$@"
)

# 升级时一次性库预检（check-migrations.sh：整库克隆 + 在克隆上演练待执行迁移）耗时随库
# 大小线性增长（5k-r4 实测 95 MB 的库约 15 秒，占停服的 2/3）。所以升级分三段：
#   1. 停服之前跑完整预检，按停写口径在克隆上演练（PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER
#      只作用于没有写入者的克隆库，不是「线上写入者已停」的声明），通过后留一张凭据。
#      失败就返回，服务一个都没停；
#   2. 停服；
#   3. migrate.sh up 带凭据：只做只读核对（迁移目录摘要、源库水位、续费闸门、停写口径一致、
#      六小时内），再迁移。PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes 只在这一步、停服之后给。
#      失败就把服务拉回来（新程序还没装，起来的是原来的版本）。
# 停服窗口里只剩：停服、凭据核对（亚秒级）、正式迁移、之后的收窄角色、装程序、重启。
#
# 首装、或升级但库里还没有 goose 记录（全新库）：没有要保护的数据，跳过克隆预检，直接迁移。
# 升级且不是全新库：预检之前先 migrate.sh check-indexes，有 INVALID 索引（上次 CONCURRENTLY
# 失败的半成品，重跑会被 IF NOT EXISTS 跳过）就返回 10，服务不停。
#
#   pandora_run_migrations <install|upgrade> <fresh: yes|no> <deploy 目录> <.env> <migrations 目录> <goose>
# deploy 目录里要有 migrate.sh 与 check-migrations.sh。要停的服务取 PANDORA_SERVICES（空格分隔），
# 缺省是三个网关。返回 0 成功；10 停服前索引检查或预检失败（服务没停）；11 迁移失败（服务已拉回）；
# 12 迁移失败（首装，没有服务可拉）。
pandora_run_migrations() {
  local mode="$1" fresh="$2" deploy="$3" env_file="$4" migrations="$5" goose="$6"
  local services=(${PANDORA_SERVICES:-aegis-public aegis-admin aegis-node})
  local base_env=(pandora_scrubbed_run "AEGIS_ENV_FILE=$env_file"
    "AEGIS_MIGRATIONS_DIR=$migrations" "GOOSE_BIN=$goose")
  # 调用方显式给了迁移 DSN 就带上（install-native.sh 用 postgres 超级用户）；.env 里有的话以 .env 为准
  [ -z "${AEGIS_MIGRATION_DATABASE_URL:-}" ] || base_env+=("AEGIS_MIGRATION_DATABASE_URL=$AEGIS_MIGRATION_DATABASE_URL")
  local attest_dir="" attestation="" log rc started
  local migrate_extra=()

  log="$(mktemp "${TMPDIR:-/tmp}/pandora-migrate.XXXXXX")"
  if [ "$fresh" = yes ]; then
    migrate_extra+=("PANDORA_SKIP_PRECHECK_FRESH_DB=yes-empty-database")
    printf '    %s\n' "全新库，跳过一次性数据库预检"
  elif [ "$mode" = upgrade ]; then
    # 上次 CREATE INDEX CONCURRENTLY 失败留下的 INVALID 索引：停服之前就拦下（migrate.sh up 自己
    # 也会在执行前拒绝，但那时服务已经停了）。只读查询，输出里带清理命令。
    if ! "${base_env[@]}" PANDORA_LOCAL_MIGRATION_APPROVED=yes "$deploy/migrate.sh" check-indexes >"$log" 2>&1; then
      printf '    ---- 索引检查完整输出 ----\n' >&2
      sed 's/^/    /' "$log" >&2
      rm -f -- "$log"
      return 10
    fi
    attest_dir="$(mktemp -d "${TMPDIR:-/tmp}/pandora-precheck.XXXXXX")"
    attestation="$attest_dir/precheck.attestation"
    printf '    %s\n' "停服之前，先在一次性克隆库上演练这次要跑的迁移（库越大越慢，服务照常在跑）"
    started="$(date +%s)"
    if ! "${base_env[@]}" \
        PANDORA_PRECHECK_REHEARSE_STOPPED_WRITER=yes \
        "PANDORA_PRECHECK_ATTESTATION_OUT=$attestation" \
        "$deploy/check-migrations.sh" >"$log" 2>&1 || [ ! -s "$attestation" ]; then
      printf '    ---- 预检完整输出 ----\n' >&2
      sed 's/^/    /' "$log" >&2
      rm -rf -- "$attest_dir" "$log"
      return 10
    fi
    printf '    %s\n' "预检通过（$(( $(date +%s) - started )) 秒），凭据已写好；停服后只核对凭据，不再演练"
    migrate_extra+=("PANDORA_PRECHECK_ATTESTATION=$attestation")
  fi

  if [ "$mode" = upgrade ]; then
    printf '    %s\n' "停止服务后迁移"
    systemctl stop "${services[@]}" 2>/dev/null || true
  fi

  # 写入者已停（升级：上面刚停；首装：服务还没装起来）——这份声明此刻才成立。
  # 具体的批准值固定在 migrate.sh 内部，这里只递交「已停止」这个事实。
  if "${base_env[@]}" \
      PANDORA_LOCAL_MIGRATION_APPROVED=yes \
      PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes \
      ${migrate_extra[@]+"${migrate_extra[@]}"} \
      "$deploy/migrate.sh" up >"$log" 2>&1; then
    tail -4 "$log" | sed 's/^/    /'
    rm -rf -- "$log" ${attest_dir:+"$attest_dir"}
    return 0
  fi
  rc=$?
  printf '    ---- 迁移完整输出（退出码 %s）----\n' "$rc" >&2
  sed 's/^/    /' "$log" >&2
  rm -rf -- "$log" ${attest_dir:+"$attest_dir"}
  if [ "$mode" = upgrade ]; then
    printf '    %s\n' "迁移失败，正在把服务拉回来" >&2
    systemctl start "${services[@]}" 2>/dev/null || true
    return 11
  fi
  return 12
}

#------------------------------------------------------------------------------
# 首装时交互式建管理员
#------------------------------------------------------------------------------
# 只在首装、且标准输入输出都是终端时问；无人值守（PANDORA_ASSUME_YES=1、
# PANDORA_NONINTERACTIVE=1、管道、CI）一律不问，由收尾提示给出手工命令。
pandora_admin_prompt_wanted() {
  [ "$1" = install ] || return 1
  [ "${PANDORA_ASSUME_YES:-}" != 1 ] && [ "${PANDORA_NONINTERACTIVE:-}" != 1 ] || return 1
  [ -t 0 ] && [ -t 1 ]
}

# 交互式建第一个管理员。调用方先把 .env 导出到环境（aegis-adminctl 经 platform/config 读配置）。
#   pandora_bootstrap_admin <aegis-adminctl>
# 密码只经 read -s 读进一个不导出的 shell 变量，再由内建 printf 经管道送到
# aegis-adminctl create --password-stdin 的标准输入：不进命令行参数、不进环境变量、
# 不回显、不写日志。邮箱打印出来没关系。
# 结果写到 PANDORA_ADMIN_STATE：created（建好了）、existing（已有管理员，没问）、
# manual（没建成，要手工建）。总是返回 0：建管理员失败不该让一个已经装好的面板报安装失败。
pandora_bootstrap_admin() {
  local adminctl="$1" email="" pw="" pw2="" attempt has_rc=0 create_rc out
  PANDORA_ADMIN_STATE=manual
  [ -x "$adminctl" ] || { printf '    %s\n' "找不到 $adminctl，管理员留给你手工建" >&2; return 0; }

  "$adminctl" has-admin >/dev/null 2>&1 || has_rc=$?
  case "$has_rc" in
    0) PANDORA_ADMIN_STATE=existing; printf '    %s\n' "库里已有可登录的管理员，不再新建"; return 0 ;;
    3) ;;
    *) printf '    %s\n' "查不到现有管理员（aegis-adminctl has-admin 退出码 $has_rc），管理员留给你手工建" >&2; return 0 ;;
  esac

  printf '    %s\n' "现在创建第一个管理员（直接回车跳过，之后再手工建）。"
  for attempt in 1 2 3; do
    printf '    管理员邮箱：'
    IFS= read -r email || email=""
    email="${email//[[:space:]]/}"
    [ -n "$email" ] || { printf '    %s\n' "跳过，之后手工建"; return 0; }
    if [[ ! "$email" =~ ^[^@]+@[^@]+\.[^@]+$ ]]; then
      printf '    %s\n' "邮箱格式不对：$email" >&2
      continue
    fi
    printf '    密码（不回显）：'
    IFS= read -r -s pw || pw=""
    printf '\n    再输一次：'
    IFS= read -r -s pw2 || pw2=""
    printf '\n'
    if [ -z "$pw" ] || [ "$pw" != "$pw2" ]; then
      pw=""; pw2=""
      printf '    %s\n' "两次密码不一致或为空，重来" >&2
      continue
    fi
    pw2=""
    # 管道最后一段是 aegis-adminctl，命令替换的退出码就是它的，与调用方开没开 pipefail 无关
    create_rc=0
    out="$(printf '%s\n' "$pw" | "$adminctl" create --email "$email" --password-stdin 2>&1)" || create_rc=$?
    pw=""
    printf '%s\n' "$out" | sed 's/^/    /'
    if [ "$create_rc" -eq 0 ]; then
      PANDORA_ADMIN_STATE=created
      PANDORA_ADMIN_EMAIL="$email"
      return 0
    fi
    printf '    %s\n' "没建成（原因见上，常见是密码强度不够），重来" >&2
  done
  printf '    %s\n' "三次都没建成，管理员留给你手工建" >&2
  return 0
}

#------------------------------------------------------------------------------
# HTTPS 边缘：要不要接管 nginx
#------------------------------------------------------------------------------
# 首装一律配（证书与 nginx 交给 edge-tls.sh setup）。升级时：已经在用（有 aegis.conf）就照常
# 重渲染、沿用证书；还没走过 nginx 边缘的旧面板（以前没申请证书、没渲染 nginx，可能另有自己的
# nginx 站点）不替人接管 80/443，除非显式 PANDORA_ACME=1（旧名 PANDORA_CERTBOT=1），或在终端里答 y。
#   pandora_edge_wanted <install|upgrade> <aegis.conf 路径>
pandora_edge_wanted() {
  local reply
  [ "$1" = install ] && return 0
  [ -f "$2" ] && return 0
  if [ "${PANDORA_ACME:-}" = 1 ] || [ "${PANDORA_CERTBOT:-}" = 1 ]; then return 0; fi
  if [ "${PANDORA_ASSUME_YES:-}" != 1 ] && [ -t 0 ]; then
    printf '    这台面板还没走 nginx 边缘。现在配 HTTPS（接管 80/443、申请 Let'"'"'s Encrypt 证书，即同意其订户协议）？[y/N] '
    read -r reply || reply=""
    case "$reply" in [yY]*) return 0 ;; esac
  fi
  return 1
}
