'use strict';

const fs = require('fs');
const http = require('http');
const path = require('path');
const { chromium } = require('playwright');

const root = path.resolve(__dirname, '..');
const html = fs.readFileSync(path.join(root, 'web', 'admin', 'index.html'));
const chrome = process.env.PANDORA_CHROME ||
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function json(route, body, status = 200) {
  return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
}

async function main() {
  const server = http.createServer((_request, response) => {
    response.writeHead(200, {
      'Content-Type': 'text/html; charset=utf-8',
      'Content-Length': String(html.length),
      'Cache-Control': 'no-store',
    });
    response.end(html);
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });

  const address = server.address();
  const base = `http://127.0.0.1:${address.port}/coupon-gate/`;
  const browser = await chromium.launch({ headless: true, executablePath: chrome });
  const context = await browser.newContext();
  const page = await context.newPage();
  let permissions = ['marketing.coupon.write'];
  let planReads = 0;
  let redemptionReads = 0;
  const writes = [];
  const unexpected = [];
  const consoleErrors = [];
  const pageErrors = [];

  page.on('console', message => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('pageerror', error => pageErrors.push(String(error)));
  await page.addInitScript(() => localStorage.setItem('aegis_admin_token', 'coupon-gate-token'));
  await page.route('**/v1/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    const apiPath = url.pathname.slice(url.pathname.indexOf('/v1/'));
    const method = request.method();
    if (method !== 'GET') writes.push(`${method} ${apiPath}`);
    if (method === 'GET' && apiPath === '/v1/me') {
      return json(route, { user_id: 'coupon-admin', permissions });
    }
    if (method === 'GET' && apiPath === '/v1/coupons') {
      return json(route, { coupons: [{
        id: 'coupon-1', code: 'SAFE20', name: '安全券', discount_type: 'percent',
        discount_value: 2000, currency: 'CNY', max_discount: 2000,
        min_order_amount: 5000, max_redemptions: 100, max_redemptions_per_user: 1,
        redeemed_count: 3, discounted_total: 2500, applicable_plan_ids: ['plan-1'],
        valid_until: '2026-12-31T00:00:00Z', status: 'active',
      }] });
    }
    if (method === 'GET' && apiPath === '/v1/orders') {
      return json(route, { orders: [], total: 0 });
    }
    if (method === 'GET' && apiPath === '/v1/plans') {
      planReads += 1;
      return json(route, { plans: [{ id: 'plan-1', name: '标准套餐', status: 'active' }] });
    }
    if (method === 'GET' && apiPath === '/v1/coupons/coupon-1/redemptions') {
      redemptionReads += 1;
      return json(route, { redemptions: [] });
    }
    unexpected.push(`${method} ${apiPath}`);
    return json(route, { error: { code: 'unexpected', message: `${method} ${apiPath}` } }, 404);
  });

  try {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(base, { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => !document.getElementById('appView').classList.contains('hide'));
    await page.evaluate(() => go('coupons'));
    await page.locator('#cpNew').waitFor({ state: 'visible' });
    assert(planReads === 0, 'coupon-only role requested catalog plans');
    assert(await page.locator('[data-redeem]').count() === 0,
      'coupon-only role saw order redemption PII action');
    assert(await page.locator('[data-off="coupon-1"]').count() === 1,
      'coupon writer lost status action');
    await page.locator('#cpNew').click();
    await page.getByText('还没有在售套餐').waitFor({ state: 'visible' });
    const mobile = await page.evaluate(() => ({
      width: document.documentElement.scrollWidth,
      viewport: innerWidth,
      modalRight: document.querySelector('.modal').getBoundingClientRect().right,
    }));
    assert(mobile.width <= mobile.viewport + 1 && mobile.modalRight <= mobile.viewport + 1,
      'coupon form overflowed mobile viewport');
    await page.keyboard.press('Escape');

    permissions = ['marketing.coupon.write', 'billing.order.read', 'catalog.read'];
    await page.setViewportSize({ width: 3840, height: 2160 });
    await page.goto(base, { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => !document.getElementById('appView').classList.contains('hide'));
    await page.evaluate(() => go('coupons'));
    await page.locator('[data-redeem="coupon-1"]').waitFor({ state: 'visible' });
    assert(planReads === 1, `full coupon role plan reads=${planReads}, want 1`);
    await page.locator('[data-redeem="coupon-1"]').click();
    await page.getByText('还没有人用过这张券').waitFor({ state: 'visible' });
    assert(redemptionReads === 1, 'redemption detail did not perform exactly one protected read');
    assert(writes.length === 0, `read-only coupon gate emitted writes: ${writes.join(', ')}`);
    assert(unexpected.length === 0, `unexpected API requests: ${unexpected.join(' | ')}`);
    assert(consoleErrors.length === 0, `console errors: ${consoleErrors.join(' | ')}`);
    assert(pageErrors.length === 0, `page errors: ${pageErrors.join(' | ')}`);
    process.stdout.write(JSON.stringify({
      gate: 'pass', engine: 'real-playwright-system-chrome',
      coupon_only_plan_reads: 0, full_role_plan_reads: planReads,
      redemption_reads: redemptionReads, responsive: [390, 3840], writes: writes.length,
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
