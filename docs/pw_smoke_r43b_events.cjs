// Pandora r43b: 监听页面自身发起的 /v1/events SSE 请求（真实前端路径）+ 全页冒烟摘要
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
  const eventsSeen = [];
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  page.on('request', req => {
    if (req.url().includes('/v1/events')) {
      eventsSeen.push({ auth: (req.headers()['authorization'] || '').slice(0, 25) + '...', when: Date.now() });
    }
    if (req.url().includes('/v1/') && req.method() === 'POST' && req.url().includes('logout')) {
      eventsSeen.push({ logout: true, when: Date.now() });
    }
  });
  page.on('response', res => {
    const u = res.url();
    if (u.includes('/v1/')) {
      if (res.status() >= 400) apiBad.push(`${res.status()} ${res.request().method()} ${u.split('/v1/')[1]}`);
    }
  });
  page.on('pageerror', e => bad.push('PAGEERROR: ' + e.message.slice(0, 200)));
  page.on('console', m => { if (m.type() === 'error') bad.push('CONSOLE: ' + m.text().slice(0, 200)); });

  await page.goto(BASE, { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(800);
  await page.fill('#email', EMAIL);
  await page.fill('#pass', PASS);
  await page.evaluate(() => document.getElementById('loginForm').requestSubmit());
  await page.waitForTimeout(4000); // 登录后前端会自动 startRealtime

  const tokenOk = await page.evaluate(() => {
    const t = localStorage.getItem('aegis_admin_token');
    return { hasToken: !!t, len: t ? t.length : 0 };
  });
  console.log('TOKEN_IN_LS:', JSON.stringify(tokenOk));
  console.log('EVENTS_REQ_SEEN:', eventsSeen.length);
  eventsSeen.forEach((e, i) => console.log('  EVENT_REQ#' + i + ':', JSON.stringify(e)));

  // 跨页触发一次 render 再观察 events 是否再次连接
  const before = eventsSeen.length;
  await page.evaluate(() => document.querySelector('.nav-item[data-page="traffic"]').click());
  await page.waitForTimeout(2500);
  console.log('EVENTS_AFTER_NAV: 新增', eventsSeen.length - before, '次连接');

  console.log('API_BAD_COUNT:', apiBad.length);
  apiBad.slice(0, 10).forEach(a => console.log('APIBAD:', a));
  console.log('JS_BAD_COUNT:', bad.length);
  bad.slice(0, 10).forEach(b => console.log('JSBAD:', b));
  await browser.close();
})().catch(e => { console.error('FATAL:', e); process.exit(1); });