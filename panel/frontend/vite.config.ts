/**
 * [INPUT]: 依赖 vite / vitest 的 defineConfig 与 Plugin 类型，依赖 @vitejs/plugin-react 的 JSX 变换，依赖 src/core/theme-boot.js 的源码，依赖 dev/mock-api.ts 的开发期假后端
 * [OUTPUT]: 对外提供按 mode 切换的构建配置：--mode admin|portal 各出一份独立产物，showcase 只允许 dev，vitest 的 test mode 以工程根为根；dev 下 v1/ 请求走 PANDORA_API 代理或假后端；__APP_RELEASE__ 取 PANDORA_RELEASE；导出 themeBootFileName 供测试核对
 * [POS]: panel/frontend 的唯一构建入口，产物由 panel/Makefile 的 frontend-embed 同步进 panel/web/{admin,portal}
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import react from '@vitejs/plugin-react'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import type { Plugin } from 'vite'
import { defineConfig } from 'vitest/config'
import { mockApi } from './dev/mock-api.ts'

// ---------------------------------------------------------------------------
// 一个工程两个入口，但必须分两次构建：一次多入口构建会把两个 index.html 放进
// 同一个 outDir、共用一个 assets/，而网关各自只下发自己目录下的 / 与 /assets/*。
// 所以 mode 即入口：root 指向 src/<app>，产物写到 dist/<app>。
// showcase 是组件与令牌的演示页，只在 dev 下存在，构建时直接拒绝，保证进不了产物。
// ---------------------------------------------------------------------------
const APPS = ['admin', 'portal'] as const
const DEV_ONLY = ['showcase'] as const
type Entry = (typeof APPS)[number] | (typeof DEV_ONLY)[number]

const isApp = (mode: string): mode is (typeof APPS)[number] => (APPS as readonly string[]).includes(mode)
const isDevOnly = (mode: string): mode is (typeof DEV_ONLY)[number] => (DEV_ONLY as readonly string[]).includes(mode)
const here = (path: string) => fileURLToPath(new URL(path, import.meta.url))

// ---------------------------------------------------------------------------
// 主题引导脚本：首帧前写好 <html data-theme>，否则深色用户每次打开都闪白。
// CSP 禁止内联脚本，Vite 又只打包 module 脚本，所以由这个插件把它原样作为
// 经典脚本输出到 assets/，文件名带内容哈希（webapp 对 assets/ 下发 immutable 缓存），
// 并在 <head> 最前面注入 <script src>。
// ---------------------------------------------------------------------------
const themeBootSource = readFileSync(here('./src/core/theme-boot.js'), 'utf8')
export const themeBootFileName = `assets/theme-boot-${createHash('sha256')
  .update(themeBootSource)
  .digest('hex')
  .slice(0, 8)}.js`

function themeBoot(): Plugin {
  return {
    name: 'pandora-theme-boot',
    configureServer(server) {
      server.middlewares.use(`/${themeBootFileName}`, (_req, res) => {
        res.setHeader('Content-Type', 'text/javascript; charset=utf-8')
        res.end(themeBootSource)
      })
    },
    generateBundle() {
      this.emitFile({ type: 'asset', fileName: themeBootFileName, source: themeBootSource })
    },
    transformIndexHtml: {
      order: 'post',
      // 紧跟 <meta charset> 之后：字符集声明留在最前，引导脚本先于样式表与入口模块执行
      handler: (html) => {
        const charset = /<meta charset="[^"]*"\s*\/?>/i
        if (!charset.test(html)) throw new Error('index.html must declare <meta charset> for the theme boot script')
        return html.replace(charset, (tag) => `${tag}\n    <script src="./${themeBootFileName}"></script>`)
      },
    },
  }
}

// ---------------------------------------------------------------------------
// 版本号：后台登录页与侧栏的「r55」由构建时注入，发布链设 PANDORA_RELEASE，本地为 dev。
// 开发期后端：设了 PANDORA_API（如 http://127.0.0.1:8081）就把 /v1 代理过去，
// 否则挂 dev/mock-api.ts 的假后端（只在 serve 时加入插件列表，构建产物里没有它）。
// ---------------------------------------------------------------------------
const define = { __APP_RELEASE__: JSON.stringify(process.env.PANDORA_RELEASE || 'dev') }

export default defineConfig(({ mode, command }) => {
  if (mode === 'test') {
    return {
      define,
      plugins: [react()],
      test: {
        root: here('.'),
        include: ['src/**/*.test.{ts,tsx}', 'tests/**/*.test.ts'],
      },
    }
  }
  if (isDevOnly(mode) && command === 'build') {
    throw new Error(`mode "${mode}" is dev-only and must never be built into the embedded bundle`)
  }
  if (!isApp(mode) && !isDevOnly(mode)) {
    throw new Error(`unknown mode "${mode}": use --mode admin, --mode portal or (dev only) --mode showcase`)
  }
  const entry: Entry = mode
  const apiTarget = process.env.PANDORA_API
  return {
    root: here(`./src/${entry}`),
    // 后台挂在 nginx 高熵前缀后面，入口与资源一律相对引用
    base: './',
    publicDir: false,
    define,
    plugins: [react(), themeBoot(), ...(command === 'serve' && isApp(entry) && !apiTarget ? [mockApi(entry)] : [])],
    server: apiTarget ? { proxy: { '/v1': { target: apiTarget, changeOrigin: true } } } : undefined,
    build: {
      outDir: here(`./dist/${entry}`),
      emptyOutDir: true,
      assetsDir: 'assets',
      // .vite/manifest.json 不进产物，也就不会被嵌入
      manifest: false,
      // 字体等小文件也走独立文件：CSP 的 font-src 只放行 'self'，不放行 data:
      assetsInlineLimit: 0,
    },
  }
})
