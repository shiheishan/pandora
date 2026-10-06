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
    files: ['src/**/*.{ts,tsx,js}'],
    languageOptions: { globals: globals.browser },
    extends: [reactHooks.configs.flat['recommended-latest']],
  },
  {
    files: ['vite.config.ts', 'eslint.config.js', 'tests/**/*.ts', 'dev/**/*.ts'],
    languageOptions: { globals: globals.node },
  },
)
