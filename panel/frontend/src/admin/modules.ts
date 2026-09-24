/**
 * [INPUT]: 无外部依赖（纯数据与纯函数）
 * [OUTPUT]: 对外提供 ModuleKey、MODULES、NAV_GROUPS、PALETTE_EXTRAS、Permissions、canRead、visibleTabs、canReadModule、resolveRoute、modulePath、PaletteItem 与 paletteItems
 * [POS]: admin 的模块与路由表：侧栏分组、顶栏面包屑、页头标签页、⌘K 命令面板与内容区的权限判断共用这一份数据（取自 管理后台.dc.html 的 MODS / NAV / EXTRA，读权限码取自 api-contract.md 各模块条目）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
export type ModuleKey = 'dash' | 'tickets' | 'users' | 'plans' | 'billing' | 'marketing' | 'nodes' | 'content' | 'system' | 'security'

export interface ModuleDef {
  title: string
  group: string
  /** [标签值, 标签名]；第一项为默认标签 */
  tabs?: ReadonlyArray<readonly [string, string]>
  /**
   * 读权限码：无标签模块一个码，有标签模块按标签各一个码（模块可见 = 至少一个标签可见），
   * null = 登录即可。码取自契约该页主列表接口的「权限」行（含修订 Rn）。
   */
  read: string | null | Readonly<Record<string, string>>
}

