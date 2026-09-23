#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
SESSION="pandora-dashboard-ui-${RUN_ID}"
CODEX_SKILL_HOME="${CODEX_HOME:-$HOME/.codex}"
PWCLI="${PANDORA_PLAYWRIGHT_CLI:-$CODEX_SKILL_HOME/skills/playwright/scripts/playwright_cli.sh}"
TMP_PARENT="${TMPDIR:-/tmp}"
TMP_ROOT="$(mktemp -d "$TMP_PARENT/pandora-dashboard-ui.XXXXXX")"
PORT_FILE="$TMP_ROOT/port"
SERVER_LOG="$TMP_ROOT/static-server.log"
PLAYWRIGHT_LOG="$TMP_ROOT/playwright.log"
HARNESS_RESULT="$TMP_ROOT/harness-result.json"
HARNESS="$TMP_ROOT/dashboard-ui-gate.js"
SERVER_PID=""
SESSION_OPEN=0

chmod 0700 "$TMP_ROOT"

cleanup() {
  local business_rc="$1" cleanup_rc=0 final_rc
  trap - EXIT INT TERM
  set +e

  if [[ "$SESSION_OPEN" -eq 1 ]]; then
    (cd "$TMP_ROOT" && "$PWCLI" --session "$SESSION" close) >>"$PLAYWRIGHT_LOG" 2>&1
    [[ $? -eq 0 ]] || cleanup_rc=1
  fi

  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" >/dev/null 2>&1
    wait "$SERVER_PID" >/dev/null 2>&1
    if kill -0 "$SERVER_PID" >/dev/null 2>&1; then
      cleanup_rc=1
    fi
  fi

  if [[ "$business_rc" -ne 0 ]]; then
    printf '%s\n' 'dashboard_ui_playwright_failure_log_begin' >&2
    tail -n 160 "$PLAYWRIGHT_LOG" 2>/dev/null >&2
    tail -n 80 "$SERVER_LOG" 2>/dev/null >&2
    printf '%s\n' 'dashboard_ui_playwright_failure_log_end' >&2
  fi

  case "$TMP_ROOT" in
    "$TMP_PARENT"/pandora-dashboard-ui.*)
      rm -rf -- "$TMP_ROOT"
      [[ ! -e "$TMP_ROOT" ]] || cleanup_rc=1
      ;;
    *)
      printf 'dashboard UI gate refused unsafe temporary path: %s\n' "$TMP_ROOT" >&2
      cleanup_rc=1
      ;;
  esac

  if [[ "$cleanup_rc" -eq 0 ]]; then
    printf '%s\n' 'dashboard_ui_cleanup_ok'
  else
    printf '%s\n' 'dashboard_ui_cleanup_failed' >&2
  fi

  final_rc="$business_rc"
  [[ "$cleanup_rc" -eq 0 ]] || final_rc=1
  exit "$final_rc"
}
trap 'cleanup "$?"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if ! command -v npx >/dev/null 2>&1; then
  printf '%s\n' 'dashboard_ui_blocked_npx_missing' >&2
  exit 2
fi
if [[ ! -x "$PWCLI" ]]; then
  printf 'dashboard_ui_blocked_playwright_cli_missing=%s\n' "$PWCLI" >&2
  exit 2
fi

if command -v python3 >/dev/null 2>&1; then
  PYTHON_BIN="$(command -v python3)"
elif command -v python >/dev/null 2>&1; then
  PYTHON_BIN="$(command -v python)"
else
  printf '%s\n' 'dashboard_ui_blocked_python_missing' >&2
  exit 2
fi

"$PYTHON_BIN" - "$ROOT" "$PORT_FILE" >"$SERVER_LOG" 2>&1 <<'PY' &
import functools
import http.server
import os
import pathlib
import sys

root = pathlib.Path(sys.argv[1]).resolve(strict=True)
port_file = pathlib.Path(sys.argv[2])

class QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, _format, *_args):
        pass

handler = functools.partial(QuietHandler, directory=str(root))
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
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
if [[ ! -s "$PORT_FILE" ]] || ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
  printf '%s\n' 'dashboard_ui_static_server_failed' >&2
  exit 1
