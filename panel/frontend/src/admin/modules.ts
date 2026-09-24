/**
 * [INPUT]: 依赖 ../core/router 的 matchPath
 * [OUTPUT]: 对外提供 ModuleKey、MODULES、NAV_GROUPS、PALETTE_EXTRAS、resolveRoute、modulePath、PaletteItem 与 paletteItems
 * [POS]: admin 的模块与路由表：侧栏分组、顶栏面包屑、页头标签页、⌘K 命令面板共用这一份数据（取自 管理后台.dc.html 的 MODS / NAV / EXTRA）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { matchPath } from '../core/router'

export type ModuleKey = 'dash' | 'tickets' | 'users' | 'plans' | 'billing' | 'marketing' | 'nodes' | 'content' | 'system' | 'security'

export interface ModuleDef {
  title: string
  group: string
  /** [标签值, 标签名]；第一项为默认标签 */
  tabs?: ReadonlyArray<readonly [string, string]>
}

// 「欠费单」按保留规则 6 做成「挂账」（契约后台-05）
export const MODULES: Readonly<Record<ModuleKey, ModuleDef>> = {
  dash: { title: '仪表盘', group: '工作台' },
  tickets: { title: '工单', group: '工作台' },
  users: {
    title: '用户',
    group: '用户运营',
    tabs: [
      ['list', '用户列表'],
      ['groups', '用户组'],
      ['bulk', '批量运营'],
      ['devices', '设备策略'],
      ['resets', '流量重置'],
    ],
  },
  plans: { title: '套餐', group: '商业' },
  billing: {
    title: '订单与收款',
    group: '商业',
    tabs: [
      ['orders', '订单'],
      ['arrears', '挂账'],
      ['providers', '支付渠道'],
      ['adjust', '收入调整'],
    ],
  },
  marketing: {
    title: '营销',
    group: '商业',
    tabs: [
      ['coupons', '优惠券'],
      ['gifts', '礼品卡'],
      ['commission', '佣金与提现'],
    ],
  },
  nodes: {
    title: '节点与服务器',
    group: '网络',
    tabs: [
      ['nodes', '节点'],
      ['servers', '服务器'],
      ['pools', '节点池'],
      ['routing', '路由'],
    ],
  },
  content: {
    title: '内容与外观',
    group: '运营',
    tabs: [
      ['announce', '公告'],
      ['kb', '知识库'],
      ['theme', '主题与插槽'],
    ],
  },
  system: {
    title: '通知与插件',
    group: '运营',
    tabs: [
      ['notify', '通知渠道'],
      ['templates', '邮件模板'],
      ['hooks', 'Webhook 钩子'],
    ],
  },
  security: {
    title: '安全与运维',
    group: '系统',
    tabs: [
      ['audit', '审计日志'],
      ['access', '访问日志'],
      ['risk', '风控'],
      ['switches', '降级开关'],
    ],
  },
}

export const NAV_GROUPS: ReadonlyArray<readonly [string, readonly ModuleKey[]]> = [
  ['工作台', ['dash', 'tickets']],
  ['用户运营', ['users']],
  ['商业', ['plans', 'billing', 'marketing']],
  ['网络', ['nodes']],
  ['运营', ['content', 'system']],
  ['系统', ['security']],
]

// ⌘K 里的深链：[模块, 标签, 名称]，名称按后端真实能力（换发订阅链接而不是「更换订阅地址」）
export const PALETTE_EXTRAS: ReadonlyArray<readonly [ModuleKey, string | null, string]> = [
  ['users', 'list', '重置密码 / 调整余额 / 换发订阅链接'],
  ['users', 'bulk', '批量生成用户 / 群发邮件 / 导出'],
  ['nodes', 'nodes', 'REALITY 密钥 / 一键安装令牌 / 吊销身份'],
  ['plans', null, '套餐向导 / 版本 / 价格'],
  ['billing', 'orders', '人工开单 / 标记已支付'],
  ['security', 'risk', '共享 IP 聚类'],
  ['marketing', 'commission', '提现审核 / 打款'],
  ['users', 'resets', '流量重置统计'],
  ['system', 'templates', '实发测试信'],
  ['billing', 'adjust', '收入调整登记与冲销'],
]

const isModule = (value: string): value is ModuleKey => Object.hasOwn(MODULES, value)

export interface AdminRoute {
  module: ModuleKey
  tab: string | null
  /** 地址不规范（未知模块、未知标签、缺标签），外框应 replace 成 canonical */
  canonical: string
}

export function modulePath(module: ModuleKey, tab?: string | null): string {
  const def = MODULES[module]
  const chosen = def.tabs ? (def.tabs.find(([k]) => k === tab)?.[0] ?? def.tabs[0]![0]) : null
  return chosen ? `/${module}/${chosen}` : `/${module}`
}

/** #/users/groups → { module: 'users', tab: 'groups' }；未知路径落到仪表盘。 */
export function resolveRoute(path: string): AdminRoute {
  const params = matchPath('/:module/:tab', path) ?? matchPath('/:module', path) ?? {}
  const module = params.module && isModule(params.module) ? params.module : 'dash'
  const canonical = modulePath(module, params.tab)
  const tab = canonical.split('/')[2] ?? null
  return { module, tab, canonical }
}

export interface PaletteItem {
  key: string
  title: string
  path: string
  module: ModuleKey
  tab: string | null
}

export function paletteItems(query: string): PaletteItem[] {
  const all: PaletteItem[] = []
  for (const [key, def] of Object.entries(MODULES) as [ModuleKey, ModuleDef][]) {
    if (def.tabs) def.tabs.forEach(([tab, title]) => all.push({ key: `${key}/${tab}`, title, path: `${def.group} / ${def.title}`, module: key, tab }))
    else all.push({ key, title: def.title, path: def.group, module: key, tab: null })
  }
  PALETTE_EXTRAS.forEach(([module, tab, title]) => all.push({ key: `x:${title}`, title, path: MODULES[module].title, module, tab }))
  const q = query.trim().toLowerCase()
  return q ? all.filter((item) => (item.title + item.path).toLowerCase().includes(q)) : all.slice(0, 12)
}
