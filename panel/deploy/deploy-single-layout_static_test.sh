#!/usr/bin/env bash
# 守卫：生产只有一种布局——install.sh 把系统包 PostgreSQL 18 + Valkey 装到 /opt/pandora，备份在 /var/backups/pandora。
# Docker 生产布局、布局开关、从别的布局迁过来的路径、旧格式备份修复已于 2026-10-09 删除（当时没有任何生产部署），
# 不许回来：
#   ① 删掉的入口与文件不在；panel/deploy 下没有任何 compose 文件（开发数据基座只在 panel/dev/）；
#   ② 发布包（build-release.sh）不打包它们，安装入口只有 deploy/install.sh；
#   ③ 生产文件（发布包里的脚本、单元、配置与模板）不调 docker、不提 compose；e2e 脚本（panel/tests 下递归的 *.sh）一律不调 docker；
#      docker 一词按「前后都不是字母数字下划线」匹配、不分大小写（-docker、${DOCKER:-docker} 都算）；
#      Makefile 先删掉允许的子串（dev/docker-compose.yml、$(DEV_COMPOSE)，DEV_COMPOSE 的定义行也只许这一种形状），
#      剩下的文本里再出现 docker / compose 就红（同一行里别处的 docker 调用不会被整行放过），
#      并且它引用的每个 compose 文件路径都存在；容器名 aegis-postgres（后面不跟 - ；备份文件名前缀 aegis-postgres- 是命名）不许出现；
#   ④ 生产文件、Go 非测试代码、e2e 脚本、Makefile 里没有布局开关、迁移标识与 Docker 布局的路径。
#   ⑤ 检查点复制 Hook 的受保护目录：Go（dbbackup/checkpoint_hook.go）与 install.sh 建的目录是同一个路径；
#      同包测试要把它指向临时目录所以是 var，因此 dbbackup 包的非测试 Go 文件里对它的赋值必须恰好只有那一处声明。
# Docker 仍是开发数据基座与 CI 的工具：run-pg18-gates.sh、run-migration-roundtrip.sh、run-smoke-*.sh、
# test-*-pg18.sh、*_docker_test.sh 与各 *_test.sh 不算生产文件。/etc/aegispanel、/var/lib/aegispanel、
# /run/aegispanel 是共用路径，照用；备份文件名的 aegis-postgres- 前缀是命名，不是容器。
set -euo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'deploy single layout: %s\n' "$*" >&2; exit 1; }
# docker 一词：前后都不是字母数字下划线（-docker、${DOCKER:-docker} 都算命中），不分大小写；compose 是子串匹配
DOCKER_RE='(^|[^A-Za-z0-9_])docker([^A-Za-z0-9_]|$)|compose'

