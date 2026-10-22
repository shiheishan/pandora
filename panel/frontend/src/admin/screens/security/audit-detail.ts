import { formatDateTime } from '../../../core/format'
import { reasonLabel } from './logic'

// 审计一行展开后的细节：把后端原样给的 data（写入方约定不放秘密）整理成「改前 → 改后」与其余键值两块

/** 常见键的中文名；查不到的显示键名原文（等宽） */
const KEY_LABEL: Readonly<Record<string, string>> = {
  amount: '金额',
  balance_after: '调整后余额',
  balance: '余额',
  reason: '原因',
  name: '名称',
  status: '状态',
  email: '邮箱',
  target_email: '对象邮箱',
  code: '标识',
  kind: '类型',
  method: '方式',
  count: '数量',
  total: '合计',
  version: '版本',
  row_version: '行版本',
  order_no: '订单号',
  order_id: '订单',
  user_id: '用户',
  plan_id: '套餐',
  price_id: '价格',
  subscription_id: '订阅',
  server_id: '服务器',
  node_id: '节点',
  pool_id: '节点池',
  group_id: '用户组',
  ledger_txn: '账本流水',
  enabled: '启用',
  note: '备注',
  admin_note: '后台备注',
  region: '地区',
  tags: '标签',
  minutes: '分钟',
  ttl_minutes: '有效分钟',
  expires_at: '到期时间',
  sessions_revoked: '吊销会话',
  server_port: '端口',
  node_type: '协议',
  provider: '渠道',
  adapter: '渠道类型',
  fee: '手续费',
  subject: '标题',
  domain: '域名',
  domains: '域名',
  scope: '范围',
  source: '来源',
  view: '查看位置',
  filter: '筛选',
  label: '标签',
  description: '说明',
  credentials_changed: '改了密钥',
  secret_changed: '改了密钥',
  password_changed: '改了密码',
  bot_token_changed: '改了机器人令牌',
  before: '改前',
  after: '改后',
  days: '天数',
  period_end: '到期时间',
  previous_end: '原到期时间',
  rescued: '救回已到期',
}

/** 账号状态值的中文（identity 的四种），只用在 status 一类键上 */
const STATUS_VALUE: Readonly<Record<string, string>> = { active: '正常', pending: '待验证', suspended: '已停用', banned: '已封禁' }

/** 不按前后缀命名、但语义是一对的键：改前键 → 改后键 */
const NAMED_PAIRS: ReadonlyArray<readonly [string, string, string]> = [['previous_end', 'period_end', 'period_end']]

export function detailKeyLabel(key: string): { text: string; raw: boolean } {
  // 只有一边的 before / after 对象拆开后的键：before.<键>、after.<键>
  const side = /^(before|after)\.(.+)$/.exec(key)
  if (side) {
    const inner = detailKeyLabel(side[2]!)
    return { text: `${KEY_LABEL[side[1]!]} · ${inner.text}`, raw: inner.raw }
  }
  const known = KEY_LABEL[key]
  return known ? { text: known, raw: false } : { text: key, raw: true }
}

const ISO_TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/

/** 一个值的显示文字：布尔「是 / 否」、时间按本地时间、标量数组用顿号连，其余对象压成一行 JSON */
export function detailValue(v: unknown, key = ''): string {
  if (v === null || v === undefined) return '空'
  if (typeof v === 'boolean') return v ? '是' : '否'
  if (typeof v === 'number') return String(v)
  if (typeof v === 'string') {
    if (v === '') return '空'
    if (key === 'reason') return reasonLabel(v) ?? v
    return ISO_TIME.test(v) ? formatDateTime(v) : v
  }
  if (Array.isArray(v)) {
    if (v.length === 0) return '无'
    if (v.every((x) => x === null || typeof x !== 'object')) return v.map((x) => detailValue(x)).join('、')
  }
  return JSON.stringify(v)
}

export interface DetailChange {
  key: string
  before: string
  after: string
  changed: boolean
}

export interface DetailFact {
  key: string
  value: string
}

export interface AuditDetail {
  changes: DetailChange[]
  facts: DetailFact[]
}

const isPlainObject = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v)
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

function pair(key: string, before: unknown, after: unknown): DetailChange {
  return { key, before: detailValue(before, key), after: detailValue(after, key), changed: !same(before, after) }
}

/**
 * 拆 data：
 * - `before` / `after` 两个对象逐键对照（任一边是标量时整体对照，键名「值」）；
 * - 标量 `before` 配 `status`（账号状态）或 `enabled`（降级开关）；
 * - `X_before` 与 `X_after`、`before_X` 与 `after_X` 成对；只有 `before_X` / `X_before` 而 data 里有 `X` 时，`X` 当改后；
 * - 其余键原样列成键值，保持后端给的顺序；只有一边的 before / after 对象逐键列出。
 */
export function auditDetail(data: Readonly<Record<string, unknown>>): AuditDetail {
  const used = new Set<string>()
  const changes: DetailChange[] = []

  if ('before' in data && 'after' in data) {
    const { before, after } = data
    used.add('before').add('after')
    if (isPlainObject(before) && isPlainObject(after)) {
      const keys = [...Object.keys(before), ...Object.keys(after).filter((k) => !(k in before))]
      for (const k of keys) changes.push(pair(k, before[k], after[k]))
    } else {
      changes.push(pair('值', before, after))
    }
  } else if ('before' in data && 'status' in data && !isPlainObject(data.before)) {
    // 改账号状态：before 是旧状态、status 是新状态（identity 的封禁 / 停用），值译成账号状态
    used.add('before').add('status')
    const name = (v: unknown) => (typeof v === 'string' ? (STATUS_VALUE[v] ?? v) : v)
    changes.push(pair('status', name(data.before), name(data.status)))
  } else if ('before' in data && 'enabled' in data && !isPlainObject(data.before)) {
    // 降级开关：before 是旧的 enabled
    used.add('before').add('enabled')
    changes.push(pair('enabled', data.before, data.enabled))
  }

  for (const [from, to, label] of NAMED_PAIRS) {
    if (from in data && to in data) {
      used.add(from).add(to)
      changes.push(pair(label, data[from], data[to]))
    }
  }

  for (const key of Object.keys(data)) {
    if (used.has(key)) continue
    let base: string | null = null
    let afterKey: string | null = null
    if (key.endsWith('_before')) {
      base = key.slice(0, -'_before'.length)
      afterKey = `${base}_after` in data ? `${base}_after` : base in data ? base : null
    } else if (key.startsWith('before_')) {
      base = key.slice('before_'.length)
      afterKey = `after_${base}` in data ? `after_${base}` : base in data ? base : null
    }
    if (base === null || afterKey === null || used.has(afterKey)) continue
    used.add(key).add(afterKey)
    changes.push(pair(base, data[key], data[afterKey]))
  }

  const facts: DetailFact[] = []
  for (const k of Object.keys(data)) {
    if (used.has(k)) continue
    const v = data[k]
    // 只有一边的 before / after 对象：逐键列出，不压成一整串 JSON
    if ((k === 'before' || k === 'after') && isPlainObject(v)) {
      for (const [ik, iv] of Object.entries(v)) facts.push({ key: `${k}.${ik}`, value: detailValue(iv, ik) })
    } else {
      facts.push({ key: k, value: detailValue(v, k) })
    }
  }
  return { changes, facts }
}
