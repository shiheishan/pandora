#!/usr/bin/env bash
# [INPUT]: 依赖 playwright CLI、同目录 test-vps-public-ui-playwright.js、环境变量 PANDORA_VPS_UI_TARGET（http(s)://host:port/）
# [OUTPUT]: 对一台已部署实例的公开门户做真实浏览器验收，gate=pass 才算通过
# [POS]: deploy 测试里唯一面向远端实例的 UI 门禁；目标地址运行时给出、渲染进临时副本，仓库里只有占位符
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
SESSION="pandora-vps-public-${RUN_ID}"
CODEX_SKILL_HOME="${CODEX_HOME:-$HOME/.codex}"
PWCLI="${PANDORA_PLAYWRIGHT_CLI:-$CODEX_SKILL_HOME/skills/playwright/scripts/playwright_cli.sh}"
HARNESS="$ROOT/deploy/test-vps-public-ui-playwright.js"
TMP_PARENT="${TMPDIR:-/tmp}"
TMP_ROOT="$(mktemp -d "$TMP_PARENT/pandora-vps-public-ui.XXXXXX")"
RESULT="$TMP_ROOT/result.json"
PLAYWRIGHT_LOG="$TMP_ROOT/playwright.log"
SESSION_OPEN=0

chmod 0700 "$TMP_ROOT"
cleanup() {
  local business_rc="$1" cleanup_rc=0 final_rc
  trap - EXIT INT TERM
  set +e
  if [[ "$SESSION_OPEN" -eq 1 ]]; then
    (cd "$TMP_ROOT" && timeout --kill-after=5s 15s "$PWCLI" --session "$SESSION" close) \
      >>"$PLAYWRIGHT_LOG" 2>&1 || cleanup_rc=1
  fi
  if [[ "$business_rc" -ne 0 ]]; then
    tail -n 160 "$PLAYWRIGHT_LOG" 2>/dev/null >&2
  fi
  case "$TMP_ROOT" in
    "$TMP_PARENT"/pandora-vps-public-ui.*)
      rm -rf -- "$TMP_ROOT" || cleanup_rc=1
      [[ ! -e "$TMP_ROOT" ]] || cleanup_rc=1
      ;;
    *) cleanup_rc=1 ;;
  esac
  final_rc="$business_rc"
  [[ "$cleanup_rc" -eq 0 ]] || final_rc=1
  exit "$final_rc"
}
trap 'cleanup "$?"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

command -v timeout >/dev/null 2>&1 || { printf '%s\n' vps_public_ui_blocked_timeout_missing >&2; exit 2; }
if command -v python3 >/dev/null 2>&1; then PYTHON_BIN="$(command -v python3)"
elif command -v python >/dev/null 2>&1; then PYTHON_BIN="$(command -v python)"
else printf '%s\n' vps_public_ui_blocked_python_missing >&2; exit 2
fi
[[ -x "$PWCLI" ]] || { printf 'vps_public_ui_blocked_playwright_cli_missing=%s\n' "$PWCLI" >&2; exit 2; }
[[ -f "$HARNESS" && ! -L "$HARNESS" ]] || { printf '%s\n' vps_public_ui_harness_missing >&2; exit 2; }
# 目标地址不写进仓库（仓库公开，测试机 IP 不外露）：运行时由环境变量给出，渲染进临时副本
TARGET="${PANDORA_VPS_UI_TARGET:-}"
[[ "$TARGET" =~ ^https?://[^/]+/$ ]] || { printf '%s\n' vps_public_ui_blocked_target_missing_or_malformed >&2; exit 2; }
RENDERED="$TMP_ROOT/harness.js"
"$PYTHON_BIN" - "$HARNESS" "$RENDERED" "$TARGET" <<'PY'
import pathlib, sys
source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
if source.count("__PANDORA_VPS_UI_TARGET__") != 1:
    raise SystemExit("VPS public UI harness target placeholder mismatch")
pathlib.Path(sys.argv[2]).write_text(source.replace("__PANDORA_VPS_UI_TARGET__", sys.argv[3]), encoding="utf-8")
PY

SESSION_OPEN=1
(cd "$TMP_ROOT" && "$PWCLI" --session "$SESSION" open about:blank) >>"$PLAYWRIGHT_LOG" 2>&1
(cd "$TMP_ROOT" && "$PWCLI" --json --session "$SESSION" run-code --filename "$RENDERED") \
  >"$RESULT" 2>&1 || {
    tee -a "$PLAYWRIGHT_LOG" <"$RESULT" >&2
    exit 1
  }
tee -a "$PLAYWRIGHT_LOG" <"$RESULT"
"$PYTHON_BIN" - "$RESULT" <<'PY'
import json, pathlib, sys
response = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
if response.get("isError") is True:
    raise SystemExit("Playwright CLI reported an error")
result = response.get("result")
if isinstance(result, str):
    result = json.loads(result)
if not isinstance(result, dict) or result.get("gate") != "pass":
    raise SystemExit("VPS public UI browser gate marker missing")
PY
printf '%s\n' vps_public_ui_playwright_gate_ok
