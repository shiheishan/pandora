#!/usr/bin/env bash
# 判断 Go 改动是否只动了注释：输出为空才算「只改注释」。
# 用法：comment-only.sh <base> <head>
# 注意：TS 的 /** */ 块里以 * 开头的行这里不算注释，前端文件另看。
set -euo pipefail
base="${1:?base}"; head="${2:?head}"
git diff "$base".."$head" -- '*.go' \
  | grep '^[-+]' | grep -v '^[-+][-+]' \
  | grep -vE '^[-+]\s*//' | grep -vE '^[-+]\s*$' || true
