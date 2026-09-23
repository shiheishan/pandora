'use strict'; // CommonJS: the workspace root is configured as ESM.

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
  const base = `http://127.0.0.1:${address.port}/admin-gate/`;
  const browser = await chromium.launch({ headless: true, executablePath: chrome });
  const context = await browser.newContext();
  const page = await context.newPage();
  const consoleErrors = [];
  const pageErrors = [];
  const writes = [];
  const orderWrites = [];
  const allWrites = [];
  let permissions = [
    'catalog.read', 'catalog.write', 'catalog.publish',
    'billing.order.read', 'billing.order.write', 'billing.payment.read',
  ];
  let paymentHistoryReads = 0;

  const plan = {
    id: 'plan-1', code: 'standard_100g', name: '标准 100G', description: '测试套餐',
    status: 'draft', row_version: 3, visibility: 'public', visible_group_ids: [],
    allow_new_purchase: true, allow_renewal: true, allow_upgrade: true, sort_order: 10,
    purchase_limit_per_user: null, stock_total: null, visible_from: null, visible_until: null,
    traffic_limit: 107374182400, max_devices: 5, active_subscriptions: 2, node_count: 3,
    version: 1,
    versions: [{
      id: 'version-1', version: 1, status: 'draft', row_version: 4,
      quota_reset_strategy: 'billing_cycle', quota_reset_day: null,
      grace_period_hours: 24, grace_keeps_service: true,
      renewal_extends_period: true, renewal_resets_quota: true,
      renewal_keeps_addons: true, max_devices: 5, max_concurrent: 3,
      device_release_hours: 12, overage_policy: 'suspend', throttle_kbps: null,
      entitlements: [], quotas: [], notes: null,
    }],
    prices: [{
      id: 'price-1', currency: 'CNY', unit_amount: 990,
      billing_interval: 'month', interval_count: 1, status: 'active', row_version: 2,
    }],
  };
  const unpaidOrder = {
    id: 'order-2', order_no: 'P202608020002', user_email: 'pending@example.com',
    user_id: 'user-2', kind: 'renewal', status: 'pending_payment', state_version: 7,
    currency: 'CNY', subtotal_amount: 1990, discount_amount: 0, tax_amount: 0,
    balance_applied: 500, total_amount: 1990, payable_amount: 1490,
    paid_amount: 0, refunded_amount: 0, created_at: '2026-08-02T03:00:00Z',
    updated_at: '2026-08-02T03:00:00Z', items: [{ id: 'item-2',
      product_name: '潘多拉续费套餐', plan_name: '标准 100G', plan_version: 1,
      quantity: 1, unit_amount: 1990, line_amount: 1990, currency: 'CNY' }],
  };

  page.on('console', message => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('pageerror', error => pageErrors.push(String(error)));
  await page.addInitScript(() => localStorage.setItem('aegis_admin_token', 'browser-gate-token'));
  await page.route('**/v1/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    const apiPath = url.pathname.slice(url.pathname.indexOf('/v1/'));
    const method = request.method();
    const key = request.headers()['idempotency-key'] || '';
    let body = {};
    if (request.postData()) body = JSON.parse(request.postData());
    if (method !== 'GET') {
      allWrites.push({ method, apiPath, key, body: structuredClone(body) });
    }

    if (method === 'GET' && apiPath === '/v1/me') {
      return json(route, { user_id: 'admin-1', permissions });
    }
    if (method === 'GET' && apiPath === '/v1/orders') {
      return json(route, { orders: [{
        id: 'order-1', order_no: 'P202608020001', user_email: 'buyer@example.com',
        kind: 'new', status: 'fulfilled', currency: 'CNY', total_amount: 990,
        payable_amount: 990, paid_amount: 990, refunded_amount: 200,
        created_at: '2026-08-02T01:00:00Z', paid_at: '2026-08-02T01:01:00Z',
      }, unpaidOrder], total: 2 });
    }
    if (method === 'GET' && apiPath === '/v1/orders/order-1') {
      return json(route, { order: {
        id: 'order-1', order_no: 'P202608020001', user_id: 'user-1',
        user_email: 'buyer@example.com', kind: 'new', status: 'fulfilled', state_version: 4,
        currency: 'CNY', subtotal_amount: 1190, discount_amount: 200, tax_amount: 0,
        balance_applied: 0, total_amount: 990, payable_amount: 990, paid_amount: 990,
        refunded_amount: 200, created_at: '2026-08-02T01:00:00Z',
        paid_at: '2026-08-02T01:01:00Z', fulfilled_at: '2026-08-02T01:02:00Z',
        subscription_id: 'subscription-1', updated_at: '2026-08-02T02:00:00Z', items: [{ id: 'item-1',
          product_name: '潘多拉标准套餐', plan_name: '标准 100G', plan_version: 1,
          quantity: 1, unit_amount: 990, line_amount: 990, currency: 'CNY' }],
      } });
    }
    if (method === 'GET' && apiPath === '/v1/orders/order-2') {
      return json(route, { order: unpaidOrder });
    }
    if (method === 'GET' && apiPath === '/v1/orders/order-1/payments') {
      paymentHistoryReads += 1;
      return json(route, {
        payment_intents: [{ id: 'intent-1', provider_code: 'epay', provider_name: '易支付',
          currency: 'CNY', amount: 990, status: 'succeeded', provider_ref: 'intent-ref',
          created_at: '2026-08-02T01:00:10Z' }],
        payments: [{ id: 'payment-1', provider_code: 'epay', provider_name: '易支付',
          provider_payment_id: 'provider-payment-1', currency: 'CNY', amount: 990,
          method: 'alipay', fee_amount: 10, refunded_amount: 200, status: 'partially_refunded',
          paid_at: '2026-08-02T01:01:00Z' }],
        refunds: [{ id: 'refund-1', currency: 'CNY', amount: 200, reason: '服务补偿',
          status: 'succeeded', entitlement_revoked: true, commission_reversed: true,
          created_at: '2026-08-02T02:00:00Z' }],
      });
    }
    if (method === 'GET' && apiPath === '/v1/orders/order-2/payments') {
      paymentHistoryReads += 1;
      return json(route, { payment_intents: [], payments: [], refunds: [] });
    }
    if (method === 'GET' && apiPath === '/v1/plans') {
      return json(route, { plans: [plan] });
    }
    if (method === 'GET' && apiPath === '/v1/plans/plan-1') {
      return json(route, { plan });
    }
    if (method === 'GET' && apiPath === '/v1/user-groups') {
      return json(route, { groups: [{ id: 'group-1', name: '企业用户' }] });
    }
    if (method === 'GET' && apiPath === '/v1/plans/plan-1/pools') {
      return json(route, {
        editable: plan.versions[0].status === 'draft', version_id: 'version-1',
        row_version: plan.versions[0].row_version,
        pools: [{ id: 'pool-1', name: '香港节点池', active_nodes: 3, bound: true }],
      });
    }
    const capture = (expectedMethod, expectedPath, validate, mutate, response, status = 200) => {
      if (method !== expectedMethod || apiPath !== expectedPath) return false;
      validate();
      writes.push({ method, apiPath, key, body: structuredClone(body) });
      mutate();
      json(route, response, status);
      return true;
    };
    if (method === 'POST' && apiPath === '/v1/orders/order-2/cancel') {
      assert(key.length >= 8, 'admin cancel omitted idempotency key');
      assert(body.expected_state_version === 7, 'admin cancel omitted order CAS');
      assert(body.reason === '客户确认取消未支付订单', 'admin cancel reason mismatch');
      orderWrites.push({ method, apiPath, key, body: structuredClone(body) });
      unpaidOrder.status = 'cancelled'; unpaidOrder.state_version = 8;
      unpaidOrder.cancel_reason = body.reason; unpaidOrder.cancelled_at = '2026-08-02T04:00:00Z';
      return json(route, { order: { order_id: 'order-2', status: 'cancelled', state_version: 8,
        cancelled_at: unpaidOrder.cancelled_at, cancel_reason: body.reason, already_terminal: false },
        already_terminal: false });
    }
    if (capture('POST', '/v1/plans', () => {
      assert(key.length >= 8, 'plan create omitted idempotency key');
      assert(body.code === 'business_300g' && body.name === '企业 300G', 'plan create body mismatch');
      assert(body.visibility === 'public' && Array.isArray(body.visible_group_ids), 'plan visibility body mismatch');
    }, () => {}, { plan: { id: 'plan-2', row_version: 1, status: 'draft' } }, 201)) return;
    if (capture('PUT', '/v1/plans/plan-1/versions/version-1', () => {
      assert(!key, 'version update unexpectedly used idempotency middleware');
      assert(body.expected_row_version === 4 && body.max_devices === 8, 'version update CAS/body mismatch');
      assert(Array.isArray(body.entitlements) && Array.isArray(body.quotas), 'version snapshot arrays missing');
    }, () => { plan.versions[0].row_version = 5; plan.versions[0].max_devices = 8; },
    { ok: true, row_version: 5 })) return;
    if (capture('POST', '/v1/plans/plan-1/prices', () => {
      assert(key.length >= 8, 'price create omitted idempotency key');
      assert(body.currency === 'USD' && body.unit_amount === 1250, 'USD minor-unit body mismatch');
      assert(body.billing_interval === 'month' && body.interval_count === 1, 'price interval mismatch');
    }, () => { plan.prices.push({ id: 'price-2', currency: 'USD', unit_amount: 1250,
      billing_interval: 'month', interval_count: 1, status: 'active', row_version: 1 }); },
    { price: { id: 'price-2', row_version: 1, status: 'active' } }, 201)) return;
    if (capture('POST', '/v1/plans/plan-1/pools', () => {
      assert(key.length >= 8, 'pool binding omitted idempotency key');
      assert(body.version_id === 'version-1', 'pool binding omitted draft version');
      assert(body.expected_version_row_version === 5, 'pool binding stale CAS body');
      assert(JSON.stringify(body.pool_ids) === '["pool-1"]', 'pool binding lost pool IDs');
    }, () => { plan.versions[0].row_version = 6; },
    { bound: 1, version_id: 'version-1', row_version: 6 })) return;
    if (capture('POST', '/v1/plans/plan-1/versions/version-1/publish', () => {
      assert(key.length >= 8, 'publish omitted idempotency key');
      assert(body.expected_plan_row_version === 3 && body.expected_version_row_version === 6,
        'publish plan/version CAS mismatch');
    }, () => {
      plan.row_version = 4; plan.status = 'active';
      plan.versions[0].status = 'published'; plan.versions[0].row_version = 7;
    }, { ok: true, plan_row_version: 4, version_row_version: 7 })) return;
    if (capture('POST', '/v1/plans/plan-1/prices/price-1/archive', () => {
      assert(key.length >= 8 && body.expected_row_version === 2, 'price archive CAS/idempotency mismatch');
    }, () => { plan.prices[0].status = 'archived'; plan.prices[0].row_version = 3; },
    { ok: true, row_version: 3 })) return;
    if (capture('POST', '/v1/plans/plan-1/archive', () => {
      assert(key.length >= 8 && body.expected_row_version === 4, 'plan archive CAS/idempotency mismatch');
    }, () => { plan.status = 'archived'; plan.row_version = 5; },
    { ok: true, row_version: 5 })) return;
    if (method !== 'GET') {
      return json(route, { error: { code: 'unexpected_write', message: `${method} ${apiPath}` } }, 422);
    }
    return json(route, { error: { code: 'unexpected_read', message: apiPath } }, 404);
  });

  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base, { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => !document.getElementById('appView').classList.contains('hide'));
    await page.evaluate(() => go('plans'));
    try {
      await page.locator('#planCreate').waitFor({ state: 'visible', timeout: 5000 });
    } catch (error) {
      const diagnostic = await page.locator('body').innerText().catch(() => 'body unavailable');
      throw new Error(`catalog landing failed; pageErrors=${pageErrors.join(' | ')}; ` +
        `consoleErrors=${consoleErrors.join(' | ')}; body=${diagnostic.slice(0, 1200)}`, { cause: error });
    }

    await page.locator('#planCreate').click();
    await page.locator('#planCode').fill('business_300g');
    await page.locator('#planName').fill('企业 300G');
    await page.locator('#planVisibility').selectOption('public');
    await page.locator('#planSave').click();
    await page.waitForFunction(() => !document.getElementById('planSave'));

    await page.locator('[data-plan-manage="plan-1"]').click();
    await page.locator('#planEdit').waitFor({ state: 'visible' });
    assert((await page.locator('.modal').innerText()).includes('¥9.90'), 'CNY 990 was not rendered as ¥9.90');
    await page.locator('[data-version-edit="version-1"]').click();
    await page.locator('#pvDevices').fill('8');
    await page.locator('#pvSave').click();
    await page.waitForFunction(() => document.querySelector('[data-version-publish="version-1"]'));

    await page.locator('#priceCreate').click();
    await page.locator('#ppCurrency').selectOption('USD');
    const writesBeforeInvalidPrice = writes.length;
    await page.locator('#ppAmount').fill('12.345');
    await page.locator('#ppSave').click();
    await page.locator('.toast-err').filter({ hasText: '有效金额' }).waitFor({ state: 'visible' });
    assert(writes.length === writesBeforeInvalidPrice, 'invalid three-decimal price emitted a request');
    await page.locator('#ppAmount').fill('12.50');
    await page.locator('#ppSave').click();
    await page.waitForFunction(() => document.querySelector('[data-version-publish="version-1"]'));
    await page.keyboard.press('Escape');

    await page.locator('[data-pools="plan-1"]').click();
    await page.locator('#ppSave').waitFor({ state: 'visible' });
    await page.waitForFunction(() => !document.getElementById('ppSave').disabled);
    assert(!(await page.locator('#ppSave').isDisabled()), 'pool save stayed disabled for editable draft');
    await page.locator('#ppSave').click();
    await page.waitForFunction(() => !document.getElementById('ppSave'));

    await page.locator('[data-plan-manage="plan-1"]').click();
    await page.locator('[data-version-publish="version-1"]').click();
    await page.waitForFunction(() => document.querySelector('[data-price-archive="price-1"]'));
    await page.locator('[data-price-archive="price-1"]').click();
    await page.waitForFunction(() => document.querySelector('#planArchive'));
    page.once('dialog', dialog => dialog.accept());
    await page.locator('#planArchive').click();
    await page.waitForFunction(() => !document.getElementById('planArchive'));

    const expected = [
      'POST /v1/plans',
      'PUT /v1/plans/plan-1/versions/version-1',
      'POST /v1/plans/plan-1/prices',
      'POST /v1/plans/plan-1/pools',
      'POST /v1/plans/plan-1/versions/version-1/publish',
      'POST /v1/plans/plan-1/prices/price-1/archive',
      'POST /v1/plans/plan-1/archive',
    ];
    assert(writes.length === expected.length, `lifecycle write count=${writes.length}, want ${expected.length}`);
    assert(JSON.stringify(writes.map(item => `${item.method} ${item.apiPath}`)) === JSON.stringify(expected),
      'lifecycle write order or endpoint mismatch');
    assert(allWrites.length === expected.length, `all write count=${allWrites.length}, want ${expected.length}`);
    assert(JSON.stringify(allWrites.map(item => `${item.method} ${item.apiPath}`)) === JSON.stringify(expected),
      'unexpected write escaped lifecycle capture');
    const poolWrite = writes.find(item => item.apiPath === '/v1/plans/plan-1/pools');
    assert(poolWrite.key, 'pool binding omitted idempotency key');
    assert(poolWrite.body.version_id === 'version-1', 'pool binding omitted draft version');
    assert(poolWrite.body.expected_version_row_version === 5, 'pool binding omitted CAS version');
    assert(JSON.stringify(poolWrite.body.pool_ids) === '["pool-1"]', 'pool binding lost pool ids');
    const priceWrite = writes.find(item => item.apiPath === '/v1/plans/plan-1/prices');
    assert(priceWrite.body.currency === 'USD' && priceWrite.body.unit_amount === 1250,
      'USD price did not round-trip as minor units');

    await page.evaluate(() => go('orders'));
    await page.locator('[data-order-detail="order-1"]').waitFor({ state: 'visible' });
    await page.locator('[data-order-detail="order-1"]').click();
    const detailText = await page.locator('.modal').innerText();
    for (const marker of ['订单详情', '订单生命周期', '商品快照', '支付尝试', '实收记录', '退款证据',
      'subscription-1', 'alipay', '潘多拉标准套餐', 'provider-payment-1', '权益已回收', '佣金已冲销']) {
      assert(detailText.includes(marker), `order evidence modal missing ${marker}`);
    }
    assert(paymentHistoryReads === 1, 'payment reader did not load its protected history exactly once');
    await page.keyboard.press('Escape');

    await page.locator('[data-order-detail="order-2"]').click();
    await page.locator('#orderCancel').waitFor({ state: 'visible' });
    const dialogs = [];
    page.on('dialog', async dialog => {
      dialogs.push(dialog.type());
      if (dialog.type() === 'prompt') await dialog.accept('客户确认取消未支付订单');
      else await dialog.accept();
    });
    await page.locator('#orderCancel').click();
    await page.waitForFunction(() => !document.getElementById('orderCancel'));
    assert(JSON.stringify(dialogs) === '["prompt","confirm"]', 'admin cancel confirmation sequence mismatch');
    assert(orderWrites.length === 1 && unpaidOrder.status === 'cancelled' && unpaidOrder.state_version === 8,
      'admin cancel did not perform one CAS transition');
    assert(allWrites.length === expected.length + 1, 'admin cancel emitted an extra or missing write');
    assert(`${allWrites.at(-1).method} ${allWrites.at(-1).apiPath}` ===
      'POST /v1/orders/order-2/cancel', 'admin cancel was not the final expected write');

    permissions = ['billing.order.read'];
    await page.goto(base, { waitUntil: 'domcontentloaded' });
    await page.locator('[data-order-detail="order-1"]').waitFor({ state: 'visible' });
    await page.locator('[data-order-detail="order-1"]').click();
    const limitedText = await page.locator('.modal').innerText();
    assert(limitedText.includes('渠道支付号和退款证据已隐藏'), 'order-only role did not receive the redacted view');
    assert(!limitedText.includes('provider-payment-1'), 'order-only role saw protected payment evidence');
    assert(paymentHistoryReads === 2, 'order-only role requested protected payment history');
    await page.keyboard.press('Escape');
    permissions = [
      'catalog.read', 'catalog.write', 'catalog.publish',
      'billing.order.read', 'billing.order.write', 'billing.payment.read',
    ];

    plan.status = 'draft'; plan.row_version = 3;
    plan.versions[0].status = 'draft'; plan.versions[0].row_version = 4;
    plan.prices = [Object.assign(plan.prices[0], { status: 'active', row_version: 2 })];
    const viewports = [
      { width: 390, height: 844, columns: '1' },
      { width: 820, height: 1180, columns: '2' },
      { width: 1440, height: 900, columns: '2' },
      { width: 3840, height: 2160, columns: '2' },
    ];
    const responsive = [];
    const measureModal = async (surface, expectedColumns = null) => {
      await page.waitForFunction(() => {
        const modal = document.querySelector('.modal');
        if (!modal) return false;
        return [...modal.querySelectorAll('.tbl-wrap')]
          .every(wrap => wrap.dataset.scrollEnhanced === 'true');
      });
      const measurement = await page.evaluate(() => {
        const modal = document.querySelector('.modal');
        const body = modal.querySelector('.modal-body');
        const grid = modal.querySelector('.grid2');
        const rect = modal.getBoundingClientRect();
        const foot = modal.querySelector('.modal-foot').getBoundingClientRect();
        const action = modal.querySelector('.modal-foot button:not([disabled])');
        const actionRect = action?.getBoundingClientRect();
        const scrollable = body.scrollHeight > body.clientHeight + 1;
        body.scrollTop = body.scrollHeight;
        const hit = actionRect
          ? document.elementFromPoint(actionRect.left + actionRect.width / 2,
            actionRect.top + actionRect.height / 2)
          : null;
        return {
          columns: grid ? getComputedStyle(grid).gridTemplateColumns.trim().split(/\s+/).length : null,
          width: rect.width, left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom,
          footLeft: foot.left, footRight: foot.right, footBottom: foot.bottom,
          footerHit: Boolean(action && hit && (hit === action || action.contains(hit))),
          scrollable, scrollTop: body.scrollTop,
          viewportWidth: innerWidth, viewportHeight: innerHeight,
          documentWidth: document.documentElement.scrollWidth,
          enhancedTables: modal.querySelectorAll('.tbl-wrap[data-scroll-enhanced="true"]').length,
        };
      });
      if (expectedColumns !== null) {
        assert(String(measurement.columns) === expectedColumns,
          `${surface}: unexpected field columns at ${measurement.viewportWidth}px`);
      }
      assert(measurement.left >= -1 && measurement.top >= -1, `${surface}: modal clipped top/left`);
      assert(measurement.width <= measurement.viewportWidth + 1, `${surface}: modal too wide`);
      assert(measurement.right <= measurement.viewportWidth + 1, `${surface}: modal clipped right`);
      assert(measurement.bottom <= measurement.viewportHeight + 1, `${surface}: modal clipped bottom`);
      assert(measurement.footLeft >= -1 && measurement.footRight <= measurement.viewportWidth + 1,
        `${surface}: modal footer clipped horizontally`);
      assert(measurement.footBottom <= measurement.viewportHeight + 1, `${surface}: modal footer unreachable`);
      assert(measurement.footerHit, `${surface}: modal footer is not hit-testable`);
      if (measurement.scrollable) {
        assert(measurement.scrollTop > 0, `${surface}: modal body did not actually scroll`);
      }
      assert(measurement.documentWidth <= measurement.viewportWidth + 1, `${surface}: document overflow`);
      responsive.push({ surface, measurement });
    };
    for (const viewport of viewports) {
      await page.setViewportSize(viewport);
      await page.goto(base, { waitUntil: 'domcontentloaded' });
      await page.waitForFunction(() => !document.getElementById('appView').classList.contains('hide'));
      await page.evaluate(() => go('plans'));
      await page.locator('#planCreate').waitFor({ state: 'visible' });
      await page.waitForFunction(() => {
        const wrap = document.querySelector('.tbl-wrap');
        return !wrap || wrap.dataset.scrollEnhanced === 'true';
      });
      const listLayout = await page.evaluate(() => ({
        documentWidth: document.documentElement.scrollWidth,
        viewportWidth: innerWidth,
        tableScrollable: document.querySelector('.tbl-wrap')?.classList.contains('is-scrollable') || false,
      }));
      assert(listLayout.documentWidth <= listLayout.viewportWidth + 1, `catalog list overflow at ${viewport.width}px`);
      if (viewport.width === 390) assert(listLayout.tableScrollable, 'mobile catalog table lacks scroll affordance');

      await page.locator('#planCreate').click();
      await page.locator('#planForm').waitFor({ state: 'visible' });
      await measureModal(`plan-create-${viewport.width}`, viewport.columns);
      await page.keyboard.press('Escape');

      await page.locator('[data-plan-manage="plan-1"]').click();
      await page.locator('[data-version-edit="version-1"]').waitFor({ state: 'visible' });
      await measureModal(`plan-manager-${viewport.width}`);
      const enhanced = await page.locator('.modal .tbl-wrap[data-scroll-enhanced="true"]').count();
      assert(enhanced >= 2, `plan manager tables were not enhanced at ${viewport.width}px`);
      await page.locator('[data-version-edit="version-1"]').click();
      await page.locator('#planVersionForm').waitFor({ state: 'visible' });
      await measureModal(`version-editor-${viewport.width}`, viewport.columns);
      await page.keyboard.press('Escape');

      await page.locator('[data-plan-manage="plan-1"]').click();
      await page.locator('#priceCreate').click();
      await page.locator('#planPriceForm').waitFor({ state: 'visible' });
      await measureModal(`price-editor-${viewport.width}`, viewport.columns);
      await page.keyboard.press('Escape');

      await page.locator('[data-pools="plan-1"]').click();
      await page.locator('#ppSave').waitFor({ state: 'visible' });
      await page.waitForFunction(() => !document.getElementById('ppSave').disabled);
      await measureModal(`pool-editor-${viewport.width}`);
      await page.keyboard.press('Escape');

      await page.evaluate(() => go('orders'));
      await page.locator('[data-order-detail="order-1"]').waitFor({ state: 'visible' });
      await page.locator('[data-order-detail="order-1"]').click();
      await page.locator('.modal').getByText('订单生命周期').waitFor({ state: 'visible' });
      await measureModal(`order-evidence-${viewport.width}`);
      assert(await page.locator('#orderCancel').count() === 0,
        `fulfilled order exposed cancellation at ${viewport.width}px`);
      const orderTables = await page.locator('.modal .tbl-wrap[data-scroll-enhanced="true"]').count();
      assert(orderTables >= 4, `order evidence tables were not enhanced at ${viewport.width}px`);
      await page.keyboard.press('Escape');
    }

    assert(consoleErrors.length === 0, `console errors: ${consoleErrors.join(' | ')}`);
    assert(pageErrors.length === 0, `page errors: ${pageErrors.join(' | ')}`);
    assert(allWrites.length === 8, `final write count=${allWrites.length}, want 8`);
    process.stdout.write(JSON.stringify({
      gate: 'pass', engine: 'real-playwright-system-chrome',
      lifecycle_writes: writes.length, admin_order_cancel_writes: orderWrites.length,
      order_evidence_role_split: true, responsive,
    }, null, 2) + '\n');
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
