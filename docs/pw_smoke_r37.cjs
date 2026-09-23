// Pandora panel admin-gateway smoke: login + navigate all 23 data-pages, capture
// pageerror / console.error / HTTP>=400. Round 37.
const { chromium } = require('playwright');
const fs = require('fs');

// 凭据直读 secure env，避免硬编码（read from /opt/pandora/secure/aegis-admin-credentials.env）
const env = fs.readFileSync('/opt/pandora/secure/aegis-admin-credentials.env', 'utf8');
const get = k => (env.match(new RegExp('^' + k + '=(.*)$', 'm')) || [])[1] || '';

(async () => {
  const BASE = 'http://127.0.0.1:9001/';
  const EMAIL = get('AEGIS_ADMIN_EMAIL');
  const PASS = get('AEGIS_ADMIN_PASSWORD');
  const PAGES = ['overview','servers','nodes','plans','orders','users','coupons',
    'giftcards','tickets','announce','content','mail','providers','plugins','risk',
    'switches','audit','bulk','commission','devices','latepay','traffic','appearance'];

  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const apiErrors = [];
  const jsErrors = [];

  page.on('pageerror', e => jsErrors.push('PAGE:' + e.message));
  page.on('console', m => { if (m.type() === 'error') jsErrors.push('CONSOLE:' + m.text().slice(0, 150)); });
  page.on('response', async res => {
    if (res.status() >= 400 && res.url().includes('/v1/')) {
      const body = await res.text().catch(() => '');
      apiErrors.push(`${res.status()} ${res.request().method()} ${res.url().split('/v1/')[1] || res.url()} → ${body.slice(0, 120)}`);
    }
  });

  await page.goto(BASE, { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(800);
  await page.fill('#email', EMAIL);
  await page.fill('#pass', PASS);
  await page.evaluate(() => document.getElementById('loginForm').requestSubmit());
  await page.waitForTimeout(3500);

  // confirm login succeeded
  const loggedIn = await page.evaluate(() => !!document.querySelector('.nav-item[data-page="overview"]'));
  console.log('LOGIN_OK:', loggedIn);

  const results = [];
  let clickFail = 0;
  for (const pg of PAGES) {
    const ok = await page.evaluate((p) => {
      const n = document.querySelector(`.nav-item[data-page="${p}"]`);
      if (!n) return 'no-nav';
      n.click(); return 'ok';
    }, pg);
    if (ok !== 'ok') clickFail++;
    await page.waitForTimeout(1100);
    const hasContent = await page.evaluate(() => {
      const v = document.getElementById('appView');
      return v && v.innerText && !v.innerText.includes('载入失败') && !v.innerText.includes('加载失败');
    });
    results.push([pg, ok === 'ok' ? 'OK' : 'NO-NAV', hasContent ? '' : 'NO-CONTENT']);
  }

  console.log('===== PAGE SMOKE (' + PAGES.length + ' pages) =====');
  results.forEach(([k, v, x]) => console.log(`${v === 'OK' ? '✅' : '❌'} ${k}${x ? ' [' + x + ']' : ''}`));
  console.log('CLICK_FAIL:', clickFail);
  console.log('\nAPI >=400 (unique):', apiErrors.length ? [...new Set(apiErrors)].slice(0, 12) : '无');
  console.log('JS errors (unique):', jsErrors.length ? [...new Set(jsErrors)].slice(0, 10) : '无');
  await browser.close();
})().catch(e => { console.error('FATAL:', e.message); process.exit(1); });
