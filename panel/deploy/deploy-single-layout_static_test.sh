#!/usr/bin/env bash
# 守卫：生产只有一种布局——install.sh 把系统包 PostgreSQL 18 + Valkey 装到 /opt/pandora，备份在 /var/backups/pandora。
# Docker 生产布局、布局开关、从别的布局迁过来的路径、旧格式备份修复已于 2026-10-09 删除（当时没有任何生产部署），
# 不许回来：
#   ① 删掉的入口与文件不在；panel/deploy 下没有任何 compose 文件（开发数据基座只在 panel/dev/）；
#   ② 发布包（build-release.sh）不打包它们，安装入口只有 deploy/install.sh；
#   ③ 生产文件（发布包里的脚本、单元、配置与模板）不调 docker、不提 compose；e2e 脚本（panel/tests/*.sh）一律不调 docker；
#      Makefile 只许在每一处 docker 行里指向 dev/docker-compose.yml（或用由它定义的 DEV_COMPOSE），
#      并且它引用的每个 compose 文件路径都存在；
#   ④ 生产文件、Go 非测试代码、e2e 脚本、Makefile 里没有布局开关、迁移标识与 Docker 布局的路径。
#   ⑤ 检查点复制 Hook 的受保护目录：Go（dbbackup/checkpoint_hook.go）与 install.sh 建的目录是同一个路径。
# Docker 仍是开发数据基座与 CI 的工具：run-pg18-gates.sh、run-migration-roundtrip.sh、run-smoke-*.sh、
# test-*-pg18.sh、*_docker_test.sh 与各 *_test.sh 不算生产文件。/etc/aegispanel、/var/lib/aegispanel、
# /run/aegispanel 是共用路径，照用；备份文件名的 aegis-postgres- 前缀是命名，不是容器。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PANEL="$(cd -- "$DEPLOY/.." && pwd)"
fail() { printf 'deploy single layout: %s\n' "$*" >&2; exit 1; }

# --- ① 删掉的入口与文件 ------------------------------------------------------------------
gone=(install-native.sh install-native-lib.sh install-linux-binaries.sh preflight-linux.sh test-install.sh
  migrate-to-new-host.sh legacy-privilege-repair.sql docker-compose.yml clear-ratelimit.sh)
for f in "${gone[@]}"; do
  [ ! -e "$DEPLOY/$f" ] || fail "deploy/$f is back (the production Docker layout and its migration paths were removed)"
done
compose="$(find "$DEPLOY" \( -name '*compose*.yml' -o -name '*compose*.yaml' \) -print)"
[ -z "$compose" ] || fail "compose files under panel/deploy: $compose (the development base lives in panel/dev/)"
[ -f "$DEPLOY/install.sh" ] || fail 'deploy/install.sh (the only installer) is missing'

# --- ② 发布包 -------------------------------------------------------------------------------
for f in "${gone[@]}"; do
  if grep -n -- "$f" "$DEPLOY/build-release.sh" | grep -v '^[0-9]*:[[:space:]]*#' | grep -q .; then
    fail "build-release.sh still ships $f"
  fi
done
grep -Eq '(^|[^-])install\.sh' "$DEPLOY/build-release.sh" || fail 'build-release.sh does not ship install.sh'

# --- ③ 生产文件不调 docker --------------------------------------------------------------------
production=()
while IFS= read -r f; do
  case "$(basename "$f")" in
    *_test.sh|*_test.ps1|run-pg18-gates.sh|run-migration-roundtrip.sh|run-smoke-*.sh|test-*-pg18.sh) continue ;;
    *.md) continue ;;   # 手册另由人审；历史实测里提到当年的布局是记录，不是入口
  esac
  production+=("$f")
done < <(find "$DEPLOY" -maxdepth 1 -type f -print; find "$DEPLOY/systemd" -type f -print)
[ "${#production[@]}" -gt 20 ] || fail "found only ${#production[@]} production files: the file walk is broken"
hits=""
for f in "${production[@]}"; do
  # 开发数据基座的路径可以在注释里被指向（参数要与它一致）
  h="$(sed -e 's|panel/dev/docker-compose\.yml||g' -e 's|dev-compose_static_test\.sh||g' "$f" | grep -nE '(^|[^A-Za-z_-])[Dd]ocker([^A-Za-z_-]|$)|compose' || true)"
  [ -z "$h" ] || hits+="${f#"$PANEL/"}:"$'\n'"$h"$'\n'
done
[ -z "$hits" ] || fail "production files mention docker / compose:"$'\n'"$hits"

# e2e 脚本：连库只走 psql（deploy/psql.sh 或 PSQL 环境变量），不许调 docker、不提容器名
hits=""
while IFS= read -r f; do
  h="$(grep -nE '(^|[^A-Za-z_-])[Dd]ocker([^A-Za-z_-]|$)|compose' "$f" | grep -vE '^[0-9]+:[[:space:]]*#' || true)"
  [ -z "$h" ] || hits+="${f#"$PANEL/"}:"$'\n'"$h"$'\n'
