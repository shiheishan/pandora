// Pandora admin-gateway 纵向功能冒烟（只读）：servers/users/plans/orders 数据渲染与关键 UI 状态
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
  const apiReq = []; // 记录关键 /v1/ 请求状态
  page.on('response', res => {
    if (res.url().includes('/v1/') && !res.url().includes('healthz')) {
      apiReq.push(`${res.status()} ${res.request().method()} ${res.url().split('/v1/')[1] || res.url()}`);
    }
  });

  await page.goto(BASE, { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(800);
  await page.fill('#email', EMAIL);
  await page.fill('#pass', PASS);
  await page.evaluate(() => document.getElementById('loginForm').requestSubmit());
  await page.waitForTimeout(3500);

  const result = {};
  async function probe(pageName, extractor, wait = 2200) {
    const ok = await page.evaluate((p) => {
      const n = document.querySelector(`.nav-item[data-page="${p}"]`);
      if (!n) return 'no-nav'; n.click(); return 'ok';
    }, pageName);
    await page.waitForTimeout(wait);
    result[pageName] = await page.evaluate(extractor);
  }

  // 1) servers: 服务器计数 + 每行删除按钮守卫状态（.sv-row 对齐 r38，替代旧 [data-sv-id]）
  await probe('servers', () => {
    const rows = document.querySelectorAll('.sv-row, table tbody tr');
    const delStates = {};
    rows.forEach(r => {
      const del = r.querySelector('[data-sv-del]');
      if (del) delStates[((r.getAttribute('data-sv-id'))||('row'+Array.from(r.parentNode.children).indexOf(r)))] = { disabled: del.disabled, title: (del.title||'').slice(0,50) };
    });
    return { rowCount: rows.length, delStates: Object.keys(delStates).slice(0,12) };
  });

  // 2) users: 用户表格行数与关键列
  await probe('users', () => {
    const t = document.querySelector('#appView table, #appView .tbl');
    return { hasTable: !!t, rowCount: t ? t.querySelectorAll('tbody tr').length : 0, textLen: (document.getElementById('appView').innerText||'').length };
  });

  // 3) plans: 套餐卡片/表格数量
  await probe('plans', () => {
    const v = document.getElementById('appView');
    const cards = v.querySelectorAll('.plan-card, [data-plan-id], table tbody tr').length;
    return { cards, textLen: (v.innerText||'').length, hasError: /载入失败|加载失败/.test(v.innerText) };
  });

  // 4) orders: 订单表格行数
  await probe('orders', () => {
    const v = document.getElementById('appView');
    const t = v.querySelector('table tbody');
    return { rowCount: t ? t.rows.length : 0, textLen: (v.innerText||'').length };
  });

  // 5) overview: 卡片统计数字是否渲染
  await probe('overview', () => {
    const v = document.getElementById('appView');
    const nums = (v.innerText||'').match(/[¥$]\s?[\d,]+\.?\d*/g) || [];
    return { statCards: v.querySelectorAll('.stat-card, .card, [class*=stat]').length, moneyTokens: nums.slice(0,8), hasError: /载入失败|加载失败/.test(v.innerText) };
  });

  // 汇总 /v1/ 请求状态分布
  const byStatus = {};
  for (const r of apiReq) { const s = r.split(' ')[0]; byStatus[s] = (byStatus[s]||0)+1; }
  const badReq = apiReq.filter(r => r.startsWith('4') || r.startsWith('5')).slice(0,10);

  console.log('===== 纵向功能冒烟 =====');
  console.log(JSON.stringify(result, null, 1));
  console.log('\n/v1/ 请求状态分布:', byStatus);
  console.log('HTTP>=400 请求:', badReq.length ? badReq : '无');
  await browser.close();
})().catch(e => { console.error('FATAL:', e.message); process.exit(1); });