#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
SESSION="pandora-auth-ui-${RUN_ID}"
CODEX_SKILL_HOME="${CODEX_HOME:-$HOME/.codex}"
PWCLI="${PANDORA_PLAYWRIGHT_CLI:-$CODEX_SKILL_HOME/skills/playwright/scripts/playwright_cli.sh}"
TMP_PARENT="${TMPDIR:-/tmp}"
TMP_ROOT="$(mktemp -d "$TMP_PARENT/pandora-auth-ui.XXXXXX")"
PORT_FILE="$TMP_ROOT/port"
SERVER_LOG="$TMP_ROOT/server.log"
PLAYWRIGHT_LOG="$TMP_ROOT/playwright.log"
HARNESS="$TMP_ROOT/auth-ui-gate.js"
RESULT="$TMP_ROOT/result.json"
SERVER_PID=""
SESSION_OPEN=0

chmod 0700 "$TMP_ROOT"

cleanup() {
  local business_rc="$1" cleanup_rc=0 final_rc
  trap - EXIT INT TERM
  set +e
  if [[ "$SESSION_OPEN" -eq 1 ]]; then
    (cd "$TMP_ROOT" && timeout --kill-after=5s 15s "$PWCLI" --session "$SESSION" close) >>"$PLAYWRIGHT_LOG" 2>&1 || cleanup_rc=1
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
    "$TMP_PARENT"/pandora-auth-ui.*)
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

command -v npx >/dev/null 2>&1 || { printf '%s\n' auth_ui_blocked_npx_missing >&2; exit 2; }
command -v timeout >/dev/null 2>&1 || { printf '%s\n' auth_ui_blocked_timeout_missing >&2; exit 2; }
[[ -x "$PWCLI" ]] || { printf 'auth_ui_blocked_playwright_cli_missing=%s\n' "$PWCLI" >&2; exit 2; }
if command -v python3 >/dev/null 2>&1; then PYTHON_BIN="$(command -v python3)"
elif command -v python >/dev/null 2>&1; then PYTHON_BIN="$(command -v python)"
else printf '%s\n' auth_ui_blocked_python_missing >&2; exit 2
fi

"$PYTHON_BIN" - "$ROOT" "$PORT_FILE" >"$SERVER_LOG" 2>&1 <<'PY' &
import functools, http.server, os, pathlib, sys
root = pathlib.Path(sys.argv[1]).resolve(strict=True)
port_file = pathlib.Path(sys.argv[2])
admin_path = "/pandora-admin-gate-20260802/"
admin_index = (root / "web" / "admin" / "index.html").read_bytes()
class Handler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, _format, *_args):
        pass
    def do_GET(self):
        request_path = self.path.split("?", 1)[0]
        if request_path in (admin_path, admin_path + "index.html"):
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(admin_index)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(admin_index)
            return
        super().do_GET()
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(Handler, directory=str(root)))
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
BASE="http://127.0.0.1:${PORT}"

