# 被 run.sh、export-schemas.sh source：按仓库 go.mod 固定本机 Go 工具链。
# 本机缺省 Go 比 go.mod 新时（如 1.27.1），pdnd 与 tools 链接会失败，所以不用 GOTOOLCHAIN=local，
# 而是取 panel 与 pdnd 的 go.mod 里较高的版本，用本机模块缓存里的同版本工具链，不联网。
# 不设 GOSUMDB=off：它与 GOPROXY=off 一起会让缓存里的工具链校验报错。
pin_go_toolchain() {
  local repo="$1" v best=""
  for m in "$repo/panel/go.mod" "$repo/pdnd/go.mod"; do
    v="$(awk '$1=="toolchain"{sub(/^go/,"",$2); print $2; exit} $1=="go"&&!t{g=$2} END{if(g)print g}' "$m" | head -1)"
    [ -n "$v" ] || continue
    if [ -z "$best" ] || [ "$(printf '%s\n%s\n' "$best" "$v" | sort -V | tail -1)" = "$v" ]; then best="$v"; fi
  done
  [ -n "$best" ] || { echo "读不到 $repo 的 go.mod 版本" >&2; return 1; }
  export GOPROXY=off GOTOOLCHAIN="go$best"
}
