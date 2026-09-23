// [INPUT]: 依赖 embed.go 的 ConsoleHTML / PortalHTML 源文本
// [OUTPUT]: 对外提供两份手写单页的字面契约测试：权限标记、API 调用形状、ARIA、CSS 断点、禁止子串
// [POS]: panel/web 的页面文本守卫，与 app_test.go（React 嵌入完整性）互不引用；断言的每一段文本都必须指向页面里仍在运行的代码，死代码删掉时对应断言一并删除
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package web

import (
	"regexp"
	"strings"
	"testing"
)

func TestPortalPasswordToggleIsAccessible(t *testing.T) {
	html := string(PortalHTML)
	for _, want := range []string{
		`id="toggleLoginPass"`,
		`type="button"`,
		`aria-label="显示密码"`,
		`aria-controls="loginPass"`,
		`aria-pressed="false"`,
		`input.type = show ? 'text' : 'password'`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("portal password visibility control is missing %q", want)
		}
	}
}

func TestKnowledgeBaseAdminAndPortalContracts(t *testing.T) {
	admin := string(ConsoleHTML)
	for _, want := range []string{
		`data-page="content" data-perm="ops.content.write"`,
		`api('/v1/content-pages?limit=200')`,
		`headers:{'Idempotency-Key':idemKey()}`,
		`expected_version:Number(b.dataset.version)`,
		`data-latest-version="'+p.latest_version+'"`,
		`expected_latest_version:current?Number(current.latest_version):0`,
		`can('catalog.read')`,
		`已阻止编辑以避免扩大内容可见范围`,
		`正文（纯文本/Markdown 源，不执行 HTML）`,
	} {
		if !strings.Contains(admin, want) {
			t.Fatalf("admin knowledge-base contract missing %q", want)
		}
	}
	portal := string(PortalHTML)
	for _, want := range []string{
		`data-page="knowledge"`,
		`PORTAL_CLIENT_VERSION='1.0.0', PORTAL_LOCALE='zh-CN'`,
		`/v1/content/pages?kind=kb_article&platform=web&client_version=`,
		`m.querySelector('#kbArticleBody').textContent=page.body||''`,
		`grid-template-columns:repeat(auto-fit,minmax(min(100%,280px),1fr))`,
	} {
		if !strings.Contains(portal, want) {
			t.Fatalf("portal knowledge-base contract missing %q", want)
		}
	}
	if strings.Contains(portal, `kbArticleBody').innerHTML`) {
		t.Fatal("knowledge-base body must never enter an innerHTML rendering sink")
	}
}

func TestConsoleServerManagementUsesDedicatedContracts(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`data-page="servers"`,
		`api('/v1/servers')`,
		`'/v1/servers/'+s.id+'/status'`,
		`'/v1/servers/'+s.id+'/nodes'`,
		`body.row_version=s.row_version`,
		`can('node.write')`,
		`ready:['draining','unhealthy','quarantined']`,
		`unhealthy:['draining','maintenance','quarantined','retired']`,
		`{method:'DELETE',body:JSON.stringify({row_version:s.row_version})}`,
		`只能由 Agent 上报`, // 只锁这半句。整句里带着「实时 CPU/内存/磁盘」这类修饰，
		// 改一次文案就红一次，而它要守的是「后台不能伪造 Agent 上报的字段」这个约束。
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin server management is missing %q", want)
		}
	}

	// Metadata editing must not submit Agent-owned facts or lifecycle state.
	start := strings.Index(html, "function serverPayload(m)")
	if start < 0 {
		t.Fatal("server metadata payload function not found")
	}
	end := strings.Index(html[start:], "function openServerEditor(s)")
	if end < 0 {
		t.Fatal("server metadata payload boundary not found")
	}
	payload := html[start : start+end]
	for _, forbidden := range []string{"agent_version", "last_heartbeat_at", "cpu_cores", "memory_mb", "disk_gb", "control_node_id", "status:"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("server metadata payload includes server-owned field %q", forbidden)
		}
	}
}

func TestAdminUltraWideLayoutUsesAvailableCanvas(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		"@media(min-width:2560px)",
		".content{max-width:3200px",
		// 意图是超宽屏不给统计卡设宽度上限。列数交给基础规则里的 auto-fit
		// 自适应，不在这里锁死——原先锁的 repeat(4,...) 已经被它替换掉了，
		// 继续锁着只会挡住这类改进。
		".stats{gap:var(--sp-5);max-width:none",
		".rev-chart-box{max-width:none}",
		".node-detail-modal{max-width:1200px}",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin ultra-wide layout missing %q", want)
		}
	}
}