# 对一棵 panel 目录树跑全部检查：不过就打印原因、返回 1（自检拿改坏的副本调它）
#   check_tree <panel 目录>
# 在子 shell 里跑：bad 直接 exit 1（在 if 里调它时 errexit 不生效，靠 return 会接着往下查、最后误报 PASS）
# 注意：调用处是 `|| fail` 与 `if`，子 shell 里的 set -e 同样不生效（bash 在这两种上下文里整个关掉 errexit），
# 所以每条检查都必须显式调 bad；新加的检查不能指望某条命令失败就自动停下（N8）
check_tree() (
  set -euo pipefail
  PANEL="$(cd -- "$1" && pwd)"; DEPLOY="$PANEL/deploy"
  bad() { printf '%s\n' "$*"; exit 1; }
  # --- ① 删掉的入口与文件 ------------------------------------------------------------------
  gone=(install-native.sh install-native-lib.sh install-linux-binaries.sh preflight-linux.sh test-install.sh
    migrate-to-new-host.sh legacy-privilege-repair.sql docker-compose.yml clear-ratelimit.sh)
  for f in "${gone[@]}"; do
    [ ! -e "$DEPLOY/$f" ] || bad "deploy/$f is back (the production Docker layout and its migration paths were removed)"
  done
  compose="$(find "$DEPLOY" \( -name '*compose*.yml' -o -name '*compose*.yaml' \) -print)"
  [ -z "$compose" ] || bad "compose files under panel/deploy: $compose (the development base lives in panel/dev/)"
  [ -f "$DEPLOY/install.sh" ] || bad 'deploy/install.sh (the only installer) is missing'

  # --- ② 发布包 -------------------------------------------------------------------------------
  for f in "${gone[@]}"; do
    if grep -n -- "$f" "$DEPLOY/build-release.sh" | grep -v '^[0-9]*:[[:space:]]*#' | grep -q .; then
      bad "build-release.sh still ships $f"
    fi
  done
  grep -Eq '(^|[^-])install\.sh' "$DEPLOY/build-release.sh" || bad 'build-release.sh does not ship install.sh'

  # --- ③ 生产文件不调 docker --------------------------------------------------------------------
  production=()
  while IFS= read -r f; do
    case "$(basename "$f")" in
      *_test.sh|*_test.ps1|run-pg18-gates.sh|run-migration-roundtrip.sh|run-smoke-*.sh|test-*-pg18.sh) continue ;;
      *.md) continue ;;   # 手册另由人审；历史实测里提到当年的布局是记录，不是入口
    esac
    production+=("$f")
  done < <(find "$DEPLOY" -maxdepth 1 -type f -print; find "$DEPLOY/systemd" -type f -print)
  [ "${#production[@]}" -gt 20 ] || bad "found only ${#production[@]} production files: the file walk is broken"
  hits=""
  for f in "${production[@]}"; do
    # 开发数据基座的路径可以在注释里被指向（参数要与它一致）
    h="$(sed -e 's|panel/dev/docker-compose\.yml||g' -e 's|dev-compose_static_test\.sh||g' "$f" | grep -niE "$DOCKER_RE" || true)"
    [ -z "$h" ] || hits+="${f#"$PANEL/"}:"$'\n'"$h"$'\n'
  done
  [ -z "$hits" ] || bad "production files mention docker / compose:"$'\n'"$hits"

  # e2e 脚本：连库只走 psql（deploy/psql.sh 或 PSQL 环境变量），不许调 docker、不提容器名
  hits=""
  while IFS= read -r f; do
    h="$(grep -niE "$DOCKER_RE" "$f" | grep -vE '^[0-9]+:[[:space:]]*#' || true)"
    [ -z "$h" ] || hits+="${f#"$PANEL/"}:"$'\n'"$h"$'\n'
  done < <(find "$PANEL/tests" -name '*.sh' -print)
  [ -z "$hits" ] || bad "e2e scripts call docker / compose:"$'\n'"$hits"
  [ -n "$(find "$PANEL/tests" -name '*.sh' -print | head -n 1)" ] || bad "no e2e scripts found under panel/tests: the file walk is broken"

  # Makefile：docker 只用于开发数据基座，一律经 $(DEV_COMPOSE)（它的定义行是唯一写出 docker compose -f dev/docker-compose.yml 的地方）
  mk="$PANEL/Makefile"
  [ -f "$mk" ] || bad 'panel/Makefile is missing'
  grep -Eq '^DEV_COMPOSE[[:space:]]*[:?]?=.*-f[[:space:]]+dev/docker-compose\.yml' "$mk" \
    || bad 'Makefile: DEV_COMPOSE must be defined from -f dev/docker-compose.yml'
  # 先删掉允许的子串再查剩下的：DEV_COMPOSE 的定义行只认「docker compose -f dev/docker-compose.yml」这一种开头，
  # 其余行里的 dev/docker-compose.yml 与 $(DEV_COMPOSE) 删掉后，同一行别处的 docker 调用照样命中
  hits="$(sed -e 's|^DEV_COMPOSE[[:space:]]*[:?]*=[[:space:]]*docker compose -f dev/docker-compose\.yml||' \
      -e 's|dev/docker-compose\.yml||g' -e 's|\$(DEV_COMPOSE)||g' "$mk" \
    | grep -niE "$DOCKER_RE" | grep -vE '^[0-9]+:[[:space:]]*#' || true)"
  [ -z "$hits" ] || bad 'Makefile calls docker / compose other than through $(DEV_COMPOSE):'$'\n'"$hits"
  # 每个被引用的 compose 文件路径都存在（相对 panel/）
  refs="$(grep -ohE '[A-Za-z0-9_./$()-]*compose[A-Za-z0-9_./-]*\.ya?ml' "$mk" | sort -u)"
  [ -n "$refs" ] || bad 'Makefile references no compose file: the check is broken'
  for r in $refs; do
    case "$r" in *'$'*) bad "Makefile compose path uses a variable, cannot be checked: $r" ;; esac
    [ -f "$PANEL/$r" ] || bad "Makefile references a compose file that does not exist: $r"
  done
  if grep -nE 'deploy/docker-compose|\$\((DEPLOY)\)/[A-Za-z0-9_.-]*compose' "$mk" | grep -q .; then
    bad 'Makefile points at a compose file under deploy/'
  fi

  # --- ④ 布局开关、迁移标识、Docker 布局路径 --------------------------------------------------------
  forbidden='PANDORA_LAYOUT|PANDORA_DB_LAYOUT|from-docker|aegis-valkey|aegis-postgres([^-]|$)|/opt/aegispanel|/var/backups/aegispanel'
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
  [ -z "$hits" ] || bad "layout switches or Docker-layout paths are back:"$'\n'"$hits"

  # --- ⑤ 检查点 Hook 目录：Go 常量与 install.sh 建的目录相同 ------------------------------------------
  hook_go="$PANEL/internal/domain/dbbackup/checkpoint_hook.go"
  go_hook="$(sed -n 's/^var checkpointHookRoot = "\(.*\)"$/\1/p' "$hook_go")"
  [ "$(wc -l <<<"$go_hook" | tr -d ' ')" -eq 1 ] && [ -n "$go_hook" ] || bad 'cannot extract checkpointHookRoot from checkpoint_hook.go'
  # 非测试代码里对它的赋值、取地址由同包 Go 测试 TestCheckpointHookRootIsNeverReassigned 按语法树查（I6）
  install_dir="$(sed -n 's/^INSTALL_DIR="\(.*\)"$/\1/p' "$DEPLOY/install-lib.sh")"
  [ "$(wc -l <<<"$install_dir" | tr -d ' ')" -eq 1 ] && [ -n "$install_dir" ] || bad 'cannot extract INSTALL_DIR from install-lib.sh'
  sh_hook="$(sed -n 's/^install -d .*"\$INSTALL_DIR\/\(checkpoint-sink\)"$/\1/p' "$DEPLOY/install.sh")"
  [ "$(wc -l <<<"$sh_hook" | tr -d ' ')" -eq 1 ] && [ -n "$sh_hook" ] || bad 'cannot find the install -d ... $INSTALL_DIR/checkpoint-sink line in install.sh'
  [ "$go_hook" = "$install_dir/$sh_hook" ] \
    || bad "checkpoint hook directory differs: Go says $go_hook, install.sh creates $install_dir/$sh_hook"
  echo "PASS ${#production[@]} ${#scan[@]}"
)

