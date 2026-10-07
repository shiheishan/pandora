import { describe, expect, it } from 'vitest'
import { poolBindingWarning } from './PoolCard'

describe('poolBindingWarning', () => {
  const pools = [
    { id: 'a', deliverable_nodes: 0 },
    { id: 'b', deliverable_nodes: 2 },
  ]

  it('warns when nothing is bound', () => {
    expect(poolBindingWarning(pools, [])).toContain('0 个节点')
  })

  it('warns when the bound pools carry no deliverable node', () => {
    expect(poolBindingWarning(pools, ['a'])).toContain('没有可下发的节点')
  })

  it('stays quiet once any bound pool can deliver', () => {
    expect(poolBindingWarning(pools, ['a', 'b'])).toBeNull()
  })
})