func TestAdminMobileNavigationAndTableAccessibilityContracts(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`aria-controls="sidebar"`,
		`aria-expanded="false"`,
		`aria-label="管理控制台导航"`,
		`function setSidebar(open,returnFocus)`,
		`$('btnMenu').setAttribute('aria-expanded',String(next))`,
		`$('sidebar').inert=mobile&&!next`,
		`mask.inert=!next;mask.disabled=!next;mask.tabIndex=next?0:-1`,
		`mask.inert=!open;mask.disabled=!open;mask.tabIndex=open?0:-1`,
		`disabled tabindex="-1" inert`,
		`document.body.classList.toggle('nav-drawer-open',next)`,
		`main.inert=next`,
		`if(mobile&&!open&&$('sidebar').contains(document.activeElement)){`,
		`$('btnMenu').focus({preventScroll:true})`,
		`if(next)requestAnimationFrame(focusSidebarEntry)`,
		`$('sideMask').onclick=()=>setSidebar(false,true)`,
		`setSidebar(false,true)`,
		`setSidebar(false,false)`,
		`body.nav-drawer-open{overflow:hidden}`,
		`.btn-icon,.pw-toggle{min-width:44px;min-height:44px}`,
		`.nav-item{height:auto;min-height:44px}`,
		`className='table-scroll-hint'`,
		`wrap.setAttribute('role','region')`,
		`wrap.removeAttribute('tabindex')`,
		`wrap.removeAttribute('role')`,
		`if(hint.textContent!==message)hint.textContent=message`,
		`const hasActions=table.hasAttribute('data-sticky-actions')`,
		`data-sticky-actions`,
		`table.classList.toggle('has-sticky-actions',hasActions)`,
		`let tableEnhanceFrame=0`,
		`if(tableEnhanceFrame)return`,
		`const tableObserver=new MutationObserver(scheduleTableEnhancement)`,
		`.tbl.has-sticky-actions th:last-child,.tbl.has-sticky-actions td:last-child`,
		`position:sticky;bottom:0;z-index:2;flex:0 0 auto`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin mobile accessibility contract is missing %q", want)
		}
	}

	for _, forbidden := range []string{
		`$('btnMenu').onclick=()=>$('sidebar').classList.toggle('open')`,
		`$('sideMask').onclick=()=>$('sidebar').classList.remove('open')`,
		`body:has(aside.open)`,
		`requestAnimationFrame(()=>updateScrollableTable(wrap))`,
		`last&&last.querySelector('button,a,.btn')`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("admin mobile navigation still contains unmanaged state %q", forbidden)
		}
	}
}

func TestPortalMobileNavigationUsesManagedDrawerState(t *testing.T) {
	html := string(PortalHTML)
	for _, want := range []string{
		`aria-controls="sidebar"`,
		`aria-expanded="false"`,
		`aria-label="用户门户导航"`,
		`function setSidebar(open,returnFocus)`,
		`function syncSidebarMode()`,
		`document.body.classList.toggle('nav-drawer-open',next)`,
		`main.inert=next`,
		`$('sidebar').inert=mobile&&!next`,
		`mask.inert=!next;mask.disabled=!next;mask.tabIndex=next?0:-1`,
		`mask.inert=!open;mask.disabled=!open;mask.tabIndex=open?0:-1`,
		`disabled tabindex="-1" inert`,
		`if(mobile&&!open&&$('sidebar').contains(document.activeElement)){`,
		`if(next)requestAnimationFrame(focusSidebarEntry)`,
		`$('sideMask').onclick=()=>setSidebar(false,true)`,
		`sidebarMQ.addEventListener('change',syncSidebarMode)`,
		`body.nav-drawer-open{overflow:hidden}`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("portal mobile navigation contract is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`$('btnMenu').onclick = () => $('sidebar').classList.toggle('open')`,
		`$('sideMask').onclick = () => $('sidebar').classList.remove('open')`,
		`body:has(aside.open)`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("portal mobile navigation still contains unmanaged state %q", forbidden)
		}
	}
}

func TestPortalUltraWideLayoutUsesAvailableCanvas(t *testing.T) {
	html := string(PortalHTML)
	for _, want := range []string{
		"@media(min-width:2560px)",
		".content{max-width:3200px",
		".stats{grid-template-columns:repeat(4,minmax(300px,1fr));gap:var(--sp-5);max-width:none",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("portal ultra-wide layout missing %q", want)
		}
	}
}

func TestAdminRevenueDashboardUsesSupportedCurrenciesAndRealTimeseries(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`revenue:{currency:'CNY',days:30,metric:'amount'}`,
		`if(!['CNY','USD'].includes(rs.currency))rs.currency='CNY'`,
		`api('/v1/revenue/timeseries?currency='+rs.currency+'&days='+rs.days)`,
		`api('/v1/stats/timeseries?days='+rs.days)`,
		`[['amount','金额'],['orders','订单量']]`,
		`['7','30','90']`,
		`function orderVolumeSVG(points)`,
		`订单量来自审计事件`,
		`不代表已支付或已履约订单数`,
		`amountMode&&can('billing.adjustment.write')`,
		`.rev-controls{display:grid;grid-template-columns:minmax(0,1fr)`,
		`.content{max-width:3200px`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin revenue dashboard is missing %q", want)
		}
	}

	start := strings.Index(html, `async function viewOverview(v){`)
	if start < 0 {
		t.Fatal("admin overview renderer not found")
	}
	end := strings.Index(html[start:], `function revenueSVG(points,currency){`)
	if end < 0 {
		t.Fatal("admin overview renderer boundary not found")
	}
	overview := html[start : start+end]
	for _, forbidden := range []string{"EUR", "GBP", "JPY"} {
		if strings.Contains(overview, forbidden) {
			t.Fatalf("admin revenue controls expose unsupported currency %q", forbidden)
		}
	}
}