out="$(check_tree "$HERE/..")" || fail "$out"
read -r _ nprod nscan <<<"$out"

# --- 自检：改坏的副本必须被拦下（I1）-------------------------------------------------------------
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-single-layout.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fresh() { rm -rf "$T/p"; mkdir -p "$T/p/internal/domain"; cp -R "$HERE/../deploy" "$HERE/../tests" "$HERE/../dev" "$HERE/../Makefile" "$HERE/../cmd" "$T/p/"
  cp -R "$HERE/../internal" "$T/p/"; }
fresh
out="$(check_tree "$T/p")" || fail "the untouched copy failed: $out"
mutant() { # <说明> ：在 $T/p 上改好之后调用，必须红
  if check_tree "$T/p" >/dev/null; then fail "self-check: $1 was not detected"; fi
  fresh
}
e2e="$(find "$T/p/tests" -name '*.sh' -print | sort | head -n 1)"
# J16：docker 的边界与大小写、容器名
printf '\nPSQL="${DOCKER:-docker} exec -i x psql"\n' >>"$e2e"; mutant 'docker after "-" in an e2e script'
printf '\nDOCKER ps\n' >>"$e2e"; mutant 'an upper-case DOCKER call in an e2e script'
printf '\nrun-x -docker ps\n' >>"$e2e"; mutant 'docker right after "-" in an e2e script'
printf '\nPSQL="psql -h aegis-postgres"\n' >>"$e2e"; mutant 'the aegis-postgres container name in an e2e script'
printf '\n# x\nsudo -n docker info\n' >>"$T/p/deploy/psql.sh"; mutant 'docker in a production script'
# J17：Makefile 同一行里允许的子串之外再调 docker；直接写 docker compose -f dev/…（只许经 $(DEV_COMPOSE)）
printf '\nmut:\n\t$(DEV_COMPOSE) ps && docker exec x true\n' >>"$T/p/Makefile"; mutant 'docker after $(DEV_COMPOSE) on the same Makefile line'
printf '\nmut2:\n\tdocker compose -f dev/docker-compose.yml ps\n' >>"$T/p/Makefile"; mutant 'a direct docker compose call in the Makefile'
sed -i.bak 's|-f dev/docker-compose.yml|-f deploy/docker-compose.yml|' "$T/p/Makefile"; mutant 'DEV_COMPOSE pointing at deploy/'
# J18：e2e 下一层目录里的脚本也扫
mkdir -p "$T/p/tests/lib"; printf 'docker ps\n' >"$T/p/tests/lib/x.sh"; mutant 'docker in a nested e2e script'
# ⑤：Hook 目录两边任一改了
sed -i.bak 's|"$INSTALL_DIR/checkpoint-sink"|"$INSTALL_DIR/checkpoint-sink2"|' "$T/p/deploy/install.sh"; mutant 'a different hook directory in install.sh'
sed -i.bak 's|/opt/pandora/checkpoint-sink"|/opt/pandora/checkpoint-sink2"|' "$T/p/internal/domain/dbbackup/checkpoint_hook.go"; mutant 'a different hook directory in Go'
# ①④：删掉的文件与布局开关回来
: >"$T/p/deploy/install-native.sh"; mutant 'install-native.sh coming back'
# N3：每条规则配一个只有它抓得到的变异（不含 docker 一词的才测得到 ④，否则会被 ③ 先抓住）
printf '\nPANDORA_LAYOUT=native\n' >>"$T/p/deploy/install-lib.sh"; mutant 'the layout switch coming back (④)'
printf '\nvar x = "aegis-valkey"\n' >>"$T/p/internal/domain/dbbackup/checkpoint_hook.go"; mutant 'the aegis-valkey container name in Go (④)'
printf '\n# /opt/aegispanel\n' >>"$T/p/dev/initdb/10-pandora-owner.sql"; mutant 'an old layout path under dev/ (④)'
printf '\n# x\nDOCKER info\n' >>"$T/p/deploy/psql.sh"; mutant 'an upper-case DOCKER call in a production script (③ case)'
printf '\nmut3:\n\tDOCKER ps\n' >>"$T/p/Makefile"; mutant 'an upper-case DOCKER call in the Makefile (③ case)'
printf '\ndocker ps # note\n' >>"$e2e"; mutant 'a docker call with a trailing comment in an e2e script (③ comment exclusion)'
printf '\nmut4:\n\tdocker ps # note\n' >>"$T/p/Makefile"; mutant 'a docker call with a trailing comment in the Makefile (③ comment exclusion)'
unit="$(find "$T/p/deploy/systemd" -name '*.service' -print | sort | head -n 1)"
printf '\nExecStartPre=/usr/bin/docker info\n' >>"$unit"; mutant 'docker in a systemd unit (③ walks deploy/systemd)'
: >"$T/p/deploy/dev-compose.yaml"; mutant 'an (empty) compose file under deploy/ (①)'
printf '\ncp deploy/clear-ratelimit.sh "$out/"\n' >>"$T/p/deploy/build-release.sh"; mutant 'build-release.sh shipping a removed file (②)'
# 改写定义行总会留下「DEV_COMPOSE」一词，先被「Makefile 里的 compose」抓住；只有定义整行没了是这条独有的
sed -i.bak '/^DEV_COMPOSE[[:space:]]*:*=/d' "$T/p/Makefile"
cmp -s "$T/p/Makefile" "$HERE/../Makefile" && fail 'mutation premise: the DEV_COMPOSE definition line not found'
mutant 'the DEV_COMPOSE definition removed (③ definition)'

printf 'deploy single layout static: PASS (%d production files, %d scanned; self-check mutants all detected)\n' "$nprod" "$nscan"