// ---------------------------------------------------------------------------
// 读权限：只看 GET v1/me 的 permissions，不硬编码角色。缺权限的接口回 404，
// 所以侧栏与 ⌘K 预先隐藏入口，直接输地址进入的按「无权限或不存在」显示。
// 仪表盘是落地页，登录即可见，卡片各自按权限取舍（契约后台-01）。
// 「欠费单」按保留规则 6 做成「挂账」（契约后台-05）。
// ---------------------------------------------------------------------------
export const MODULES: Readonly<Record<ModuleKey, ModuleDef>> = {
  dash: { title: '仪表盘', group: '工作台', read: null },
  tickets: { title: '工单', group: '工作台', read: 'ops.ticket.read' },
  users: {
    title: '用户',
    group: '用户运营',
    read: { list: 'iam.user.read', groups: 'iam.user.read', bulk: 'iam.user.read', devices: 'iam.user.read', resets: 'metering.reset.read' },
    tabs: [
      ['list', '用户列表'],
      ['groups', '用户组'],
      ['bulk', '批量运营'],
      ['devices', '设备策略'],
      ['resets', '流量重置'],
    ],
  },
  plans: { title: '套餐', group: '商业', read: 'catalog.read' },
  billing: {
    title: '订单与收款',
    group: '商业',
    read: { orders: 'billing.order.read', arrears: 'billing.ledger.read', providers: 'billing.payment.read', adjust: 'billing.ledger.read' },
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
    // 修订 R6：优惠券与分销改用营销域读权限
    read: { coupons: 'marketing.coupon.read', gifts: 'marketing.giftcard.read', commission: 'marketing.commission.read' },
    tabs: [
      ['coupons', '优惠券'],
      ['gifts', '礼品卡'],
      ['commission', '佣金与提现'],
    ],
  },
  nodes: {
    title: '节点与服务器',
    group: '网络',
    read: { nodes: 'node.read', servers: 'node.read', pools: 'node.read', routing: 'node.read' },
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
    // 公告与知识库没有单独的读权限，列表接口就挂写权限
    read: { announce: 'ops.announcement.write', kb: 'ops.content.write', theme: 'platform.appearance.read' },
    tabs: [
      ['announce', '公告'],
      ['kb', '知识库'],
      ['theme', '主题与插槽'],
    ],
  },
  system: {
    title: '通知与插件',
    group: '运营',
    // 通知渠道的两条读接口（settings/mail、settings/telegram）挂的是审计读权限
    read: { notify: 'security.audit.read', templates: 'ops.notification.read', hooks: 'platform.plugin.read' },
    tabs: [
      ['notify', '通知渠道'],
      ['templates', '邮件模板'],
      ['hooks', 'Webhook 钩子'],
    ],
  },
  security: {
    title: '安全与运维',
    group: '系统',
    read: { audit: 'security.audit.read', access: 'security.audit.read', risk: 'security.audit.read', switches: 'security.audit.read' },
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

/** 当前管理员的权限码集合（GET v1/me 的 permissions） */
export type Permissions = ReadonlySet<string>

/** 某个模块（有标签时是某个标签）是否可读；未知标签一律不可读。 */
export function canRead(module: ModuleKey, tab: string | null, perms: Permissions): boolean {
  const read = MODULES[module].read
  if (read === null) return true
  if (typeof read === 'string') return perms.has(read)
  if (tab === null || !Object.hasOwn(read, tab)) return false
  return perms.has(read[tab]!)
}

/** 有权限的标签，保持设计稿顺序；无标签模块返回空数组。 */
export function visibleTabs(module: ModuleKey, perms: Permissions): ReadonlyArray<readonly [string, string]> {
  return (MODULES[module].tabs ?? []).filter(([tab]) => canRead(module, tab, perms))
}

export function canReadModule(module: ModuleKey, perms: Permissions): boolean {
  return MODULES[module].tabs ? visibleTabs(module, perms).length > 0 : canRead(module, null, perms)
}

export interface AdminRoute {
  module: ModuleKey
  tab: string | null
  /** 模块（与标签）之后剩下的路径段，已 URI 解码，交给页面自己解释（如 #/users/list/<用户 id>） */
  rest: string[]
  /** 地址不规范（未知模块、未知标签、缺标签、空段），外框应 replace 成 canonical */
  canonical: string
}

/**
 * 模块的规范地址。缺省标签取第一个；给了 perms 时取第一个有权限的标签，
 * 侧栏与 ⌘K 的链接因此直接落在能看的那一页。
 */
export function modulePath(module: ModuleKey, tab?: string | null, perms?: Permissions): string {
  const tabs = perms ? visibleTabs(module, perms) : (MODULES[module].tabs ?? [])
  const all = MODULES[module].tabs
  if (!all) return `/${module}`
  const chosen = all.find(([k]) => k === tab)?.[0] ?? tabs[0]?.[0] ?? all[0]![0]
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
 * 规范化只作用于模块与标签：未知模块落到仪表盘，缺标签补默认标签（给了 perms 时取第一个有权限的），
 * 未知标签连同其后的段一起丢掉（那些段是相对于一个不存在的标签的）；
 * 合法地址后面的 rest 原样保留，只去掉空段并重新编码。
 */
export function resolveRoute(path: string, perms?: Permissions): AdminRoute {
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
      tab = modulePath(module, null, perms).split('/')[2]!
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

/** ⌘K 候选：只列有权限的模块、标签与深链；空查询取前 12 条。 */
export function paletteItems(query: string, perms: Permissions): PaletteItem[] {
  const all: PaletteItem[] = []
  for (const [key, def] of Object.entries(MODULES) as [ModuleKey, ModuleDef][]) {
    if (def.tabs) visibleTabs(key, perms).forEach(([tab, title]) => all.push({ key: `${key}/${tab}`, title, path: `${def.group} / ${def.title}`, module: key, tab }))
    else if (canRead(key, null, perms)) all.push({ key, title: def.title, path: def.group, module: key, tab: null })
  }
  PALETTE_EXTRAS.forEach(([module, tab, title]) => {
    if (tab === null ? canReadModule(module, perms) : canRead(module, tab, perms)) all.push({ key: `x:${title}`, title, path: MODULES[module].title, module, tab })
  })
  const q = query.trim().toLowerCase()
  return q ? all.filter((item) => (item.title + item.path).toLowerCase().includes(q)) : all.slice(0, 12)
}
