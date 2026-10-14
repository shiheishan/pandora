export type ModuleKey = 'dash' | 'tickets' | 'users' | 'plans' | 'billing' | 'marketing' | 'nodes' | 'certs' | 'content' | 'system' | 'security'

interface ModuleDef {
  title: string
  group: string
  /** [标签值, 标签名]；第一项为默认标签 */
  tabs?: ReadonlyArray<readonly [string, string]>
}

// ---------------------------------------------------------------------------
// 后台不分权限：登录了就是管理员，侧栏、⌘K、页头标签列出全部模块与标签。
// 「欠费单」按保留规则 6 做成「挂账」（契约后台-05）。
// ---------------------------------------------------------------------------
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
  plans: {
    title: '套餐',
    group: '商业',
    // 流量包是后端有、设计稿缺的标签（修订 R73）
    tabs: [
      ['catalog', '套餐'],
      ['packs', '流量包'],
    ],
  },
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
      ['subscription', '订阅下发'],
    ],
  },
  // 节点证书（面板集中签发 DNS-01）
  certs: {
    title: '证书',
    group: '网络',
    tabs: [
      ['list', '证书列表'],
      ['dns', 'DNS 凭据'],
      ['acme', 'ACME 设置'],
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
  ['网络', ['nodes', 'certs']],
  ['运营', ['content', 'system']],
  ['系统', ['security']],
]

// ⌘K 里的深链：[模块, 标签, 名称]，名称按后端真实能力（换发订阅链接而不是「更换订阅地址」）
const PALETTE_EXTRAS: ReadonlyArray<readonly [ModuleKey, string | null, string]> = [
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
  ['nodes', 'subscription', '订阅模板 / 信息节点 / 订阅域名'],
]

const isModule = (value: string): value is ModuleKey => Object.hasOwn(MODULES, value)

interface AdminRoute {
  module: ModuleKey
  tab: string | null
  /** 模块（与标签）之后剩下的路径段，已 URI 解码，交给页面自己解释（如 #/users/list/<用户 id>） */
  rest: string[]
  /** 地址不规范（未知模块、未知标签、缺标签、空段），外框应 replace 成 canonical */
  canonical: string
}

/** 模块的规范地址：给的标签不认得或没给时取第一个标签。 */
export function modulePath(module: ModuleKey, tab?: string | null): string {
  const tabs = MODULES[module].tabs
  if (!tabs) return `/${module}`
  const chosen = tabs.find(([k]) => k === tab)?.[0] ?? tabs[0]![0]
  return `/${module}/${chosen}`
}

function decodeSegments(segments: string[]): string[] | null {
  try {
    return segments.map((s) => decodeURIComponent(s))
  } catch {
    return null
  }
}

/**
 * #/users/list/abc → { module: 'users', tab: 'list', rest: ['abc'] }。
 * 规范化只作用于模块与标签：未知模块落到仪表盘，缺标签补默认标签，
 * 未知标签连同其后的段一起丢掉（那些段是相对于一个不存在的标签的）；
 * 合法地址后面的 rest 原样保留，只去掉空段并重新编码。
 */
export function resolveRoute(path: string): AdminRoute {
  const [, head = '', ...tail] = path.split('/')
  if (!isModule(head)) return { module: 'dash', tab: null, rest: [], canonical: '/dash' }
  const module = head
  const tabs = MODULES[module].tabs
  let tab: string | null = null
  let restRaw = tail
  if (tabs) {
    const [want = '', ...after] = tail
    if (tabs.some(([k]) => k === want)) {
      tab = want
      restRaw = after
    } else {
      tab = tabs[0]![0]
      restRaw = []
    }
  }
  const rest = decodeSegments(restRaw.filter((s) => s !== '')) ?? []
  const base = tab ? `/${module}/${tab}` : `/${module}`
  const canonical = [base, ...rest.map((s) => encodeURIComponent(s))].join('/')
  return { module, tab, rest, canonical }
}

export interface PaletteItem {
  key: string
  title: string
  path: string
  module: ModuleKey
  tab: string | null
}

/** ⌘K 候选：全部模块、标签与深链；空查询取前 12 条。 */
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
