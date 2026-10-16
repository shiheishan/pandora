export type ModuleKey = 'dash' | 'tickets' | 'users' | 'plans' | 'billing' | 'marketing' | 'nodes' | 'content' | 'system' | 'security'

interface ModuleDef {
  title: string
  group: string
  /** [标签值, 标签名]；第一项为默认标签 */
  tabs?: ReadonlyArray<readonly [string, string]>
}

// ---------------------------------------------------------------------------
// 后台不分权限：登录了就是管理员，侧栏、⌘K、页签栏列出全部模块与页签。
// 结构照 10-16 设计稿：用户只剩三个页签，套餐没有页签，营销与安全各加一个 signup。
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
    ],
  },
  plans: { title: '套餐', group: '商业' },
  billing: {
    title: '订单与收款',
    group: '商业',
    tabs: [
      ['orders', '订单'],
      ['arrears', '待处理到账'],
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
      ['signup', '注册试用与邀请'],
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
      ['signup', '注册防护'],
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

/**
 * 已登记、但页面还没接上的页签（「模块:页签」）：外框直接画空状态，不交给模块页面分发。
 * 各模块块接好页面后从这里删掉自己那一项。
 */
export const PENDING_TABS: ReadonlySet<string> = new Set()

export const isPendingTab = (module: ModuleKey, tab: string | null): boolean => tab != null && PENDING_TABS.has(`${module}:${tab}`)

// ⌘K 里的深链：[模块, 页签, 能在这一页做的事]，名称按后端真实能力（换发订阅链接而不是「更换订阅地址」）
const PALETTE_EXTRAS: ReadonlyArray<readonly [ModuleKey, string | null, string]> = [
  ['users', 'list', '重置密码 / 调整余额 / 换发订阅链接'],
  ['users', 'bulk', '批量生成用户 / 群发邮件 / 导出'],
  ['plans', null, '新建套餐 / 价格 / 线路 / 版本'],
  ['billing', 'orders', '人工开单 / 标记已支付'],
  ['billing', 'adjust', '收入调整登记与冲销'],
  ['marketing', 'commission', '提现审核 / 打款'],
  ['marketing', 'signup', '注册试用 / 邀请码上限'],
  ['nodes', 'nodes', 'REALITY 密钥 / 吊销身份'],
  ['nodes', 'servers', '安装命令 / 安装令牌'],
  ['nodes', 'subscription', '订阅模板 / 订阅域名'],
  ['system', 'templates', '实发测试信'],
  ['security', 'risk', '共享 IP 聚类 / 设备策略'],
  ['security', 'signup', '人机验证 / 邮箱后缀'],
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
  /** 左侧小标签：「模块」或「页面」；后续实体搜索加「用户」「订单」「节点」 */
  kind: string
  title: string
  /** 右侧灰字：模块所属分组，或深链能做的事 */
  sub: string
  /** 标题用等宽字（邮箱、订单号、节点名这类实体） */
  mono?: boolean
  module: ModuleKey
  tab: string | null
}

export interface PaletteGroup {
  label: string
  items: PaletteItem[]
}

const PAGE_LIMIT = 8

/**
 * ⌘K 候选，按组给出（设计稿：空查询只有「快速跳转」一组，列全部模块；
 * 有查询时「页面」一组列匹配的模块、「模块 › 页签」与深链）。
 * 实体搜索（用户 / 订单 / 节点）由调用方追加分组，不在这里。
 */
export function paletteGroups(query: string): PaletteGroup[] {
  const modules: PaletteItem[] = (Object.entries(MODULES) as [ModuleKey, ModuleDef][]).map(([key, def]) => ({
    key,
    kind: '模块',
    title: def.title,
    sub: def.group,
    module: key,
    tab: null,
  }))
  const q = query.trim().toLowerCase()
  if (!q) return [{ label: '快速跳转', items: modules }]

  const pages: PaletteItem[] = [...modules]
  for (const [key, def] of Object.entries(MODULES) as [ModuleKey, ModuleDef][]) {
    def.tabs?.forEach(([tab, title]) => pages.push({ key: `${key}/${tab}`, kind: '页面', title: `${def.title} › ${title}`, sub: def.group, module: key, tab }))
  }
  PALETTE_EXTRAS.forEach(([module, tab, sub]) => {
    const def = MODULES[module]
    const tabTitle = tab ? def.tabs?.find(([k]) => k === tab)?.[1] : null
    pages.push({ key: `x:${sub}`, kind: '页面', title: tabTitle ? `${def.title} › ${tabTitle}` : def.title, sub, module, tab })
  })
  const hits = pages.filter((item) => `${item.title} ${item.sub}`.toLowerCase().includes(q)).slice(0, PAGE_LIMIT)
  return hits.length ? [{ label: '页面', items: hits }] : []
}
