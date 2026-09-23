// 针对性复查：nodes/traffic 页面完整 innerText + 该页 /v1/ 请求，判断是空态还是渲染缺失
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

  for (const p of ['nodes', 'traffic', 'plans', 'orders']) {
    apiReq.length = 0;
    await page.evaluate((pg) => document.querySelector(`.nav-item[data-page="${pg}"]`).click(), p);
    await page.waitForTimeout(2500);
    const txt = await page.evaluate(() => document.getElementById('appView').innerText);
    // 去掉导航栏公共部分（取 appView 内容，导航在 appView 外？实际导航可能在 appView 内，这里打印全文）
    console.log(`===== ${p} 页面 (${txt.length} chars) =====`);
    console.log(txt.replace(/\n{2,}/g, '\n').slice(0, 700));
    console.log(`  [${p}] /v1/ 请求:`, apiReq.join(' | ') || '无');
  }
  await browser.close();
})().catch(e => { console.error('FATAL:', e.message); process.exit(1); });
