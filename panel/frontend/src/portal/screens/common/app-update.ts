import { CLIENTS, DEVICES, detectDevice, type Device } from './clients'

// ---------------------------------------------------------------------------
// 「在 App 里点一次更新」怎么点（首次点击测试 s4：新手不知道点哪）。clients.ts 按设备推荐的每个 App
// 一段，三步：在哪个页面、点哪个按钮、看到什么算成功。帮助中心里是内置的一篇（#/help?update=<设备>），
// 完成页与换新链接页「点一次更新」旁边链接到这台设备那一段。
// 叫法只用门户的（配置名、链接、App），App 自己的按钮名照它的界面写；中文界面里带禁用词的，写英文界面的按钮名。
// ---------------------------------------------------------------------------
export interface AppUpdateSteps {
  /** 在哪个页面 */
  where: string
  /** 点哪个按钮 */
  tap: string
  /** 看到什么算成功 */
  ok: string
}

export const APP_UPDATE: Readonly<Record<string, AppUpdateSteps>> = {
  Shadowrocket: {
    where: '打开 Shadowrocket，停在底部的「首页」',
    tap: '在列表里找到你的配置名那一行，向左滑，点「更新」',
    ok: '那一行下面的节点刷新成新的，就好了',
  },
  Stash: {
    where: '打开 Stash，进入「配置」页（英文界面是「Profiles」）',
    tap: '找到你的配置名，向左滑点「更新」，或者在列表上往下拉一下',
    ok: '配置名下面的更新时间变成刚刚，就好了',
  },
  'Quantumult X': {
    where: '点右下角的风车按钮，往下找到「节点」那一栏',
    tap: '找到你的配置名，向左滑点「更新」',
    ok: '回到首页，节点列表变成新的，就好了',
  },
  Loon: {
    where: '打开 Loon，点底部的「配置」',
    tap: '在「节点」那一栏找到你的配置名，向左滑点「更新」',
    ok: '节点列表刷新成新的，就好了',
  },
  'sing-box': {
    where: '打开 sing-box，点「Profiles」（配置）',
    tap: '点你的配置名，再点「Update」',
    ok: '「Last Updated」变成刚才的时间，就好了',
  },
  'Clash Meta for Android': {
    where: '打开 Clash Meta，点首页的「配置」',
    tap: '在你的配置名右边点 ⋮，选「更新」',
    ok: '配置名下面的时间变成「刚刚」，就好了',
  },
  Hiddify: {
    where: '打开 Hiddify，看首页最上面那张配置卡片',
    tap: '点卡片右边的刷新按钮 ↻',
    ok: '卡片上显示刚刚更新，就好了',
  },
  v2rayNG: {
    where: '打开 v2rayNG，点右上角的 ⋮（三个点）',
    tap: '点菜单里更新的那一项（英文界面是「Update subscription」）',
    ok: '底部提示更新成功、节点列表刷新，就好了',
  },
  'Clash Verge': {
    where: '打开 Clash Verge，点左边栏的配置页（英文界面是「Profiles」）',
    tap: '在你的配置卡片上点右上角的刷新按钮 ↻，或者右键选「更新」',
    ok: '卡片上的时间变成「刚刚」，就好了',
  },
  v2rayN: {
    where: '打开 v2rayN，点顶部菜单栏里管理分组的那个菜单（英文界面是「Subscription Group」）',
    tap: '选「更新」开头、写着「不通过代理」的那一项',
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
