'use strict';

const fs = require('fs');
const http = require('http');
const path = require('path');
const { chromium } = require('playwright');

const root = path.resolve(__dirname, '..');
const adminHTML = fs.readFileSync(path.join(root, 'web', 'admin', 'index.html'));
const portalHTML = fs.readFileSync(path.join(root, 'web', 'portal', 'index.html'));
const chrome = process.env.PANDORA_CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const assert = (ok, message) => { if (!ok) throw new Error(message); };
const json = (route, body, status = 200) => route.fulfill({
  status, contentType: 'application/json', body: JSON.stringify(body),
});

const withdrawTicket = {
  id: '71000000-0000-4000-8000-000000000002',
  ticket_no: 'TK20260802-WITHDRAW', subject: '尚未回复的工单', category: 'technical', priority: 'normal',
  status: 'open', user_id: '72000000-0000-4000-8000-000000000001', user_email: 'user@example.test',
  created_at: '2026-08-02T00:00:00Z', updated_at: '2026-08-02T00:00:00Z', last_reply_at: '2026-08-02T00:00:00Z', message_count: 1,
  messages: [{id: 'mw1', author_kind: 'user', body: '这个问题不需要处理了', created_at: '2026-08-02T00:00:00Z'}],
};

const ticket = {
  id: '71000000-0000-4000-8000-000000000001',
  ticket_no: 'TK20260802-TEST',
  subject: '长主题 <img src=x onerror="window.__supportXss=1"> ' + '响应式验收'.repeat(12),
  category: 'technical', priority: 'high', status: 'pending_agent',
  user_id: '72000000-0000-4000-8000-000000000001', user_email: 'user@example.test',
  created_at: '2026-08-02T00:00:00Z', updated_at: '2026-08-02T00:05:00Z',
  last_reply_at: '2026-08-02T00:05:00Z', message_count: 3,
  sla_first_response_due: '2026-08-02T12:00:00Z', first_responded_at: null,
  messages: [
    {id: 'm1', author_kind: 'user', body: '公开问题 ' + '无空格内容'.repeat(60), created_at: '2026-08-02T00:00:00Z'},
    {id: 'm2', author_kind: 'agent', internal_note: true, body: 'INTERNAL-SECRET-NEVER-PORTAL', created_at: '2026-08-02T00:03:00Z'},
    {id: 'm3', author_kind: 'agent', body: '公开客服回复 <script>window.__supportXss=2</script>', created_at: '2026-08-02T00:05:00Z'},
  ],
};

async function modalGeometry(page) {
  return page.locator('.modal').evaluate(element => {
    const rect = element.getBoundingClientRect();
    const thread = element.querySelector('#tkThread');
    thread.scrollTop = thread.scrollHeight;
    return {
      left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom,
      width: innerWidth, height: innerHeight,
      pageScroll: document.documentElement.scrollWidth,
      pageClient: document.documentElement.clientWidth,
      threadScroll: thread.scrollTop,
      threadHeight: thread.scrollHeight,
      threadClient: thread.clientHeight,
    };
  });
}

function assertGeometry(value, label) {
  assert(value.pageScroll <= value.pageClient + 1, `${label} page overflow`);
  assert(value.left >= -1 && value.right <= value.width + 1, `${label} horizontal modal bounds`);
  assert(value.top >= -1 && value.bottom <= value.height + 1, `${label} vertical modal bounds`);
  assert(value.threadHeight <= value.threadClient + 1 || value.threadScroll > 0,
    `${label} thread overflow is not scrollable`);
}

