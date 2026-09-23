/**
 * [INPUT]: 依赖入口 HTML 的 meta（pandora-app / pandora-api-base / pandora-contracts）与 Vite 环境变量；依赖 ./contracts 的 PendingContract
 * [OUTPUT]: 对外提供 runtime（domain、apiBase）、legacyEntry、tokenKey、resolveApiBase、apiUrl、hasContract
 * [POS]: core 的运行时环境读取器：这是哪个域、API 基址在哪、哪些待接后端契约被显式打开，都只从这里读；其余模块不直接碰 document
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { PendingContract } from "./contracts";
export type AppDomain = "admin" | "portal";
export function resolveApiBase(entry: string, value: string): string {
  const documentUrl = new URL(entry);
  const url = new URL(value, documentUrl);
  if (
    url.origin !== documentUrl.origin ||
    !["http:", "https:"].includes(url.protocol)
  )
    throw new Error("API地址必须与页面同源");
  url.search = "";
  url.hash = "";
  if (!url.pathname.endsWith("/")) url.pathname += "/";
  return url.href;
}
export function apiUrl(base: string, path: string): string {
  const relative = path.replace(/^\/+/, "");
  if (
    !relative.startsWith("v1/") ||
    relative.includes("..") ||
    relative.includes("\\")
  )
    throw new Error("无效的接口路径");
  const result = new URL(relative, base);
  if (
    result.origin !== new URL(base).origin ||
    !result.pathname.startsWith(new URL(base).pathname)
  )
    throw new Error("接口路径越界");
  return result.href;
}
const meta = (name: string) =>
  document.querySelector<HTMLMetaElement>(`meta[name="${name}"]`)?.content;
export const runtime = {
  domain: (meta("pandora-app") === "admin" ? "admin" : "portal") as AppDomain,
  apiBase: resolveApiBase(
    document.URL,
    import.meta.env.DEV
      ? import.meta.env.VITE_API_BASE || "/"
      : meta("pandora-api-base") || "../",
  ),
};
// 原版手写单页的地址。并存期它就在网关根（即 API 基址），React 挂在 /app/；
// 切换时 / 改下发 React、原版挪到 /legacy/，届时只改这一处。
export const legacyEntry = runtime.apiBase;
export const tokenKey =
  runtime.domain === "admin" ? "aegis_admin_token" : "aegis_token";
// 待接后端契约默认关闭。入口 HTML 写 <meta name="pandora-contracts" content="a b">，
// 或构建时 VITE_PANDORA_CONTRACTS="a b"，才显式打开对应入口。
// 每次调用都读 DOM 而不是模块加载时读一次：测试可以在渲染前注入 meta。
export function hasContract(name: PendingContract): boolean {
  const declared = [
    ...Array.from(
      document.querySelectorAll<HTMLMetaElement>('meta[name="pandora-contracts"]'),
      (element) => element.content,
    ),
    String(import.meta.env.VITE_PANDORA_CONTRACTS ?? ""),
  ].join(" ");
  return declared.split(/\s+/).includes(name);
}
