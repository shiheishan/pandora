#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
SESSION="pandora-registration-ui-${RUN_ID}"
CODEX_SKILL_HOME="${CODEX_HOME:-$HOME/.codex}"
PWCLI="${PANDORA_PLAYWRIGHT_CLI:-$CODEX_SKILL_HOME/skills/playwright/scripts/playwright_cli.sh}"
SOURCE_HARNESS="$ROOT/deploy/test-registration-mode-ui-playwright.js"
TMP_PARENT="${TMPDIR:-/tmp}"
TMP_ROOT="$(mktemp -d "$TMP_PARENT/pandora-registration-ui.XXXXXX")"
PORT_FILE="$TMP_ROOT/port"
SERVER_LOG="$TMP_ROOT/server.log"
PLAYWRIGHT_LOG="$TMP_ROOT/playwright.log"
HARNESS="$TMP_ROOT/registration-ui-gate.js"
RESULT="$TMP_ROOT/result.json"
SERVER_PID=""
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
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" >/dev/null 2>&1
    for _ in $(seq 1 100); do
      kill -0 "$SERVER_PID" >/dev/null 2>&1 || break
      sleep 0.05
    done
    if kill -0 "$SERVER_PID" >/dev/null 2>&1; then
      kill -KILL "$SERVER_PID" >/dev/null 2>&1
      cleanup_rc=1
    fi
    wait "$SERVER_PID" >/dev/null 2>&1 || true
  fi
  if [[ "$business_rc" -ne 0 ]]; then
    tail -n 160 "$PLAYWRIGHT_LOG" 2>/dev/null >&2
    tail -n 80 "$SERVER_LOG" 2>/dev/null >&2
  fi
  case "$TMP_ROOT" in
    "$TMP_PARENT"/pandora-registration-ui.*)
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

command -v timeout >/dev/null 2>&1 || { printf '%s\n' registration_ui_blocked_timeout_missing >&2; exit 2; }
if command -v python3 >/dev/null 2>&1; then PYTHON_BIN="$(command -v python3)"
elif command -v python >/dev/null 2>&1; then PYTHON_BIN="$(command -v python)"
else printf '%s\n' registration_ui_blocked_python_missing >&2; exit 2
fi
[[ -x "$PWCLI" ]] || { printf 'registration_ui_blocked_playwright_cli_missing=%s\n' "$PWCLI" >&2; exit 2; }
[[ -f "$SOURCE_HARNESS" && ! -L "$SOURCE_HARNESS" ]] || { printf '%s\n' registration_ui_harness_missing >&2; exit 2; }

"$PYTHON_BIN" - "$ROOT" "$PORT_FILE" >"$SERVER_LOG" 2>&1 <<'PY' &
import http.server, os, pathlib, sys
root = pathlib.Path(sys.argv[1]).resolve(strict=True)
port_file = pathlib.Path(sys.argv[2])
portal = (root / "web" / "portal" / "index.html").read_bytes()
admin = (root / "web" / "admin" / "index.html").read_bytes()
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, _format, *_args):
        pass
    def do_GET(self):
        body = admin if self.path.split("?", 1)[0].startswith("/admin-gate/") else portal
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
port_file.write_text(str(server.server_port), encoding="ascii")
os.chmod(port_file, 0o600)
server.serve_forever()
PY
SERVER_PID="$!"
for _ in $(seq 1 100); do
  [[ -s "$PORT_FILE" ]] && break
  kill -0 "$SERVER_PID" >/dev/null 2>&1 || break
  sleep 0.05
done
[[ -s "$PORT_FILE" ]] && kill -0 "$SERVER_PID" >/dev/null 2>&1 || exit 1
PORT="$(<"$PORT_FILE")"

"$PYTHON_BIN" - "$SOURCE_HARNESS" "$HARNESS" "http://127.0.0.1:${PORT}" <<'PY'
import pathlib, sys
source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
if source.count("__PANDORA_REG_UI_BASE__") != 1:
    raise SystemExit("registration UI harness placeholder count mismatch")
pathlib.Path(sys.argv[2]).write_text(source.replace("__PANDORA_REG_UI_BASE__", sys.argv[3]), encoding="utf-8")
PY
chmod 0600 "$HARNESS"

SESSION_OPEN=1
(cd "$TMP_ROOT" && "$PWCLI" --session "$SESSION" open about:blank) >>"$PLAYWRIGHT_LOG" 2>&1
(cd "$TMP_ROOT" && "$PWCLI" --json --session "$SESSION" run-code --filename "$HARNESS") \
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
    raise SystemExit("registration UI browser gate marker missing")
if result.get("engine") != "real-playwright-browser":
    raise SystemExit("registration UI browser engine marker mismatch")
if result.get("responsive_widths") != [390, 768, 1024, 1920, 3840]:
    raise SystemExit("registration UI width matrix mismatch")
if result.get("mode_cases") != [
    "closed", "config_failure", "malformed", "open",
    "invite_only_without_code", "invite_only_with_code",
]:
    raise SystemExit("registration UI mode matrix mismatch")
if len(result.get("captures") or []) != 30:
    raise SystemExit("registration UI capture count mismatch")
for marker in (
    "registration_start_bound_invite", "open_manual_invite_bound",
    "pending_default_closed", "admin_round_trip",
):
    if result.get(marker) is not True:
        raise SystemExit("registration UI marker missing: " + marker)
PY
printf '%s\n' registration_mode_ui_playwright_gate_ok
