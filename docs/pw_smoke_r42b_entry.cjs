// Pandora r42b 入口可达性验证：users 用户详情弹窗内「调整」按钮 + giftcards「新建礼品卡」弹窗
// 只打开弹窗验证元素存在，不提交任何写操作（调账/生成均不触发）
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
  const bad = [];
  page.on('pageerror', e => bad.push('PAGEERROR: ' + e.message.slice(0, 200)));
  page.on('console', m => { if (m.type() === 'error') bad.push('CONSOLE: ' + m.text().slice(0, 200)); });

  await page.goto(BASE, { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(800);
  await page.fill('#email', EMAIL);
  await page.fill('#pass', PASS);
  await page.evaluate(() => document.getElementById('loginForm').requestSubmit());
  await page.waitForTimeout(3500);

  // ========== 1) users 用户详情弹窗「调整」按钮 ==========
  await page.evaluate(() => document.querySelector('.nav-item[data-page="users"]').click());
  await page.waitForTimeout(1500);

  const userRowClick = await page.evaluate(() => {
    // 详情入口是 button[data-uid]（绑定 userModal）
    const btn = document.querySelector('button[data-uid]');
    if (!btn) return null;
    const uid = btn.getAttribute('data-uid');
    btn.click();
    return uid;
  });
  await page.waitForTimeout(2000);

  const modalInfo = await page.evaluate(() => {
    const m = document.querySelector('.modal');
    if (!m) return { open: false };
    return {
      open: true,
      title: (m.querySelector('.modal-title, h3') || {}).textContent || '',
      hasAdjBal: !!m.querySelector('#uAdjBal'),
      adjBalText: (m.querySelector('#uAdjBal') || {}).textContent || '',
      hasReset: !!m.querySelector('#uReset'),
      hasGroup: !!m.querySelector('#uSetGrp'),
      bodyLen: (m.textContent || '').length
    };
  });
  console.log('USER_ROW:', userRowClick);
  console.log('USER_MODAL:', JSON.stringify(modalInfo));
  // 点击「调整」按钮，验证调账弹窗（不做真实提交）
  if (modalInfo.hasAdjBal) {
    const adj = await page.evaluate(() => {
      const b = document.getElementById('uAdjBal');
      b.click();
      return true;
    });
    await page.waitForTimeout(1200);
    const adjModal = await page.evaluate(() => {
      const all = [...document.querySelectorAll('.modal')];
      const m = all[all.length - 1]; // 取最上层弹窗
      if (!m) return { open: false, modalCount: all.length };
      return {
        open: true,
        modalCount: all.length,
        title: (m.querySelector('.modal-title, h3') || {}).textContent || '',
        hasAmt: !!m.querySelector('#abAmt'),
        hasWhy: !!m.querySelector('#abWhy'),
        goBtn: (m.querySelector('#abGo') || {}).textContent || ''
      };
    });
    console.log('ADJ_MODAL:', JSON.stringify(adjModal));
    await page.keyboard.press('Escape');
    await page.waitForTimeout(800);
  }
  // 关闭
  await page.keyboard.press('Escape');
  await page.waitForTimeout(800);

  // ========== 2) giftcards「新建礼品卡」弹窗 ==========
  await page.evaluate(() => document.querySelector('.nav-item[data-page="giftcards"]').click());
  await page.waitForTimeout(1500);
  const gcBtn = await page.evaluate(() => {
    const b = document.getElementById('gcNew');
    if (!b) return null;
    b.click();
    return b.textContent.trim();
  });
  await page.waitForTimeout(1200);
  const gcModal = await page.evaluate(() => {
    const m = document.querySelector('.modal');
    if (!m) return { open: false };
    const btns = [...m.querySelectorAll('button')].map(b => b.textContent.trim());
    return {
      open: true,
      title: (m.querySelector('.modal-title, h3') || {}).textContent || '',
      hasBal: !!m.querySelector('#gcBal'),
      hasName: !!m.querySelector('#gcName, #gcTitle, #gcDesc'),
      buttons: btns
    };
  });
  console.log('GC_BTN:', gcBtn);
  console.log('GC_MODAL:', JSON.stringify(gcModal));
  await page.keyboard.press('Escape');
  await page.waitForTimeout(800);

  console.log('JS/CONSOLE 错误:', bad.length ? bad : '无 ✓');
  await browser.close();
})().catch(e => { console.error('FATAL:', e.message); process.exit(1); });