func TestAdminCatalogUsesAuthoritativePlanLifecycle(t *testing.T) {
	html := string(ConsoleHTML)
	start := strings.Index(html, `async function viewPlans(v){`)
	if start < 0 {
		t.Fatal("admin plan renderer not found")
	}
	end := strings.Index(html[start:], `//---------------------------------------------------------------- 支付渠道`)
	if end < 0 {
		t.Fatal("admin plan renderer boundary not found")
	}
	catalog := html[start : start+end]

	for _, want := range []string{
		`id="planCreate"`,
		`function openPlanEditor(planId)`,
		`function openPlanManager(planId)`,
		`function openPlanVersionEditor(plan,version)`,
		`function openPlanPriceEditor(plan)`,
		`api('/v1/plans/'+planId)`,
		`api('/v1/plans/'+planId+'/versions'`,
		`'/v1/plans/'+planId+'/versions/'+version.id+'/publish'`,
		`'/v1/plans/'+plan.id+'/prices'`,
		`'/v1/plans/'+planId+'/prices/'+price.id+'/archive'`,
		`'/v1/plans/'+planId+'/archive'`,
		`openPlanPools(b.dataset.pools)`,
		`headers:{'Idempotency-Key':idemKey()}`,
		`expected_row_version`,
		`expected_plan_row_version`,
		`expected_version_row_version`,
		`can('catalog.write')`,
		`can('catalog.publish')`,
		`can('catalog.publish')&&plan.status!=='archived'?'<button class="btn btn-ghost" id="planEdit">`,
		`can('iam.user.read')?api('/v1/user-groups'):Promise.resolve({groups:[]})`,
		`if(groupOption&&!canReadGroups)groupOption.disabled=true`,
		`(cur.visible_group_ids||[])`,
		`data-sticky-actions`,
		`['public','公开']`,
		`<option value="CNY">CNY</option><option value="USD">USD</option>`,
	} {
		if !strings.Contains(catalog, want) {
			t.Fatalf("admin catalog lifecycle is missing %q", want)
		}
	}

	for _, forbidden := range []string{
		`'/v1/plans/'+p.id+'/status'`,
		`'/v1/plans/'+planId+'/status'`,
		`method:'DELETE'`,
	} {
		if strings.Contains(catalog, forbidden) {
			t.Fatalf("admin catalog still uses unsupported lifecycle call %q", forbidden)
		}
	}
}

