import { CLIENTS, DEVICES, detectDevice, type Device } from './clients'

// ---------------------------------------------------------------------------
// 「在 App 里点一次更新」怎么点（首次点击测试 s4：新手不知道点哪）。clients.ts 按设备推荐的每个 App
// 一段，三步：在哪个页面、点哪个按钮、看到什么算成功。帮助中心里是内置的一篇（#/help?update=<设备>），
// 完成页与换新链接页「点一次更新」旁边链接到这台设备那一段。
//
// 叫法只用门户的（配置名、链接、App）。例外（用户 10-08 定）：App 自己的按钮名、页面名照它的简体中文
// 界面原样写，登记成 { button }，页面上做成按钮样式；带「订阅」等禁用词的另给 note，在这一篇里第一次
// 出现时补半句说明。禁用词守卫（tests/mock-portal-wording.test.ts）只放行本文件里 button 的值。
// 按钮名都有出处（App 源码里的本地化字符串，或官方/社区手册），见各段注释；查不到确切文字的不写按钮名，
// 只写「刷新」「更新」这类通用说法。
// ---------------------------------------------------------------------------
/** App 界面上的一个按钮或页面名：照它的中文界面原样写；note 是第一次出现时补的半句说明 */
export interface AppButton {
  button: string
  note?: string
}

/** 一步的文字：纯文字，或文字与 App 按钮名交替 */
export type GuideText = string | ReadonlyArray<string | AppButton>

export interface AppUpdateSteps {
  /** 在哪个页面 */
  where: GuideText
  /** 点哪个按钮 */
  tap: GuideText
  /** 看到什么算成功 */
  ok: GuideText
}

/** 一步的纯文字（按钮名照原样，不带说明）：测试与无样式的场合用 */
export const guideText = (t: GuideText): string => (typeof t === 'string' ? t : t.map((x) => (typeof x === 'string' ? x : x.button)).join(''))

const REFRESH_NOTE = '就是更新你添加的那条链接'

export const APP_UPDATE: Readonly<Record<string, AppUpdateSteps>> = {
  // 社区手册 LOWERTOP/Shadowrocket（源自官方群组关键词）：「订阅右滑 > 更新」
  Shadowrocket: {
    where: ['打开 Shadowrocket，停在底部的', { button: '首页' }],
    tap: ['在列表里找到你的配置名那一行，向右滑，点', { button: '更新' }],
    ok: '那一行下面的节点刷新成新的，就好了',
  },
  // stash.wiki 只写了导入路径（设置 → 配置文件），没写手动更新的按钮：只写通用说法
  Stash: {
    where: ['打开 Stash，进入', { button: '设置' }, '里的', { button: '配置文件' }],
    tap: '找到你的配置名，用它旁边的刷新或更新把它更新一次',
    ok: '节点列表刷新成新的，就好了',
  },
  // 没有官方界面文档；多篇带截图的教程一致：节点那一栏里向左滑，点「更新」
  'Quantumult X': {
    where: '点右下角的风车按钮，往下找到「节点」那一栏',
    tap: ['找到你的配置名，向左滑点', { button: '更新' }],
    ok: '回到首页，节点列表变成新的，就好了',
  },
  // nsloon.app 手册只有名词，没有按钮级说明：只写通用说法
  Loon: {
    where: ['打开 Loon，点底部的', { button: '配置' }, '，找到节点那一栏'],
    tap: '找到你的配置名，用它的刷新或更新把它更新一次',
    ok: '节点列表刷新成新的，就好了',
  },
  // SagerNet/sing-box-for-android values-zh-rCN：title_configuration「配置」、profile_update「更新」
  'sing-box': {
    where: ['打开 sing-box，点', { button: '配置' }, '（英文界面是「Profiles」）'],
    tap: ['点你的配置名，再点', { button: '更新' }],
    ok: '配置的更新时间变成刚才，就好了',
  },
  // MetaCubeX/ClashMetaForAndroid values-zh：profile「配置」、update「更新」（配置项菜单）
  'Clash Meta for Android': {
    where: ['打开 Clash Meta，点首页的', { button: '配置' }],
    tap: ['在你的配置名右边点 ⋮，选', { button: '更新' }],
    ok: '配置名下面的时间变成「刚刚」，就好了',
  },
  // hiddify-app zh-CN.i18n.json：卡片上的同步图标提示 pages.profiles.update「更新配置文件」
  Hiddify: {
    where: '打开 Hiddify，看首页最上面那张配置卡片',
    tap: ['点卡片右边的刷新按钮 ↻（鼠标停上去写着', { button: '更新配置文件' }, '）'],
    ok: '卡片上显示刚刚更新，就好了',
  },
  // 2dust/v2rayNG values-zh-rCN：title_sub_update「更新订阅」，主界面右上角三点菜单的最后一项
  v2rayNG: {
    where: '打开 v2rayNG，点右上角的 ⋮（三个点）',
    tap: ['点菜单最后一项', { button: '更新订阅', note: REFRESH_NOTE }],
    ok: '底部提示更新成功、节点列表刷新，就好了',
  },
  // clash-verge-rev src/locales/zh：侧栏 profiles「订 阅」（页面标题「订阅」）、右键菜单 update「更新」、刷新图标提示「刷新」
  'Clash Verge': {
    where: ['打开 Clash Verge，点左边栏的', { button: '订阅', note: '这一页放的就是你添加的链接' }],
    tap: ['在你的配置卡片上点右上角的刷新按钮 ↻，或者右键选', { button: '更新' }],
    ok: '卡片上的时间变成「刚刚」，就好了',
  },
  // 2dust/v2rayN ResUI.zh-Hans.resx：menuSubscription「订阅分组」、menuSubUpdate「更新全部订阅 (不通过代理)」
  v2rayN: {
    where: ['打开 v2rayN，点顶部菜单栏里的', { button: '订阅分组', note: '放你添加的链接的地方' }],
    tap: ['选', { button: '更新全部订阅 (不通过代理)', note: '把你添加的链接都更新一遍' }],
    ok: '下面的日志写着更新成功、节点列表刷新，就好了',
  },
}

/** 每段开头说清「你的配置名」是哪个 */
export const PROFILE_NAME_HINT = '你的配置名就是「我的套餐」卡片上「App 里显示为」后面的那个名字。'
/** 每段末尾的退路：按钮找不到时重新添加一次，效果一样 */
export const UPDATE_FALLBACK = '找不到这些按钮？在 App 里删掉这个配置，回「我的套餐」点「添加到 App」重新加一次，效果一样。'

/** 这台设备上推荐的 App，按推荐顺序（先常用的，再「其他 App」） */
export function guideApps(device: Device): string[] {
  return [...CLIENTS[device].top, ...CLIENTS[device].rest].map((a) => a.name)
}

/** 帮助中心里内置那一篇的地址：#/help?update=<设备> */
export const UPDATE_GUIDE_PARAM = 'update'

/** 地址里的设备；不认识的按这台设备算（链接被改坏时也给一篇能看的） */
export function parseGuideDevice(raw: string | null, fallback: Device): Device {
  return DEVICES.find((d) => d === raw) ?? fallback
}

/** 这台设备（按 UA 猜，与「添加到 App」同一个判断） */
export function thisDevice(): Device {
  return detectDevice(navigator.userAgent, navigator.maxTouchPoints)
}
