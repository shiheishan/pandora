'use strict';

const fs = require('fs');
const http = require('http');
const path = require('path');
const { chromium } = require('playwright');

const root = path.resolve(__dirname, '..');
const portalHTML = fs.readFileSync(path.join(root, 'web', 'portal', 'index.html'));
const chrome = process.env.PANDORA_CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const assert = (ok, message) => { if (!ok) throw new Error(message); };
const json = (route, body, status = 200) => route.fulfill({
  status,
  contentType: 'application/json',
  body: JSON.stringify(body),
});

async function main() {
  const server = http.createServer((_request, response) => {
    response.writeHead(200, {
      'Content-Type': 'text/html; charset=utf-8',
      'Content-Length': String(portalHTML.length),
      'Cache-Control': 'no-store',
    });
    response.end(portalHTML);
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });

  const base = `http://127.0.0.1:${server.address().port}`;
  const browser = await chromium.launch({headless: true, executablePath: chrome});
  const context = await browser.newContext();
  const page = await context.newPage();
  const errors = [];
  let nodeReads = 0;
  let mode = 'nodes';
  let failNext = false;
  const subID = '10000000-0000-4000-8000-000000000001';
  const subscription = {
    id: subID,
    plan_id: '20000000-0000-4000-8000-000000000001',
    price_id: '30000000-0000-4000-8000-000000000001',
    plan_name: '标准套餐',
    plan_version: 3,
    status: 'active',
    current_period_start: '2026-08-01T00:00:00Z',
    current_period_end: '2026-09-01T00:00:00Z',
    currency: 'CNY',
    amount: 1200,
    quotas: [],
  };
	const inactiveSubscription = {
	  ...subscription,
	  id: '10000000-0000-4000-8000-000000000002',
	  plan_name: '已停用套餐',
	  status: 'canceled',
	};
  const safeNodes = [
    {
      name: '香港 01 <img src=x onerror="window.__nodeXss=1">',
	  protocol: 'vless',
      traffic_rate: 1.5,
      host: 'secret.example.com',
      port: 443,
      protocol_config: {password: 'must-not-render'},
      internal_id: 'must-not-render-id',
    },
    {name: '新加坡 01', protocol: 'shadowsocks', traffic_rate: 1},
  ];

  page.on('console', message => {
    const value = message.text();
    if (message.type() === 'error' && !value.includes('Failed to load resource') && !value.includes('ERR_FAILED')) {
      errors.push(value);
    }
  });
  page.on('pageerror', error => errors.push(String(error)));
  await page.addInitScript(() => localStorage.setItem('aegis_token', 'node-preview-gate'));
  await page.route('**/v1/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    const apiPath = url.pathname;
    if (request.method() === 'GET' && apiPath === '/v1/me') {
      return json(route, {user_id: 'user-1', email: 'user@example.com', status: 'active'});
    }
    if (request.method() === 'GET' && apiPath === '/v1/me/subscriptions') {
	  return json(route, {subscriptions: [subscription, inactiveSubscription]});
    }
    if (request.method() === 'GET' && apiPath === '/v1/me/subscription-links') {
      return json(route, {links: []});
    }
    if (request.method() === 'GET' && apiPath === '/v1/plans') return json(route, {plans: []});
    if (request.method() === 'GET' && apiPath === '/v1/me/announcements') {
      return json(route, {announcements: []});
    }
    if (request.method() === 'GET' && apiPath === '/v1/me/notifications') {
      return json(route, {notifications: [], unread: 0});
    }
    if (request.method() === 'GET' && apiPath === `/v1/me/subscriptions/${subID}/nodes`) {
      nodeReads += 1;
      if (failNext) {
        failNext = false;
        return json(route, {error: {message: 'temporary failure'}}, 503);
      }
      return json(route, {count: mode === 'empty' ? 0 : safeNodes.length, nodes: mode === 'empty' ? [] : safeNodes});
    }
    if (request.method() === 'GET' && apiPath === '/v1/events') return route.abort();
    return json(route, {error: {message: `unexpected ${request.method()} ${apiPath}`}}, 404);
  });

  async function openSubscriptions(width) {
    await page.setViewportSize({width, height: width < 1000 ? 900 : 1200});
    await page.goto(base + '/portal/', {waitUntil: 'domcontentloaded'});
    await page.waitForFunction(() => !document.getElementById('appView').classList.contains('hide'));
    await page.evaluate(() => go('subs'));
    await page.locator(`[data-sub-nodes-toggle="${subID}"]`).waitFor({state: 'visible'});
	await page.getByText(/当前订阅不可用/).waitFor({state: 'visible'});
	assert(await page.locator(`[data-sub-nodes-toggle="${inactiveSubscription.id}"]`).count() === 0,
	  'inactive subscription exposed a node request button');
  }

  try {
    for (const width of [390, 820, 1440, 3840]) {
      const before = nodeReads;
      await openSubscriptions(width);
      assert(nodeReads === before, `node preview was not lazy at ${width}`);
      await page.locator(`[data-sub-nodes-toggle="${subID}"]`).click();
      await page.getByText('新加坡 01').waitFor({state: 'visible'});
	  const toggle = page.locator(`[data-sub-nodes-toggle="${subID}"]`);
	  assert(await toggle.getAttribute('aria-expanded') === 'true', `aria-expanded drifted at ${width}`);
	  assert(await toggle.getAttribute('aria-controls') === `sn-${subID}`, `aria-controls drifted at ${width}`);
      assert(nodeReads === before + 1, `node preview request count drifted at ${width}`);
      const geometry = await page.evaluate(() => ({
        scroll: document.documentElement.scrollWidth,
        client: document.documentElement.clientWidth,
      }));
      assert(geometry.scroll <= geometry.client + 1, `node preview overflow at ${width}`);
      assert(await page.locator('img[src="x"]').count() === 0, 'node name created an image element');
      const text = await page.locator(`#sn-${subID}`).textContent();
      for (const secret of ['secret.example.com', '443', 'must-not-render', 'must-not-render-id']) {
        assert(!text.includes(secret), `node preview rendered forbidden value ${secret}`);
      }
    }

    mode = 'empty';
    await openSubscriptions(390);
    await page.locator(`[data-sub-nodes-toggle="${subID}"]`).click();
    await page.getByText('当前没有可用节点').waitFor({state: 'visible'});

    mode = 'nodes';
    failNext = true;
    await openSubscriptions(390);
    await page.locator(`[data-sub-nodes-toggle="${subID}"]`).click();
    const retry = page.locator(`[data-sub-nodes-retry="${subID}"]`);
    await retry.waitFor({state: 'visible'});
    await retry.click();
    await page.getByText('新加坡 01').waitFor({state: 'visible'});

    assert(!(await page.evaluate(() => window.__nodeXss)), 'node preview XSS payload executed');
    assert(errors.length === 0, `browser errors: ${errors.join(' | ')}`);
    process.stdout.write(JSON.stringify({
      gate: 'pass',
      engine: 'real-playwright-system-chrome',
      responsive: [390, 820, 1440, 3840],
      lazy: true,
      empty: true,
      retry: true,
      safe_fields_only: true,
      xss: 'blocked',
      node_reads: nodeReads,
    }) + '\n');
  } finally {
    await context.close();
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
}

main().catch(error => {
  console.error(error && error.stack ? error.stack : error);
  process.exitCode = 1;
});