func TestAdminOrderDetailShowsPaymentAndRefundEvidenceWithoutMutation(t *testing.T) {
	html := string(ConsoleHTML)
	start := strings.Index(html, `async function viewOrders(v){`)
	if start < 0 {
		t.Fatal("admin order renderer not found")
	}
	end := strings.Index(html[start:], `//---------------------------------------------------------------- 套餐`)
	if end < 0 {
		t.Fatal("admin order renderer boundary not found")
	}
	orders := html[start : start+end]
	for _, want := range []string{
		`data-order-detail=`,
		`async function openOrderDetail(orderId)`,
		`api('/v1/orders/'+orderId)`,
		`can('billing.payment.read')`,
		`api('/v1/orders/'+orderId+'/payments')`,
		`paymentHistory.payment_intents||[]`,
		`paymentHistory.payments||[]`,
		`paymentHistory.refunds||[]`,
		`订单生命周期`,
		`o.fulfilled_at`,
		`o.subscription_id`,
		`o.cancel_reason`,
		`支付方式`,
		`x.method||'—'`,
		`渠道支付号和退款证据已隐藏`,
		`退款必须走独立申请、审批、渠道确认和权益回收流程`,
		`data-sticky-actions`,
		`can('billing.order.write')`,
		`['draft','pending_payment','processing'].includes(o.status)`,
		`'/v1/orders/'+orderId+'/cancel'`,
		`headers:{'Idempotency-Key':idemKey()}`,
		`expected_state_version:o.state_version`,
		`if(e.status===409){m.remove();openOrderDetail(orderId)}`,
	} {
		if !strings.Contains(orders, want) {
			t.Fatalf("admin order evidence view is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`method:'PUT'`, `method:'DELETE'`, `'/refund'`, `'/status'`,
	} {
		if strings.Contains(orders, forbidden) {
			t.Fatalf("admin order evidence view contains unsafe mutation %q", forbidden)
		}
	}
}

func TestAdminCouponUIUsesMarketingPermissionAndOptionalCatalog(t *testing.T) {
	html := string(ConsoleHTML)
	start := strings.Index(html, `async function viewCoupons(v){`)
	if start < 0 {
		t.Fatal("admin coupon renderer not found")
	}
	end := strings.Index(html[start:], `async function viewCommission(v){`)
	if end < 0 {
		t.Fatal("admin coupon renderer boundary not found")
	}
	coupons := html[start : start+end]
	for _, want := range []string{
		`data-page="coupons" data-perm="marketing.coupon.write"`,
		`can('catalog.read') ? api('/v1/plans') : Promise.resolve({plans:[]})`,
		`can('billing.order.read')?'<button`,
		`can('marketing.coupon.write') && c.status`,
		`can('marketing.coupon.write')?'<button`,
		// 批量生成一次能造出上千张能减钱的券，入口必须和单张创建
		// 受同一个写权限保护，不能因为放在同一行就漏掉。
		`id="cpBatch"`,
		`openCouponForm(pl.plans || [], true)`,
	} {
		if !strings.Contains(html, want) && !strings.Contains(coupons, want) {
			t.Fatalf("admin coupon UI is missing %q", want)
		}
	}
	if strings.Contains(coupons, `billing.provider.write`) {
		t.Fatal("coupon UI still depends on payment-provider write permission")
	}
}

func TestAdminPlanPoolBindingUsesDraftCASContract(t *testing.T) {
	html := string(ConsoleHTML)
	start := strings.Index(html, `async function openPlanPools(planId){`)
	if start < 0 {
		t.Fatal("admin plan pool editor not found")
	}
	end := strings.Index(html[start:], `async function openPoolManager(){`)
	if end < 0 {
		t.Fatal("admin plan pool editor boundary not found")
	}
	editor := html[start : start+end]

	for _, want := range []string{
		`can('catalog.publish')`,
		`data.editable`,
		`data.version_id`,
		`Number(data.row_version)>0`,
		`(editable?'':' disabled')`,
		`if(!editable){save.remove();return}`,
		`headers:{'Idempotency-Key':idemKey()}`,
		`version_id:data.version_id`,
		`expected_version_row_version:data.row_version`,
		`pool_ids:ids`,
	} {
		if !strings.Contains(editor, want) {
			t.Fatalf("admin plan pool binding is missing %q", want)
		}
	}
	if strings.Contains(editor, `body: JSON.stringify({pool_ids: ids})`) {
		t.Fatal("admin plan pool binding still sends the obsolete pool-only body")
	}
}

func TestAdminCatalogResponsiveModalLayout(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`.grid2{display:grid;grid-template-columns:repeat(2,minmax(0,1fr))`,
		`.check-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr))`,
		`.grid2,.check-grid{grid-template-columns:minmax(0,1fr)}`,
		`.modal.wide{max-width:1120px}`,
		`requestAnimationFrame(()=>enhanceScrollableTables(m))`,
		`<table class="tbl" data-sticky-actions>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin catalog responsive modal layout is missing %q", want)
		}
	}
}

func TestAdminAPISerializesOnlyPlainObjectBodies(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`let body=opts.body`,
		`typeof body==='object'`,
		`!Array.isArray(body)`,
		`Object.getPrototypeOf(body)===Object.prototype`,
		`if(plainBody)body=JSON.stringify(body)`,
		`if(!hasContentType&&(plainBody||typeof body==='string'))h['Content-Type']='application/json'`,
		`fetch(ADMIN_BASE+path,{method:opts.method||'GET',headers:h,body})`,
		`body:{mode:m, grace:g}`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin API body serialization guard is missing %q", want)
		}
	}
	if strings.Contains(html, `body:opts.body`) {
		t.Fatal("admin API still forwards an unnormalized request body")
	}
}

func TestPortalAPISerializesOnlyPlainObjectBodies(t *testing.T) {
	html := string(PortalHTML)
	for _, want := range []string{
		`let body = opts.body`,
		`typeof body === 'object'`,
		`!Array.isArray(body)`,
		`Object.getPrototypeOf(body) === Object.prototype`,
		`if (plainBody) body = JSON.stringify(body)`,
		`if (!hasContentType && (plainBody || typeof body === 'string'))`,
		`fetch(path, {method: opts.method || 'GET', headers: h, body})`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("portal API body serialization guard is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`Object.assign({'Content-Type':'application/json'}, opts.headers || {})`,
		`body: opts.body`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("portal API still contains unsafe body handling %q", forbidden)
		}
	}
}

func TestBrowserLogoutRevokesServerSessionBeforeClearingLocalToken(t *testing.T) {
	for _, tc := range []struct {
		name, html, clearCall, clearFunction string
	}{
		{"admin", string(ConsoleHTML), `clearAdminAuthentication()`, `function clearAdminAuthentication(){`},
		{"portal", string(PortalHTML), `clearPortalAuthentication()`, `function clearPortalAuthentication(){`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			html := tc.html
			start := strings.Index(html, `$('btnLogout').onclick`)
			if start < 0 {
				t.Fatal("logout handler is missing")
			}
			end := start + 900
			if end > len(html) {
				end = len(html)
			}
			handler := html[start:end]
			logout := strings.Index(handler, `'/v1/auth/logout'`)
			clear := strings.Index(handler, tc.clearCall)
			if logout < 0 {
				t.Fatal("server logout request is missing")
			}
			if clear < 0 || logout > clear {
				t.Fatal("local token is cleared before the server session is revoked")
			}
			clearStart := strings.Index(html, tc.clearFunction)
			if clearStart < 0 || !strings.Contains(html[clearStart:clearStart+240], `localStorage.removeItem(TK)`) {
				t.Fatal("authentication clearing helper does not remove the local token")
			}
			if !strings.Contains(handler, `if(e.status!==401)`) && !strings.Contains(handler, `if (e.status !== 401)`) {
				t.Fatal("logout does not distinguish an already-invalid credential")
			}
		})
	}
}

func TestAdminNodeTokenSnippetsUseSafeSavedConfiguration(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`function shellQuote(s)`,
		`if(!r.install_command) throw new Error`,
		`esc(r.install_command)`,
		`if(!r.panel_url) throw new Error`,
		`url: "'+r.panel_url+'"`,
		`node_type: "'+(n.node_type||'')+'"`,
		`const qnodeKernel=n.kernel==='xray-core'?'xray':(n.kernel==='sing-box'?'singbox':'native')`,
		`<option value="pandora-native">Pandora NativeCore</option>`,
		`if(Number(protocolConfig.tls)===2||protocolConfig.security==='reality')m.querySelector('#neKernel').value='pandora-native'`,
		`id="ndTokenOutput"`,
		`if(previous)previous.outerHTML=output`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("safe node token snippet is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`shellQuote(location.origin)`,
		`location.origin.replace(/:\d+$/,':9003')`,
		`node_type: "'+(npType.value||n.node_type||'')+'"`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("node token snippet still contains unsafe or unsaved source %q", forbidden)
		}
	}
}

func TestAdminDowngradedProtocolRemainsExplicitlyReadOnly(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`n&&n.node_type&&!protocolSchema(data.schemas,n.node_type)`,
		`n.node_type&&!protocolSchema(schemas,n.node_type)`,
		`legacy-read-compatible':'旧版只读'`,
		`'pending-certificate-lifecycle':'待证书生命周期'`,
		`selected disabled`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("read-only downgraded protocol guard is missing %q", want)
		}
	}
}

func TestEmbeddedConsolesDoNotRequestMissingFavicon(t *testing.T) {
	for name, html := range map[string]string{
		"admin":  string(ConsoleHTML),
		"portal": string(PortalHTML),
	} {
		if !strings.Contains(html, `<link rel="icon" href="data:image/svg+xml,`) {
			t.Fatalf("%s console must embed its favicon", name)
		}
	}
}

func TestRegistrationModeUIIsDefaultClosedAndAdminControlled(t *testing.T) {
	portal := string(PortalHTML)
	for _, want := range []string{
		`id="registerTab" data-tab="register"`,
		`aria-disabled="true" disabled`,
		`id="tabRegister" class="hide" autocomplete="on" aria-hidden="true" inert`,
		`let registrationMode = 'closed'`,
		`if (!['closed','invite_only','open'].includes(c.registration_mode))`,
		`registrationMode = 'closed'`,
		`registerForm.setAttribute('inert', '')`,
		`registrationMode === 'invite_only'`,
		`inviteInput.required = inviteRequired`,
		`inviteInput.setAttribute('aria-required', String(inviteRequired))`,
		`startButton.before(inviteField)`,
		`invite_code: inviteCode`,
	} {
		if !strings.Contains(portal, want) {
			t.Fatalf("portal registration policy contract is missing %q", want)
		}
	}

	// 站点配置必须在展示注册 UI 之前加载完，否则注册表单会先按默认的
	// closed 渲染再跳变，甚至让依赖 registrationMode 的逻辑读到未初始化的值。
	//
	// 这里不锁调用形式：原先断言的是字面的 await loadSiteConfig()，后来
	// 改成 await Promise.all([loadSiteConfig(), loadAppearance()]) 并行拉，
	// 约束一样成立，测试却红了。改成从 boot 函数体里找，await 和并行都认，
	// 去掉 await 才红。
	boot := portal[strings.Index(portal, "async function boot()"):]
	// 函数体里没有 boot(); 这个串，第一个出现的就是它自己的调用点。
	if end := strings.Index(boot, "boot();"); end > 0 {
		boot = boot[:end]
	}
	if !strings.Contains(boot, "loadSiteConfig()") {
		t.Fatal("boot 没有加载站点配置")
	}
	if !strings.Contains(boot, "await") {
		t.Fatal("boot 里的站点配置加载没有被 await，注册 UI 会先按默认值渲染")
	}

	admin := string(ConsoleHTML)
	for _, want := range []string{
		`id="mRegistrationMode"`,
		`<option value="closed"`,
		`<option value="invite_only"`,
		`<option value="open"`,
		`registration_mode: $('mRegistrationMode').value`,
	} {
		if !strings.Contains(admin, want) {
			t.Fatalf("admin registration policy contract is missing %q", want)
		}
	}
}

func TestAdminNodeProtocolEditorIsSchemaDrivenAndDoesNotRenderStoredSecrets(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`api('/v1/node-protocol-schemas')`,
		`schema.allowed_properties||[]`,
		`schema.sensitive_properties||[]`,
		`type="password"`,
		`autocomplete="new-password"`,
		`切换协议会清空不兼容参数`,
		`旧配置继续运行且不会在表单中回显`,
		`readProtocolConfigFields`,
		`Number(protocolConfig.tls)===2||protocolConfig.security==='reality'`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("schema-driven node protocol editor is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`id="npProto"`,
		`id="neMethod"`,
		`protocol_config:{method:`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("node protocol editor still contains fixed/raw field %q", forbidden)
		}
	}

	// The password control is intentionally created without a value attribute.
	// Existing sensitive values may only be preserved in the request closure;
	// they must never be interpolated into the DOM.
	start := strings.Index(html, `if(sensitive.has(key)){`)
	if start < 0 {
		t.Fatal("sensitive protocol field renderer start not found")
	}
	// 结束锚点是敏感分支之后那段「普通文本框」的开头。字段值现在按点号
	// 路径取（protoGet），不再是 cfg[key]。
	end := strings.Index(html[start:], `const plain=protoGet(cfg,key);`)
	if end < 0 {
		t.Fatal("sensitive protocol field renderer boundary not found")
	}
	sensitiveRenderer := html[start : start+end]
	if strings.Contains(sensitiveRenderer, `value="`) {
		t.Fatal("sensitive protocol field renderer must not emit a value attribute")
	}
}

func TestPortalChangePasswordMatchesRegistrationPolicyAndIsAccessible(t *testing.T) {
	html := string(PortalHTML)
	for _, want := range []string{
		`至少8位且同时包含字母和数字`,
		`id="togglePwdNew" type="button" aria-label="显示密码"`,
		`aria-controls="pwdNew" aria-pressed="false"`,
		`id="togglePwdConfirm" type="button" aria-label="显示密码"`,
		`aria-controls="pwdConfirm" aria-pressed="false"`,
		`function bindPasswordToggle(button, input, eye)`,
		`button.setAttribute('aria-label', label)`,
		`button.setAttribute('aria-pressed', String(show))`,
		`passwordLetterRE=new RegExp('\\p{L}','u')`,
		`passwordDigitRE=new RegExp('\\p{Nd}','u')`,
		`if(!passwordLetterRE||!passwordDigitRE)`,
		`function utf8ByteLength(value)`,
		`if(typeof TextEncoder==='function')return new TextEncoder().encode(value).length`,
		`function passwordPolicyError(password)`,
		`Array.from(password).length < 8`,
		`utf8ByteLength(password)>256`,
		`!passwordLetterRE.test(password)||!passwordDigitRE.test(password)`,
		`id="regPassPolicy" aria-live="polite"`,
		`$('regPass').addEventListener('input',updateRegistrationPasswordState)`,
		`newInput.addEventListener('input', updatePasswordState)`,
		`confirmInput.addEventListener('input', updatePasswordState)`,
		`const policyError = passwordPolicyError(newPassword)`,
		`if (newPassword !== confirmPassword)`,
		`aria-describedby="pwdOldError" aria-invalid="false"`,
		`oldError.textContent='当前密码必填'`,
		`oldInput.setAttribute('aria-invalid','true')`,
		`oldInput.focus()`,
		`if(submitting&&!force)return`,
		`m.querySelectorAll('[data-close]').forEach(b => b.onclick = () => close())`,
		`m.querySelectorAll('[data-close]').forEach(b=>b.disabled=true)`,
		`dialog.setAttribute('aria-busy','true')`,
		`close(true)`,
		`clearPortalAuthentication()`,
		`密码已修改，请使用新密码重新登录`,
		`.btn,.btn-icon,.pw-toggle{min-height:44px}`,
		`.btn-icon,.pw-toggle{min-width:44px}`,
		`api('/v1/me/password'`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("portal change-password accessibility contract is missing %q", want)
		}
	}

	start := strings.Index(html, `function openChangePasswordModal(){`)
	if start < 0 {
		t.Fatal("change-password modal function not found")
	}
	end := strings.Index(html[start:], `const pwBtn = document.getElementById('btnChangePassword')`)
	if end < 0 {
		t.Fatal("change-password modal function boundary not found")
	}
	modal := html[start : start+end]
	for _, want := range []string{
		`id="pwdOld" type="password" autocomplete="current-password" required`,
		`required aria-describedby="pwdPolicy" aria-invalid="false"`,
		`required aria-describedby="pwdMatch" aria-invalid="false"`,
		`bindPasswordToggle(m.querySelector('#togglePwdNew'), newInput, m.querySelector('#pwdNewEye'))`,
		`bindPasswordToggle(m.querySelector('#togglePwdConfirm'), confirmInput, m.querySelector('#pwdConfirmEye'))`,
		`new_password: newPassword`,
	} {
		if !strings.Contains(modal, want) {
			t.Fatalf("change-password modal is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`(?=.*[A-Za-z])(?=.*[0-9])`,
		`!/[A-Za-z]/.test(password)`,
		`!/[0-9]/.test(password)`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("portal password policy still contains incompatible contract %q", forbidden)
		}
	}
}

func TestAdminChangePasswordIsAccessibleAndForcesReauthentication(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`id="btnChangePassword" title="修改密码" aria-label="修改密码"`,
		`function adminUTF8ByteLength(value)`,
		`function openAdminChangePassword()`,
		`id="adminPasswordForm"`,
		`autocomplete="current-password"`,
		`autocomplete="new-password"`,
		`至少8位且同时包含字母和数字`,
		`adminUTF8ByteLength(password)>256`,
		`new RegExp('\\p{L}','u')`,
		`new RegExp('\\p{Nd}','u')`,
		`api('/v1/me/password'`,
		`old_password:oldInput.value`,
		`new_password:newInput.value`,
		`clearAdminAuthentication()`,
		`localStorage.removeItem(TK)`,
		`密码已修改，请使用新密码重新登录`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin change-password contract is missing %q", want)
		}
	}
}

