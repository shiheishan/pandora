#!/usr/bin/env bash
# govulncheck 扫 pandora 的三个 Go 模块，只报「被调用」的漏洞，并另出一份只含发布物的视图。
# 用法（仓库根或 worktree 内任意目录）：bash .claude/skills/deps-upgrade/scripts/vulncheck.sh [输出目录]
# 退出码：0 全部无被调用漏洞；3 有被调用漏洞；1 扫描本身出错。
#
# - 每个模块用它 go.mod 的 go 指令对应的工具链（GOTOOLCHAIN=go<版本>），与 CI 的 setup-go 同版本；
#   本机默认 Go 比 CI 新时，不这样设会按本机版本报标准库漏洞，结论对不上 CI。
# - subscription-e2e/tools 经 replace 跟随 pdnd，pdnd 依赖一变它就不 tidy；这里用 -mod=mod 扫，
#   扫前备份、扫后还原它的 go.mod / go.sum，不留改动。
# - 发布物视图：GOOS=linux 下只扫 panel ./cmd/... 与 pdnd 的 . 、./cmd/pandora-h3-probe，
#   去掉 tools/、测试辅助、-tags compat 才链进来的 core/xray 等路径。
# - 要联网（拉 govulncheck、漏洞库、缺的工具链）；沙箱里跑不通就关沙箱。
set -uo pipefail

root="$(git rev-parse --show-toplevel)"
out="${1:-${TMPDIR:-/tmp}/vulncheck-$(git -C "$root" rev-parse --short HEAD)}"
mkdir -p "$out/bin"
tools="$root/.claude/skills/subscription-e2e/tools"

gover() { awk '/^go /{print $2; exit}' "$1/go.mod"; }

# govulncheck 本身用本机工具链装一次；GOOS=linux 时 `go run` 会产出跑不了的二进制，所以先装成文件。
if ! GOTOOLCHAIN=local GOBIN="$out/bin" go install golang.org/x/vuln/cmd/govulncheck@latest > "$out/install.log" 2>&1; then
  echo "安装 govulncheck 失败，见 $out/install.log" >&2
  exit 1
fi
vc="$out/bin/govulncheck"

worst=0
scan() { # 名字 目录 [环境变量...] -- 包...
  local name="$1" dir="$2"; shift 2
  local envs=()
  while [ "$1" != "--" ]; do envs+=("$1"); shift; done
  shift
  local tc="go$(gover "$dir")"
  (cd "$dir" && env GOTOOLCHAIN="$tc" ${envs[@]+"${envs[@]}"} "$vc" "$@") > "$out/$name.txt" 2>&1
  local rc=$?
  printf '\n== %s（%s，%s）退出 %s ==\n' "$name" "${dir#"$root"/}" "$tc" "$rc"
  case "$rc" in
    0) echo "无被调用的漏洞" ;;
    3)
      # 每条漏洞：编号、所在模块与版本、修复版本、第一条调用链（看它从生产代码还是 tools/ 进来）
      awk '
        /^Vulnerability #/ { id=$3; first=1; next }
        /^ *Found in:/ { found=$3 }
        /^ *Fixed in:/ { fixed=$3 }
        /^ *#1: / && first { sub(/^ *#1: /, ""); printf "%-14s %-40s -> %-36s %s\n", id, found, fixed, $0; first=0 }
      ' "$out/$name.txt" | cut -c1-220
      grep -E '^Your code is affected' "$out/$name.txt"
      [ "$worst" -lt 3 ] && worst=3 ;;
    *) echo "扫描出错，见 $out/$name.txt"; tail -5 "$out/$name.txt"; worst=1 ;;
  esac
}

scan panel "$root/panel" -- ./...
scan pdnd "$root/pdnd" -- ./...

cp "$tools/go.mod" "$out/tools.go.mod.bak"; cp "$tools/go.sum" "$out/tools.go.sum.bak"
scan sub-e2e-tools "$tools" GOFLAGS=-mod=mod -- ./...
if ! cmp -s "$tools/go.mod" "$out/tools.go.mod.bak" || ! cmp -s "$tools/go.sum" "$out/tools.go.sum.bak"; then
  echo "（subscription-e2e/tools 的 go.mod 不 tidy：-mod=mod 补了 require，已还原；升依赖时要在那里 go mod tidy）"
fi
cp "$out/tools.go.mod.bak" "$tools/go.mod"; cp "$out/tools.go.sum.bak" "$tools/go.sum"

echo; echo "---- 发布物视图（GOOS=linux，只含发布的二进制） ----"
scan panel-release "$root/panel" GOOS=linux -- ./cmd/...
scan pdnd-release "$root/pdnd" GOOS=linux -- . ./cmd/pandora-h3-probe

echo; echo "完整输出在 $out/<名字>.txt；看某条的全部调用链：grep -n -A40 '<GO-编号>' $out/<名字>.txt"
exit "$worst"
