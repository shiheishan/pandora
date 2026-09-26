/**
 * [INPUT]: 依赖 vitest/config 的 defineConfig，依赖 vitest/node 的 BaseSequencer，依赖 @vitejs/plugin-react（页面模块里可能连带 .tsx）
 * [OUTPUT]: 冒烟专用的 vitest 配置：只收 tests/smoke/*.smoke.ts，node 环境、串行、单条 90 秒，文件顺序固定为读表在前、写路径（writes.smoke.ts）最后
 * [POS]: tests/smoke 的运行配置，CI 的 panel-smoke.yml 以 `npx vitest run -c tests/smoke/vitest.config.ts` 调用；与工程的 vite.config.ts 分开，文件后缀 .smoke.ts 也不落进默认的 *.test.ts，所以不进 make frontend-check
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { fileURLToPath } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'
import { BaseSequencer, type TestSpecification } from 'vitest/node'

// ============================================================================
//  文件顺序：读表在前，写路径最后
// ============================================================================
// 写路径会改数据（回复工单、改开关、建公告……），读表的条数与「验到行」都以种子为准，
// 所以 writes.smoke.ts 必须排在两张读表之后。默认排序按缓存里的耗时与文件大小，
// 不保证这一点；顺序是这套冒烟自己的数据依赖，放在配置里，本机单跑也一样成立
class ReadsBeforeWrites extends BaseSequencer {
  async sort(files: TestSpecification[]): Promise<TestSpecification[]> {
    const isWrite = (f: TestSpecification) => /[\\/]writes\.smoke\.ts$/.test(f.moduleId)
    const byName = (a: TestSpecification, b: TestSpecification) => a.moduleId.localeCompare(b.moduleId)
    return [...files.filter((f) => !isWrite(f)).sort(byName), ...files.filter(isWrite)]
  }
}

export default defineConfig({
  root: fileURLToPath(new URL('../..', import.meta.url)),
  plugins: [react()],
  // 页面模块里可能引用构建期常量
  define: { __APP_RELEASE__: JSON.stringify('smoke') },
  test: {
    include: ['tests/smoke/**/*.smoke.ts'],
    environment: 'node',
    // 两个文件共用后台 IP 限流额度，串行跑
    fileParallelism: false,
    sequence: { sequencer: ReadsBeforeWrites },
    testTimeout: 90_000,
    hookTimeout: 90_000,
  },
})
