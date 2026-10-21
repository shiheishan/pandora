import { describe, expect, it } from 'vitest'
import * as runtime from './runtime'
import {
  CERT_WARN_DAYS,
  certBadge,
  certReasonText,
  runState,
  runtimeReasonText,
  sameRunState,
  type RuntimeFields,
} from './runtime'

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

const NOW = Date.parse('2026-10-20T00:00:00Z')

const certBase = {
  mode: 'acme' as const,
  identifier: 'node1.example.com',
  identifier_kind: 'domain' as const,
  error_code: null,
  error_detail: null,
  next_attempt_at: null,
  reported_at: '2026-10-19T12:00:00Z',
}

describe('runtimeReasonText', () => {
  it('translates pdnd reason codes into Chinese', () => {
    expect(runtimeReasonText('port_in_use:443/tcp:7f3c2a10-0000-4000-8000-000000000001', '东京-01')).toBe('端口 443/TCP 被节点「东京-01」占用')
    expect(runtimeReasonText('port_in_use:8443/udp:7f3c2a10-0000-4000-8000-000000000001', null)).toBe('端口 8443/UDP 被本面板的另一个节点占用')
    expect(runtimeReasonText('port_in_use:443/tcp:other')).toBe('端口 443/TCP 被本机其他服务占用')
    expect(runtimeReasonText('port_in_use:8388/tcp+udp:other')).toBe('端口 8388/TCP+UDP 被本机其他服务占用')
    expect(runtimeReasonText('not_started')).toBe('入站没有起来')
    expect(runtimeReasonText('config_apply_failed')).toBe('新配置装不上，仍在用旧配置服务')
    expect(runtimeReasonText('serving_cached_config')).toBe('面板还没确认，正用本地缓存的配置服务')
    expect(runtimeReasonText('cert_pending')).toBe("等待证书：节点端正在向 Let's Encrypt 申请")
    expect(runtimeReasonText('cert_expired')).toBe('证书已过期：续期一直失败，客户端会校验失败')
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

  it('prioritizes cert_pending and cert_expired before other failures', () => {
    expect(
      runState({ ...base, effective_state: 'failed', runtime_reason: 'cert_pending', runtime_status: 'degraded' }),
    ).toEqual({
      label: '等待证书',
      tone: 'info',
      detail: '节点端正在申请证书，申请到就自动生效；进度看「证书」一栏',
    })
    expect(runState({ ...base, runtime_reason: 'cert_expired', runtime_status: 'degraded' })).toEqual({
      label: '证书已过期',
      tone: 'danger',
      detail: '证书已过期：续期一直失败，客户端会校验失败',
    })
  })

  it('falls back to the failed receipt when the node reports no reason', () => {
    const failed = { ...base, effective_state: 'failed' as const, last_apply_failure: { phase: 'failed', detail: 'bind: address already in use', at: '2026-10-07T00:00:00Z' } }
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

describe('certReasonText', () => {
  it('maps known codes and passes through unknown', () => {
    expect(certReasonText('validation_failed')).toContain("Let's Encrypt")
    expect(certReasonText('rate_limited')).toContain('频率限制')
    expect(certReasonText('weird_code')).toBe('weird_code')
    expect(certReasonText(null)).toBe('')
  })
})

describe('certBadge', () => {
  it('returns null when cert is absent', () => {
    expect(certBadge(null, NOW)).toBeNull()
  })

  it('shows applying when there is no cert and no error', () => {
    expect(certBadge({ ...certBase, not_after: null }, NOW)).toMatchObject({ label: '证书申请中', tone: 'info' })
  })

  it('shows apply failed when there is no cert but an error', () => {
    expect(
      certBadge({ ...certBase, not_after: null, error_code: 'validation_failed', error_detail: 'connection refused' }, NOW),
    ).toMatchObject({ label: '证书申请失败', tone: 'danger' })
  })

  it('shows expired when not_after is in the past', () => {
    expect(certBadge({ ...certBase, not_after: '2026-10-19T00:00:00Z' }, NOW)).toMatchObject({ label: '证书已过期', tone: 'danger' })
  })

  it('warns on renewal failure while cert is still valid', () => {
    expect(
      certBadge({ ...certBase, not_after: '2026-11-01T00:00:00Z', error_code: 'rate_limited' }, NOW),
    ).toMatchObject({ label: '续期失败，12 天内到期', tone: 'warn' })
  })

  it('uses tighter warn window for IP certificates', () => {
    const ip = {
      ...certBase,
      identifier: '192.0.2.10',
      identifier_kind: 'ip' as const,
      not_after: '2026-10-21T00:00:00Z',
    }
    expect(certBadge(ip, NOW)).toMatchObject({ label: '1 天内到期', tone: 'warn' })
    expect(certBadge({ ...ip, not_after: '2026-10-25T00:00:00Z' }, NOW)).toMatchObject({ label: '证书有效，5 天内到期', tone: 'ok' })
  })

  it('warns when domain cert is within 14 days', () => {
    expect(certBadge({ ...certBase, not_after: '2026-10-30T00:00:00Z' }, NOW)).toMatchObject({ label: '10 天内到期', tone: 'warn' })
    expect(CERT_WARN_DAYS.other).toBe(14)
  })
})
