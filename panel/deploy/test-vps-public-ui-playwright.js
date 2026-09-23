async (page) => {
  const target = '__PANDORA_VPS_UI_TARGET__';

  const assert = (condition, message) => {
    if (!condition) throw new Error(message);
  };
  const consoleErrors = [];
  const pageErrors = [];
  page.on('console', message => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('pageerror', error => pageErrors.push(error.message));

  const widths = [390, 768, 1024, 1920, 3840];
  const captures = [];
  let securityHeaders;
  for (const width of widths) {
    await page.setViewportSize({width, height: width <= 768 ? 844 : 1080});
    const response = await page.goto(target, {waitUntil: 'domcontentloaded'});
    assert(response, `missing_response:${width}`);
    assert(response.status() === 200, `unexpected_status:${width}:${response.status()}`);
    if (!securityHeaders) securityHeaders = response.headers();

    await page.waitForSelector('#authView:not(.hide)');
    assert(await page.title() === '潘多拉面板', `title_mismatch:${width}`);
    assert(await page.locator('#appView').evaluate(element => element.classList.contains('hide')),
      `authenticated_shell_exposed:${width}`);
    assert(await page.locator('#loginEmail').isVisible(), `email_not_visible:${width}`);
    assert(await page.locator('#loginPass').isVisible(), `password_not_visible:${width}`);
    assert(await page.locator('#toggleLoginPass').isVisible(), `password_eye_not_visible:${width}`);

    const geometry = await page.evaluate(() => {
      const auth = document.querySelector('.auth-shell').getBoundingClientRect();
      return {
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
        authLeft: auth.left,
        authRight: auth.right,
        authWidth: auth.width,
      };
    });
    assert(geometry.scrollWidth <= geometry.clientWidth + 1, `horizontal_overflow:${width}`);
    assert(geometry.authLeft >= -1 && geometry.authRight <= width + 1,
      `auth_shell_outside_viewport:${width}`);
    assert(geometry.authWidth >= Math.min(320, width - 32), `auth_shell_collapsed:${width}`);

    await page.locator('#loginPass').fill('Aa123456');
    assert(await page.locator('#loginPass').getAttribute('type') === 'password',
      `password_initial_type:${width}`);
    await page.locator('#toggleLoginPass').click();
    assert(await page.locator('#loginPass').getAttribute('type') === 'text',
      `password_eye_show_failed:${width}`);
    assert(await page.locator('#toggleLoginPass').getAttribute('aria-label') === '隐藏密码',
      `password_eye_show_label:${width}`);
    await page.locator('#toggleLoginPass').click();
    assert(await page.locator('#loginPass').getAttribute('type') === 'password',
      `password_eye_hide_failed:${width}`);
    assert(await page.locator('#toggleLoginPass').getAttribute('aria-label') === '显示密码',
      `password_eye_hide_label:${width}`);

    captures.push({width, ...geometry});
  }

  const header = name => securityHeaders[name] || '';
  assert(header('content-security-policy').includes("default-src 'none'"), 'csp_missing');
  assert(header('content-security-policy').includes("frame-ancestors 'none'"),
    'csp_frame_ancestors_missing');
  assert(header('x-content-type-options').toLowerCase() === 'nosniff', 'nosniff_missing');
  assert(header('x-frame-options').toUpperCase() === 'DENY', 'frame_deny_missing');
  assert(header('referrer-policy').toLowerCase() === 'no-referrer', 'referrer_policy_missing');
  assert(pageErrors.length === 0, `page_errors:${pageErrors.join('|')}`);
  assert(consoleErrors.length === 0, `console_errors:${consoleErrors.join('|')}`);

  return {
    gate: 'pass',
    engine: 'real-playwright-browser',
    target: target.replace(/\/$/, ''),
    surface: 'public-portal',
    responsive_widths: widths,
    password_toggle: true,
    security_headers: true,
    captures,
  };
}
