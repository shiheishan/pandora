/**
 * [INPUT]: 依赖 ../../../core/format 的 formatMoney，依赖 ../common/traffic 的 compactBytes，依赖 ../common/orders 的 intervalLabel，依赖 ./api 的 GiftCard / Redemption 类型
 * [OUTPUT]: 对外提供 TOPUP_PRESETS、parseTopupAmount、ledgerLabel、giftFace、giftNote、redemptionGain、normalizeGiftCode
 * [POS]: portal/screens/wallet 的纯映射：充值金额元 → 分与上下限校验（¥1–¥50000，契约门户-05）、余额流水的账本类型中文名（挂账按保留规则 6 称「挂账转入」）、礼品卡卡面与说明、兑换记录的「获得」列；有单元测试
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatMoney } from '../../../core/format'
import { intervalLabel } from '../common/orders'
import { compactBytes } from '../common/traffic'
import type { GiftCard, Redemption } from './api'

export const TOPUP_PRESETS = [50, 100, 200, 500] as const

/** 元 → 分；最多两位小数，¥1–¥50000（后端 100–5000000 分） */
export function parseTopupAmount(input: string): { cents: number } | { error: string } {
  const text = input.trim()
  if (!text) return { error: '请输入充值金额' }
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return { error: '金额最多两位小数' }
  const cents = Math.round(Number(text) * 100)
  if (cents < 100) return { error: '最少充值 ¥1' }
  if (cents > 5_000_000) return { error: '单次最多充值 ¥50,000' }
  return { cents }
}

/** 契约门户-05 余额明细的 kind 映射；未知类型返回 null，由调用方显示 memo */
const LEDGER: Readonly<Record<string, string>> = {
  balance_topup: '充值',
  balance_hold: '下单抵扣（冻结）',
  balance_release: '订单取消退回',
  balance_adjusted: '人工调整',
  commission_to_balance: '佣金转入',
  gift_card_balance: '礼品卡',
  late_payment_applied: '挂账转入',
  late_payment_suspense: '挂账转入',
}
export const ledgerLabel = (kind: string): string | null => LEDGER[kind] ?? null

/** 卡码只收字母数字（服务端也转大写去空白）；输入里的空格与连字符之外的符号原样留给后端判 */
export const normalizeGiftCode = (raw: string) => raw.replace(/\s+/g, '').toUpperCase()

/** 卡面大字：余额 / 流量 / 延期 / 套餐 / 盲盒 */
export function giftFace(card: GiftCard): string {
  if (card.type === 'mystery') {
    const labels = (card.rewards.pool ?? []).map((p) => p.label).filter(Boolean)
    return labels.length ? `盲盒：可能抽到 ${labels.join(' / ')}` : '盲盒'
  }
  if (card.type === 'plan') return [card.plan_name ?? card.name, intervalLabel(card.interval, card.interval_count)].filter(Boolean).join(' ')
  const r = card.rewards
  const parts = [
    r.balance ? `${formatMoney(r.balance)} 余额` : '',
    r.traffic_bytes ? `流量 +${compactBytes(r.traffic_bytes)}` : '',
    r.expire_days ? `延长 ${r.expire_days} 天` : '',
    r.reset_quota ? '重置本期流量' : '',
  ].filter(Boolean)
  return parts.length ? parts.join(' + ') : card.name
}

/** 卡面小字：优先后台写的说明，缺省按类型给固定文案 */
export function giftNote(card: GiftCard): string {
  if (card.description.trim()) return card.description
  if (card.type === 'plan') return '兑换后开通或延长对应套餐'
  if (card.type === 'mystery') return '兑换时随机抽取其中一项'
  if (card.rewards.traffic_bytes) return '流量进流量包余额，不过期，用完为止'
  if (card.rewards.balance) return '兑换后直接计入余额'
  return '兑换后立即生效'
}

/** 我的礼品卡「获得」列：盲盒奖品名优先，其次余额 / 流量 / 延期，套餐卡用模板名 */
export function redemptionGain(r: Redemption): string {
  if (r.prize_label) return r.prize_label
  const parts = [r.balance ? `${formatMoney(r.balance)} 余额` : '', r.traffic_bytes ? `流量 +${compactBytes(r.traffic_bytes)}` : '', r.expire_days ? `延长 ${r.expire_days} 天` : ''].filter(Boolean)
  return parts.length ? parts.join(' + ') : r.template_name
}