func TestAdminDashboardTrafficUsesIndependentStatePermissionsAndSharedSnapshot(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`dashboardTraffic:{range:'7d',limit:10,tab:'nodes'}`,
		`if(!['24h','7d','30d'].includes(ds.range))ds.range='7d'`,
		`if(![5,10,20].includes(ds.limit))ds.limit=10`,
		`if(kind==='nodes')return can('metering.read')&&can('node.read')`,
		`if(kind==='users')return can('metering.read')&&can('iam.user.read')`,
		`return can('ops.notification.read')`,
		`const permitted=['nodes','users'].filter(dashboardAllowed)`,
		`if(!dashboardAllowed('backlog'))return`,
		`'/v1/dashboard/traffic/'+kind+'?range='`,
		`api('/v1/dashboard/backlog/notifications')`,
		`const anchor=firstResult.status==='fulfilled'&&firstResult.value&&firstResult.value.snapshot_at||''`,
		`api(dashboardTrafficPath(second,anchor))`,
		`await Promise.allSettled(tasks)`,
		`未请求通知积压：缺少 `,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin DASH-01 permission/snapshot contract is missing %q", want)
		}
	}

	stateStart := strings.Index(html, `const state={`)
	overviewStart := strings.Index(html, `async function viewOverview(v){`)
	if stateStart < 0 || overviewStart < 0 || stateStart >= overviewStart {
		t.Fatal("admin state or overview boundary not found")
	}
	stateBlock := html[stateStart:overviewStart]
	if strings.Contains(stateBlock, `revenue:{currency:'CNY',days:30,metric:'amount',range:`) {
		t.Fatal("dashboard traffic state is incorrectly coupled to revenue state")
	}
}

