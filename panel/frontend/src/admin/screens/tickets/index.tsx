import { useMutation } from '@tanstack/react-query'
import { useCallback, useEffect, useState } from 'react'
import { navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, SearchField, Toolbar, useToast } from '../../../ui'
import { useAdminMe } from '../../me'
import { useFailure, useIntentKey } from '../../actions'
import type { AdminScreenProps } from '../index'
import { Detail } from './Detail'
import { escalatedSchema, useInvalidateTickets } from './api'
import { isFilter, queueParams, type QueueFilter } from './model'
import { Queue } from './Queue'
import { TicketSettingsDialog } from './TicketSettingsDialog'
import css from './Tickets.module.css'

export default function Tickets({ rest }: AdminScreenProps) {
  const location = useHashLocation()
  const me = useAdminMe()
  const [settings, setSettings] = useState(false)
  const rawFilter = location.query.get('f')
  const filter: QueueFilter = isFilter(rawFilter) ? rawFilter : 'active'
  const q = location.query.get('q') ?? ''
  const selected = rest[0] ?? null

  // 筛选与搜索回写地址（replace，不留历史），保留当前选中的工单
  const setQuery = useCallback(
    (next: { f?: QueueFilter; q?: string }) => {
      const f = next.f ?? filter
      navigate(selected ? `/tickets/${encodeURIComponent(selected)}` : '/tickets', {
        replace: true,
        query: { f: f === 'active' ? undefined : f, q: next.q ?? q },
      })
    },
    [filter, q, selected],
  )

  return (
    <div className={css.screen}>
      <Toolbar end={<ToolbarEnd onOpenSettings={() => setSettings(true)} />}>
        <SearchBox value={q} onChange={(text) => setQuery({ q: text })} />
      </Toolbar>
      <TicketSettingsDialog open={settings} onClose={() => setSettings(false)} />
      {selected && (
        <button
          type="button"
          className={css.pickBack}
          onClick={() =>
            navigate('/tickets', {
              query: { f: filter === 'active' ? undefined : filter, q: q || undefined },
            })
          }
        >
          ← 返回工单列表
        </button>
      )}
      <div className={`${css.page} ${selected ? css.pickDetail : css.pickList}`}>
        <Queue
          filter={filter}
          query={q}
          params={queueParams(filter, me.data?.user_id, q)}
          selected={selected}
          onFilter={(f) => setQuery({ f })}
        />
        <Detail id={selected} />
      </div>
    </div>
  )
}

/** 顶部工具栏右侧：「立即检查 SLA 超时」与「工单设置」 */
function ToolbarEnd({ onOpenSettings }: { onOpenSettings: () => void }) {
  return (
    <>
      <EscalateButton />
      <Button size="sm" onClick={onOpenSettings}>
        工单设置
      </Button>
    </>
  )
}

/** 搜索框：输入 300ms 后才回写地址（后端 ILIKE，别每个字都打一次） */
function SearchBox({ value, onChange }: { value: string; onChange: (q: string) => void }) {
  const [text, setText] = useState(value)
  const [synced, setSynced] = useState(value)
  if (value !== synced) {
    // 地址被外部改了（切筛选、前进后退）：跟着地址走
    setSynced(value)
    setText(value)
  }
  useEffect(() => {
    if (text === value) return
    const timer = setTimeout(() => onChange(text), 300)
    return () => clearTimeout(timer)
  }, [text, value, onChange])
  return <SearchField value={text} onChange={(e) => setText(e.target.value)} placeholder="标题、用户或编号" aria-label="搜索工单" width={240} />
}

/** 「立即检查 SLA 超时」：租户级批量扫描，不是单张升级（契约）；后台每 5 分钟也会自动跑 */
function EscalateButton() {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateTickets()
  const intent = useIntentKey()
  const run = useMutation({
    mutationFn: () => api.post('v1/tickets/escalate', escalatedSchema, { idempotencyKey: intent.keyFor('escalate') }),
    onSuccess: (r) => {
      intent.reset()
      void invalidate()
      toast(r.escalated > 0 ? `已把 ${r.escalated} 张超时工单升级` : '没有新的超时工单')
    },
    onError: (e) => fail(e, { intent }),
  })
  return (
    <Button size="sm" busy={run.isPending} onClick={() => run.mutate()} title="立即扫描首次响应已超时的工单并升级（后台每 5 分钟也会自动扫一次）">
      立即检查 SLA 超时
    </Button>
  )
}
