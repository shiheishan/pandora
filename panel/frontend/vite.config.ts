/**
 * [INPUT]: 依赖 vite / vitest 的 defineConfig，依赖 @vitejs/plugin-react 的 JSX 变换
 * [OUTPUT]: 对外提供按 mode 切换的构建配置：--mode admin|portal 各出一份独立产物，vitest 的 test mode 以工程根为根
 * [POS]: panel/frontend 的唯一构建入口，产物由 panel/Makefile 的 frontend-embed 同步进 panel/web/{admin,portal}
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'

// ---------------------------------------------------------------------------
// 一个工程两个入口，但必须分两次构建：一次多入口构建会把两个 index.html 放进
// 同一个 outDir、共用一个 assets/，而网关各自只下发自己目录下的 / 与 /assets/*。
// 所以 mode 即入口：root 指向 src/<app>，产物写到 dist/<app>。
// ---------------------------------------------------------------------------
const APPS = ['admin', 'portal'] as const
type App = (typeof APPS)[number]

const isApp = (mode: string): mode is App => (APPS as readonly string[]).includes(mode)
const here = (path: string) => fileURLToPath(new URL(path, import.meta.url))

export default defineConfig(({ mode }) => {
  if (mode === 'test') {
    return {
      plugins: [react()],
      test: {
        root: here('.'),
        include: ['src/**/*.test.{ts,tsx}', 'tests/**/*.test.ts'],
      },
    }
  }
  if (!isApp(mode)) {
    throw new Error(`unknown mode "${mode}": use --mode admin or --mode portal`)
  }
  return {
    root: here(`./src/${mode}`),
    // 后台挂在 nginx 高熵前缀后面，入口与资源一律相对引用
    base: './',
    publicDir: false,
    plugins: [react()],
    build: {
      outDir: here(`./dist/${mode}`),
      emptyOutDir: true,
      assetsDir: 'assets',
      // .vite/manifest.json 不进产物，也就不会被嵌入
      manifest: false,
    },
  }
})