async function configureAdmin(page, permissions, writes, errors) {
  await page.addInitScript(() => localStorage.setItem('aegis_admin_token', 'support-ui-gate'));
  let replyFailures = 0;
  await page.route('**/v1/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    const apiPath = url.pathname.slice(url.pathname.indexOf('/v1/'));
    const method = request.method();
    if (method !== 'GET') {
      writes.push({
        path: apiPath, method, key: request.headers()['idempotency-key'] || '',
        body: request.postData() ? request.postDataJSON() : null,
      });
    }
    if (method === 'GET' && apiPath === '/v1/me') {
      return json(route, {user_id: '73000000-0000-4000-8000-000000000001', permissions});
    }
    if (method === 'GET' && apiPath === '/v1/tickets') {
      return json(route, {tickets: [ticket], total: 1});
    }
    if (method === 'GET' && apiPath === `/v1/tickets/${ticket.id}`) return json(route, ticket);
    if (method === 'GET' && apiPath === '/v1/events') return route.abort();
    if (method === 'POST' && apiPath === `/v1/tickets/${ticket.id}/reply`) {
      replyFailures += 1;
      if (replyFailures <= 2) return json(route, {error: {message: 'simulated retry'}}, 503);
      return json(route, {ok: true});
    }
    if (method === 'POST' && apiPath.endsWith('/status')) return json(route, {ok: true});
    if (method === 'POST' && apiPath === '/v1/tickets/escalate') return json(route, {escalated: 0});
    return json(route, {error: {message: `unexpected ${method} ${apiPath}`}}, 404);
  });
  page.on('console', message => {
    if (message.type() === 'error' && !message.text().includes('Failed to load resource')) errors.push(message.text());
  });
  page.on('pageerror', error => errors.push(String(error)));
}

async function runAdminReadOnly(browser, base) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const writes = [], errors = [];
  await configureAdmin(page, ['ops.ticket.read'], writes, errors);
  await page.goto(base + '/admin/', {waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() => !document.getElementById('appView').classList.contains('hide'));
  await page.evaluate(() => go('tickets'));
  await page.locator('tr[data-id]').click();
  await page.locator('.modal').waitFor({state: 'visible'});
  for (const selector of ['#tkScan','#tkBody','#tkInternal','#tkSend','#tkResolve','#tkClose2']) {
    assert(await page.locator(selector).count() === 0, `read-only rendered ${selector}`);
  }
  assert(await page.locator('#tkReadOnlyNotice').count() >= 1, 'read-only notice missing');
  assert(await page.locator('.msg.note').count() === 1, 'read-only agent cannot inspect internal notes');
  assert(writes.length === 0, 'read-only support view emitted a write request');
  assert(errors.length === 0, `read-only browser errors: ${errors.join(' | ')}`);
  await context.close();
}

async function runAdminWriter(browser, base) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const writes = [], errors = [];
  await configureAdmin(page, ['ops.ticket.read', 'ops.ticket.write'], writes, errors);

  for (const width of [390, 820, 1440, 3840]) {
    await page.setViewportSize({width, height: width < 1000 ? 900 : 1200});
    await page.goto(base + '/admin/', {waitUntil: 'domcontentloaded'});
    await page.waitForFunction(() => !document.getElementById('appView').classList.contains('hide'));
    await page.evaluate(() => go('tickets'));
    await page.locator('tr[data-id]').click();
    await page.locator('#tkSend').waitFor({state: 'visible'});
    assertGeometry(await modalGeometry(page), `admin ${width}`);
    await page.keyboard.press('Escape');
  }

  await page.evaluate(() => go('tickets'));
  await page.locator('tr[data-id]').click();
  await page.locator('#tkBody').fill('第一次回复内容');
  await page.locator('#tkSend').click();
  await page.locator('#tkBody').fill('第二次回复内容');
  await page.locator('#tkSend').click();
  await page.locator('#tkSend').click();
  await page.waitForFunction(() => document.querySelectorAll('.toast').length > 0);

  const replies = writes.filter(write => write.path.endsWith('/reply'));
  assert(replies.length === 3, `expected three reply attempts, got ${replies.length}`);
  assert(replies.every(write => write.key), 'admin reply omitted idempotency key');
  assert(replies[0].key !== replies[1].key, 'changed reply payload reused its key');
  assert(replies[1].key === replies[2].key, 'same reply retry did not reuse its key');

  await page.locator('#tkResolve').waitFor({state: 'visible'});
  await page.locator('#tkResolve').click();
  await page.waitForFunction(() => !document.querySelector('.modal'));
  await page.evaluate(() => go('tickets'));
  await page.locator('#tkScan').click();
  await page.waitForFunction(() => document.querySelectorAll('.spin').length === 0);
  const statusWrite = writes.find(write => write.path.endsWith('/status'));
  const scanWrite = writes.find(write => write.path === '/v1/tickets/escalate');
  assert(statusWrite && statusWrite.key, 'admin status omitted idempotency key');
  assert(scanWrite && scanWrite.key, 'admin SLA scan omitted idempotency key');
  assert(!(await page.evaluate(() => window.__supportXss)), 'admin support XSS payload executed');
  assert(await page.locator('img[src="x"]').count() === 0, 'admin subject created an image element');
  assert(errors.length === 0, `writer browser errors: ${errors.join(' | ')}`);
  await context.close();
  return writes.length;
}