fi
PORT="$(<"$PORT_FILE")"
[[ "$PORT" =~ ^[0-9]+$ ]] || { printf '%s\n' 'dashboard_ui_static_server_bad_port' >&2; exit 1; }
BASE_URL="http://127.0.0.1:${PORT}/web/admin/"

"$PYTHON_BIN" - "$BASE_URL" <<'PY'
import sys
import urllib.request

with urllib.request.urlopen(sys.argv[1], timeout=5) as response:
    if response.status != 200:
        raise SystemExit("static server did not return HTTP 200")
    body = response.read(256 * 1024)
    if b'id="authView"' not in body or b'id="appView"' not in body:
        raise SystemExit("admin document markers missing")
PY

cat >"$HARNESS" <<'JS'
async (page) => {
  const baseURL = '__PANDORA_DASHBOARD_UI_BASE_URL__';
  if (!baseURL || !baseURL.startsWith('http://127.0.0.1:')) {
    throw new Error('loopback base URL is required');
  }

  const SNAPSHOT = '2026-07-30T08:09:10.123456Z';
  const SNAPSHOT_NEW = '2026-07-30T08:10:11.654321Z';
  const HUGE = '184467440737095516170';
  const HUGE_GROUPED = '184,467,440,737,095,516,170 B';
  const delay = ms => page.waitForTimeout(ms);
  const assert = (condition, message) => {
    if (!condition) throw new Error('dashboard UI assertion failed: ' + message);
  };
  const json = (route, body, status = 200) => route.fulfill({
    status,
    contentType: 'application/json; charset=utf-8',
    headers: {'cache-control': 'no-store'},
    body: JSON.stringify(body),
  });
  const error = (route, status, message) => json(route, {
    error: {code: 'dashboard_ui_gate_mock', message},
  }, status);
  const parseQuery = search => {
    const result = {};
    for (const pair of String(search || '').replace(/^\?/, '').split('&')) {
      if (!pair) continue;
      const separator = pair.indexOf('=');
      const rawKey = separator < 0 ? pair : pair.slice(0, separator);
      const rawValue = separator < 0 ? '' : pair.slice(separator + 1);
      result[decodeURIComponent(rawKey.replace(/\+/g, ' '))] =
        decodeURIComponent(rawValue.replace(/\+/g, ' '));
    }
    return result;
  };

  const overview = {
    revenue: [{currency: 'CNY', today: 0, actual_today: 0, adjustment_today: 0}],
    subscriptions: {active: 1, trialing: 0, expiring_7_days: 0},
    users: {total: 1, today: 0, last_7_days: 0},
    orders: {pending: 0, paid_today: 0},
    ledger_drift_accounts: 0,
  };
  const revenue = {
    currency: 'CNY',
    from: '2026-07-24',
    to: '2026-07-30',
    points: [{
      date: '2026-07-30', actual_credit: 0, actual_debit: 0,
      adjustment: 0, displayed_net: 0,
    }],
  };

  function trafficBase(range, snapshot, totals, ranking, items) {
    return {
      range,
      snapshot_at: snapshot,
      from_at: '2026-07-23T08:09:10.123456Z',
      to_at: snapshot,
      basis: 'strict-valid non-duplicate raw usage entries',
      totals,
      ranking,
      quality: {
        report_count: 4,
        duplicate_report_count: 1,
        invalid_report_count: 1,
        valid_entry_count: 2,
        invalid_entry_count: 1,
      },
      items,
    };
  }

  function nodePayload(range = '7d', snapshot = SNAPSHOT, label = 'NODE-UNATTRIBUTED') {
    return trafficBase(range, snapshot, {
      reported_bytes: HUGE,
      attributed_bytes: '92233720368547758070',
      unattributed_bytes: '922337203685477580100',
    }, {
      limit: 10,
      returned_bytes: HUGE,
      other_node_bytes: '0',
    }, [{
      node_id: '00000000-0000-4000-8000-000000000041',
      name: 'node-gate',
      display_name: label,
      upload_bytes: '92233720368547758070',
      download_bytes: '922337203685477580100',
      total_bytes: HUGE,
      report_count: 4,
      contributing_entry_count: 2,
      last_report_at: snapshot,
    }]);
  }

  function userPayload(range = '7d', snapshot = SNAPSHOT) {
    return trafficBase(range, snapshot, {
      reported_bytes: HUGE,
      attributed_bytes: '92233720368547758070',
      unattributed_bytes: '922337203685477580100',
    }, {
      limit: 10,
      returned_bytes: '92233720368547758070',
      other_user_bytes: '0',
    }, [{
      user_id: '00000000-0000-4000-8000-000000000042',
      email: 'secret.person@example.com',
      email_masked: 's***n@example.com',
      upload_bytes: '2',
      download_bytes: '92233720368547758068',
      total_bytes: '92233720368547758070',
      subscription_count: 1,
      contributing_entry_count: 1,
      last_report_at: snapshot,
    }]);
  }

  const backlog = {
    as_of: SNAPSHOT,
    processor_state: 'unobservable',
    backlog_state: 'backlogged',
    scanner_interval_seconds: 60,
    max_ready_lag_seconds: 601,
    oldest_ready_at: '2026-07-30T07:59:09.123456Z',
    last_sent_at: '2026-07-30T08:08:00.123456Z',
    counts: {
      ready: 7,
      ready_retry: 3,
      scheduled: 5,
      scheduled_retry: 2,
      sending_unobservable: 1,
      failed_total: 11,
      suppressed_total: 13,
      bounced_total: 17,
    },
    assessment: {threshold_seconds: 600, reason: 'max_ready_lag_exceeds_threshold'},
  };

  page.__dashboardHarness = {scenario: 'none', permissions: [], requests: []};
  await page.addInitScript(() => {
    localStorage.setItem('aegis_admin_token', 'dashboard-ui-gate-token');
  });

  await page.context().route('**/*', async route => {
    const request = route.request();
    const requestURL = request.url();
    const authorityEnd = requestURL.indexOf('/', requestURL.indexOf('://') + 3);
    const pathAndQuery = authorityEnd < 0 ? '/' : requestURL.slice(authorityEnd);
    const question = pathAndQuery.indexOf('?');
    const pathname = question < 0 ? pathAndQuery : pathAndQuery.slice(0, question);
    const search = question < 0 ? '' : pathAndQuery.slice(question);
    const searchParams = parseQuery(search);
    const harness = page.__dashboardHarness;
    if (!pathname.includes('/v1/')) return route.continue();

    harness.requests.push({path: pathname, search, method: request.method()});
    const apiPath = pathname.slice(pathname.indexOf('/v1/'));
    if (apiPath === '/v1/me') {
      return json(route, {
        user_id: '00000000-0000-4000-8000-000000000001',
        permissions: [...harness.permissions],
      });
    }
    if (apiPath === '/v1/overview') return json(route, overview);
    if (apiPath === '/v1/revenue/timeseries') return json(route, revenue);
    if (apiPath === '/v1/revenue/adjustments') return json(route, {adjustments: []});

    if (apiPath === '/v1/dashboard/traffic/nodes') {
      const scenario = harness.scenario;
      const range = searchParams.range || '7d';
      if (scenario === 'anchor-fail') return error(route, 503, 'node anchor failed');
      if (scenario === 'race' && range === '7d') {
        await delay(850);
        return json(route, nodePayload('7d', SNAPSHOT, 'OLD-NODE-MUST-NOT-RENDER'));
      }
      if (scenario === 'race' && range === '30d') {
        return json(route, nodePayload('30d', SNAPSHOT_NEW, 'NEW-NODE-WINS'));
      }
      return json(route, nodePayload(range, SNAPSHOT));
    }
    if (apiPath === '/v1/dashboard/traffic/users') {
      const range = searchParams.range || '7d';
      const snapshot = searchParams.snapshot_at ||
        (harness.scenario === 'race' && range === '30d' ? SNAPSHOT_NEW : SNAPSHOT);
      return json(route, userPayload(range, snapshot));
    }
    if (apiPath === '/v1/dashboard/backlog/notifications') {
      if (harness.scenario === 'anchor-fail') return error(route, 503, 'backlog failed');
      return json(route, backlog);
    }
    return error(route, 599, 'unexpected API route ' + apiPath);
  });

  async function openScenario(name, permissions) {
    const harness = page.__dashboardHarness;
    harness.scenario = name;
    harness.permissions = [...permissions];
    harness.requests = [];
    await page.goto(baseURL, {waitUntil: 'domcontentloaded'});
    try {
      await page.waitForSelector('#dashboardTrafficCard', {state: 'attached', timeout: 7000});
      await page.waitForSelector('#dashboardBacklogCard', {state: 'attached', timeout: 7000});
    } catch (cause) {
      const diagnostic = await page.evaluate(() => ({
        url: location.href,
        authHidden: document.querySelector('#authView')?.classList.contains('hide'),
        appHidden: document.querySelector('#appView')?.classList.contains('hide'),
        view: document.querySelector('#view')?.textContent?.slice(0, 800),
        toasts: document.querySelector('#toasts')?.textContent?.slice(0, 800),
      }));
      throw new Error('dashboard shell did not render: ' + JSON.stringify({
        diagnostic,
        requests: harness.requests,
        cause: String(cause),
      }));
    }
  }
  const dashboardRequests = () => page.__dashboardHarness.requests.filter(
    request => request.path.includes('/v1/dashboard/'));
  const trafficRequests = kind => dashboardRequests().filter(
    request => request.path.endsWith('/traffic/' + kind));
  const query = request => parseQuery(request.search);
  const hasNoDocumentOverflow = () => page.evaluate(() => {
    const documentElement = document.documentElement;
    const body = document.body;
    return documentElement.scrollWidth <= documentElement.clientWidth + 1 &&
      body.scrollWidth <= documentElement.clientWidth + 1;
  });

  // Permission gating: the browser must not even send the protected requests.
  await openScenario('no-permission', ['billing.ledger.read']);
  await page.waitForFunction(() =>
    document.querySelectorAll('#dashboardTrafficCard [role="status"]').length > 0 &&
    document.querySelectorAll('#dashboardBacklogCard [role="status"]').length > 0);
  assert(dashboardRequests().length === 0, 'no-permission mode sent a dashboard request');
  assert(await page.locator('#dashboardTrafficCard [aria-live="polite"]').count() > 0,
    'traffic permission state lacks a polite live region');
  assert(await page.locator('#dashboardBacklogCard [aria-live="polite"]').count() > 0,
    'backlog permission state lacks a polite live region');
  assert(await page.locator('[data-backlog-refresh]').count() === 0,
    'backlog refresh was exposed without permission');

  const fullPermissions = [
    'billing.ledger.read', 'metering.read', 'node.read', 'iam.user.read',
    'ops.notification.read',
  ];

  // Success: nodes establish the snapshot anchor and users reuse it.
  await openScenario('success', fullPermissions);
  await page.waitForSelector('#dashboardTrafficCard .dashboard-traffic-table');
  await page.waitForFunction(() =>
    document.querySelector('#dashboardBacklogCard')?.textContent.includes('601'));
  const nodeSuccess = trafficRequests('nodes');
  const userSuccess = trafficRequests('users');
  assert(nodeSuccess.length === 1, 'success path must request nodes exactly once');
  assert(userSuccess.length === 1, 'success path must request users exactly once');
  assert(!Object.hasOwn(query(nodeSuccess[0]), 'snapshot_at'), 'anchor request unexpectedly had snapshot_at');
  assert(query(userSuccess[0]).snapshot_at === SNAPSHOT,
    'second ranking did not reuse the first ranking snapshot');

  const nodeText = await page.locator('#dashboardTrafficCard').innerText();
  assert(nodeText.includes('NODE-UNATTRIBUTED'), 'node ranking omitted unattributed node bytes');
  assert(nodeText.includes(HUGE_GROUPED), 'BigInt byte value lost exact decimal rendering');
  assert(nodeText.includes('922,337,203,685,477,580,100 B'),
    'unattributed byte total was not rendered exactly');
  assert(await page.locator('#dashboardBacklogCard').innerText().then(text =>
    text.includes('601') && text.includes('unobservable') && text.includes('7')),
  'backlog state/counts/unobservable processor status missing');

  // Roving tabindex and the complete tab keyboard contract.
  const nodeTab = page.locator('#dashboardTab-nodes');
  const userTab = page.locator('#dashboardTab-users');
  assert(await nodeTab.getAttribute('tabindex') === '0', 'selected tab is not tabbable');
  assert(await userTab.getAttribute('tabindex') === '-1', 'inactive tab is tabbable');
  await nodeTab.focus();
  await nodeTab.press('ArrowRight');
  await page.waitForFunction(() => document.activeElement?.id === 'dashboardTab-users');
  assert(await userTab.getAttribute('aria-selected') === 'true', 'ArrowRight did not select users');
  assert(await userTab.getAttribute('tabindex') === '0' &&
    await nodeTab.getAttribute('tabindex') === '-1', 'ArrowRight broke roving tabindex');
  const userText = await page.locator('#dashboardTrafficCard').innerText();
  assert(userText.includes('s***n@example.com'), 'masked user identity is missing');
  assert(!userText.includes('secret.person@example.com'), 'unmasked user identity leaked into the DOM');
  await userTab.press('ArrowLeft');
  await page.waitForFunction(() => document.activeElement?.id === 'dashboardTab-nodes');
  await nodeTab.press('End');
  await page.waitForFunction(() => document.activeElement?.id === 'dashboardTab-users');
  await userTab.press('Home');
  await page.waitForFunction(() => document.activeElement?.id === 'dashboardTab-nodes');
  assert(await nodeTab.getAttribute('aria-selected') === 'true', 'Home did not select the first tab');

  const refreshName = await page.locator('[data-backlog-refresh]').getAttribute('aria-label');
  assert(Boolean(refreshName && refreshName.trim()), 'backlog refresh button lacks an accessible name');
  assert(await page.locator('#dashboardBacklogCard [role="status"][aria-live="polite"]').count() > 0,
    'backlog success state lacks a polite live region');

  // Responsive matrix and focusable internal table scrolling on mobile.
  for (const [width, height] of [
    [360, 800], [390, 844], [768, 1024], [1024, 1366],
    [1440, 900], [1920, 1080], [3840, 2160],
  ]) {
    await page.setViewportSize({width, height});
    await page.waitForTimeout(120);
    assert(await hasNoDocumentOverflow(), `${width}x${height} has document horizontal overflow`);
    if (width <= 390) {
      await page.waitForSelector('#dashboardTrafficCard .tbl-wrap[role="region"][tabindex="0"]');
      const region = page.locator('#dashboardTrafficCard .tbl-wrap[role="region"]').first();
      assert(Boolean(await region.getAttribute('aria-label')), 'mobile table region lacks accessible name');
      await region.focus();
      assert(await region.evaluate(element => document.activeElement === element),
        'mobile scrollable table region cannot receive focus');
    }
  }

  // Anchor failure: the second ranking still runs without snapshot_at, and
  // traffic/backlog failures stay inside their own cards.
  await openScenario('anchor-fail', fullPermissions);
  await page.waitForSelector('#dashboardTrafficCard [data-dashboard-retry="nodes"]');
  await page.waitForSelector('#dashboardBacklogCard [data-backlog-retry]');
  const nodeFailed = trafficRequests('nodes');
  const userAfterFailure = trafficRequests('users');
  assert(nodeFailed.length === 1 && userAfterFailure.length === 1,
    'anchor failure did not continue to the second ranking');
  assert(!Object.hasOwn(query(nodeFailed[0]), 'snapshot_at'), 'failed anchor had snapshot_at');
  assert(!Object.hasOwn(query(userAfterFailure[0]), 'snapshot_at'),
    'second ranking reused a nonexistent snapshot after anchor failure');
  assert(await page.locator('#dashboardTrafficCard [role="alert"]').count() === 1,
    'traffic error is not isolated in its card');
  assert(await page.locator('#dashboardBacklogCard [role="alert"]').count() === 1,
    'backlog error is not isolated in its card');
  assert(await page.locator('#view').innerText().then(text => !text.includes('载入失败：')),
    'local dashboard failure replaced the complete overview');
  assert(Boolean(await page.locator('[data-dashboard-retry="nodes"]').getAttribute('aria-label')),
    'traffic retry lacks an accessible name');
  assert(Boolean(await page.locator('[data-backlog-retry]').getAttribute('aria-label')),
    'backlog retry lacks an accessible name');
  await page.locator('#dashboardTab-users').click();
  assert(await page.locator('#dashboardTrafficCard .dashboard-traffic-table').count() === 1,
    'successful second ranking is not independently viewable');

  // A slow 7d response must not overwrite the newer 30d cache.
  await openScenario('race', fullPermissions);
  await page.waitForSelector('#dashboardRange');
  await page.locator('#dashboardRange').selectOption('30d');
  await page.waitForFunction(() =>
    document.querySelector('#dashboardTrafficCard')?.textContent.includes('NEW-NODE-WINS'));
  await page.waitForTimeout(1100);
  const raceText = await page.locator('#dashboardTrafficCard').innerText();
  assert(raceText.includes('NEW-NODE-WINS'), 'new response did not win the cache race');
  assert(!raceText.includes('OLD-NODE-MUST-NOT-RENDER'), 'stale response overwrote the new cache');
  const raceNodes = trafficRequests('nodes');
  const raceUsers = trafficRequests('users');
  assert(raceNodes.some(request => query(request).range === '7d'),
    'race did not start the slow old request');
  assert(raceNodes.some(request => query(request).range === '30d'),
    'race did not start the fast new request');
  assert(raceUsers.some(request => query(request).range === '30d' &&
    query(request).snapshot_at === SNAPSHOT_NEW),
  'new ranking pair did not share the new snapshot');
  assert(!raceUsers.some(request => query(request).range === '7d'),
    'stale anchor continued into a stale second-ranking request');
  assert(await hasNoDocumentOverflow(), 'race completion introduced horizontal overflow');

  return {
    gate: 'pass',
    engine: 'real-playwright-browser',
    scenarios: ['permission', 'anchor', 'failure-isolation', 'race', 'bigint', 'privacy', 'a11y', 'responsive'],
  };
}
JS
chmod 0600 "$HARNESS"
sed -i "s|__PANDORA_DASHBOARD_UI_BASE_URL__|$BASE_URL|g" "$HARNESS"

