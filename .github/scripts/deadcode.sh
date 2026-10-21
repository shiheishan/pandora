#!/usr/bin/env bash
# 死代码检查：从 main 包出发（不带 -test）用 golang.org/x/tools/cmd/deadcode 找到不了的函数，
# 只剩测试在调的函数也算死代码。panel 与 pdnd 两个 module 共用这一份。
#
# 用法：bash .github/scripts/deadcode.sh <module 目录> <入口包...>
#   如：bash .github/scripts/deadcode.sh panel ./cmd/pandora ./tools/...
# 本机要与 CI 同口径时加 GOTOOLCHAIN=go<go.mod 的版本>。
#
# 误报写进 <module 目录>/.deadcode-ignore：每行一个 ERE，匹配 deadcode 的输出行
# （<文件>:<行>:<列>: unreachable func: <名字>），# 开头是注释，每条上面写理由；行号会变，模式里不写行号。
# 一条模式一行都没匹配上（误报已经没了）也判失败，免得忽略表越积越多。
set -euo pipefail

# 钉版本；升级时本脚本、verify skill 的命令表一起改
DEADCODE=golang.org/x/tools/cmd/deadcode@v0.52.0

[ "$#" -ge 2 ] || { echo "用法: $0 <module 目录> <入口包...>" >&2; exit 2; }
cd "$1"
shift

out="$(go run "$DEADCODE" "$@")"

patterns=()
if [ -f .deadcode-ignore ]; then
  while IFS= read -r line; do
    case "$line" in '' | '#'*) continue ;; esac
    patterns+=("$line")
  done < .deadcode-ignore
fi

left="$out"
stale=()
for p in ${patterns[@]+"${patterns[@]}"}; do
  if ! grep -qE -- "$p" <<<"$out"; then
    stale+=("$p")
  fi
  left="$(grep -vE -- "$p" <<<"$left" || true)"
done

rc=0
if [ -n "$left" ]; then
  echo "没有调用方的函数（确实没用就删；只给测试用的挪进 _test.go；误报写进 $(pwd)/.deadcode-ignore 并注明理由）：" >&2
  printf '%s\n' "$left" >&2
  rc=1
fi
if [ "${#stale[@]}" -gt 0 ]; then
  echo "$(pwd)/.deadcode-ignore 里这些模式已经匹配不到任何输出，删掉：" >&2
  printf '  %s\n' "${stale[@]}" >&2
  rc=1
fi
[ "$rc" -ne 0 ] || echo "deadcode: 没有未忽略的死代码"
exit "$rc"
