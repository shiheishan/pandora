// Pandora r43 收口冒烟：登录后全 data-page 渲染 + events 权限(登录后 200) + 写路径幂等键齐全
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
  const writeReqs = [];
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  page.on('request', req => {
    const u = req.url();
    if (u.includes('/v1/') && req.method() !== 'GET') {
      writeReqs.push({ method: req.method(), path: u.split('/v1/')[1] || u.split('9001')[1], idem: req.headers()['idempotency-key'] || null, status: null });
    }
  });
  page.on('response', res => {
    const u = res.url();
    if (u.includes('/v1/')) {
      const s = res.status();
      const isWrite = res.request().method() !== 'GET';
      if (s >= 400 && !u.includes('healthz')) apiBad.push(`${s} ${res.request().method()} ${u.split('/v1/')[1]}`);
      if (isWrite) {
        const w = writeReqs.find(x => x.path === (u.split('/v1/')[1] || u.split('9001')[1]));
        if (w) w.status = s;
      }
    }
  });
  page.on('pageerror', e => bad.push('PAGEERROR: ' + e.message.slice(0, 200)));
  page.on('console', m => { if (m.type() === 'error') bad.push('CONSOLE: ' + m.text().slice(0, 200)); });

  await page.goto(BASE, { waitUntil: 'load', timeout: 30000 });
  await page.waitForTimeout(800);
  await page.fill('#email', EMAIL);
  await page.fill('#pass', PASS);
  await page.evaluate(() => document.getElementById('loginForm').requestSubmit());
  await page.waitForTimeout(3500);

  // 1) events SSE 登录后可达性
  const evt = await page.evaluate(async () => {
    try {
      const token = localStorage.getItem('aegis_admin_token') || '';
      const h = {};
      if (token) h.Authorization = 'Bearer ' + token;
      const ctl = new AbortController();
      const t = setTimeout(() => ctl.abort(), 3000);
      const res = await fetch('/v1/events', { headers: h, signal: ctl.signal });
      clearTimeout(t);
      return { status: res.status, ct: res.headers.get('content-type') };
    } catch (e) { return { err: String(e).slice(0, 100) }; }
  });
  console.log('EVENTS_AFTER_LOGIN:', JSON.stringify(evt));

  // 2) 遍历所有 data-page
  const pages = await page.evaluate(() => [...document.querySelectorAll('.nav-item[data-page]')].map(n => n.getAttribute('data-page')));
  console.log('PAGE_COUNT:', pages.length);
  const render = [];
  for (const p of pages) {
    await page.evaluate(pg => document.querySelector(`.nav-item[data-page="${pg}"]`).click(), p);
    await page.waitForTimeout(900);
    const info = await page.evaluate(pg => {
      const main = document.querySelector('.page[data-page="' + pg + '"], .main-content, #main');
      return { page: pg, bodyLen: (document.body.textContent || '').length, mainLen: main ? (main.textContent || '').length : -1, title: document.title };
    }, p);
    render.push(info);
  }
  for (const r of render) console.log('PAGE:', JSON.stringify(r));

  // 3) 主题管理页入口 + 新建弹窗（不提交）
  await page.evaluate(() => document.querySelector('.nav-item[data-page="appearance"]').click());
  await page.waitForTimeout(900);

  console.log('WRITE_REQS:', JSON.stringify(writeReqs));
  console.log('API_BAD_COUNT:', apiBad.length);
  apiBad.slice(0, 15).forEach(a => console.log('APIBAD:', a));
  console.log('JS_BAD_COUNT:', bad.length);
  bad.slice(0, 15).forEach(b => console.log('JSBAD:', b));

  const summary = {
    pages_rendered: render.filter(r => r.mainLen > 0 || r.bodyLen > 100).length,
    pages_total: pages.length,
    api_ge_400: apiBad.length,
    js_errors: bad.length,
    events_after_login: evt,
    write_req_total: writeReqs.length,
    write_req_missing_idem: writeReqs.filter(w => w.method === 'POST' && w.idem === null && w.path && !w.path.includes('login') && !w.path.includes('logout') && !w.path.includes('password')).length
  };
  console.log('SUMMARY:', JSON.stringify(summary, null, 1));
  await browser.close();
})().catch(e => { console.error('FATAL:', e); process.exit(1); });