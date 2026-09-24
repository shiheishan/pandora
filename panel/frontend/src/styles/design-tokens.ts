/**
 * [INPUT]: 无依赖；逐条转写自设计稿 设计规范.dc.html（02 颜色、03 字体、04 尺寸）与模块文件共用的 html:root 令牌块
 * [OUTPUT]: 对外提供 COLOR_TOKENS、TYPE_SCALE、SPACE_SCALE、RADIUS_SCALE、ROLE_TOKENS 与 normalizeCssValue
 * [POS]: styles 的设计稿核对表：tokens.css / roles.css 是实现，这里是设计稿原值；tests/tokens.test.ts 拿它逐条比对 CSS 文本，showcase 拿它比对浏览器里算出的值
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

export type TokenGroup = 'neutral' | 'brand' | 'status' | 'overlay' | 'supplement'

export interface ColorToken {
  name: string
  light: string
  dark: string
  group: TokenGroup
  use: string
}

const c = (group: TokenGroup, name: string, light: string, dark: string, use: string): ColorToken => ({
  name,
  light,
  dark,
  group,
  use,
})

// ---------------------------------------------------------------------------
// 颜色：前 33 个与模块文件 html:root / [data-theme="dark"] 逐字一致；
// supplement 组来自规范页组件区 L / D 对象里模块文件没有收成变量的值。
// ---------------------------------------------------------------------------
export const COLOR_TOKENS: readonly ColorToken[] = [
  c('neutral', '--bg', '#f5f4f0', '#111113', '页面底色'),
  c('neutral', '--surface', '#fff', '#1a1a1d', '卡片、输入框、表格、菜单'),
  c('neutral', '--surface-2', '#f9f8f5', '#1f1f22', '列表行悬停、次级底'),
  c('neutral', '--surface-3', '#efede8', '#26262a', '分段控件底、菜单项悬停、中性标签、骨架块'),
  c('neutral', '--border', '#e7e5df', '#2b2b2f', '卡片描边、分隔线'),
  c('neutral', '--border-strong', '#dcd9d1', '#37373c', '输入框和次按钮的描边'),
  c('neutral', '--border-hover', '#c9c6bd', '#4a4a50', '描边悬停、关闭态开关'),
  c('neutral', '--text', '#1c1c1f', '#ecebe7', '正文、标题、Toast 底、后台主按钮'),
  c('neutral', '--text-2', '#62615c', '#9a988f', '辅助说明、未选中导航'),
  c('neutral', '--text-3', '#6e6c66', '#8a8880', '占位符、表头、时间戳'),

  c('brand', '--brand', '#b9442b', '#e46e52', '门户主按钮、链接、进度条'),
  c('brand', '--brand-hover', '#a33a23', '#ec8267', '悬停和按下'),
  c('brand', '--brand-ink', '#8c3019', '#f2a58f', '浅底上的文字、链接悬停'),
  c('brand', '--brand-soft', '#f6e4de', '#3a231c', '头像、公告横幅、聚焦外圈、进度条轨道'),
  c('brand', '--brand-tint', '#fbf3ef', '#201816', '套餐卡底色'),
  c('brand', '--on-brand', '#fff', '#170d0a', '朱砂实心底上的字'),

  c('status', '--ok', '#1f7a4f', '#6fcf9b', '在线、已支付、已打款、生效中'),
  c('status', '--ok-soft', '#e3f3ea', '#15271e', '成功标签底'),
  c('status', '--ok-line', '#c9f0d8', '#1f3a2b', '成功描边'),
  c('status', '--warn', '#9a5c00', '#f0b35a', '待处理、维护中、7 天内到期、流量 ≥ 80%'),
  c('status', '--warn-soft', '#fbf0dc', '#2c2213', '警告标签底'),
  c('status', '--warn-line', '#f0d9b0', '#4a3a1c', '警告描边'),
  c('status', '--danger', '#b3263a', '#f07a86', '离线、欠费、失败、危险操作、流量 ≥ 95%'),
  c('status', '--danger-soft', '#f8e1e3', '#2c1519', '危险标签底'),
  c('status', '--danger-line', '#eec3c8', '#4d252b', '危险描边'),
  c('status', '--danger-tint', '#fdf3f3', '#221417', '危险区块极浅底'),
  c('status', '--info', '#34507c', '#93b1dc', '处理中、客服已回复、人工确认'),
  c('status', '--info-soft', '#e6ebf3', '#172031', '信息标签底'),
  c('status', '--info-line', '#cdd6e4', '#2a3a52', '信息描边'),

  c('overlay', '--on-ink', '#fff', '#111113', '墨色实心底上的字（后台主按钮）'),
  c('overlay', '--hdr', 'rgba(245,244,240,.9)', 'rgba(17,17,19,.88)', '半透明顶栏'),
  c('overlay', '--scrim', 'rgba(20,20,24,.4)', 'rgba(0,0,0,.55)', '弹窗遮罩'),
  c('overlay', '--shadow', '0 16px 36px -14px rgba(28,28,31,.25)', '0 16px 40px -12px rgba(0,0,0,.6)', '菜单、Toast、弹窗'),

  c('supplement', '--on-danger', '#fff', '#1a0f12', '危险实心按钮上的字'),
  c('supplement', '--toast-ok', '#6fcf9b', '#1f7a4f', 'Toast 成功圆点（反色底）'),
  c('supplement', '--toast-danger', '#f07a86', '#b3263a', 'Toast 错误圆点（反色底）'),
  c('supplement', '--shadow-segment', '0 1px 2px rgba(20,20,24,.08)', '0 1px 2px rgba(20,20,24,.08)', '分段控件选中'),
  c('supplement', '--sidebar', '#151518', '#0a0a0b', '后台侧栏（两种模式下都是深底）'),
  c('supplement', '--sidebar-active', '#26262a', '#1f1f22', '侧栏当前菜单底'),
  c('supplement', '--sidebar-text', '#ecebe7', '#ecebe7', '侧栏字标、当前菜单'),
  c('supplement', '--sidebar-text-2', '#9a988f', '#9a988f', '侧栏未选中菜单'),
  c('supplement', '--sidebar-text-3', '#8a8880', '#8a8880', '侧栏 ADMIN 标记'),
  c('supplement', '--sidebar-brand', '#e46e52', '#e46e52', '侧栏 Logo 点与当前菜单圆点'),
]

export interface ScaleToken {
  name: string
  value: string
  use: string
}

const s = (name: string, value: string, use: string): ScaleToken => ({ name, value, use })

export interface TypeToken {
  name: string
  size: string
  weight: number
  tracking: string
  sample: string
  spec: string
}

export const TYPE_SCALE: readonly TypeToken[] = [
  { name: 'display', size: '36px', weight: 600, tracking: '-0.025em', sample: '设计规范', spec: '36 / 600 / −2.5% · 仅用于营销和空状态' },
  { name: 'h1', size: '24px', weight: 600, tracking: '-0.015em', sample: '我的订阅', spec: '24 / 600 / −1.5% · 页面标题' },
  { name: 'h2', size: '18px', weight: 600, tracking: '-0.01em', sample: '订阅地址', spec: '18 / 600 · 区块标题' },
  { name: 'h3', size: '15px', weight: 600, tracking: '0', sample: '标准版 · 月付', spec: '15 / 600 · 卡片标题' },
  { name: 'body', size: '14px', weight: 400, tracking: '0', sample: '在已登录的设备上打开快捷登录生成链接。', spec: '14 / 400 / 行高 1.55 · 门户正文' },
  { name: 'body-sm', size: '13px', weight: 400, tracking: '0', sample: '门户会立即隐藏这条公告。', spec: '13 / 400 / 行高 1.5 · 后台正文' },
  { name: 'caption', size: '12px', weight: 400, tracking: '0', sample: '到期 2026-10-31 · 剩余 38 天', spec: '12 / 400 · 中文最小字号：标签、表头、说明' },
  { name: 'micro', size: '11px', weight: 500, tracking: '0', sample: '3 · VLESS · ADMIN', spec: '11 / 500 · 只用于数字和英文：角标、协议名' },
]

export const SPACE_SCALE: readonly ScaleToken[] = [
  s('--space-4', '4px', '图标与文字'),
  s('--space-8', '8px', '按钮组、标签组'),
  s('--space-12', '12px', '表单字段之间'),
  s('--space-16', '16px', '卡片之间、列表行左右、后台卡片内边距'),
  s('--space-20', '20px', '门户卡片内边距（含套餐卡）、标题与内容'),
  s('--space-24', '24px', '卡片内分组之间'),
  s('--space-32', '32px', '页面顶部'),
  s('--space-64', '64px', '页面底部'),
]

export const RADIUS_SCALE: readonly ScaleToken[] = [
  s('--radius-tag', '5px', '所有标签（统一方角，不用胶囊）'),
  s('--radius-sm', '7px', '后台控件、菜单项'),
  s('--radius-md', '9px', '门户按钮、输入框'),
  s('--radius-lg', '12px', '菜单、Toast、后台卡片'),
  s('--radius-xl', '14px', '门户卡片、弹窗'),
  s('--radius-pill', '999px', '余额、角标、头像、开关'),
]

// ---------------------------------------------------------------------------
// 角色令牌：规范「门户：朱砂用在哪里」「后台：保持中性」与控件高度表
// ---------------------------------------------------------------------------
export interface RoleToken {
  name: string
  portal: string
  admin: string
  use: string
}

const r = (name: string, portal: string, admin: string, use: string): RoleToken => ({ name, portal, admin, use })

export const ROLE_TOKENS: readonly RoleToken[] = [
  r('--accent', 'var(--brand)', 'var(--text)', '主按钮、开关、选中、进度'),
  r('--accent-hover', 'var(--brand-hover)', 'var(--text)', '主按钮悬停'),
  r('--on-accent', 'var(--on-brand)', 'var(--on-ink)', '主按钮上的字'),
  r('--focus-halo', 'var(--brand-soft)', 'var(--border)', '输入框聚焦外圈 3px'),
  r('--focus-ring', 'var(--brand)', 'var(--text)', '键盘焦点外圈'),
  r('--link', 'var(--brand)', 'var(--text)', '文字链接'),
  r('--link-hover', 'var(--brand-ink)', 'var(--text-2)', '链接悬停'),
  r('--link-decoration', 'none', 'underline', '链接下划线'),
  r('--link-underline', 'transparent', 'var(--border-hover)', '下划线颜色'),
  r('--body-size', 'var(--fs-body)', 'var(--fs-body-sm)', '正文字号 14 / 13'),
  r('--body-leading', 'var(--lh-body)', 'var(--lh-body-sm)', '正文行高 1.55 / 1.5'),
  r('--h-control', '40px', '32px', '门户主按钮与输入框 / 后台默认按钮与输入框'),
  r('--h-control-sm', '36px', '28px', '门户弹窗按钮与菜单项 / 后台工具栏与表格内'),
  r('--h-control-xs', '32px', '24px', '门户顶栏控件 / 后台紧凑按钮'),
  r('--radius-control', 'var(--radius-md)', 'var(--radius-sm)', '按钮、输入框圆角 9 / 7'),
  r('--radius-card', 'var(--radius-xl)', 'var(--radius-lg)', '卡片圆角 14 / 12'),
  r('--pad-card', 'var(--space-20)', 'var(--space-16)', '卡片内边距 20 / 16'),
]

/** 比较 CSS 值时忽略空白、大小写与小数点前的 0：rgba(20, 20, 24, 0.4) ≡ rgba(20,20,24,.4)。 */
export function normalizeCssValue(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replace(/\s*,\s*/g, ',')
    .replace(/\s+/g, ' ')
    .replace(/(^|[^\d.])0\.(\d)/g, '$1.$2')
    .replace(/'/g, '"')
}