(
  cd "$TMP_ROOT"
  "$PWCLI" --session "$SESSION" open about:blank
) >>"$PLAYWRIGHT_LOG" 2>&1
SESSION_OPEN=1

(
  cd "$TMP_ROOT"
  "$PWCLI" --session "$SESSION" snapshot
)
if ! (
  cd "$TMP_ROOT"
  "$PWCLI" --json --session "$SESSION" run-code --filename "$HARNESS"
) >"$HARNESS_RESULT" 2>&1; then
  tee -a "$PLAYWRIGHT_LOG" <"$HARNESS_RESULT" >&2
  exit 1
fi
tee -a "$PLAYWRIGHT_LOG" <"$HARNESS_RESULT"
if ! "$PYTHON_BIN" - "$HARNESS_RESULT" <<'PY'
import json
import pathlib
import sys

response = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
if response.get("isError") is True:
    raise SystemExit("Playwright CLI reported an error")
result = response.get("result")
if isinstance(result, str):
    result = json.loads(result)
if not isinstance(result, dict) or result.get("gate") != "pass":
    raise SystemExit("Playwright gate marker missing")
PY
then
  printf '%s\n' 'dashboard_ui_playwright_result_invalid' >&2
  exit 1
fi

printf '%s\n' 'dashboard_ui_no_permission_zero_requests_ok'
printf '%s\n' 'dashboard_ui_snapshot_anchor_ok'
printf '%s\n' 'dashboard_ui_anchor_failure_isolation_ok'
printf '%s\n' 'dashboard_ui_stale_response_guard_ok'
printf '%s\n' 'dashboard_ui_bigint_privacy_unattributed_backlog_ok'
printf '%s\n' 'dashboard_ui_responsive_a11y_ok'
printf '%s\n' 'dashboard_ui_playwright_gate_ok'