func TestAdminDashboardByteFormattingIsBigIntSafeAndUsersStayMasked(t *testing.T) {
	html := string(ConsoleHTML)
	start := strings.Index(html, `function dashboardBytes(value){`)
	end := strings.Index(html[start:], `function dashboardCount(value){`)
	if start < 0 || end < 0 {
		t.Fatal("dashboard byte formatter boundary not found")
	}
	formatter := html[start : start+end]
	for _, want := range []string{
		`const DASHBOARD_BYTE_RE=/^(0|[1-9][0-9]*)$/`,
		`const exact=groupDashboardDecimal(decimal)+' B'`,
		`if(typeof BigInt!=='function')return exact`,
		`let amount=BigInt(decimal)`,
		`fraction=(amount%divisor)*100n/divisor`,
		`return exact+'（'`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin DASH-01 byte formatter is missing %q", want)
		}
	}
	for _, forbidden := range []string{`Number(`, `parseInt(`, `parseFloat(`} {
		if strings.Contains(formatter, forbidden) {
			t.Fatalf("dashboard byte formatter risks precision loss via %q", forbidden)
		}
	}

	tableStart := strings.Index(html, `function dashboardTrafficTable(kind,items){`)
	tableEnd := strings.Index(html[tableStart:], `function renderDashboardTrafficCard(){`)
	if tableStart < 0 || tableEnd < 0 {
		t.Fatal("dashboard traffic table boundary not found")
	}
	table := html[tableStart : tableStart+tableEnd]
	if !strings.Contains(table, `item.email_masked||'***'`) {
		t.Fatal("dashboard user ranking does not render the masked email field")
	}
	for _, forbidden := range []string{`item.email||`, `item.email)`, `item.full_email`, `me.email`} {
		if strings.Contains(table, forbidden) {
			t.Fatalf("dashboard masked renderer can expose full PII via %q", forbidden)
		}
	}
}

