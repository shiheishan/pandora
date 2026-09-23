// Pandora r49 全导航冒烟：登录 → 遍历所有 nav-item → 捕获全部 /v1/ API≥400 + JS 错误 → 写路径(主题新建/删除) → 清理
const { chromium } = require('playwright');
const fs = require('fs');
const env = fs.readFileSync('/opt/pandora/secure/aegis-admin-credentials.env', 'utf8');
const get = k => (env.match(new RegExp('^' + k + '=(.*)$', 'm')) || [])[1] || '';

(async () => {
  const BASE = 'http://127.0.0.1:9001/';
  const EMAIL = get('AEGIS_ADMIN_EMAIL');
  const PASS = get('AEGIS_ADMIN_PASSWORD');
  const bad = [];
  const apiBad = [];
  const captured = [];
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });

  page.on('request', req => {
    const u = req.url();
    if (u.includes('/v1/') && req.method() !== 'GET' && !u.includes('healthz')) {
      captured.push({ method: req.method(), path: u.split('/v1/')[1], idem: req.headers()['idempotency-key'] || null, status: null });
    }
  });
  page.on('response', async res => {
    const u = res.url();
    if (u.includes('/v1/') && !u.includes('events') && !u.includes('healthz')) {
      const path = u.split('/v1/')[1] || u;
      const cap = captured.find(x => x.path === path && x.status === null);
      if (cap) cap.status = res.status();
      if (res.status() >= 400) {
        const body = await res.text().catch(() => '');
        apiBad.push(`${res.status()} ${res.request().method()} ${path} → ${body.slice(0, 120)}`);
      }
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

  const loggedIn = await page.evaluate(() => !!document.querySelector('.nav-item, .sidebar, #logoutBtn, [data-page]'));
  console.log('LOGIN_OK:', loggedIn);
  if (!loggedIn) { console.log('FATAL: 登录失败'); await browser.close(); process.exit(1); }

  const navPages = await page.evaluate(() =>
    [...document.querySelectorAll('.nav-item[data-page]')].map(n => n.getAttribute('data-page'))
  );
  console.log('NAV_PAGES:', JSON.stringify(navPages));
  const pageResults = [];
  for (const pg of navPages) {
    const before = bad.length;
    const beforeApi = apiBad.length;
    await page.evaluate(p => document.querySelector(`.nav-item[data-page="${p}"]`).click(), pg);
    await page.waitForTimeout(1000);
    const errs = bad.slice(before);
    const aerrs = apiBad.slice(beforeApi);
    pageResults.push({ page: pg, jsErrors: errs.length, apiBad: aerrs.length });
    console.log(`PAGE[${pg}]: jsErr=${errs.length} api>=400=${aerrs.length}`);
  }

  // 写路径：主题新建 → 删除
  const hasAppearance = navPages.includes('appearance');
  let writeResult = 'SKIP';
  if (hasAppearance) {
    await page.evaluate(() => document.querySelector('.nav-item[data-page="appearance"]').click());
    await page.waitForTimeout(1200);
    const hasNew = await page.evaluate(() => !!document.getElementById('thNew'));
    if (hasNew) {
      await page.evaluate(() => document.getElementById('thNew').click());
      await page.waitForTimeout(500);
      const code = 'r49_tmp_' + Date.now().toString().slice(-5);
      await page.fill('#thCode', code);
      await page.fill('#thName', 'r49 临时主题');
      await page.evaluate(() => { const el = document.getElementById('thSite'); if (el) el.value = 'r49-tmp'; });
      await page.evaluate(() => {
        const btns = [...document.querySelectorAll('.modal button, .modal .btn')];
        const b = btns.find(x => /保存/.test(x.textContent || ''));
        if (b) b.click();
      });
      await page.waitForTimeout(2500);
      const inUI = await page.evaluate(c => !!document.querySelector(`[data-th-del="${c}"]`), code);
      writeResult = inUI ? 'CREATE_OK' : 'CREATE_MISSING';
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

  const writes = captured.filter(c => c.method !== 'GET');
  const noIdem = writes.filter(c => !c.idem);
  const badStatus = writes.filter(c => c.status && c.status >= 400);
  console.log('CAPTURED_WRITES:', JSON.stringify(writes));
  console.log('NO_IDEM_PATHS:', JSON.stringify(noIdem.map(c => c.path + ':' + c.method)));
  console.log('BAD_STATUS_PATHS:', JSON.stringify(badStatus.map(c => c.status + ' ' + c.path + ':' + c.method)));
  console.log('API>=400_TOTAL:', apiBad.length);
  [...new Set(apiBad)].slice(0, 12).forEach(b => console.log('APIBAD:', b));
  console.log('JS_BAD_COUNT:', bad.length);
  [...new Set(bad)].slice(0, 10).forEach(b => console.log('JSBAD:', b));
  await browser.close();
})().catch(e => { console.error('FATAL:', e); process.exit(1); });
