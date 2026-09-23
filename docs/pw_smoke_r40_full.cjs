// Pandora admin-gateway r40 全页面冒烟（只读）：遍历所有 data-page 导航，检查渲染/API/JS 错误
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
  const apiReq = [];
  const jsErrors = [];
  page.on('response', res => {
    if (res.url().includes('/v1/') && !res.url().includes('healthz')) {
      apiReq.push(`${res.status()} ${res.request().method()} ${res.url().split('/v1/')[1] || res.url()}`);
    }
  });
  page.on('pageerror', e => jsErrors.push('PAGEERROR: ' + e.message.slice(0, 200)));
  page.on('console', m => { if (m.type() === 'error') jsErrors.push('CONSOLE: ' + m.text().slice(0, 200)); });

  await page.goto(BASE, { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(800);
  await page.fill('#email', EMAIL);
  await page.fill('#pass', PASS);
  await page.evaluate(() => document.getElementById('loginForm').requestSubmit());
  await page.waitForTimeout(3500);

  // 收集全部 data-page 导航项
  const pages = await page.evaluate(() => {
    const items = document.querySelectorAll('.nav-item[data-page]');
    const seen = new Set();
    items.forEach(i => seen.add(i.getAttribute('data-page')));
    return Array.from(seen);
  });
  console.log('DATA-PAGES:', pages.length, JSON.stringify(pages));

  const results = {};
  for (const p of pages) {
    const nav = await page.evaluate((pg) => {
      const n = document.querySelector(`.nav-item[data-page="${pg}"]`);
      if (!n) return 'no-nav';
      n.click(); return 'ok';
    }, p);
    await page.waitForTimeout(1800);
    results[p] = await page.evaluate((pg) => {
      const v = document.getElementById('appView');
      const txt = v ? (v.innerText || '') : '';
      return {
        nav: 'ok',
        textLen: txt.length,
        hasTable: !!v && !!v.querySelector('table'),
        hasCard: !!v && !!v.querySelector('[class*=card]'),
        hasError: /载入失败|加载失败|出错了|错误/.test(txt.slice(0, 3000)),
        snippet: txt.replace(/\s+/g, ' ').slice(0, 90)
      };
    }, p);
  }

  // 汇总 /v1/ 请求状态分布
  const byStatus = {};
  for (const r of apiReq) { const s = r.split(' ')[0]; byStatus[s] = (byStatus[s] || 0) + 1; }
  const badReq = apiReq.filter(r => r.startsWith('4') || r.startsWith('5')).slice(0, 15);

  const empty = Object.entries(results).filter(([k, v]) => v.textLen < 5).map(([k]) => k);
  const errored = Object.entries(results).filter(([k, v]) => v.hasError).map(([k, v]) => k + ':' + v.snippet);

  console.log('===== r40 全页面冒烟 =====');
  console.log('EMPTY_PAGES:', empty.length ? empty : '无');
  console.log('ERROR_PAGES:', errored.length ? errored : '无');
  console.log('/v1/ 状态分布:', byStatus);
  console.log('HTTP>=400:', badReq.length ? badReq : '无');
  console.log('JS/CONSOLE 错误:', jsErrors.length ? jsErrors : '无');
  // 打印每个页面核心指标（压缩）
  for (const [k, v] of Object.entries(results)) {
    console.log(`  ${k}: len=${v.textLen} table=${v.hasTable} card=${v.hasCard} :: ${v.snippet}`);
  }
  await browser.close();
})().catch(e => { console.error('FATAL:', e.message); process.exit(1); });