async function configurePortal(page, writes, errors) {
  await page.addInitScript(() => localStorage.setItem('aegis_token', 'support-portal-gate'));
  let createFailures = 0, withdrawn = false;
  const commission = {currency: 'CNY', available: 1234, pending: 0, withdrawing: 0, settled: 0, invitees: 1, min_withdraw: 100, rate_percent: 10};
  const balance = {currency: 'CNY', balance: 2000, entries: []};
  const notificationPreferences = [
    {category: 'transactional', channel: 'email', enabled: true, locked: true},
    {category: 'service', channel: 'email', enabled: true, locked: false},
    {category: 'marketing', channel: 'email', enabled: true, locked: false},
    {category: 'service', channel: 'telegram', enabled: true, locked: false},
    {category: 'marketing', channel: 'telegram', enabled: false, locked: false},
  ];
  await page.route('**/v1/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    const apiPath = url.pathname.slice(url.pathname.indexOf('/v1/'));
    const method = request.method();
    if (method !== 'GET') writes.push({
      path: apiPath, method, key: request.headers()['idempotency-key'] || '',
      body: request.postData() ? request.postDataJSON() : null,
    });
    if (method === 'GET' && apiPath === '/v1/site-config') return json(route, {registration_mode: 'closed'});
    if (method === 'GET' && apiPath === '/v1/me') return json(route, {user_id: ticket.user_id, email: ticket.user_email});
    if (method === 'GET' && apiPath === '/v1/support/tickets') return json(route, {tickets: [ticket, {...withdrawTicket, status: withdrawn ? 'closed' : withdrawTicket.status}]});
    if (method === 'GET' && apiPath === `/v1/support/tickets/${ticket.id}`) return json(route, ticket);
    if (method === 'GET' && apiPath === `/v1/support/tickets/${withdrawTicket.id}`) return json(route, {
      ...withdrawTicket, status: withdrawn ? 'closed' : withdrawTicket.status,
      messages: withdrawn ? [...withdrawTicket.messages, {id: 'mw2', author_kind: 'system', body: '工单已由用户撤回', created_at: '2026-08-02T00:01:00Z'}] : withdrawTicket.messages,
    });
    if (method === 'GET' && apiPath === '/v1/me/commission') return json(route, {summary: commission, entries: [], withdrawals: []});
    if (method === 'GET' && apiPath === '/v1/me/invite') return json(route, {invite: {code: 'TESTCODE'}});
    if (method === 'GET' && apiPath === '/v1/me/balance') return json(route, balance);
    if (method === 'GET' && apiPath === '/v1/me/notification-preferences') return json(route, {preferences: notificationPreferences});
    if (method === 'GET' && apiPath === '/v1/events') return route.abort();
    if (method === 'GET') return json(route, {subscriptions: [], announcements: [], tickets: []});
    if (method === 'POST' && apiPath === '/v1/support/tickets') {
      createFailures += 1;
      if (createFailures <= 2) return json(route, {error: {message: 'simulated retry'}}, 503);
      return json(route, ticket);
    }
    if (method === 'POST' && apiPath.endsWith('/reply')) return json(route, {ok: true});
    if (method === 'POST' && apiPath.endsWith('/close')) return json(route, {ok: true});
    if (method === 'POST' && apiPath === `/v1/support/tickets/${withdrawTicket.id}/withdraw`) {
      withdrawn = true;
      return json(route, {withdrawn: true});
    }
    if (method === 'POST' && apiPath === '/v1/me/commission/transfer') {
      const {amount} = request.postDataJSON();
      if (!Number.isInteger(amount) || amount <= 0 || amount > commission.available) {
        return json(route, {error: {message: '可提现佣金不足'}}, 409);
      }
      commission.available -= amount;
      balance.balance += amount;
      return json(route, {ledger_txn_id: '73000000-0000-4000-8000-000000000001', amount});
    }
    if (method === 'PUT' && apiPath === '/v1/me/notification-preferences') {
      const next = request.postDataJSON();
      const current = notificationPreferences.find(item => item.category === next.category && item.channel === next.channel);
      if (current) current.enabled = next.enabled;
      return json(route, {ok: true});
    }
    return json(route, {error: {message: `unexpected ${method} ${apiPath}`}}, 404);
  });
  page.on('console', message => {
    if (message.type() === 'error' && !message.text().includes('Failed to load resource')) errors.push(message.text());
  });
  page.on('pageerror', error => errors.push(String(error)));
}

