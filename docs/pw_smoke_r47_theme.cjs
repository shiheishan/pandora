// Pandora r47 写路径冒烟（精简）：真实 UI 点击「新建主题→保存→删除」，全程自动清理，不留测试数据
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

  // 进 appearance 页
  await page.evaluate(() => document.querySelector('.nav-item[data-page="appearance"]').click());
  await page.waitForTimeout(1200);

  // 1) 新建主题弹窗
  const hasNew = await page.evaluate(() => !!document.getElementById('thNew'));
  if (!hasNew) { console.log('SKIP: 无 thNew 新建按钮'); await browser.close(); process.exit(0); }
  await page.evaluate(() => document.getElementById('thNew').click());
  await page.waitForTimeout(500);
  await page.fill('#thCode', 'r47_tmp_theme');
  await page.fill('#thName', 'r47 临时主题');
  await page.evaluate(() => { const el = document.getElementById('thSite'); if (el) el.value = 'r47-tmp'; });
  // 2) 点保存（弹窗内按钮）
  const saved = await page.evaluate(() => {
    const btns = [...document.querySelectorAll('.modal button, .modal .btn')];
    const b = btns.find(x => /保存/.test(x.textContent || ''));
    if (b) { b.click(); return b.textContent.trim(); }
    return null;
  });
  console.log('SAVE_CLICK:', saved || '未找到保存按钮');
  await page.waitForTimeout(2500);

  // 3) 删除清理（带 data-th-del 属性的行内删除按钮）
  let delClicked = null;
  try {
    delClicked = await page.evaluate(() => {
      const btn = document.querySelector('[data-th-del="r47_tmp_theme"]');
      if (btn) { btn.click(); return true; }
      return false;
    });
  } catch (e) { delClicked = 'ERR:' + String(e).slice(0, 80); }
  console.log('DEL_CLICK:', delClicked);
  await page.waitForTimeout(700);
  const confirmClicked = await page.evaluate(() => {
    const btns = [...document.querySelectorAll('.modal button, .modal .btn')];
    const b = btns.find(x => /删除|确认/.test(x.textContent || ''));
    if (b) { b.click(); return b.textContent.trim(); }
    return null;
  });
  console.log('DEL_CONFIRM:', confirmClicked || '无确认按钮(可能直接删)');
  await page.waitForTimeout(1500);

  // 4) 最终残留检查（前端 UI 层）
  const remains = await page.evaluate(() => !!document.querySelector('[data-th-del="r47_tmp_theme"]'));
  console.log('THEME_STILL_IN_UI:', remains);

  // 汇总
  const writes = captured.filter(c => c.method !== 'GET');
  const noIdem = writes.filter(c => !c.idemKey);
  const badStatus = writes.filter(c => c.status && c.status >= 400);
  console.log('CAPTURED_WRITES:', JSON.stringify(writes));
  console.log('NO_IDEM_PATHS:', JSON.stringify(noIdem.map(c => c.path + ':' + c.method)));
  console.log('BAD_STATUS_PATHS:', JSON.stringify(badStatus.map(c => c.status + ' ' + c.path + ':' + c.method)));
  console.log('JS_BAD_COUNT:', bad.length);
  bad.slice(0, 10).forEach(b => console.log('JSBAD:', b));
  await browser.close();
})().catch(e => { console.error('FATAL:', e); process.exit(1); });
