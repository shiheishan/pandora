'use strict';

const fs = require('fs');
const http = require('http');
const path = require('path');
const { chromium } = require('playwright');

const root = path.resolve(__dirname, '..');
const adminHTML = fs.readFileSync(path.join(root, 'web', 'admin', 'index.html'));
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
      'Content-Length': String(adminHTML.length),
      'Cache-Control': 'no-store',
    });
    response.end(adminHTML);
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
  const writes = [];
  let catalogReads = 0;
  page.on('console', message => {
    const value = message.text();
    if (message.type() === 'error' && !value.includes('Failed to load resource') && !value.includes('ERR_FAILED')) {
      errors.push(value);
    }
  });
  page.on('pageerror', error => errors.push(String(error)));

  const activePlan = {id: '20000000-0000-4000-8000-000000000001', name: '标准套餐', status: 'active'};
  const archivedPlan = {id: '20000000-0000-4000-8000-000000000002', name: '历史套餐', status: 'archived'};
  const missingPlan = {id: '20000000-0000-4000-8000-000000000003', name: '不可用套餐', status: 'missing'};
  const announcement = {
    id: '10000000-0000-4000-8000-000000000001',
    title: '维护 <img src=x onerror="window.__announcementXss=1">',
    body: '正文 <script>window.__announcementXss=2</script>\n' + '滚动验收内容。\n'.repeat(160),
    severity: 'warning',
    pinned: true,
    status: 'published',
    version: 7,
    target_plan_ids: [activePlan.id, archivedPlan.id],
    plan_targets: [activePlan, archivedPlan],
    publish_at: '2026-08-02T06:30:00Z',
    expires_at: '2026-08-10T06:30:00Z',
    created_at: '2026-08-01T00:00:00Z',
  };
  const unsafeAudience = {
    ...announcement,
    id: '10000000-0000-4000-8000-000000000002',
    title: '缺失套餐定向公告',
    version: 2,
    status: 'draft',
    target_plan_ids: [missingPlan.id],
    plan_targets: [missingPlan],
  };

  await page.addInitScript(() => localStorage.setItem('aegis_admin_token', 'announcement-gate'));
  await page.route('**/v1/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    const apiPath = url.pathname.slice(url.pathname.indexOf('/v1/'));
    const method = request.method();
    if (method !== 'GET') {
      writes.push({
        method,
        path: apiPath,
        key: request.headers()['idempotency-key'] || '',
        body: request.postDataJSON(),
      });
    }
    if (method === 'GET' && apiPath === '/v1/me') {
      return json(route, {user_id: 'announcement-admin', permissions: ['ops.announcement.write']});
    }
    if (method === 'GET' && apiPath === '/v1/announcements') {
      return json(route, {announcements: [announcement, unsafeAudience], plans: [activePlan, archivedPlan]});
    }
    if (method === 'GET' && apiPath === '/v1/plans') {
      catalogReads += 1;
      return json(route, {error: {message: 'catalog endpoint must not be used'}}, 500);
    }
    if (method === 'POST' && apiPath === '/v1/announcements') {
      return json(route, {id: 'new', status: 'scheduled', version: 1});
    }
    if (method === 'POST' && apiPath === `/v1/announcements/${announcement.id}`) {
      return json(route, {id: announcement.id, status: 'draft', version: 8});
    }
    if (method === 'POST' && apiPath === `/v1/announcements/${announcement.id}/withdraw`) {
      return json(route, {ok: true, version: 8});
    }
    if (method === 'GET' && apiPath === '/v1/events') return route.abort();
    return json(route, {error: {message: `unexpected ${method} ${apiPath}`}}, 404);
  });

  try {
    for (const width of [390, 820, 1440, 3840]) {
      await page.setViewportSize({width, height: width < 1000 ? 900 : 1200});
      await page.goto(base + '/admin-hidden/', {waitUntil: 'domcontentloaded'});
      await page.waitForFunction(() => !document.getElementById('appView').classList.contains('hide'));
      await page.evaluate(() => go('announce'));
      await page.locator('#annNew').waitFor({state: 'visible'});
      const geometry = await page.evaluate(() => ({
        scroll: document.documentElement.scrollWidth,
        client: document.documentElement.clientWidth,
      }));
      assert(geometry.scroll <= geometry.client + 1, `announcement page overflow at ${width}`);
      await page.locator(`[data-edit="${announcement.id}"]`).click();
      await page.locator('#anPub').waitFor({state: 'visible'});
      const modal = await page.locator('.modal').evaluate(element => {
        const rect = element.getBoundingClientRect();
        const body = element.querySelector('.modal-body');
        body.scrollTop = body.scrollHeight;
        return {
          left: rect.left,
          right: rect.right,
          top: rect.top,
          bottom: rect.bottom,
          viewportWidth: innerWidth,
          viewportHeight: innerHeight,
          scrollTop: body.scrollTop,
          scrollHeight: body.scrollHeight,
          clientHeight: body.clientHeight,
        };
      });
      assert(modal.left >= -1 && modal.right <= modal.viewportWidth + 1 && modal.top >= -1,
        `announcement modal horizontal bounds at ${width}`);
      assert(modal.bottom <= modal.viewportHeight + 1, `announcement modal vertical bounds at ${width}`);
      assert(modal.scrollHeight <= modal.clientHeight + 1 || modal.scrollTop > 0,
        `announcement modal overflow was not scrollable at ${width}`);
      await page.keyboard.press('Escape');
    }

    assert(catalogReads === 0, 'announcement page requested the separate catalog endpoint');
    assert(!(await page.evaluate(() => window.__announcementXss)), 'announcement XSS payload executed');
    assert(await page.locator('img[src="x"]').count() === 0, 'announcement title created an image element');

    await page.locator(`[data-edit="${unsafeAudience.id}"]`).click();
    await page.getByText(/存在已删除的定向套餐/).waitFor({state: 'visible'});
    assert(await page.locator('#anDraft').isDisabled(), 'missing target did not disable draft save');
    assert(await page.locator('#anPub').isDisabled(), 'missing target did not disable publish');
    await page.keyboard.press('Escape');

    await page.locator(`[data-edit="${announcement.id}"]`).click();
    await page.locator('#anTitle').fill('维护窗口已调整');
    assert(await page.locator('#anDraft').count() === 0, 'published announcement exposed draft transition');
    await page.locator('#anPub').click();
    await page.waitForFunction(() => !document.querySelector('.modal'));

    await page.locator('#annNew').click();
    await page.locator('#anTitle').fill('计划维护');
    await page.locator('#anBody').fill('预计持续十分钟。');
    await page.locator('#anFrom').fill('2026-08-08T15:30');
    await page.locator(`.anPlan[value="${activePlan.id}"]`).check();
    await page.locator('#anPub').click();
    await page.waitForFunction(() => !document.querySelector('.modal'));

    page.once('dialog', dialog => dialog.accept());
    await page.locator(`[data-wd="${announcement.id}"]`).click();
    await page.waitForFunction(() => document.querySelectorAll('.spin').length === 0);

    const edit = writes.find(write => write.path === `/v1/announcements/${announcement.id}`);
    const create = writes.find(write => write.path === '/v1/announcements');
    const withdraw = writes.find(write => write.path.endsWith('/withdraw'));
    assert(edit && edit.key, 'announcement edit did not carry an idempotency key');
    assert(edit.body.expected_version === 7 && edit.body.publish === true,
      'published announcement edit lost CAS or publication state');
    assert(edit.body.target_plan_ids.length === 2, 'announcement edit silently widened its audience');
    assert(edit.body.publish_at.endsWith('Z') && edit.body.expires_at.endsWith('Z'),
      'announcement edit did not send timezone-qualified timestamps');
    assert(create && create.key, 'announcement create did not carry an idempotency key');
    assert(create.body.expected_version === 0, 'announcement create sent a non-zero CAS version');
    assert(create.body.publish_at.endsWith('Z'), 'scheduled announcement did not send RFC3339 time');
    assert(create.body.target_plan_ids.length === 1, 'announcement create lost its target plan');
    assert(withdraw && withdraw.key, 'announcement withdraw did not carry an idempotency key');
    assert(withdraw.body.expected_version === 7, 'announcement withdraw lost CAS version');
    assert(errors.length === 0, `browser errors: ${errors.join(' | ')}`);

    process.stdout.write(JSON.stringify({
      gate: 'pass',
      engine: 'real-playwright-system-chrome',
      responsive: [390, 820, 1440, 3840],
      modal_responsive: true,
      catalog_reads: catalogReads,
      writes: writes.length,
      audience_fail_closed: true,
      timezone_rfc3339: true,
      xss: 'blocked',
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
