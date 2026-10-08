// ---------------------------------------------------------------------------
// 当前场景：单独成模块，目录（catalog.ts）与夹具（fixtures.ts）都要按它取数，放在任一边都会成环。
//   default / empty / multi / legacy / error / slow：见 fixtures.ts 的说明
//   proto-*：购买流程原型（.claude/purchase-proto）的 11 个场景，数据照原型 SCENARIOS，
//            套餐与流量包换成原型的三档目录，首次点击测试用；proto-legacy 是用户 10-07 补的「升级前的流量包挪一次」
// ---------------------------------------------------------------------------
export const PROTO_SCENARIOS = ['proto-s1', 'proto-s2', 'proto-s3', 'proto-s4', 'proto-s5a', 'proto-s5b', 'proto-s6', 'proto-s7', 'proto-s7b', 'proto-s7c', 'proto-s8', 'proto-legacy'] as const
export const SCENARIOS = ['default', 'empty', 'multi', 'legacy', 'error', 'slow', ...PROTO_SCENARIOS] as const
export type Scenario = (typeof SCENARIOS)[number]
export type ProtoScenario = (typeof PROTO_SCENARIOS)[number]

let current: Scenario = 'default'
const listeners: Array<() => void> = []

export const scenario = () => current
export const isProto = (s: Scenario = current): s is ProtoScenario => s.startsWith('proto-')

/** 切换场景；各模块经 onScenarioChange 登记清空自己的状态 */
export function switchScenario(name: string): boolean {
  if (!(SCENARIOS as readonly string[]).includes(name)) return false
  current = name as Scenario
  for (const fn of listeners) fn()
  return true
}

export function onScenarioChange(fn: () => void) {
  listeners.push(fn)
}
