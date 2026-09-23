/**
 * [INPUT]: 依赖 vitest 的 afterEach/vi，依赖 @testing-library/react 的 cleanup/configure，依赖 @testing-library/jest-dom 的断言扩展
 * [OUTPUT]: 对外提供 jsdom 环境补丁（pandora meta、matchMedia、ResizeObserver、scrollTo）、每用例清理、全局 asyncUtilTimeout
 * [POS]: tests/ 的 vitest setupFiles 唯一入口，被 vitest.config.ts 消费，所有 *.test.ts(x) 共享
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import "@testing-library/jest-dom/vitest";
import { afterEach, vi } from "vitest";
import { cleanup, configure } from "@testing-library/react";

// findBy*/waitFor 默认只等 1s。2 核 CI runner 上 antd 弹窗入场可超过它
// （refund-execution 的确认按钮曾因此找不到）。10s 仍低于 vitest.config.ts
// 的 30s testTimeout，卡死的用例照样被单测超时兜住；元素出现即返回，本机不变慢。
// 用例不再各自传 timeout。
configure({ asyncUtilTimeout: 10_000 });

document.head.innerHTML =
  '<meta name="pandora-app" content="admin"><meta name="pandora-api-base" content="../"><meta name="pandora-contracts" content="">';
Object.defineProperty(window, "matchMedia", {
  value: vi.fn((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })),
  writable: true,
});
globalThis.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
window.scrollTo = vi.fn();
const computeStyle = window.getComputedStyle.bind(window);
window.getComputedStyle = (element) => computeStyle(element);
afterEach(() => {
  cleanup();
  localStorage.clear();
  sessionStorage.clear();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
