// Pandora admin smoke round 38: servers page delete-button guard (read-only).
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
  const apiErrors = [], jsErrors = [];
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
  const loggedIn = await page.evaluate(() => !!document.querySelector('.nav-item[data-page="overview"]'));
  console.log('LOGIN_OK:', loggedIn);

  // 打开服务器页
  await page.evaluate(() => document.querySelector('.nav-item[data-page="servers"]').click());
  await page.waitForTimeout(2500);

  // 读取服务器列表及删除按钮状态（只读，不点击）
  const info = await page.evaluate(() => {
    const rows = document.querySelectorAll('.sv-row, [data-sv-id], table tbody tr');
    const out = [];
    rows.forEach(r => {
      const name = (r.querySelector('.sv-name, td:first-child')?.innerText || r.innerText || '').slice(0, 40);
      const del = r.querySelector('[data-sv-del]');
      if (del || name) {
        out.push({ name, hasDel: !!del, delDisabled: del ? del.disabled : null, delTitle: del ? (del.title || '').slice(0, 60) : '' });
      }
    });
    return out.slice(0, 15);
  });
  console.log('SERVER ROWS:', JSON.stringify(info, null, 1));

  console.log('\nAPI >=400:', apiErrors.length ? [...new Set(apiErrors)].slice(0, 8) : '无');
  console.log('JS errors:', jsErrors.length ? [...new Set(jsErrors)].slice(0, 6) : '无');
  await browser.close();
})().catch(e => { console.error('FATAL:', e.message); process.exit(1); });
