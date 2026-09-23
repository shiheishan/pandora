'use strict';

const fs = require('fs');
const http = require('http');
const path = require('path');
const { chromium } = require('playwright');

const root = path.resolve(__dirname, '..');
const adminHTML = fs.readFileSync(path.join(root, 'web', 'admin', 'index.html'));
const portalHTML = fs.readFileSync(path.join(root, 'web', 'portal', 'index.html'));
const chrome = process.env.PANDORA_CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const assert = (ok, message) => { if (!ok) throw new Error(message); };
const json = (route, body, status = 200) => route.fulfill({status, contentType:'application/json', body:JSON.stringify(body)});

async function main(){
  const server=http.createServer((request,response)=>{
    const data=request.url.startsWith('/portal')?portalHTML:adminHTML;
    response.writeHead(200,{'Content-Type':'text/html; charset=utf-8','Content-Length':String(data.length),'Cache-Control':'no-store'});
    response.end(data);
  });
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve)});
  const base=`http://127.0.0.1:${server.address().port}`;
  const browser=await chromium.launch({headless:true,executablePath:chrome});
  const context=await browser.newContext();
  const admin=await context.newPage();
  const portal=await context.newPage();
  const errors=[];
  for(const page of [admin,portal]){
	page.on('console',m=>{
	  const text=m.text();
	  if(m.type()==='error'&&!text.includes('Failed to load resource')&&!text.includes('ERR_FAILED'))errors.push(text);
	});
    page.on('pageerror',e=>errors.push(String(e)));
  }
  const writes=[];
  let planReads=0,plansFail=false,permissions=['ops.content.write'];
  const longBody='正文 <img src=x onerror="window.__kbXss=1"> 只应作为文本\n'+('用于验证滚动的长正文。\n'.repeat(220));
  const pageRow={id:'10000000-0000-7000-8000-000000000001',slug:'getting-started',kind:'kb_article',category:'入门',version:4,latest_version:4,is_latest_in_audience:true,title:'开始使用',summary:'安全入门指南',body:longBody,locale:'zh-CN',sanitizer_version:'plain-v1',target_platforms:['web'],min_client_version:'1.0.0',max_client_version:'',target_plan_ids:['20000000-0000-7000-8000-000000000001'],visibility:'authenticated',status:'published',updated_at:'2026-08-02T10:00:00Z',published_at:'2026-08-02T10:00:00Z'};
  const androidRow={...pageRow,id:'10000000-0000-7000-8000-000000000002',version:3,title:'Android 开始使用',target_platforms:['android'],min_client_version:'',updated_at:'2026-08-02T09:00:00Z'};

  await admin.addInitScript(()=>localStorage.setItem('aegis_admin_token','content-gate'));
  await admin.route('**/v1/**',async route=>{
    const req=route.request(),url=new URL(req.url()),p=url.pathname.slice(url.pathname.indexOf('/v1/')),method=req.method();
    if(method!=='GET')writes.push({method,path:p,key:req.headers()['idempotency-key']||'',body:req.postDataJSON()});
    if(method==='GET'&&p==='/v1/me')return json(route,{user_id:'content-admin',permissions});
    if(method==='GET'&&p==='/v1/content-pages')return json(route,{pages:[{...pageRow,body:undefined},{...androidRow,body:undefined}]});
    if(method==='GET'&&p==='/v1/content-pages/'+pageRow.id)return json(route,{page:pageRow});
    if(method==='GET'&&p==='/v1/content-pages/'+androidRow.id)return json(route,{page:androidRow});
    if(method==='GET'&&p==='/v1/plans'){planReads++;return plansFail?json(route,{error:{message:'plan backend unavailable'}},500):json(route,{plans:[]})}
    if(method==='POST'&&p==='/v1/content-pages')return json(route,{page:{id:'new',slug:'getting-started',version:4,status:'published'}},201);
    if(method==='POST'&&p.endsWith('/archive'))return json(route,{page:{id:pageRow.id,version:3,status:'archived'}});
    if(method==='GET'&&p==='/v1/events')return route.abort();
    return json(route,{error:{message:`unexpected ${method} ${p}`}},404);
  });

  try{
    for(const width of [390,820,1440,3840]){
      await admin.setViewportSize({width,height:width<1000?900:1200});
      await admin.goto(base+'/admin-hidden/',{waitUntil:'domcontentloaded'});
      await admin.waitForFunction(()=>!document.getElementById('appView').classList.contains('hide'));
      await admin.evaluate(()=>go('content'));
      await admin.locator('#contentCreate').waitFor({state:'visible'});
      assert(await admin.locator('[data-content-edit]').count()===2,'both audience channels must remain editable');
      await admin.locator('[data-content-edit]').first().click();
      await admin.locator('#ctPublish').waitFor({state:'visible'});
      const geometry=await admin.evaluate(()=>({scroll:document.documentElement.scrollWidth,client:document.documentElement.clientWidth}));
      assert(geometry.scroll<=geometry.client+1,`admin overflow at ${width}`);
      const modal=await admin.locator('.modal').evaluate(el=>{const r=el.getBoundingClientRect(),body=el.querySelector('.modal-body');body.scrollTop=body.scrollHeight;return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,viewport:innerWidth,scrollTop:body.scrollTop}});
      assert(modal.left>=-1&&modal.right<=modal.viewport+1&&modal.top>=-1,`admin modal bounds at ${width}`);
      assert(modal.scrollTop>0,`admin modal did not scroll at ${width}`);
      await admin.keyboard.press('Escape');
    }
    permissions=['ops.content.write','catalog.read'];plansFail=true;
    const writesBeforePlanFailure=writes.length;
    await admin.goto(base+'/admin-hidden/',{waitUntil:'domcontentloaded'});
    await admin.waitForFunction(()=>!document.getElementById('appView').classList.contains('hide'));
    await admin.evaluate(()=>go('content'));
    await admin.locator('[data-content-edit]').first().click();
    await admin.getByText(/已阻止编辑以避免扩大内容可见范围/).waitFor({state:'visible'});
    assert(await admin.locator('.modal').count()===0,'plans 500 still opened content editor');
    assert(writes.length===writesBeforePlanFailure,'plans 500 emitted a write');
    const planReadsAfterFailure=planReads;
    permissions=['ops.content.write'];plansFail=false;
    await admin.goto(base+'/admin-hidden/',{waitUntil:'domcontentloaded'});
    await admin.waitForFunction(()=>!document.getElementById('appView').classList.contains('hide'));
    await admin.evaluate(()=>go('content'));
    await admin.locator('[data-content-edit="'+androidRow.id+'"]').click();
    await admin.locator('#ctPublish').waitFor({state:'visible'});
    assert(planReads===planReadsAfterFailure,'content-only role requested catalog plans after fail-closed probe');
    assert(await admin.locator('.ctPlan').count()===0,'content-only role received plan controls');
    await admin.locator('#ctPublish').click();
    await admin.waitForFunction(()=>!document.querySelector('.modal'));
    admin.once('dialog',d=>d.accept());
    await admin.locator('[data-content-archive="'+pageRow.id+'"]').click();
    await admin.waitForFunction(()=>document.querySelectorAll('.spin').length===0);
    const create=writes.find(x=>x.path==='/v1/content-pages');
    const archive=writes.find(x=>x.path.endsWith('/archive'));
    assert(create&&create.key,'content publish did not carry idempotency key');
    assert(create.body.expected_latest_version===4,'audience update did not use global stale-editor CAS baseline');
    assert(create.body.target_platforms.length===1&&create.body.target_platforms[0]==='android','android audience update drifted into another channel');
    assert(create.body.target_plan_ids.length===1,'role without catalog.read did not preserve plan targeting');
    assert(archive&&archive.key&&archive.body.expected_version===4,'archive lost idempotency/CAS contract');

    await portal.addInitScript(()=>localStorage.setItem('aegis_token','portal-content-gate'));
    await portal.route('**/v1/**',async route=>{
      const req=route.request(),url=new URL(req.url()),p=url.pathname,method=req.method();
      if(method==='GET'&&p==='/v1/me')return json(route,{user_id:'user-1',email:'user@example.com',status:'active'});
      if(method==='GET'&&p==='/v1/me/subscriptions')return json(route,{subscriptions:[]});
      if(method==='GET'&&p==='/v1/me/subscription-links')return json(route,{links:[]});
      if(method==='GET'&&p==='/v1/plans')return json(route,{plans:[]});
      if(method==='GET'&&p==='/v1/me/announcements')return json(route,{announcements:[]});
      if(method==='GET'&&p==='/v1/me/notifications')return json(route,{notifications:[],unread:0});
      if(method==='GET'&&p==='/v1/content/pages')return json(route,{pages:[{...pageRow,body:undefined}]});
      if(method==='GET'&&p==='/v1/content/pages/getting-started')return json(route,{page:pageRow});
      if(method==='GET'&&p==='/v1/events')return route.abort();
      return json(route,{error:{message:`unexpected ${method} ${p}`}},404);
    });
    for(const width of [390,820,1440,3840]){
      await portal.setViewportSize({width,height:width<1000?900:1200});
      await portal.goto(base+'/portal/',{waitUntil:'domcontentloaded'});
      await portal.waitForFunction(()=>!document.getElementById('appView').classList.contains('hide'));
      await portal.evaluate(()=>go('knowledge'));
      await portal.locator('[data-kb-slug="getting-started"]').waitFor({state:'visible'});
      const geometry=await portal.evaluate(()=>({scroll:document.documentElement.scrollWidth,client:document.documentElement.clientWidth}));
      assert(geometry.scroll<=geometry.client+1,`portal overflow at ${width}`);
      await portal.locator('[data-kb-slug="getting-started"]').click();
      await portal.locator('#kbArticleBody').waitFor({state:'visible'});
      const modal=await portal.locator('.modal').evaluate(el=>{const r=el.getBoundingClientRect(),body=el.querySelector('.modal-body');body.scrollTop=body.scrollHeight;return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,viewport:innerWidth,scrollTop:body.scrollTop}});
      assert(modal.left>=-1&&modal.right<=modal.viewport+1&&modal.top>=-1,`portal modal bounds at ${width}`);
      assert(modal.scrollTop>0,`portal modal did not scroll at ${width}`);
      assert((await portal.locator('#kbArticleBody').textContent()).includes('<img src=x'),'article body was not preserved as text');
      assert(await portal.locator('#kbArticleBody img').count()===0,'article HTML executed in portal');
      await portal.locator('.modal [data-close]').last().click();
    }
    assert(!(await portal.evaluate(()=>window.__kbXss)),'article XSS payload executed');
    assert(errors.length===0,`browser errors: ${errors.join(' | ')}`);
    process.stdout.write(JSON.stringify({gate:'pass',engine:'real-playwright-system-chrome',responsive:[390,820,1440,3840],modal_responsive:true,writes:writes.length,plan_failure_reads:planReads,plan_failure_writes:0,xss:'blocked'})+'\n');
  }finally{
    await context.close();await browser.close();await new Promise(resolve=>server.close(resolve));
  }
}

main().catch(error=>{console.error(error&&error.stack?error.stack:error);process.exitCode=1});
