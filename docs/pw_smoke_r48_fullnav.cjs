// Pandora r48 全导航冒烟：登录 → 遍历所有 nav-item 页面 → 写路径(主题新建/删除) → 清理
const { chromium } = require('playwright');
const fs = require('fs');
const env = fs.readFileSync('/opt/pandora/secure/aegis-admin-credentials.env', 'utf8');
const get = k => (env.match(new RegExp('^' + k + '=(.*)$', 'm')) || [])[1] || '';

(async () => {
  const BASE = 'http://127.0.0.1:9001/';
  const EMAIL = get('AEGIS_ADMIN_EMAIL');
  const PASS = get('AEGIS_ADMIN_PASSWORD');
  const bad = [];
  const captured = [];
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  page.on('request', req => {
    const u = req.url();
    if (u.includes('/v1/') && req.method() !== 'GET' && !u.includes('healthz')) {
      captured.push({ method: req.method(), path: u.split('/v1/')[1], idem: req.headers()['idempotency-key'] || null, status: null });
    }
  });
  page.on('response', res => {
    if (res.url().includes('/v1/') && res.request().method() !== 'GET') {
      const c = captured.find(x => x.path === res.url().split('/v1/')[1]);
      if (c) c.status = res.status();
    }
  });
  page.on('pageerror', e => bad.push('PAGEERROR: ' + e.message.slice(0, 150)));
  page.on('console', m => { if (m.type() === 'error') bad.push('CONSOLE: ' + m.text().slice(0, 150)); });

  await page.goto(BASE, { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(800);
  await page.fill('#email', EMAIL);
  await page.fill('#pass', PASS);
  await page.evaluate(() => document.getElementById('loginForm').requestSubmit());
  await page.waitForTimeout(3500);

  // 登录态确认
  const loggedIn = await page.evaluate(() => !!document.querySelector('.nav-item, .sidebar, #logoutBtn, [data-page]'));
  console.log('LOGIN_OK:', loggedIn);
  if (!loggedIn) { console.log('FATAL: 登录失败'); await browser.close(); process.exit(1); }

  // 遍历所有导航页
  const navPages = await page.evaluate(() =>
    [...document.querySelectorAll('.nav-item[data-page]')].map(n => n.getAttribute('data-page'))
  );
  console.log('NAV_PAGES:', JSON.stringify(navPages));
  const pageResults = [];
  for (const pg of navPages) {
    const before = bad.length;
    await page.evaluate(p => document.querySelector(`.nav-item[data-page="${p}"]`).click(), pg);
    await page.waitForTimeout(900);
    const errs = bad.slice(before);
    const respErr = await page.evaluate(() => {
      // 页面加载时收集失败的 v1 请求（通过 performance entries）
      return performance.getEntriesByType('resource')
        .filter(r => r.name.includes('/v1/') && r.name.includes('9001'))
        .map(r => r.name.split('/v1/')[1]);
    });
    pageResults.push({ page: pg, jsErrors: errs.length });
    console.log(`PAGE[${pg}]: jsErr=${errs.length} resourceEntries=${respErr.length}`);
  }

  // 写路径：主题新建 → 删除（r47 流程复用）
  const hasAppearance = navPages.includes('appearance');
  let writeResult = 'SKIP';
  if (hasAppearance) {
    await page.evaluate(() => document.querySelector('.nav-item[data-page="appearance"]').click());
    await page.waitForTimeout(1200);
    const hasNew = await page.evaluate(() => !!document.getElementById('thNew'));
    if (hasNew) {
      await page.evaluate(() => document.getElementById('thNew').click());
      await page.waitForTimeout(500);
      const code = 'r48_tmp_' + Date.now().toString().slice(-5);
      await page.fill('#thCode', code);
      await page.fill('#thName', 'r48 临时主题');
      await page.evaluate(() => { const el = document.getElementById('thSite'); if (el) el.value = 'r48-tmp'; });
      await page.evaluate(() => {
        const btns = [...document.querySelectorAll('.modal button, .modal .btn')];
        const b = btns.find(x => /保存/.test(x.textContent || ''));
        if (b) b.click();
      });
      await page.waitForTimeout(2500);
      const inUI = await page.evaluate(c => !!document.querySelector(`[data-th-del="${c}"]`), code);
      writeResult = inUI ? 'CREATE_OK' : 'CREATE_MISSING';
      // 删除清理
      await page.evaluate(c => { const btn = document.querySelector(`[data-th-del="${c}"]`); if (btn) btn.click(); }, code);
      await page.waitForTimeout(700);
      await page.evaluate(() => {
        const btns = [...document.querySelectorAll('.modal button, .modal .btn')];
        const b = btns.find(x => /删除|确认/.test(x.textContent || ''));
        if (b) b.click();
      });
      await page.waitForTimeout(1500);
      const remains = await page.evaluate(c => !!document.querySelector(`[data-th-del="${c}"]`), code);
      writeResult += '/DEL_' + (remains ? 'FAIL' : 'OK');
      console.log('WRITE_PATH:', writeResult);
    } else {
      console.log('WRITE_PATH: SKIP(no thNew)');
    }
  } else {
    console.log('WRITE_PATH: SKIP(no appearance)');
  }

  // 汇总
  const writes = captured.filter(c => c.method !== 'GET');
  const noIdem = writes.filter(c => !c.idem);
  const badStatus = writes.filter(c => c.status && c.status >= 400);
  console.log('CAPTURED_WRITES:', JSON.stringify(writes));
  console.log('NO_IDEM_PATHS:', JSON.stringify(noIdem.map(c => c.path + ':' + c.method)));
  console.log('BAD_STATUS_PATHS:', JSON.stringify(badStatus.map(c => c.status + ' ' + c.path + ':' + c.method)));
  console.log('JS_BAD_COUNT:', bad.length);
  bad.slice(0, 10).forEach(b => console.log('JSBAD:', b));
  await browser.close();
})().catch(e => { console.error('FATAL:', e); process.exit(1); });
