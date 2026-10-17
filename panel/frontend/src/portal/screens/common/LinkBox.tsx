import type { ReactNode } from 'react'
import { useToast } from '../../../ui'
import { Btn, LinkChip } from '../../kit'
import { copyText } from './clients'
import css from './LinkBox.module.css'
import type { Naming } from './purchase'
import type { Subscription } from './subscriptions'

// ---------------------------------------------------------------------------
// 一份的链接框（原型 .link）：「链接 ····a3f9 [复制] [添加到 App]」+「App 里显示为：…」。
// 卡片与结果页的「下一步」共用；链接本身不整条显示（太长、也不该随手截图外传）。
// ---------------------------------------------------------------------------
export function LinkBox({
  sub,
  url,
  naming,
  label = '链接',
  onImport,
  children,
}: {
  sub: Subscription
  url: string | undefined
  naming: Naming
  label?: string
  onImport: () => void
  children?: ReactNode
}) {
  const toast = useToast()
  const tail = naming.tail(sub)

  async function copy() {
    if (!url) return
    const ok = await copyText(url)
    toast(ok ? `已复制「${naming.sn(sub)}」的链接（····${tail}）` : '复制失败，请稍后再试', ok ? 'ok' : 'danger')
  }

  return (
    <div className={css.box}>
      <div className={css.top}>
        <LinkChip
          className={css.chip}
          label={label}
          code={url ? `····${tail}` : '还没有链接'}
          action={
            <button type="button" disabled={!url} onClick={() => void copy()}>
              复制
            </button>
          }
        />
        <Btn tone="hot" grow disabled={!url} onClick={onImport}>
          添加到 App
        </Btn>
      </div>
      <p className={css.sub}>
        App 里显示为：<b>{sub.client_name}</b>
      </p>
      {children}
    </div>
  )
}
