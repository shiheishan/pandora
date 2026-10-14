import { defineConfig } from '@playwright/test'
import { join, resolve } from 'node:path'

// ============================================================================
//  购买路径的无头浏览器测试（Playwright + Chromium），只对一套已起的冒烟栈跑：
//  真 PG18 + 真网关 + 网关嵌入的真前端产物（门户在门户网关根上，后台在后台网关根上）。
//  输入：SMOKE_STATE=<run-smoke-stack.sh 的状态目录>；产物：BROWSER_OUT（缺省 <状态目录>/browser）。
//  怎么跑、选择器约定见 .claude/rules/frontend-browser-e2e.md
// ============================================================================

const out = resolve(process.env.BROWSER_OUT || join(process.env.SMOKE_STATE ?? '.', 'browser'))

export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  // 每条路径各用各的现场生成的用户，互不依赖，可以并行；共享的只有种子（套餐、渠道），只读
  fullyParallel: true,
  workers: Number(process.env.BROWSER_WORKERS || 4),
  // 不重试：一次没过就是红，偶发也要看见
  retries: 0,
  forbidOnly: Boolean(process.env.CI),
  timeout: 150_000,
  expect: { timeout: 10_000 },
  globalSetup: './seed.ts',
  outputDir: join(out, 'test-results'),
  reporter: [['list'], ['html', { outputFolder: join(out, 'html'), open: 'never' }]],
  use: {
    browserName: 'chromium',
    trace: 'retain-on-failure',
    actionTimeout: 15_000,
    navigationTimeout: 20_000,
  },
})
