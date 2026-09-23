/**
 * [INPUT]: 依赖 ../internal/api/admin|public/router.go 的 chi 路由字面量、../migrations/*.sql 的权限字典、src/ 下全部 v1/ 路径与权限码字面量、src/core/contracts.ts 登记表、../web/{admin,portal}/index.html 旧单页的 v1 调用、./legacy-parity.ts 迁移清单
 * [OUTPUT]: 对外提供前后端接口面同构、旧页与 React 操作对等两组 vitest 契约测试
 * [POS]: tests 的漂移守卫：前端调用的每条 v1 路径与每个权限码，要么后端真的有，要么登记为待接契约；登记了而后端已提供的也算漂移。
 *        同一套路径归一化再对照旧单页：旧页有、React 没有的操作必须恰好等于 legacy-parity.ts，清单只会缩小
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { pendingContracts } from "../src/core/contracts";
import { legacyOnlyPaths, type LegacyDomain } from "./legacy-parity";

// 传字符串而非 new URL(...)：jsdom 环境替换了全局 URL，node 的 fileURLToPath 不认它的实例
const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const panelRoot = resolve(frontendRoot, "..");

// 一个模板表达式同时代表多条后端字面量路由时，在这里显式列出它展开后的每一条。
const dynamicSegments: Record<string, string[]> = {
  "v1/nodes/{}/{}": ["v1/nodes/{}/copy", "v1/nodes/{}/move"],
  "v1/dashboard/traffic/{}": ["v1/dashboard/traffic/nodes", "v1/dashboard/traffic/users"],
};

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    if (statSync(full).isDirectory()) sourceFiles(full, out);
    else if (/\.tsx?$/.test(name)) out.push(full);
  }
  return out;
}

// 路径参数与模板表达式统一写成 {}；粘在段尾的表达式（查询串或可选后缀）去掉；不含查询串与尾斜杠。
function normalize(path: string): string {
  return (path.split("?")[0] ?? "")
    .replace(/\{[^}]*\}/g, "{}")
    .replace(/([^/])\{\}/g, "$1")
    .replace(/\/+$/, "");
}

const gatewayRouters: Record<LegacyDomain, string> = {
  admin: "internal/api/admin/router.go",
  portal: "internal/api/public/router.go",
};

function backendPaths(files = Object.values(gatewayRouters)): Set<string> {
  const paths = new Set<string>();
  for (const file of files) {
    const source = readFileSync(join(panelRoot, file), "utf8");
    for (const match of source.matchAll(/\b(?:Get|Post|Put|Patch|Delete)\(\s*"(\/[^"]*)"/g))
      paths.add(normalize("v1/" + (match[1] ?? "").replace(/^\/+/, "")));
  }
  return paths;
}

// 读 start 处的模板字面量，${...} 记成 {}（允许嵌套花括号与引号）；返回文本与闭合反引号之后的位置。
function readTemplate(source: string, start: number): [string, number] {
  let cursor = start + 1;
  let depth = 0;
  let text = "";
  while (cursor < source.length) {
    const char = source[cursor] ?? "";
    if (depth === 0 && char === "`") break;
    if (char === "$" && source[cursor + 1] === "{") {
      if (depth === 0) text += "{}";
      depth += 1;
      cursor += 2;
      continue;
    }
    if (depth > 0) {
      if (char === "{") depth += 1;
      else if (char === "}") depth -= 1;
    } else text += char;
    cursor += 1;
  }
  return [text, cursor + 1];
}

// 读 start 处的单/双引号字面量，处理反斜杠转义；返回文本与闭合引号之后的位置。
function readQuoted(source: string, start: number): [string, number] {
  const quote = source[start];
  let cursor = start + 1;
  let text = "";
  while (cursor < source.length && source[cursor] !== quote) {
    if (source[cursor] === "\\") {
      text += source[cursor + 1] ?? "";
      cursor += 2;
      continue;
    }
    text += source[cursor] ?? "";
    cursor += 1;
  }
  return [text, cursor + 1];
}

const readString = (source: string, start: number) =>
  source[start] === "`" ? readTemplate(source, start) : readQuoted(source, start);

// 提取源码里以 v1/ 开头的字符串与模板字面量。
function pathLiterals(source: string): string[] {
  const found: string[] = [];
  for (const match of source.matchAll(/["'](v1\/[^"']*)["']/g)) found.push(match[1] ?? "");
  let index = source.indexOf("`v1/");
  while (index >= 0) {
    const [text, end] = readTemplate(source, index);
    found.push(text);
    index = source.indexOf("`v1/", end);
  }
  return found;
}

// `${endpoint}/${id}/reply` 这种以常量开头的模板：在同文件找 const endpoint = ... 里的 v1/ 字面量逐个代入。
// 一个常量可能按域取不同前缀，代入结果由调用方按该域的路由表筛掉不存在的组合。
function indirectPathLiterals(source: string): string[] {
  const bases = new Map<string, string[]>();
  for (const match of source.matchAll(/const (\w+) = ([^;\n]+)/g)) {
    const literals = [...(match[2] ?? "").matchAll(/["'](v1\/[^"']*)["']/g)].map((literal) => literal[1] ?? "");
    if (literals.length) bases.set(match[1] ?? "", literals);
  }
  const found: string[] = [];
  for (const match of source.matchAll(/`\$\{(\w+)\}\//g)) {
    const prefixes = bases.get(match[1] ?? "");
    if (!prefixes) continue;
    const [text] = readTemplate(source, match.index ?? 0);
    for (const prefix of prefixes) found.push(prefix + text.slice("{}".length));
  }
  return found;
}

function frontendPaths(): Map<string, Set<string>> {
  const found = new Map<string, Set<string>>();
  for (const file of sourceFiles(join(frontendRoot, "src"))) {
    const source = readFileSync(file, "utf8");
    for (const raw of pathLiterals(source)) {
      if (raw.endsWith("/")) continue; // 前缀判断，不是端点
      const path = normalize(raw);
      const sites = found.get(path) ?? new Set<string>();
      sites.add(relative(frontendRoot, file));
      found.set(path, sites);
    }
  }
  return found;
}

// 按域归属 React 源码：features/admin、features/portal 各归一域，其余共享代码两域都算。
function frontendPathsByDomain(): Record<LegacyDomain, Set<string>> {
  const routers = {
    admin: backendPaths([gatewayRouters.admin]),
    portal: backendPaths([gatewayRouters.portal]),
  };
  const found: Record<LegacyDomain, Set<string>> = { admin: new Set(), portal: new Set() };
  for (const file of sourceFiles(join(frontendRoot, "src"))) {
    const where = relative(frontendRoot, file).replaceAll("\\", "/");
    const domains: LegacyDomain[] = where.startsWith("src/features/admin/")
      ? ["admin"]
      : where.startsWith("src/features/portal/")
        ? ["portal"]
        : ["admin", "portal"];
    const source = readFileSync(file, "utf8");
    for (const raw of pathLiterals(source)) {
      if (raw.endsWith("/")) continue;
      const path = normalize(raw);
      for (const domain of domains)
        for (const each of [path, ...(dynamicSegments[path] ?? [])]) found[domain].add(each);
    }
    for (const raw of indirectPathLiterals(source)) {
      const path = normalize(raw);
      for (const domain of domains) if (routers[domain].has(path)) found[domain].add(path);
    }
  }
  return found;
}

// 旧单页用字符串拼接组路径：'/v1/orders/' + o.id + '/mark-paid'、s ? '/v1/servers/' + s.id : '/v1/servers'。
// 从每个 '/v1/ 字面量出发顺着 + 往后读：字面量原样接上，表达式记成 {}，碰到查询串或拼接结束就停。
// 起始字面量必须长得像路径，页面文案里提到的 /v1/... 不算调用。
function legacyPaths(html: string): Set<string> {
  const found = new Set<string>();
  const skipSpace = (at: number) => {
    while (at < html.length && /\s/.test(html.charAt(at))) at += 1;
    return at;
  };
  const skipExpression = (at: number) => {
    let depth = 0;
    while (at < html.length) {
      const char = html.charAt(at);
      if ("([{".includes(char)) depth += 1;
      else if (")]}".includes(char)) {
        if (depth === 0) return at;
        depth -= 1;
      } else if (depth === 0 && "+,;:?\n".includes(char)) return at;
      else if ("'\"`".includes(char)) {
        at = readString(html, at)[1];
        continue;
      }
      at += 1;
    }
    return at;
  };
  for (const match of html.matchAll(/(['"`])\/v1\//g)) {
    let [text, cursor] = readString(html, match.index ?? 0);
    if (!/^\/v1\/[\w\-./{}:]*(?:\?.*)?$/.test(text)) continue;
    while (!text.includes("?")) {
      let next = skipSpace(cursor);
      if (html.charAt(next) !== "+") break;
      next = skipSpace(next + 1);
      if ("'\"`".includes(html.charAt(next))) {
        const [literal, end] = readString(html, next);
        text += literal;
        cursor = end;
      } else {
        text += "{}";
        cursor = skipExpression(next);
      }
    }
    const raw = text.replace(/^\/+/, "");
    if (!raw.endsWith("/")) found.add(normalize(raw));
  }
  return found;
}

function permissionCatalog(): Set<string> {
  const codes = new Set<string>();
  const dir = join(panelRoot, "migrations");
  for (const name of readdirSync(dir)) {
    if (!name.endsWith(".sql")) continue;
    const source = readFileSync(join(dir, name), "utf8");
    for (const statement of source.matchAll(/INSERT INTO permissions[\s\S]*?;/g))
      for (const code of (statement[0] ?? "").matchAll(/'([a-z]+(?:\.[a-z_]+)+)'/g)) codes.add(code[1] ?? "");
  }
  return codes;
}

function frontendPermissions(catalog: Set<string>): Map<string, Set<string>> {
  const domains = [...new Set([...catalog].map((code) => code.split(".")[0] ?? ""))].filter(Boolean);
  const pattern = new RegExp(`"((?:${domains.join("|")})\\.[a-z_]+(?:\\.[a-z_]+)*)"`, "g");
  const found = new Map<string, Set<string>>();
  for (const file of sourceFiles(join(frontendRoot, "src"))) {
    const source = readFileSync(file, "utf8");
    for (const match of source.matchAll(pattern)) {
      const code = match[1] ?? "";
      const sites = found.get(code) ?? new Set<string>();
      sites.add(relative(frontendRoot, file));
      found.set(code, sites);
    }
  }
  return found;
}

const describeSites = (entries: Map<string, Set<string>>, keys: string[]) =>
  keys.map((key) => `${key} ← ${[...(entries.get(key) ?? [])].sort().join(", ")}`);

describe("frontend and gateway API surface stay in step", () => {
  const backend = backendPaths();
  const pendingPaths = new Set(Object.values(pendingContracts).flatMap((spec) => spec.paths));
  const pendingPermissions = new Set(Object.values(pendingContracts).flatMap((spec) => spec.permissions ?? []));

  it("reads a non-trivial route table from both gateways", () => {
    expect(backend.has("v1/me")).toBe(true);
    expect(backend.has("v1/orders/{}/cancel")).toBe(true);
    expect(backend.has("v1/support/tickets")).toBe(true);
  });

  it("only calls paths a gateway registers or a pending contract lists", () => {
    const frontend = frontendPaths();
    const exists = (path: string) =>
      backend.has(path) || pendingPaths.has(path) || (dynamicSegments[path]?.every((expanded) => backend.has(expanded)) ?? false);
    const drifted = [...frontend.keys()].filter((path) => !exists(path)).sort();
    expect(describeSites(frontend, drifted)).toEqual([]);
  });

  it("drops a pending contract once the backend provides it", () => {
    expect([...pendingPaths].filter((path) => backend.has(path)).sort()).toEqual([]);
    expect(Object.keys(dynamicSegments).filter((path) => backend.has(path))).toEqual([]);
  });

  it("only uses permission codes the migrations seed or a pending contract lists", () => {
    const catalog = permissionCatalog();
    expect(catalog.has("billing.refund.request")).toBe(true);
    const used = frontendPermissions(catalog);
    const unknown = [...used.keys()].filter((code) => !catalog.has(code) && !pendingPermissions.has(code)).sort();
    expect(describeSites(used, unknown)).toEqual([]);
    expect([...pendingPermissions].filter((code) => catalog.has(code))).toEqual([]);
  });
});

describe("legacy pages and the React candidate stay in step", () => {
  const react = frontendPathsByDomain();

  for (const domain of ["admin", "portal"] as const) {
    const legacy = legacyPaths(readFileSync(join(panelRoot, "web", domain, "index.html"), "utf8"));
    const router = backendPaths([gatewayRouters[domain]]);

    it(`reads the ${domain} legacy page's calls, all of them real ${domain} routes`, () => {
      expect(legacy.size).toBeGreaterThan(domain === "admin" ? 80 : 30);
      expect(legacy.has("v1/me")).toBe(true);
      const unknown = [...legacy]
        .filter((path) => !router.has(path) && !(dynamicSegments[path]?.every((expanded) => router.has(expanded)) ?? false))
        .sort();
      expect(unknown).toEqual([]);
    });

    it(`registers every ${domain} operation only the legacy page performs`, () => {
      const missing = [...legacy].filter((path) => !react[domain].has(path));
      const registered = Object.keys(legacyOnlyPaths[domain]);
      expect({
        unregistered: missing.filter((path) => !(path in legacyOnlyPaths[domain])).sort(),
        alreadyMigrated: registered.filter((path) => !missing.includes(path)).sort(),
      }).toEqual({ unregistered: [], alreadyMigrated: [] });
    });
  }
});
