/**
 * [INPUT]: 依赖 vitest/config 的 defineConfig，@vitejs/plugin-react，tests/setup.ts
 * [OUTPUT]: 对外提供 vitest 运行配置：jsdom + /prefix/app/ 基址、并发与超时上限
 * [POS]: panel/frontend 的测试运行器配置，被 npm run check 与 CI 的 React candidate job 消费
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    // Bound concurrent jsdom/Ant Design trees to avoid CPU starvation in UI tests.
    maxWorkers: 2,
    // antd + jsdom 的重页面在 2 核 CI runner 上单例可超 10s，默认 5s 会误报。
    // 统一放宽到 30s，用例里不再各自传第三参数。
    testTimeout: 30_000,
    environment: "jsdom",
    environmentOptions: { jsdom: { url: "http://localhost/prefix/app/" } },
    setupFiles: ["./tests/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}", "tests/**/*.test.{ts,tsx}"],
    restoreMocks: true,
  },
});
