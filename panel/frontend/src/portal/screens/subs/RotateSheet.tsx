import { useEffect } from 'react'
import { navigate } from '../../../core/router'
import { Btn, Sheet } from '../../kit'
import { Callout, flowCss } from '../common/Flow'
import type { Naming } from '../common/purchase'
import { useRotateLink, type Subscription } from '../common/subscriptions'
import { rotateWaitText } from './model'
import css from './Sheets.module.css'

/**
 * 弹层「换一个新链接？」（原型 sheetRotate）：只换这一份，别的份不受影响；按份限频，
 * 超限（429）时按钮置灰写剩下的冷却时间。换好后去完成页，把新链接与「添加到 App」放在最上面。
 */
export function RotateSheet({ sub, held, naming, onClose }: { sub: Subscription | null; held: readonly Subscription[]; naming: Naming; onClose: () => void }) {
  // Sheet 一直挂着（开关只改 open），关掉时原生 dialog 才会把焦点还给点开它的按钮；
  // 换了一份就清掉上一份的限频 / 失败状态
  const rotate = useRotateLink()
  const { reset } = rotate
  const id = sub?.id ?? null
  useEffect(() => reset(), [id, reset])
  const wait = rotate.isError ? rotateWaitText(rotate.error) : null
  const failed = rotate.isError && !wait ? (rotate.error instanceof Error ? rotate.error.message : '没换成，请稍后再试') : null

  function go() {
    if (!sub) return
    const oldTail = naming.tail(sub)
    rotate.mutate(sub.id, {
      onSuccess: () => {
        onClose()
        navigate(`/subs/${sub.id}/rotated`, { query: { old: oldTail } })
      },
    })
  }

  return (
    <Sheet
      open={sub !== null}
      onClose={onClose}
      title="换一个新链接？"
      dismissible={!rotate.isPending}
      actions={
        <>
          <Btn tone="soft" onClick={onClose}>
            不换了
          </Btn>
          <Btn tone="danger" busy={rotate.isPending} disabled={wait !== null} onClick={go} id="btn-rotate-go">
            {wait ?? '换新链接'}
          </Btn>
        </>
      }
    >
      {sub && (
        <div className={css.body}>
          <Callout>
            {naming.multi
              ? `只换「${naming.dn(sub)}」这一份：旧链接马上失效，用它的设备要重新添加新链接。${held
                  .filter((s) => s.id !== sub.id)
                  .map((x) => `「${naming.dn(x)}」`)
                  .join('、')}不受影响。`
              : '旧链接马上失效，添加过它的设备都要重新添加新链接。到期日和流量不变。'}
          </Callout>
          <p className={css.faint}>换了就不能换回旧链接。每份 10 分钟内只能换一次，每天最多 5 次。</p>
          {failed && <p className={flowCss.errorBox}>{failed}</p>}
        </div>
      )}
    </Sheet>
  )
}