done < <(find "$PANEL/tests" -maxdepth 1 -name '*.sh' -print)
[ -z "$hits" ] || fail "e2e scripts call docker / compose:"$'\n'"$hits"
[ -n "$(find "$PANEL/tests" -maxdepth 1 -name '*.sh' -print | head -n 1)" ] || fail "no e2e scripts found under panel/tests: the file walk is broken"

# Makefile：docker 只用于开发数据基座，每个 docker 行都要指向 dev/docker-compose.yml 或用 DEV_COMPOSE（它的定义行指向那份文件）
mk="$PANEL/Makefile"
[ -f "$mk" ] || fail 'panel/Makefile is missing'
grep -Eq '^DEV_COMPOSE[[:space:]]*[:?]?=.*-f[[:space:]]+dev/docker-compose\.yml' "$mk" \
  || fail 'Makefile: DEV_COMPOSE must be defined from -f dev/docker-compose.yml'
hits="$(grep -nE '(^|[^A-Za-z_-])[Dd]ocker([^A-Za-z_-]|$)|compose' "$mk" \
  | grep -vE '^[0-9]+:[[:space:]]*#' | grep -vE '^[0-9]+:DEV_COMPOSE' \
  | grep -vF -e 'dev/docker-compose.yml' | grep -vE '\$\((DEV_COMPOSE)\)' || true)"
[ -z "$hits" ] || fail "Makefile calls docker / compose without dev/docker-compose.yml:"$'\n'"$hits"
# 每个被引用的 compose 文件路径都存在（相对 panel/）
refs="$(grep -ohE '[A-Za-z0-9_./$()-]*compose[A-Za-z0-9_./-]*\.ya?ml' "$mk" | sort -u)"
[ -n "$refs" ] || fail 'Makefile references no compose file: the check is broken'
for r in $refs; do
  case "$r" in *'$'*) fail "Makefile compose path uses a variable, cannot be checked: $r" ;; esac
  [ -f "$PANEL/$r" ] || fail "Makefile references a compose file that does not exist: $r"
done
if grep -nE 'deploy/docker-compose|\$\((DEPLOY)\)/[A-Za-z0-9_.-]*compose' "$mk" | grep -q .; then
  fail 'Makefile points at a compose file under deploy/'
fi

# --- ④ 布局开关、迁移标识、Docker 布局路径 --------------------------------------------------------
forbidden='PANDORA_LAYOUT|PANDORA_DB_LAYOUT|from-docker|aegis-valkey|exec aegis-postgres|/opt/aegispanel|/var/backups/aegispanel'
scan=("${production[@]}")
while IFS= read -r f; do scan+=("$f"); done < <(
  find "$PANEL/internal" "$PANEL/cmd" -name '*.go' ! -name '*_test.go' -print
  find "$PANEL/tests" -name '*.sh' -print
  find "$PANEL/dev" -type f -print
  printf '%s\n' "$PANEL/Makefile")
hits=""
for f in "${scan[@]}"; do
  h="$(grep -nE "$forbidden" "$f" || true)"
  [ -z "$h" ] || hits+="${f#"$PANEL/"}:"$'\n'"$h"$'\n'
done
[ -z "$hits" ] || fail "layout switches or Docker-layout paths are back:"$'\n'"$hits"

# --- ⑤ 检查点 Hook 目录：Go 常量与 install.sh 建的目录相同 ------------------------------------------
go_hook="$(sed -n 's/^var checkpointHookRoot = "\(.*\)"$/\1/p' "$PANEL/internal/domain/dbbackup/checkpoint_hook.go")"
[ "$(wc -l <<<"$go_hook" | tr -d ' ')" -eq 1 ] && [ -n "$go_hook" ] || fail 'cannot extract checkpointHookRoot from checkpoint_hook.go'
install_dir="$(sed -n 's/^INSTALL_DIR="\(.*\)"$/\1/p' "$DEPLOY/install-lib.sh")"
[ "$(wc -l <<<"$install_dir" | tr -d ' ')" -eq 1 ] && [ -n "$install_dir" ] || fail 'cannot extract INSTALL_DIR from install-lib.sh'
sh_hook="$(sed -n 's/^install -d .*"\$INSTALL_DIR\/\(checkpoint-sink\)"$/\1/p' "$DEPLOY/install.sh")"
[ "$(wc -l <<<"$sh_hook" | tr -d ' ')" -eq 1 ] && [ -n "$sh_hook" ] || fail 'cannot find the install -d ... $INSTALL_DIR/checkpoint-sink line in install.sh'
[ "$go_hook" = "$install_dir/$sh_hook" ] \
  || fail "checkpoint hook directory differs: Go says $go_hook, install.sh creates $install_dir/$sh_hook"

printf 'deploy single layout static: PASS (%d production files, %d scanned)\n' "${#production[@]}" "${#scan[@]}"
