import { useState } from 'react'
import { Button, useToast } from '../../../ui'
import { Chips, flowCss } from '../common/Flow'
import type { Holdings } from '../common/holdings'
import { gb, isDeadSub } from '../common/purchase'
import type { Subscription } from '../common/subscriptions'
import { useTransferPacks } from './api'
import css from './Subs.module.css'

/**
 * 流量包转移提示条（设计稿 2.7、原型「加错了也不怕：停用后可以转」）：还没加到任何一份的流量包，
 * 或已经彻底停用的那份上没用完的流量包，提示用户加到一份在用的上。只有一份能加时直接给按钮。
 */
export function TransferBars({ h, unattached }: { h: Holdings; unattached: number }) {
  const sources: Array<{ from: string | null; bytes: number; text: string }> = []
  if (unattached > 0) sources.push({ from: null, bytes: unattached, text: `有 ${gb(unattached)} 流量包还没加到任何一份。` })
  for (const s of h.subs.data ?? []) {
    if (isDeadSub(s) && s.pack_remaining_bytes > 0) sources.push({ from: s.id, bytes: s.pack_remaining_bytes, text: `「${s.label ? `${s.label} · ${s.plan_name}` : s.plan_name}」已经停用，还有 ${gb(s.pack_remaining_bytes)} 流量包没用完。` })
  }
  if (!sources.length) return null
  return sources.map((src) => <TransferBar key={src.from ?? 'unattached'} h={h} src={src} />)
}

function TransferBar({ h, src }: { h: Holdings; src: { from: string | null; bytes: number; text: string } }) {
  const toast = useToast()
  const transfer = useTransferPacks()
  const [target, setTarget] = useState<string | null>(null)
  const targets = h.held
  const name = (s: Subscription) => (h.naming.multi ? `「${h.naming.sn(s)}」` : `你的${s.plan_name}`)

  function go(to: Subscription) {
    transfer.mutate(
      { from: src.from, to: to.id },
      {
        onSuccess: (r) => toast(r.moved_bytes > 0 ? `已把 ${gb(r.moved_bytes)} 加到${name(to)}` : '没有可以转的流量包了'),
        onError: (e) => toast(e.message || '没转成，请稍后再试', 'danger'),
      },
    )
  }

  if (!targets.length) {
    return (
      <div className={css.transfer} role="status">
        <span>{src.text}买个套餐后会自动加上，不会浪费。</span>
      </div>
    )
  }
  if (targets.length === 1) {
    const only = targets[0]!
    return (
      <div className={css.transfer} role="status">
        <span>{src.text}</span>
        <div className={css.transferRow}>
          <Button size="sm" variant="primary" busy={transfer.isPending} onClick={() => go(only)}>
            加到{name(only)}
          </Button>
        </div>
      </div>
    )
  }
  const chosen = targets.find((s) => s.id === target) ?? null
  return (
    <div className={css.transfer} role="status">
      <span>{src.text}加到哪一份？</span>
      <Chips label="加到哪一份" selected={target} onSelect={setTarget} items={targets.map((s) => ({ key: s.id, label: h.naming.sn(s) }))} />
      <div className={css.transferRow}>
        <Button size="sm" variant="primary" disabled={!chosen} busy={transfer.isPending} onClick={() => chosen && go(chosen)}>
          {chosen ? `加到「${h.naming.sn(chosen)}」` : '先选加到哪一份'}
        </Button>
        <span className={flowCss.faint}>只能加到在用的套餐上，加上后续费、换套餐都跟着这份走。</span>
      </div>
    </div>
  )
}