func TestAdminDashboardNodeRankingKeepsUnattributedTraffic(t *testing.T) {
	html := string(ConsoleHTML)
	if !strings.Contains(html, `const allUnattributed=kind==='users'&&totals.reported_bytes!=='0'&&totals.attributed_bytes==='0'`) {
		t.Fatal("fully unattributed traffic may suppress the user ranking only; node ranking must remain visible")
	}
}

func TestAdminDashboardLocalStatesResponsiveLayoutAndAccessibility(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		`dashboard-ops-grid{display:grid;grid-template-columns:minmax(0,2fr) minmax(320px,1fr)`,
		`.dashboard-ops-grid>.card{min-width:0`,
		`@media(max-width:1180px){.dashboard-ops-grid{grid-template-columns:minmax(0,1fr)}}`,
		`.dashboard-summary{grid-template-columns:repeat(2,minmax(0,1fr));padding:var(--sp-4)}`,
		`class="tbl-wrap"><table class="tbl dashboard-traffic-table"`,
		`role="tablist" aria-label="流量排行对象"`,
		`role="tabpanel" aria-labelledby="dashboardTab-`,
		`tabindex="'+(ds.tab===kind?'0':'-1')+'"`,
		`if(event.key==='ArrowRight')`,
		`else if(event.key==='ArrowLeft')`,
		`else if(event.key==='Home')`,
		`else if(event.key==='End')`,
		`aria-label="流量排行时间范围"`,
		`aria-label="流量排行数量"`,
		`aria-label="重试通知积压"`,
		`aria-label="刷新通知积压"`,
		`role="alert" aria-live="assertive"`,
		`role="status" aria-live="polite"`,
		`所选时间范围暂无非零严格有效流量条目`,
		`全部上报流量均未归属`,
		`const ranking=allUnattributed?''`,
		`部分上报含无效数据`,
		`无超阈值到期积压`,
		`通知已积压：最大等待 `,
		`未记录 worker 心跳，当前不可观测`,
		`data.processor_state==='unobservable'`,
		`不是计费账本`,
		`不代表 worker 在线`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin DASH-01 UI/accessibility contract is missing %q", want)
		}
	}
}

// portal 里不能出现两个同名的顶层函数。
//
// 这条守的是一个真实踩过的坑：深浅色切换和站点主题注入曾经都叫
// applyTheme。函数声明会被提升，后定义的直接把前面那个覆盖掉——
// 切换按钮传进去的是 'dark' / 'light' 字符串，而接手的那个函数按主题
// 对象来读，取不到 tokens 就悄悄返回。结果是按钮点了没反应、
// localStorage 里存的偏好也读不出来，页面全靠 <html data-theme="dark">
// 这个硬编码撑着，而这一切没有任何报错。
func TestPortalHasNoDuplicateTopLevelFunctions(t *testing.T) {
	portal := string(PortalHTML)
	seen := map[string]int{}
	for _, line := range strings.Split(portal, "\n") {
		if !strings.HasPrefix(line, "function ") {
			continue // 只看顶层：缩进过的是嵌套定义，作用域各自独立
		}
		name := strings.TrimPrefix(line, "function ")
		if i := strings.IndexAny(name, "("); i > 0 {
			name = strings.TrimSpace(name[:i])
			seen[name]++
		}
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("顶层函数 %q 定义了 %d 次，后面的会静默覆盖前面的", name, n)
		}
	}
}

// 深浅色切换与站点主题必须是两个函数，各自被正确调用。
func TestPortalColorSchemeAndSiteThemeAreSeparate(t *testing.T) {
	portal := string(PortalHTML)
	for _, want := range []string{
		"function applyColorScheme(",
		"function applySiteTheme(",
		"applyColorScheme(localStorage.getItem(THK)", // 启动时读偏好
		"applySiteTheme(appearance.theme)",           // 拉到外观后注入
	} {
		if !strings.Contains(portal, want) {
			t.Errorf("portal 缺少 %q", want)
		}
	}
	// 切换按钮必须走深浅色那条，不能再指向主题注入
	if !strings.Contains(portal, "btnTheme').onclick = () =>\n  applyColorScheme(") {
		t.Error("主题切换按钮没有绑定到 applyColorScheme")
	}
}

// 节点编辑表单要按当前传输过滤字段。
//
// 不过滤的话，vless 的 allowed_properties 有三十多项，会一次性全堆出来，
// 其中大半和当前传输无关。更糟的是后端会拒绝它们——「mtu 只在
// network=mkcp 时有效」——于是表单里能填、一保存就报错。
func TestAdminNodeFormFiltersFieldsByNetwork(t *testing.T) {
	html := string(ConsoleHTML)
	for _, want := range []string{
		"const ND_FIELD_NETWORKS=",
		"function ndFieldAppliesTo(key,network)",
		"function ndNormalizeNetwork(v)",
		".filter(k=>ndFieldAppliesTo(k,currentNetwork))",
		"function bindProtocolNetworkSwitch(root,schema,config)",
		"bindProtocolNetworkSwitch(root,schema,config)",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("节点表单的传输过滤缺少 %q", want)
		}
	}

	// mKCP 的九个参数都必须登记归属，否则它们会在 tcp / ws 节点上也冒出来
	for _, key := range []string{
		"mtu:['mkcp']", "tti:['mkcp']", "uplink_capacity:['mkcp']",
		"downlink_capacity:['mkcp']", "congestion:['mkcp']",
		"read_buffer_size:['mkcp']", "write_buffer_size:['mkcp']",
		"mask:['mkcp']", "mask_password:['mkcp']",
	} {
		if !strings.Contains(html, key) {
			t.Errorf("mKCP 字段归属表缺少 %q", key)
		}
	}

	// 切换传输时要保留已填的值，否则改一下传输，刚填的东西全没了
	if !strings.Contains(html, "const kept=Object.assign({},config||{})") {
		t.Error("切换传输时没有保留已填的值")
	}
	// kcp 是客户端配置里的写法，要能对上归属表里的 mkcp
	if !strings.Contains(html, "n==='kcp'||n==='m-kcp'") {
		t.Error("传输名归一化没处理 kcp / m-kcp 别名")
	}
}

