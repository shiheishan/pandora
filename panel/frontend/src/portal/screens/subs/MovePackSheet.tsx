import { useState } from 'react'
import { Button, Modal, useToast } from '../../../ui'
import { Callout, ChoiceList, flowCss } from '../common/Flow'
import { gb, leftOf, type Naming } from '../common/purchase'
import type { Subscription } from '../common/subscriptions'
import { useTransferPacks } from './api'
import css from './Sheets.module.css'

export interface MoveTarget {
  from: Subscription
  to: readonly Subscription[]
}

/**
 * 弹层「挪流量包」（用户 10-07）：升级前买的流量包迁移时挂到了到期最晚的那份，允许用户自己挪一次。
 * 只有一个可去的那份时不让选；多份时不预选。确认前写清「挪过去后这一份就不能再用这些流量，只能挪一次」。
 * 走转移接口 POST v1/me/traffic-packs/transfer（from 是这一份，to 是选中的那份）。
 */
export function MovePackSheet({ target, naming, onClose }: { target: MoveTarget | null; naming: Naming; onClose: () => void }) {
  return (
    <Modal open={target !== null} onClose={onClose} title="挪流量包">
      {target && <MoveBody key={target.from.id} target={target} naming={naming} onClose={onClose} />}
    </Modal>
  )
}

function MoveBody({ target, naming, onClose }: { target: MoveTarget; naming: Naming; onClose: () => void }) {
  const toast = useToast()
  const transfer = useTransferPacks()
  const only = target.to.length === 1 ? target.to[0]! : null
  const [picked, setPicked] = useState<string | null>(only?.id ?? null)
  const to = target.to.find((s) => s.id === picked) ?? null
  const bytes = target.from.legacy_movable_pack_bytes
  const size = gb(bytes)

  function go() {
    if (!to) return
    transfer.mutate(
      { from: target.from.id, to: to.id },
      {
        onSuccess: (r) => {
          toast(r.moved_bytes > 0 ? `已把 ${gb(r.moved_bytes)} 挪到「${naming.dn(to)}」` : '没有可以挪的流量包了')
          onClose()
        },
        onError: (e) => toast(e.message || '没挪成，请稍后再试', 'danger'),
      },
    )
  }

  return (
    <div className={css.body}>
      {!only && (
        <>
          <p className={flowCss.q}>挪到哪一份？</p>
          <ChoiceList
            label="挪到哪一份"
            selected={picked}
            onSelect={setPicked}
            items={target.to.map((s) => {
              const left = leftOf(s).left
              return { key: s.id, label: naming.dn(s), desc: left === null ? '不限流量' : `剩 ${gb(left)} → ${gb(left + bytes)}` }
            })}
          />
        </>
      )}
      <Callout>
        {to
          ? `${size} 流量包从「${naming.dn(target.from)}」挪到「${naming.dn(to)}」。挪过去后这一份就不能再用这些流量，只能挪一次。`
          : `${size} 流量包会从「${naming.dn(target.from)}」挪走。挪过去后这一份就不能再用这些流量，只能挪一次。`}
        {to && <AfterMove from={target.from} to={to} bytes={bytes} naming={naming} />}
      </Callout>
      <Button variant="primary" block busy={transfer.isPending} disabled={!to} onClick={go} id="btn-move-go">
        {to ? `挪到「${naming.sn(to)}」` : '先选挪到哪一份'}
      </Button>
      <Button block onClick={onClose}>
        不挪了
      </Button>
    </div>
  )
}

/** 挪完两边各剩多少（首次点击测试：只有文字没有数字，挪完才吓一跳）；流量包不过期，跟着那一份走 */
function AfterMove({ from, to, bytes, naming }: { from: Subscription; to: Subscription; bytes: number; naming: Naming }) {
  const a = leftOf(from).left
  const b = leftOf(to).left
  const parts = [a !== null ? `「${naming.sn(from)}」剩 ${gb(Math.max(0, a - bytes))}` : '', b !== null ? `「${naming.sn(to)}」剩 ${gb(b + bytes)}` : ''].filter(Boolean)
  return (
    <p className={css.after}>
      {parts.length ? `挪完：${parts.join('，')}。` : ''}流量包不过期，以后跟着「{naming.sn(to)}」走，它续费、换套餐都还在。
    </p>
  )
}
