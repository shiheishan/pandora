#!/usr/bin/env bash
# 线上下发链路的 curl 实证：协议、压缩、缓存头、304，以及每个首屏资源的耗时与传输量。
# 用法：online.sh <站点根 URL> [后台前缀]，如 online.sh https://panel.example.com ops_xxx
# 输出 Markdown 表；只读请求，不带任何令牌。结果里的域名、前缀不要原样贴进仓库或报告。
set -euo pipefail
origin="${1:?用法: online.sh <站点根 URL> [后台前缀]}"
origin="${origin%/}"
prefix="${2:-}"; prefix="${prefix#/}"; prefix="${prefix%/}"

pages=("门户|$origin/")
[ -n "$prefix" ] && pages+=("后台|$origin/$prefix/")

hdr() { # 取某个响应头的值（大小写不敏感，去掉回车）
  { grep -i "^$1:" || true; } | head -1 | cut -d: -f2- | tr -d '\r' | sed 's/^ *//'
}

for entry in "${pages[@]}"; do
  label="${entry%%|*}"; url="${entry#*|}"
  echo "## $label"
  echo
  h="$(curl -sS -D - -o /dev/null --http2 -H 'Accept-Encoding: gzip, br' "$url")"
  proto="$(curl -sS -o /dev/null -w '%{http_version}' --http2 "$url")"
  etag="$(printf '%s' "$h" | hdr etag)"
  code304="-"
  [ -n "$etag" ] && code304="$(curl -sS -o /dev/null -w '%{http_code}' -H "If-None-Match: $etag" "$url")"
  echo "- index.html：HTTP/$proto，Cache-Control \`$(printf '%s' "$h" | hdr cache-control)\`，ETag $( [ -n "$etag" ] && echo 有 || echo 无 )，带 If-None-Match 再请求回 $code304"
  echo "- 安全头：CSP $( [ -n "$(printf '%s' "$h" | hdr content-security-policy)" ] && echo 有 || echo 无 )，HSTS $( [ -n "$(printf '%s' "$h" | hdr strict-transport-security)" ] && echo 有 || echo 无 )"
  echo
  # 首屏资源：index.html 里直接引用的 js / css（base 是 ./，资源是相对路径）
  assets="$(curl -sS --compressed "$url" | { grep -oE '(src|href)="\./assets/[^"]+\.(js|css)"' || true; } | sed -E 's/^(src|href)="\.\///; s/"$//' | sort -u)"
  echo "| 资源 | 协议 | Content-Type | Content-Encoding | Vary | Cache-Control | 原始字节 | 压缩后传输 | TTFB s | 总耗时 s |"
  echo "|---|---|---|---|---|---|---|---|---|---|"
  for a in $assets; do
    u="$url$a"
    ah="$(curl -sS -D - -o /dev/null --http2 -H 'Accept-Encoding: gzip, br' "$u")"
    raw="$(curl -sS -o /dev/null -w '%{size_download}' -H 'Accept-Encoding: identity' "$u")"
    read -r comp ttfb total proto2 <<<"$(curl -sS -o /dev/null --http2 -H 'Accept-Encoding: gzip, br' -w '%{size_download} %{time_starttransfer} %{time_total} %{http_version}' "$u")"
    echo "| $a | $proto2 | $(printf '%s' "$ah" | hdr content-type) | $(printf '%s' "$ah" | hdr content-encoding) | $(printf '%s' "$ah" | hdr vary) | $(printf '%s' "$ah" | hdr cache-control) | $raw | $comp | $ttfb | $total |"
  done
  echo
done
echo "判读：JS 的 Content-Encoding 为空就是没压缩（看 nginx 的 gzip_types 有没有 text/javascript）；协议不是 2 就是 443 没开 HTTP/2。"
