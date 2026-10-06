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