async function runPortal(browser, base) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const writes = [], errors = [];
  await configurePortal(page, writes, errors);
  await page.goto(base + '/', {waitUntil: 'domcontentloaded'});
  await page.waitForFunction(() => !document.getElementById('appView').classList.contains('hide'));

  for (const width of [390, 820, 1440, 3840]) {
    await page.setViewportSize({width, height: width < 1000 ? 900 : 1200});
    await page.evaluate(() => go('support'));
    await page.locator(`[data-id="${ticket.id}"]`).click();
    await page.locator('#tkSend').waitFor({state: 'visible'});
    assertGeometry(await modalGeometry(page), `portal ${width}`);
    assert(!(await page.locator('body').innerText()).includes('INTERNAL-SECRET-NEVER-PORTAL'),
      'portal rendered an internal note from a malformed response');
    await page.keyboard.press('Escape');
  }

  await page.evaluate(() => go('support'));
  await page.locator('#tkBody').waitFor({state: 'visible'});
  assert(await page.locator('#tkBody').count() === 1, 'portal body-only ticket field missing');
  assert(await page.locator('#tkSubject,#tkCategory,#tkPriority').count() === 0,
    'portal exposed unnecessary ticket fields');
  await page.locator('#tkBody').fill('第一次提交的详细问题说明');
  await page.locator('#tkSubmit').click();
  await page.locator('#tkBody').fill('第二次提交的详细问题说明');
  await page.locator('#tkSubmit').click();
  await page.locator('#tkSubmit').click();
  await page.waitForFunction(() => document.querySelector('.modal'));

  const creates = writes.filter(write => write.path === '/v1/support/tickets');
  assert(creates.length === 3, `expected three create attempts, got ${creates.length}`);
  assert(creates.every(write => write.key), 'portal create omitted idempotency key');
  assert(creates.every(write => Object.keys(write.body).join(',') === 'body'),
    'portal create sent fields other than body');
  assert(creates[0].key !== creates[1].key, 'changed create body reused its key');
  assert(creates[1].key === creates[2].key, 'same create retry did not reuse its key');

  await page.locator('#tkReply').fill('用户补充公开说明');
  await page.locator('#tkSend').click();
  await page.waitForFunction(() => document.querySelector('.modal'));
  await page.locator('#tkCloseBtn').click();
  await page.waitForFunction(() => !document.querySelector('.modal'));
  const reply = writes.find(write => write.path.endsWith('/reply'));
  const close = writes.find(write => write.path.endsWith('/close'));
  assert(reply && reply.key, 'portal reply omitted idempotency key');
  assert(close && close.key, 'portal close omitted idempotency key');

  await page.evaluate(() => go('support'));
  await page.locator(`[data-id="${withdrawTicket.id}"]`).click();
  await page.locator('#tkWithdrawBtn').click();
  assert(writes.filter(write => write.path.endsWith('/withdraw')).length === 0,
    'withdraw wrote before confirmation');
  await page.locator('#tkWithdrawReason').fill('已经自行解决');
  await page.locator('#tkWithdrawGo').click();
  await page.waitForFunction(() => document.querySelector('.modal') && !document.querySelector('#tkWithdrawGo'));
  const withdraw = writes.find(write => write.path === `/v1/support/tickets/${withdrawTicket.id}/withdraw`);
  assert(withdraw && withdraw.key, 'portal withdraw omitted idempotency key');
  assert(JSON.stringify(withdraw.body) === JSON.stringify({reason: '已经自行解决'}),
    'portal withdraw sent an unexpected body');
  assert((await page.locator('body').innerText()).includes('此工单已关闭'),
    'portal withdraw did not refresh ticket detail');
  assert(await page.locator('#tkReply,#tkWithdrawBtn').count() === 0,
    'closed withdrawn ticket kept an action');
  await page.keyboard.press('Escape');

  await page.evaluate(() => go('referral'));
  await page.locator('#rfTransfer').waitFor({state: 'visible'});
  await page.locator('#rfTransfer').click();
  const invalid = ['0', '-1', 'abc', '12.345', '12.35'];
  for (const amount of invalid) {
    await page.locator('#rfTransferAmt').fill(amount);
    await page.locator('#rfTransferGo').click();
  }
  assert(writes.filter(write => write.path === '/v1/me/commission/transfer').length === 0,
    'invalid commission amount emitted a transfer');
  await page.locator('#rfTransferAmt').fill('12.34');
  await page.locator('#rfTransferGo').click();
  await page.waitForFunction(() => !document.querySelector('.modal'));
  const transfer = writes.find(write => write.path === '/v1/me/commission/transfer');
  assert(transfer && transfer.key, 'portal commission transfer omitted idempotency key');
  assert(JSON.stringify(transfer.body) === JSON.stringify({amount: 1234}),
    'portal commission transfer did not use exact minor units');
  assert(await page.locator('#rfTransfer').isDisabled(), 'transfer stayed enabled after available commission refreshed');
  assert((await page.locator('body').innerText()).includes('当前余额 ¥32.34'),
    'portal commission transfer did not refresh balance');

  await page.evaluate(() => go('security'));
  await page.locator('#npMarketingEmail').waitFor({state: 'visible'});
  assert(await page.locator('#npTransactionalEmail').isDisabled(),
    'transactional notification preference was not locked');
  await page.locator('#npMarketingEmail').click();
  const preferenceWrite = writes.find(write => write.path === '/v1/me/notification-preferences');
  assert(preferenceWrite && preferenceWrite.method === 'PUT',
    'notification preference did not use PUT');
  assert(JSON.stringify(preferenceWrite.body) === JSON.stringify({category: 'marketing', channel: 'email', enabled: false}),
    'notification preference sent an unexpected body');
  assert(!(await page.evaluate(() => window.__supportXss)), 'portal support XSS payload executed');
  assert(errors.length === 0, `portal browser errors: ${errors.join(' | ')}`);
  await context.close();
  return writes.length;
}

async function main() {
  const server = http.createServer((request, response) => {
    const body = request.url.startsWith('/admin') ? adminHTML : portalHTML;
    response.writeHead(200, {'Content-Type': 'text/html; charset=utf-8', 'Content-Length': String(body.length)});
    response.end(body);
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const base = `http://127.0.0.1:${server.address().port}`;
  const browser = await chromium.launch({headless: true, executablePath: chrome});
  try {
    await runAdminReadOnly(browser, base);
    const adminWrites = await runAdminWriter(browser, base);
    const portalWrites = await runPortal(browser, base);
    process.stdout.write(JSON.stringify({
      gate: 'pass', engine: 'real-playwright-system-chrome',
      responsive: [390, 820, 1440, 3840], read_only: 'fail-closed',
      stable_retry_keys: true, internal_notes: 'portal-hidden', xss: 'blocked',
      admin_writes: adminWrites, portal_writes: portalWrites,
    }) + '\n');
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
}

main().catch(error => {
  console.error(error && error.stack ? error.stack : error);
  process.exitCode = 1;
});