cat >"$HARNESS" <<'JS'
async (page) => {
  const base = '__PANDORA_AUTH_UI_BASE__';
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const captures = [];
  const logoutCaptures = [];
  let logoutStatus = 204;
  let logoutRequests = 0;
  let eventRequests = 0;

  await page.context().route('**/*', async route => {
    const request = route.request();
    const requestURL = request.url();
    const authorityEnd = requestURL.indexOf('/', requestURL.indexOf('://') + 3);
    const pathAndQuery = authorityEnd < 0 ? '/' : requestURL.slice(authorityEnd);
    const pathname = pathAndQuery.split('?', 1)[0];
    if (!pathname.includes('/v1/')) return route.continue();
    if (pathname.endsWith('/v1/__body_probe')) {
      captures.push({
        headers: await request.allHeaders(),
        body: request.postData(),
        method: request.method(),
      });
      return route.fulfill({status: 200, contentType: 'application/json', body: '{"ok":true}'});
    }
    if (pathname.endsWith('/v1/auth/logout')) {
      logoutRequests++;
      logoutCaptures.push({
        headers: await request.allHeaders(),
        method: request.method(),
        pathname,
      });
      if (logoutStatus === 204) return route.fulfill({status: 204, body: ''});
      return route.fulfill({
        status: logoutStatus,
        contentType: 'application/json',
        body: JSON.stringify({error: {code: 'auth_ui_gate', message: 'logout gate'}}),
      });
    }
    if (pathname.endsWith('/v1/events')) {
      eventRequests++;
      return route.fulfill({status: 401, contentType: 'application/json', body: '{}'});
    }
    return route.fulfill({status: 200, contentType: 'application/json', body: '{}'});
  });

  async function waitStored(key, expected) {
    await page.waitForFunction(([storageKey, value]) => localStorage.getItem(storageKey) === value,
      [key, expected]);
  }

  function assertLogoutRequest(label, before, expectedPath, expectedToken) {
    assert(logoutCaptures.length === before + 1, label + ': logout capture count');
    const request = logoutCaptures[before];
    assert(request.method === 'POST', label + ': logout method');
    assert(request.headers.authorization === 'Bearer ' + expectedToken, label + ': logout Authorization');
    assert(request.pathname === expectedPath, label + ': logout pathname');
  }

  async function exercise(label, pagePath, storageKey, expectedLogoutPath,
    passwordInputID, passwordToggleID) {
    captures.length = 0;
    logoutCaptures.length = 0;
    await page.goto(base + pagePath, {waitUntil: 'domcontentloaded'});
    const passwordInput = page.locator('#' + passwordInputID);
    const passwordToggle = page.locator('#' + passwordToggleID);
    assert(await passwordInput.getAttribute('type') === 'password', label + ': password starts hidden');
    assert(await passwordToggle.getAttribute('aria-controls') === passwordInputID,
      label + ': password toggle target');
    assert(await passwordToggle.getAttribute('aria-pressed') === 'false',
      label + ': password toggle initial state');
    assert(Boolean((await passwordToggle.getAttribute('aria-label'))?.trim()),
      label + ': password toggle accessible name');
    await passwordToggle.click();
    assert(await passwordInput.getAttribute('type') === 'text', label + ': password reveal');
    assert(await passwordToggle.getAttribute('aria-pressed') === 'true',
      label + ': password reveal state');
    await passwordToggle.click();
    assert(await passwordInput.getAttribute('type') === 'password', label + ': password rehide');
    assert(await passwordToggle.getAttribute('aria-pressed') === 'false',
      label + ': password rehide state');

    for (const [width, height] of [
      [390, 844], [768, 1024], [1024, 1366], [1920, 1080], [3840, 2160],
    ]) {
      await page.setViewportSize({width, height});
      await page.waitForTimeout(80);
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
        `${label}: ${width}x${height} document overflow`);
      if (width === 390) {
        const box = await passwordToggle.boundingBox();
        assert(Boolean(box && box.width >= 42 && box.height >= 42),
          label + ': mobile password toggle touch target');
      }
    }
    await page.setViewportSize({width: 1440, height: 900});

    // Exercise the actual authenticated app shell at phone/tablet widths.  A
    // no-overflow login page is not evidence that its navigation drawer is
    // usable, focus-safe or correctly isolated from the main content.
    await page.evaluate(() => {
      document.getElementById('authView').classList.add('hide');
      document.getElementById('appView').classList.remove('hide');
      syncSidebarMode();
    });
    for (const [width, height] of [[390, 844], [768, 1024], [1024, 1366]]) {
      await page.setViewportSize({width, height});
      await page.evaluate(() => syncSidebarMode());
      const menu = page.locator('#btnMenu');
      const sidebar = page.locator('#sidebar');
      const main = page.locator('#appView main');
      const mask = page.locator('#sideMask');
      assert(await menu.getAttribute('aria-expanded') === 'false',
        `${label}: ${width} drawer initially closed`);
      assert(await sidebar.evaluate(element => element.inert),
        `${label}: ${width} closed drawer is not inert`);
      assert(!(await main.evaluate(element => element.inert)),
        `${label}: ${width} closed drawer leaves main inert`);
      await menu.click();
      await page.waitForFunction(() => document.getElementById('btnMenu')?.getAttribute('aria-expanded') === 'true');
      await page.waitForFunction(() => document.getElementById('sidebar')?.contains(document.activeElement));
      assert(!(await sidebar.evaluate(element => element.inert)),
        `${label}: ${width} open drawer remains inert`);
      assert(await main.evaluate(element => element.inert),
        `${label}: ${width} open drawer does not isolate main`);
      assert(!(await mask.evaluate(element => element.inert)) && !(await mask.isDisabled()),
        `${label}: ${width} open drawer mask unavailable`);
      await page.keyboard.press('Escape');
      await page.waitForFunction(() => document.activeElement?.id === 'btnMenu');
      assert(await menu.getAttribute('aria-expanded') === 'false',
        `${label}: ${width} Escape did not close drawer`);
      assert(await sidebar.evaluate(element => element.inert),
        `${label}: ${width} closed drawer did not restore inert`);
      assert(!(await main.evaluate(element => element.inert)),
        `${label}: ${width} closed drawer did not restore main`);
    }
    for (const [width, height] of [[1920, 1080], [3840, 2160]]) {
      await page.setViewportSize({width, height});
      await page.evaluate(() => syncSidebarMode());
      await page.waitForTimeout(80);
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
        `${label}: authenticated ${width}x${height} document overflow`);
      const sidebar = page.locator('#sidebar');
      const main = page.locator('#appView main');
      assert(!(await sidebar.evaluate(element => element.inert)),
        `${label}: authenticated ${width} sidebar unexpectedly inert`);
      assert(!(await main.evaluate(element => element.inert)),
        `${label}: authenticated ${width} main unexpectedly inert`);
      const [sidebarBox, mainBox] = await Promise.all([sidebar.boundingBox(), main.boundingBox()]);
      assert(Boolean(sidebarBox && mainBox && mainBox.x >= sidebarBox.x + sidebarBox.width - 1),
        `${label}: authenticated ${width} sidebar overlaps main`);
      assert(Boolean(mainBox && mainBox.width >= width * 0.65),
        `${label}: authenticated ${width} main underuses viewport`);
    }
    await page.evaluate(() => {
      setSidebar(false, false);
      document.getElementById('appView').classList.add('hide');
      document.getElementById('authView').classList.remove('hide');
    });
    await page.setViewportSize({width: 1440, height: 900});
    await page.evaluate(async () => {
      token = 'body-token';
      await api('/v1/__body_probe', {method:'POST', body:{alpha:1}});
      await api('/v1/__body_probe', {method:'POST', body:'{"beta":2}'});
      await api('/v1/__body_probe', {method:'POST', headers:{'content-type':'application/merge-patch+json'}, body:'{"gamma":3}'});
      const form = new FormData(); form.append('name', 'pandora');
      await api('/v1/__body_probe', {method:'POST', body:form});
      await api('/v1/__body_probe', {method:'POST', body:new URLSearchParams({alpha:'one', beta:'two'})});
      await api('/v1/__body_probe', {method:'POST', body:new Blob(['blob-body'], {type:'text/plain'})});
      await api('/v1/__body_probe', {method:'POST', body:new TextEncoder().encode('array-buffer').buffer});
      await api('/v1/__body_probe', {method:'POST'});
    });
    assert(captures.length === 8, label + ': request capture count');
    const contentType = capture => capture.headers['content-type'] || '';
    assert(captures.every(capture => capture.method === 'POST'), label + ': request method');
    assert(captures.every(capture => capture.headers.authorization === 'Bearer body-token'), label + ': Authorization');
    assert(captures[0].body === '{"alpha":1}' && contentType(captures[0]) === 'application/json', label + ': plain object');
    assert(captures[1].body === '{"beta":2}' && contentType(captures[1]) === 'application/json', label + ': string JSON');
    assert(captures[2].body === '{"gamma":3}' && contentType(captures[2]) === 'application/merge-patch+json', label + ': custom content type');
    assert(contentType(captures[3]).startsWith('multipart/form-data; boundary=') && captures[3].body.includes('pandora'), label + ': FormData');
    assert(captures[4].body === 'alpha=one&beta=two' && contentType(captures[4]) === 'application/x-www-form-urlencoded;charset=UTF-8', label + ': URLSearchParams');
    assert(captures[5].body === 'blob-body' && contentType(captures[5]) === 'text/plain', label + ': Blob');
    assert(captures[6].body === 'array-buffer' && contentType(captures[6]) === '', label + ': ArrayBuffer');
    assert(captures[7].body === null && contentType(captures[7]) === '', label + ': empty body');

    logoutStatus = 503;
    const logoutBefore503 = logoutRequests;
    const logoutCaptureBefore503 = logoutCaptures.length;
    const eventsBefore503 = eventRequests;
    await page.evaluate(([key, value]) => {
      token = value; localStorage.setItem(key, value);
      document.getElementById('authView').classList.add('hide');
      document.getElementById('appView').classList.remove('hide');
      document.getElementById('btnLogout').click();
    }, [storageKey, 'keep-on-5xx']);
    await waitStored(storageKey, 'keep-on-5xx');
    await page.waitForFunction(() => !document.getElementById('btnLogout').disabled);
    for (let attempt = 0; attempt < 40 && eventRequests <= eventsBefore503; attempt++) {
      await page.waitForTimeout(25);
    }
    const failedState = await page.evaluate(() => ({
      tokenValue: token,
      appHidden: document.getElementById('appView').classList.contains('hide'),
      authHidden: document.getElementById('authView').classList.contains('hide'),
      toast: document.getElementById('toasts').textContent,
    }));
    assert(logoutRequests === logoutBefore503 + 1, label + ': 503 logout request count');
    assertLogoutRequest(label + ': 503', logoutCaptureBefore503, expectedLogoutPath, 'keep-on-5xx');
    assert(eventRequests > eventsBefore503, label + ': 503 realtime restart');
    assert(failedState.tokenValue === 'keep-on-5xx' && !failedState.appHidden && failedState.authHidden, label + ': 503 credential/view state');
    assert(failedState.toast.includes('服务端注销失败'), label + ': 503 error toast');

    logoutStatus = 401;
    const logoutBefore401 = logoutRequests;
    const logoutCaptureBefore401 = logoutCaptures.length;
    const eventsBefore401 = eventRequests;
    await page.evaluate(() => {
      document.getElementById('toasts').replaceChildren();
      document.getElementById('btnLogout').click();
    });
    await waitStored(storageKey, null);
    await page.waitForTimeout(100);
    const unauthorizedState = await page.evaluate(() => ({
      tokenValue: token,
      appHidden: document.getElementById('appView').classList.contains('hide'),
      authHidden: document.getElementById('authView').classList.contains('hide'),
      toast: document.getElementById('toasts').textContent,
    }));
    assert(logoutRequests === logoutBefore401 + 1, label + ': 401 logout request count');
    assertLogoutRequest(label + ': 401', logoutCaptureBefore401, expectedLogoutPath, 'keep-on-5xx');
    assert(eventRequests === eventsBefore401, label + ': 401 realtime remains stopped');
    assert(!unauthorizedState.toast.includes('服务端注销失败'), label + ': 401 has no server-failure toast');
    assert(unauthorizedState.tokenValue === '' && unauthorizedState.appHidden && !unauthorizedState.authHidden, label + ': 401 credential/view state');

    logoutStatus = 204;
    const logoutBefore204 = logoutRequests;
    const logoutCaptureBefore204 = logoutCaptures.length;
    const eventsBefore204 = eventRequests;
    await page.evaluate(([key, value]) => {
      document.getElementById('toasts').replaceChildren();
      token = value; localStorage.setItem(key, value);
      document.getElementById('authView').classList.add('hide');
      document.getElementById('appView').classList.remove('hide');
      document.getElementById('btnLogout').click();
    }, [storageKey, 'clear-on-204']);
    await waitStored(storageKey, null);
    await page.waitForTimeout(100);
    const successState = await page.evaluate(() => ({
      tokenValue: token,
      appHidden: document.getElementById('appView').classList.contains('hide'),
      authHidden: document.getElementById('authView').classList.contains('hide'),
      toast: document.getElementById('toasts').textContent,
    }));
    assert(logoutRequests === logoutBefore204 + 1, label + ': 204 logout request count');
    assertLogoutRequest(label + ': 204', logoutCaptureBefore204, expectedLogoutPath, 'clear-on-204');
    assert(eventRequests === eventsBefore204, label + ': 204 realtime remains stopped');
    assert(!successState.toast.includes('服务端注销失败'), label + ': 204 has no server-failure toast');
    assert(successState.tokenValue === '' && successState.appHidden && !successState.authHidden, label + ': 204 credential/view state');
  }

  const adminPath = '/pandora-admin-gate-20260802/';
  await exercise('admin', adminPath, 'aegis_admin_token', adminPath + 'v1/auth/logout',
    'pass', 'togglePass');
  await exercise('portal', '/web/portal/', 'aegis_token', '/v1/auth/logout',
    'loginPass', 'toggleLoginPass');
  return {
    gate:'pass', engine:'real-playwright-browser', surfaces:['admin','portal'],
    custom_admin_path_frontend:true, password_toggle:true, mobile_tablet_drawer:true,
    authenticated_wide_portal:true,
    responsive_widths:[390,768,1024,1920,3840], body_cases:8, logout_cases:[204,401,503]
  };
}
JS
chmod 0600 "$HARNESS"
sed -i "s|__PANDORA_AUTH_UI_BASE__|$BASE|g" "$HARNESS"

SESSION_OPEN=1
(cd "$TMP_ROOT" && "$PWCLI" --session "$SESSION" open about:blank) >>"$PLAYWRIGHT_LOG" 2>&1
(cd "$TMP_ROOT" && "$PWCLI" --session "$SESSION" snapshot) >>"$PLAYWRIGHT_LOG" 2>&1
(cd "$TMP_ROOT" && "$PWCLI" --json --session "$SESSION" run-code --filename "$HARNESS") >"$RESULT" 2>&1 || {
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
    raise SystemExit("auth UI browser gate marker missing")
PY
printf '%s\n' auth_ui_bodyinit_admin_portal_ok
printf '%s\n' auth_ui_logout_204_401_503_ok
printf '%s\n' auth_ui_playwright_gate_ok
