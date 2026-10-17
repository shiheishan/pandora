import { WITHDRAW_METHODS } from '../../../core/withdraw-methods'
import { minorToYuan, parseCount, yuanToMinor, type Built, type FieldErrors } from './logic'
import type { CommissionOverview, CommissionOverride } from './schemas'

export interface SettingsForm {
  rate: string
  freezeDays: string
  scope: 'first_order' | 'every_order'
  distributionOn: boolean
  l1: string
  l2: string
  l3: string
  withdrawOn: boolean
  minWithdraw: string
  methods: string[]
}

/** 概览 → 佣金规则表单 */
export function settingsToForm(o: CommissionOverview): SettingsForm {
  return {
    rate: String(o.rate_percent),
    freezeDays: String(o.freeze_days),
    scope: o.scope,
    distributionOn: o.distribution_enabled,
    l1: String(o.distribution_levels[0]),
    l2: String(o.distribution_levels[1]),
    l3: String(o.distribution_levels[2]),
    withdrawOn: o.withdraw_enabled,
    minWithdraw: minorToYuan(o.min_withdraw),
    methods: [...o.withdraw_methods],
  }
}

function parseLevel(s: string): number | null {
  const t = s.trim()
  if (!/^\d+$/.test(t)) return null
  const n = Number(t)
  if (!Number.isInteger(n) || n < 0 || n > 100) return null
  return n
}

/** 三级份额之和（非法项按 0 算，只用于表单上的合计提示） */
export function levelsSum(f: SettingsForm): number {
  return [f.l1, f.l2, f.l3].reduce((sum, s) => sum + (parseLevel(s) ?? 0), 0)
}

const levelMsg = '比例需在 0 到 100 之间'

/** 表单 → POST v1/commission/config（一次发全部 10 个字段）；错误键与后端 fields 同名 */
export function buildSettingsConfig(f: SettingsForm): Built<Record<string, unknown>> {
  const errors: FieldErrors = {}

  // 1. 返佣比例：0–50 整数
  const rateTrim = f.rate.trim()
  let rate: number | null = null
  if (!/^\d+$/.test(rateTrim)) {
    errors.rate_percent = '佣金比例需在 0 到 50 之间'
  } else {
    const n = Number(rateTrim)
    if (!Number.isInteger(n) || n < 0 || n > 50) {
      errors.rate_percent = '佣金比例需在 0 到 50 之间'
    } else {
      rate = n
    }
  }

  // 2. 结算冻结期：0–90 整数
  const freeze = parseCount(f.freezeDays)
  if (freeze === null || Number.isNaN(freeze) || freeze < 0 || freeze > 90) {
    errors.freeze_days = '冻结天数需在 0 到 90 之间'
  }

  // 3. 最低提现金额：非负金额，最多两位小数
  const minW = yuanToMinor(f.minWithdraw)
  if (minW === null) {
    errors.min_withdraw = '填金额，最多两位小数'
  }

  // 4. 三级分销
  const l1 = parseLevel(f.l1)
  const l2 = parseLevel(f.l2)
  const l3 = parseLevel(f.l3)
  if (l1 === null) errors.distribution_l1 = levelMsg
  if (l2 === null) errors.distribution_l2 = levelMsg
  if (l3 === null) errors.distribution_l3 = levelMsg

  const sum = (l1 ?? 0) + (l2 ?? 0) + (l3 ?? 0)
  if (l1 !== null && l2 !== null && l3 !== null && sum > 100) {
    errors.distribution_l1 = '三级比例之和不能超过 100'
  }
  if (f.distributionOn && l1 === 0 && l2 === 0 && l3 === 0) {
    errors.distribution_l1 = '开启多级分销时至少要给一级设比例'
  }

  // 5. 提现方式：按目录顺序输出，至少选一种
  const methods = WITHDRAW_METHODS.filter((m) => f.methods.includes(m))
  if (methods.length === 0) {
    errors.withdraw_methods = f.withdrawOn
      ? '开着提现时至少要选一种提现方式'
      : '关着提现时也至少要选一种提现方式，重新打开提现时直接生效'
  }

  if (Object.keys(errors).length) return { ok: false, errors }

  return {
    ok: true,
    body: {
      rate_percent: rate!,
      freeze_days: freeze!,
      min_withdraw: minW!,
      scope: f.scope,
      distribution_enabled: f.distributionOn,
      distribution_l1: l1!,
      distribution_l2: l2!,
      distribution_l3: l3!,
      withdraw_enabled: f.withdrawOn,
      withdraw_methods: methods,
    },
  }
}

export interface OverrideForm {
  email: string
  rate: string
  scope: '' | 'first_order' | 'every_order'
}

/** 表单 → POST v1/commission/overrides；比例或范围留空 = 跟全局走（发 null） */
export function buildOverrideBody(
  f: OverrideForm,
): Built<{ email: string; rate_percent: number | null; scope: string | null }> {
  const errors: FieldErrors = {}
  const email = f.email.trim()
  if (!email) errors.email = '请填写用户邮箱'

  const rateTrim = f.rate.trim()
  let rate_percent: number | null = null
  if (rateTrim !== '') {
    if (!/^\d+$/.test(rateTrim)) {
      errors.rate_percent = '佣金比例需在 0 到 50 之间'
    } else {
      const n = Number(rateTrim)
      if (!Number.isInteger(n) || n < 0 || n > 50) errors.rate_percent = '佣金比例需在 0 到 50 之间'
      else rate_percent = n
    }
  }

  const scope = f.scope === '' ? null : f.scope

  if (rateTrim === '' && f.scope === '') {
    errors.rate_percent = '比例与计佣范围至少设一项'
  }

  if (Object.keys(errors).length) return { ok: false, errors }

  return { ok: true, body: { email, rate_percent, scope } }
}

/** 单独设置一行的说明，如「比例 15% · 仅首单」「不拿佣金 · 范围跟全局」 */
export function overrideSummary(o: Pick<CommissionOverride, 'rate_percent' | 'scope'>): string {
  const ratePart =
    o.rate_percent === null ? '比例跟全局' : o.rate_percent === 0 ? '不拿佣金' : `比例 ${o.rate_percent}%`
  const scopePart =
    o.scope === null ? '范围跟全局' : o.scope === 'first_order' ? '仅首单' : '每笔订单'
  return `${ratePart} · ${scopePart}`
}