// 窄屏收起次要列的 CSS 用 nth-child 定位，列顺序一变就会收错列。
//
// 节点表和订单表各有 11 列，1400px 以下开始逐档收起。收哪一列全靠序号，
// 而序号在表头、表体、CSS 三处各写了一遍 —— 前两处改了、CSS 没跟上时，
// 界面不会报错，只会安静地藏错一列（比如把「操作」藏了，那张表就只能
// 看不能用）。
//
// 这条测试锁住列数。列数变了说明有人动过表结构，此时必须回去核对
// index.html 里那段 @media 注释标注的列名与序号。
func TestWideTablesKeepColumnCountInSyncWithResponsiveCSS(t *testing.T) {
	html := string(ConsoleHTML)

	for _, tc := range []struct {
		name   string
		header string
		want   int
		// conditional 是只在特定模式下渲染的列数。节点表的「排序值」只在
		// 点了「编辑排序」之后才出现，源码里它是一个三元表达式，静态扫描
		// 数得到但正常渲染时并不在表里。CSS 的 nth-child 是按常驻列算的，
		// 所以这里要把它从列数里减掉，否则每次都对不上。
		conditional int
	}{
		{
			name: "节点表",
			// 表头现在跨多行拼接，锚点只取第一列这个完整的字面量。
			header:      `<th><input type="checkbox" id="ndAll" aria-label="全选当前结果"></th>`,
			want:        11,
			conditional: 1,
		},
		{
			name:   "订单表",
			header: `<th>单号</th><th>用户</th><th>类型</th><th>套餐</th><th>周期</th>`,
			want:   11,
		},
	} {
		idx := strings.Index(html, tc.header)
		if idx < 0 {
			t.Errorf("%s 的表头结构变了，找不到锚点；"+
				"改动表格列时请同步核对 index.html 里 @media 那段的列序注释", tc.name)
			continue
		}
		end := strings.Index(html[idx:], "</tr>")
		if end < 0 {
			t.Fatalf("%s 表头没有闭合", tc.name)
		}
		if got := strings.Count(html[idx:idx+end], "<th") - tc.conditional; got != tc.want {
			t.Errorf("%s 现在有 %d 列，之前是 %d 列。"+
				"窄屏收起列的 CSS 按列序号写在 @media 里，请回去核对该收哪几列",
				tc.name, got, tc.want)
		}
	}

	// 操作列任何宽度都不能收 —— 收了这张表就只能看不能用。
	for _, bad := range []string{
		".node-table th:nth-child(11)",
		".order-table th:nth-child(11)",
	} {
		if strings.Contains(html, bad) {
			t.Errorf("%s 会把操作列藏掉，表格将只能看不能用", bad)
		}
	}
}

// 关键字和标识符被粘成一个词，语法检查抓不到。
//
// 生产上出过一次：serverEditorBody 里的
//
//	const head =        →  consthead=
//	return head +       →  returnhead+
//
// 编辑脚本吃掉了换行，三处一起中招。「新增服务器」和「编辑服务器」
// 因此完全失效 —— 点了什么都不发生，日志里也没有任何记录，因为请求
// 压根没发出去。
//
// 而 new Function(code) 的语法检查是绿的：consthead=x 在解析阶段
// 完全合法（就是给一个未声明的变量赋值），要执行到那一行才抛
// ReferenceError。严格模式也一样 —— 它只解析，不执行。
//
// 所以只能按字面扫。白名单里是真实存在的驼峰词，加新词之前先确认
// 它不是又一次粘连。
func TestConsoleHasNoGluedKeywords(t *testing.T) {
	html := string(ConsoleHTML)

	// 这些是代码里真实存在的标识符，不是粘连。
	allowed := map[string]bool{
		"returnFocus": true, "returned": true, "returned_bytes": true,
		"returns": true, "letter": true, "letters": true, "letterSpacing": true,
		"variant": true, "constructor": true, "newValue": true, "newline": true,
		"vars": true, "cases": true, "case_kind": true, "functionality": true,
		// 这两个是正常的驼峰函数名，第一版规则曾经误报过。
		"deleteServer": true, "deleteAdminNode": true,
	}

	for _, kw := range []string{
		"const", "let", "var", "return", "function", "await", "typeof",
		"case", "else", "break", "continue", "delete", "instanceof",
	} {
		re := regexp.MustCompile(`\b` + kw + `([a-z][A-Za-z0-9_]*)`)
		for _, m := range re.FindAllString(html, -1) {
			if allowed[m] {
				continue
			}
			t.Errorf("%q 看起来是 %q 和后面的标识符粘在了一起（少了空格或换行）。"+
				"这类错误语法检查抓不到 —— consthead=x 解析得过，只有执行到才炸。"+
				"确认它是正常标识符的话，加进本测试的白名单", m, kw)
		}
	}
}
