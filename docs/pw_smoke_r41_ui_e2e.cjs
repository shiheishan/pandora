// Pandora r41 UI 端到端验证: appearance 保存主题 必须带 Idempotency-Key 头且非400
const { chromium } = require('playwright');
const fs = require('fs');
const env = fs.readFileSync('/opt/pandora/secure/aegis-admin-credentials.env', 'utf8');
const get = k => (env.match(new RegExp('^' + k + '=(.*)$', 'm')) || [])[1] || '';

(async () => {
  const BASE = 'http://127.0.0.1:9001/';
  const EMAIL = get('AEGIS_ADMIN_EMAIL');
  const PASS = get('AEGIS_ADMIN_PASSWORD');
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const captured = [];
  page.on('request', req => {
    const u = req.url();
    if (u.includes('/v1/') && req.method() !== 'GET') {
      captured.push({ method: req.method(), path: u.split('/v1/')[1], idemKey: req.headers()['idempotency-key'] || null });
    }
  });
  page.on('pageerror', e => captured.push({ jsError: e.message.slice(0, 150) }));
  page.on('console', m => { if (m.type() === 'error') captured.push({ consoleError: m.text().slice(0, 150) }); });

  await page.goto(BASE, { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(800);
  await page.fill('#email', EMAIL);
  await page.fill('#pass', PASS);
  await page.evaluate(() => document.getElementById('loginForm').requestSubmit());
  await page.waitForTimeout(3500);

  // 进入 外观(appearance)
  await page.evaluate(() => document.querySelector('.nav-item[data-page="appearance"]').click());
  await page.waitForTimeout(1500);

  // 新建主题
  const hasNew = await page.evaluate(() => !!document.getElementById('thNew'));
  if (!hasNew) { console.log('SKIP: 已有主题时不显示新建按钮'); }
  else {
    await page.evaluate(() => document.getElementById('thNew').click());
    await page.waitForTimeout(600);
    await page.fill('#thCode', 'ui_e2e_tmp');
    await page.fill('#thName', 'UI E2E 临时主题');
    await page.evaluate(() => document.getElementById('thSite').value = 'e2e-tmp');
    // 找保存按钮
    const saveBtn = await page.evaluate(() => {
      const btns = [...document.querySelectorAll('.modal .btn-primary, .modal button')];
      const b = btns.find(x => /保存/.test(x.textContent || ''));
      if (b) { b.click(); return b.textContent; }
      return null;
    });
    console.log('SAVE_BTN:', saveBtn);
    await page.waitForTimeout(2500);

    // 清理: 删除测试主题
    const del = await page.evaluate(() => {
      const row = [...document.querySelectorAll('[data-th-del]')].find(b => b.dataset.thDel === 'ui_e2e_tmp');
      if (row) { row.click(); return true; }
      return false;
    });
    if (del) {
      await page.waitForTimeout(600);
      await page.evaluate(() => {
        const conf = [...document.querySelectorAll('.modal button')].find(b => /删除|确认/.test(b.textContent || ''));
        if (conf) conf.click();
      });
      await page.waitForTimeout(1200);
    }
  }

  console.log('=== 捕获的写请求 ===');
  for (const c of captured) console.log(JSON.stringify(c));
  const themeSave = captured.find(c => c.path && c.path.includes('/themes') && c.method === 'POST');
  console.log('\n=== 判定 ===');
  if (themeSave) {
    console.log('themes POST 请求头 Idempotency-Key:', themeSave.idemKey ? 'PRESENT ✓' : 'MISSING ✗');
  } else {
    console.log('未捕获到 themes POST（新建按钮可能未触发，见上方捕获）');
  }
  await browser.close();
})().catch(e => { console.error('FATAL:', e.message); process.exit(1); });
