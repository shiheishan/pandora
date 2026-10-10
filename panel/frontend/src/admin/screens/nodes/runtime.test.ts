import { describe, expect, it } from 'vitest'
import * as runtime from './runtime'
import { applyPhaseLabel, copyPortValue, runState, runtimeReasonText, sameRunState, type RuntimeFields } from './runtime'

const base: RuntimeFields = {
  serving_status: 'active',
  last_heartbeat_at: '2026-10-07T00:00:00Z',
  stale: false,
  effective_state: 'applied',
  runtime_status: 'running',
  runtime_reason: null,
  runtime_reason_node: null,
  last_apply_failure: null,
}

describe('runtimeReasonText', () => {
  it('translates pdnd reason codes into Chinese', () => {
    expect(runtimeReasonText('port_in_use:443/tcp:7f3c2a10-0000-4000-8000-000000000001', '东京-01')).toBe('端口 443/TCP 被节点「东京-01」占用')
    expect(runtimeReasonText('port_in_use:8443/udp:7f3c2a10-0000-4000-8000-000000000001', null)).toBe('端口 8443/UDP 被本面板的另一个节点占用')
    expect(runtimeReasonText('port_in_use:443/tcp:other')).toBe('端口 443/TCP 被本机其他服务占用')
    expect(runtimeReasonText('not_started')).toBe('入站没有起来')
    expect(runtimeReasonText('config_apply_failed')).toBe('新配置装不上，仍在用旧配置服务')
    expect(runtimeReasonText('serving_cached_config')).toBe('面板还没确认，正用本地缓存的配置服务')
    expect(runtimeReasonText('something_new')).toBe('something_new')
    expect(runtimeReasonText(null)).toBe('')
  })
})

describe('runState', () => {
  it('shows the four states the design asks for', () => {
    expect(runState(base)).toEqual({ label: '运行中', tone: 'ok', detail: '' })
    expect(runState({ ...base, effective_state: 'pending' })?.label).toBe('待生效')
    expect(runState({ ...base, runtime_status: 'degraded', runtime_reason: 'serving_cached_config' })?.label).toBe('降级：用缓存服务')
    expect(runState({ ...base, runtime_status: 'degraded', runtime_reason: 'port_in_use:443/tcp:other' })).toEqual({
      label: '生效失败：端口 443/TCP 被本机其他服务占用',
      tone: 'danger',
      detail: '端口 443/TCP 被本机其他服务占用',
    })
  })

  it('falls back to the failed receipt when the node reports no reason', () => {
    const failed = { ...base, effective_state: 'failed' as const, last_apply_failure: { phase: 'failed', detail: 'bind: address already in use', at: '2026-10-07T00:00:00Z', generation: 3 } }
    expect(runState(failed)?.label).toBe('生效失败：bind: address already in use')
  })

  it('stays quiet for nodes the online state already explains', () => {
    expect(runState({ ...base, last_heartbeat_at: null })).toBeNull()
    expect(runState({ ...base, serving_status: 'retired' })).toBeNull()
    expect(runState({ ...base, stale: true })).toBeNull()
    // 离线但最后一次报的是失败：失败照样显示
    expect(runState({ ...base, stale: true, runtime_status: 'degraded', runtime_reason: 'not_started' })?.tone).toBe('danger')
  })

  it('compares by what the row shows', () => {
    expect(sameRunState(runState(base), runState({ ...base }))).toBe(true)
    expect(sameRunState(runState(base), runState({ ...base, effective_state: 'pending' }))).toBe(false)
    expect(sameRunState(null, null)).toBe(true)
  })
})

describe('legacy port conflict', () => {
  it('is not part of the runtime module', () => {
    expect(runtime).not.toHaveProperty('portConflictText')
  })
})

describe('copy and conflicts', () => {
  it('parses the copy port box', () => {
    expect(copyPortValue('')).toBeUndefined()
    expect(copyPortValue(' 8443 ')).toBe(8443)
    expect(copyPortValue('0')).toBe('invalid')
    expect(copyPortValue('65536')).toBe('invalid')
    expect(copyPortValue('44a')).toBe('invalid')
  })

  it('names a failed apply phase', () => {
    expect(applyPhaseLabel('health_failed')).toBe('健康检查失败')
  })
})
