async (page) => {
  const base = '__PANDORA_REG_UI_BASE__';
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  let siteConfig = {status: 200, body: {registration_mode: 'closed', email_verification: true}};
  const registrationStarts = [];
  const adminPosts = [];

  await page.context().route('**/v1/**', async route => {
    const request = route.request();
    const requestURL = request.url();
    const authorityEnd = requestURL.indexOf('/', requestURL.indexOf('://') + 3);
    const pathAndQuery = authorityEnd < 0 ? '/' : requestURL.slice(authorityEnd);
    const pathname = pathAndQuery.split('?', 1)[0];
    if (pathname.endsWith('/v1/site-config')) {
	  if (siteConfig.delay) await page.waitForTimeout(siteConfig.delay);
      return route.fulfill({
        status: siteConfig.status,
        contentType: 'application/json',
		body: JSON.stringify(siteConfig.status === 200 ? siteConfig.body :
          {error: {code: 'unavailable', message: 'unavailable'}}),
      });
    }
    if (pathname.endsWith('/v1/auth/register/start')) {
      registrationStarts.push(JSON.parse(request.postData() || '{}'));
      return route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify({
		registration_token: 'gate-token', expires_at: '2030-01-01T00:00:00Z',
		verification_required: true,
      })});
    }
    if (pathname.endsWith('/v1/settings/mail') && request.method() === 'GET') {
		return route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify({
        smtp_host: 'smtp.example.com', smtp_port: 465, encryption: 'ssl',
        smtp_username: 'mailer', has_password: true, from_address: 'noreply@example.com',
		  from_name: 'Pandora', email_verification: true, registration_mode: 'invite_only',
		})});
    }
    if (pathname.endsWith('/v1/settings/mail') && request.method() === 'POST') {
      adminPosts.push(JSON.parse(request.postData() || '{}'));
		return route.fulfill({status: 200, contentType: 'application/json', body: '{"ok":true}'});
    }
		return route.fulfill({status: 200, contentType: 'application/json', body: '{}'});
  });

  const cases = [
    {name: 'closed', cfg: {status: 200, body: {registration_mode: 'closed', email_verification: true}},
      invite: 'HIDDEN88', visible: false},
    {name: 'config_failure', cfg: {status: 503, body: {}}, invite: 'HIDDEN88', visible: false},
    {name: 'malformed', cfg: {status: 200, body: {registration_mode: 'surprise', email_verification: false}},
      invite: 'HIDDEN88', visible: false},
    {name: 'open', cfg: {status: 200, body: {registration_mode: 'open', email_verification: true}},
      invite: '', visible: true, required: false},
    {name: 'invite_only_without_code',
      cfg: {status: 200, body: {registration_mode: 'invite_only', email_verification: true}},
      invite: '', visible: false},
    {name: 'invite_only_with_code',
      cfg: {status: 200, body: {registration_mode: 'invite_only', email_verification: true}},
      invite: 'VALID888', visible: true, required: true},
  ];
  const widths = [390, 768, 1024, 1920, 3840];
  const captures = [];

	// The parser has loaded but site policy is still pending: registration must
	// already be hidden, disabled, aria-hidden and inert before the response.
	await page.setViewportSize({width: 390, height: 844});
	siteConfig = {status: 200, delay: 350,
	  body: {registration_mode: 'open', email_verification: true}};
	let delayedResponse = page.waitForResponse(response => response.url().includes('/v1/site-config'));
	await page.goto(base + '/', {waitUntil: 'domcontentloaded'});
	assert(!(await page.locator('#registerTab').isVisible()), 'pending:tab_visible');
	assert(await page.locator('#registerTab').isDisabled(), 'pending:tab_enabled');
	assert(await page.locator('#registerTab').getAttribute('aria-disabled') === 'true',
	  'pending:tab_aria_enabled');
	assert(await page.locator('#tabRegister').getAttribute('aria-hidden') === 'true',
	  'pending:form_aria_visible');
	assert(await page.locator('#tabRegister').evaluate(element => element.inert),
	  'pending:form_not_inert');
	await delayedResponse;
	await page.waitForFunction(() => document.getElementById('registerTab')?.getAttribute('aria-disabled') === 'false');
	assert(await page.locator('#registerTab').isVisible(), 'pending:open_not_applied_after_response');

  for (const width of widths) {
    await page.setViewportSize({width, height: width <= 768 ? 844 : 1080});
    for (const item of cases) {
      siteConfig = item.cfg;
      const responseWait = page.waitForResponse(response => response.url().includes('/v1/site-config'));
      await page.goto(base + '/' + (item.invite ? '?invite=' + item.invite : ''),
        {waitUntil: 'domcontentloaded'});
      await responseWait;
      await page.waitForTimeout(40);

      const tab = page.locator('#registerTab');
      const form = page.locator('#tabRegister');
      const invite = page.locator('#regInvite');
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1),
        `${item.name}:${width}:overflow`);
      assert(await tab.isVisible() === item.visible, `${item.name}:${width}:tab_visibility`);
      assert((await tab.isDisabled()) === !item.visible, `${item.name}:${width}:tab_disabled`);
	  assert(await tab.getAttribute('aria-disabled') === String(!item.visible),
		`${item.name}:${width}:tab_aria_disabled`);
	  assert(await form.getAttribute('aria-hidden') === String(!item.visible),
		`${item.name}:${width}:form_aria_hidden`);
      if (!item.visible) {
        assert(await form.evaluate(element => element.inert), `${item.name}:${width}:form_not_inert`);
        assert(!(await form.isVisible()), `${item.name}:${width}:form_visible`);
		await page.locator('#regEmail').focus();
		assert(!(await form.evaluate(element => element.contains(document.activeElement))),
		  `${item.name}:${width}:closed_control_focusable`);
		assert(await page.evaluate(() => document.activeElement?.id === 'loginTab'),
		  `${item.name}:${width}:focus_not_on_login`);
      } else {
		assert(!(await form.evaluate(element => element.inert)), `${item.name}:${width}:form_inert`);
        assert((await invite.getAttribute('aria-required')) === String(Boolean(item.required)),
          `${item.name}:${width}:invite_aria_required`);
        assert((await invite.evaluate(element => element.required)) === Boolean(item.required),
          `${item.name}:${width}:invite_required`);
        if (item.invite) {
          assert(await form.isVisible(), `${item.name}:${width}:deep_link_not_open`);
          assert(await invite.inputValue() === item.invite, `${item.name}:${width}:invite_not_prefilled`);
        }
      }
      captures.push({width, mode: item.name, visible: item.visible});
    }
  }

  // Prove the invite is present at start, not introduced at completion.
  siteConfig = {status: 200, body: {registration_mode: 'invite_only', email_verification: true}};
  let responseWait = page.waitForResponse(response => response.url().includes('/v1/site-config'));
  await page.goto(base + '/?invite=VALID888', {waitUntil: 'domcontentloaded'});
  await responseWait;
  await page.locator('#regEmail').fill('new@example.com');
  const startWait = page.waitForRequest(request => request.url().includes('/v1/auth/register/start'));
  await page.locator('#btnRegStart').click();
  await startWait;
  assert(registrationStarts.length === 1, 'registration_start_capture_count');
  assert(registrationStarts[0].invite_code === 'VALID888', 'start_invite_not_bound');

	// Open mode permits a manually entered optional invite and must bind it at start.
	siteConfig = {status: 200, body: {registration_mode: 'open', email_verification: true}};
	responseWait = page.waitForResponse(response => response.url().includes('/v1/site-config'));
	await page.goto(base + '/', {waitUntil: 'domcontentloaded'});
	await responseWait;
	await page.locator('#registerTab').click();
	await page.locator('#regEmail').fill('manual@example.com');
	await page.locator('#regInvite').fill('MANUAL88');
	const manualStartWait = page.waitForRequest(request => request.url().includes('/v1/auth/register/start'));
	await page.locator('#btnRegStart').click();
	await manualStartWait;
	assert(registrationStarts.length === 2, 'open_manual_start_capture_count');
	assert(registrationStarts[1].invite_code === 'MANUAL88', 'open_manual_invite_not_bound');

  // Render the real administrator editor and verify round-trip payload.
  await page.goto(base + '/admin-gate/', {waitUntil: 'domcontentloaded'});
  await page.evaluate(async () => { await viewMail(document.getElementById('view')); });
  const selector = page.locator('#mRegistrationMode');
  assert(await selector.evaluate(element => Array.from(element.options).map(option => option.value).join(',')) ===
    'closed,invite_only,open', 'admin_mode_enum');
  assert(await selector.inputValue() === 'invite_only', 'admin_mode_load');
	await selector.evaluate(element => {
	  element.onchange = null;
	  element.value = 'open';
	});
  const postWait = page.waitForRequest(request =>
    request.url().includes('/v1/settings/mail') && request.method() === 'POST');
	await page.evaluate(async () => { await saveMail(); });
  await postWait;
  assert(adminPosts.length === 1 && adminPosts[0].registration_mode === 'open',
    'admin_mode_post');

  return {
    gate: 'pass',
    engine: 'real-playwright-browser',
    responsive_widths: widths,
    mode_cases: cases.map(item => item.name),
    registration_start_bound_invite: true,
	open_manual_invite_bound: true,
	pending_default_closed: true,
    admin_round_trip: true,
    captures,
  };
}
