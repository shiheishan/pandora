import { useState } from 'react'
import { Button, Input, Modal, useToast } from '../../../ui'
import { flowCss } from '../common/Flow'
import { nameIdeas, profileName, type Naming } from '../common/purchase'
import type { Subscription } from '../common/subscriptions'
import { useRename } from './api'
import { renameError } from './model'
import css from './Sheets.module.css'

/**
 * 弹层「给这份起个名字」（原型 sheetRename）：候选名排除别的份已用的（不排除自己）；
 * 写明 App 里会显示成什么、在 App 里更新一次就能看到。空着保存就是清除名字。
 */
export function RenameSheet({ sub, all, naming, site, onClose }: { sub: Subscription | null; all: readonly Subscription[]; naming: Naming; site: string; onClose: () => void }) {
  return (
    <Modal open={sub !== null} onClose={onClose} title={sub?.label ? '改个名字' : '给这份起个名字'}>
      {sub && <RenameBody key={sub.id} sub={sub} all={all} naming={naming} site={site} onClose={onClose} />}
    </Modal>
  )
}

function RenameBody({ sub, all, naming, site, onClose }: { sub: Subscription; all: readonly Subscription[]; naming: Naming; site: string; onClose: () => void }) {
  const toast = useToast()
  const rename = useRename()
  const [value, setValue] = useState(sub.label ?? '')
  const [error, setError] = useState<string | null>(null)
  const tail = naming.tail(sub)

  function save() {
    setError(null)
    rename.mutate(
      { id: sub.id, label: value },
      {
        onSuccess: (r) => {
          toast(r.label ? `已改名，App 里更新一次后显示「${r.client_name}」` : '已清除名字')
          onClose()
        },
        onError: (e) => setError(renameError(e)),
      },
    )
  }

  return (
    <div className={css.body}>
      <p className={flowCss.faint}>
        {sub.plan_name}
        {tail && ` · 链接 ····${tail}`}
      </p>
      <Input
        aria-label="名字"
        placeholder="例如：妈妈的 iPad"
        maxLength={16}
        value={value}
        onChange={(e) => {
          setValue(e.target.value)
          setError(null)
        }}
        onKeyDown={(e) => e.key === 'Enter' && save()}
        error={error ?? undefined}
        data-autofocus
      />
      <div className={flowCss.chips}>
        {nameIdeas(all, sub)
          .slice(0, 4)
          .map((v) => (
            <button key={v} type="button" className={flowCss.chip} onClick={() => setValue(v)}>
              {v}
            </button>
          ))}
      </div>
      <p className={flowCss.faint}>
        App 里会显示成「{profileName(site, value, sub.plan_name)}」，在 App 里更新一次就能看到。随时能改。
      </p>
      <Button variant="primary" block busy={rename.isPending} onClick={save}>
        保存
      </Button>
      <Button block onClick={onClose}>
        取消
      </Button>
    </div>
  )
}
