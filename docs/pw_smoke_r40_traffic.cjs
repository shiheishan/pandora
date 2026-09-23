// traffic 页面深挖：逐秒轮询内容区，看是懒加载延迟还是真空
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
  page.on('response', res => {
    if (res.url().includes('/v1/')) apiReq.push(`${res.status()} ${res.request().method()} ${res.url().split('/v1/')[1] || res.url()}`);
  });
  await page.goto(BASE, { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(800);
  await page.fill('#email', EMAIL);
  await page.fill('#pass', PASS);
  await page.evaluate(() => document.getElementById('loginForm').requestSubmit());
  await page.waitForTimeout(3500);
  await page.evaluate(() => document.querySelector('.nav-item[data-page="traffic"]').click());
  for (let i = 0; i < 6; i++) {
    await page.waitForTimeout(1500);
    const info = await page.evaluate(() => {
      const v = document.getElementById('appView');
      return { len: (v.innerText || '').length, html: (v.innerHTML || '').length, txt: (v.innerText || '').replace(/\n{2,}/g, '\n').slice(-600) };
    });
    console.log(`t+${(i + 1) * 1.5}s len=${info.len} html=${info.html}`);
    if (info.len > 300) { console.log('CONTENT:', info.txt); break; }
  }
  console.log('API:', apiReq.join(' | ') || '无');
  const final = await page.evaluate(() => {
    const v = document.getElementById('appView');
    return (v.innerText || '').replace(/\n{2,}/g, '\n');
  });
  console.log('===== FINAL =====');
  console.log(final.slice(0, 1500));
  await browser.close();
})().catch(e => { console.error('FATAL:', e.message); process.exit(1); });