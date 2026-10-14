// ---------------------------------------------------------------------------
// 提现方式：目录与后端 settings 的 WithdrawMethodCatalog 同序同名；other 是迁移前的旧申请（自由填写），
// 只用于展示，不能再选。USDT 地址与交易哈希只做格式提示，真正的校验（含校验和）在后端。
// ---------------------------------------------------------------------------
export const WITHDRAW_METHODS = ['alipay', 'wechat', 'bank', 'paypal', 'usdt_trc20', 'usdt_erc20', 'usdt_bep20'] as const
export type WithdrawMethod = (typeof WITHDRAW_METHODS)[number]

const LABEL: Record<string, string> = {
  alipay: '支付宝',
  wechat: '微信',
  bank: '银行卡',
  paypal: 'PayPal',
  usdt_trc20: 'USDT（TRC20）',
  usdt_erc20: 'USDT（ERC20）',
  usdt_bep20: 'USDT（BEP20）',
  other: '其他（自由填写）',
}

/** 方式的中文名；目录外的原样显示 */
export const withdrawMethodLabel = (method: string): string => LABEL[method] ?? method

export const isUSDTMethod = (method: string): boolean => method.startsWith('usdt_')

/** 收款账号输入框的占位提示 */
export function payoutAccountHint(method: string): string {
  switch (method) {
    case 'alipay':
      return '支付宝账号与实名，如 138****0000 张三'
    case 'wechat':
      return '微信号与实名'
    case 'bank':
      return '开户行、卡号与户名'
    case 'paypal':
      return 'PayPal 邮箱'
    case 'usdt_trc20':
      return 'T 开头的 TRC20 地址（34 位）'
    case 'usdt_erc20':
    case 'usdt_bep20':
      return '0x 开头的地址（42 位）'
    default:
      return '收款信息'
  }
}

/** 打款时流水号的提示：USDT 填链上交易哈希 */
export function payoutReferenceHint(method: string): string {
  if (method === 'usdt_trc20') return 'TRC20 交易哈希（64 位十六进制）'
  if (method === 'usdt_erc20' || method === 'usdt_bep20') return '交易哈希（0x 开头加 64 位十六进制）'
  return '转账流水号'
}

/** 前端预检：只看形状（长度、前缀、字符集），不算校验和；通过的不一定合法，以后端为准 */
export function looksLikeUSDTAddress(method: string, address: string): boolean {
  const a = address.trim()
  if (method === 'usdt_trc20') return /^T[1-9A-HJ-NP-Za-km-z]{33}$/.test(a)
  if (method === 'usdt_erc20' || method === 'usdt_bep20') return /^0x[0-9a-fA-F]{40}$/.test(a)
  return true
}

/** 前端预检交易哈希的形状 */
export function looksLikeTxHash(method: string, hash: string): boolean {
  const h = hash.trim()
  if (method === 'usdt_trc20') return /^[0-9a-fA-F]{64}$/.test(h)
  if (method === 'usdt_erc20' || method === 'usdt_bep20') return /^0x[0-9a-fA-F]{64}$/.test(h)
  return true
}
