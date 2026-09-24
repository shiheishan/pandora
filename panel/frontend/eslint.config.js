// [INPUT]: 依赖 eslint/config 的 defineConfig，@eslint/js、typescript-eslint、eslint-plugin-react-hooks、globals
// [OUTPUT]: 对外提供 ESLint flat config：TS 推荐规则 + React Hooks 规则，浏览器与 node 两套全局
// [POS]: panel/frontend 的 lint 入口，被 npm run lint / check 调用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
import js from '@eslint/js'
import { defineConfig } from 'eslint/config'
import reactHooks from 'eslint-plugin-react-hooks'
import globals from 'globals'
import tseslint from 'typescript-eslint'

export default defineConfig(
  { ignores: ['dist', 'node_modules'] },
  js.configs.recommended,
  tseslint.configs.recommended,
  {
    files: ['src/**/*.{ts,tsx}'],
    languageOptions: { globals: globals.browser },
    extends: [reactHooks.configs.flat['recommended-latest']],
  },
  {
    files: ['vite.config.ts', 'eslint.config.js', 'tests/**/*.ts'],
    languageOptions: { globals: globals.node },
  },
)
