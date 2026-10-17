import { useState } from 'react'
import { Input, useToast } from '../../../ui'
import { Btn, Sheet } from '../../kit'
import { nameIdeas, profileName, type Naming } from '../common/purchase'
import type { Subscription } from '../common/subscriptions'
import { useRename } from './api'
import { renameError } from './model'
import css from './Sheets.module.css'

const FORM_ID = 'rename-form'

/**
 * 弹层「给这份起个名字」（原型 sheetRename）：候选名排除别的份已用的（不排除自己）；
 * 写明 App 里会显示成什么、在 App 里更新一次就能看到。空着保存就是清除名字。
 * Sheet 一直挂着（开关只改 open），关掉时原生 dialog 才会把焦点还给点开它的按钮；
 * 底部「保存」用 form 属性提交里面那张表单，表单按这一份重建（key）。
 */
export function RenameSheet({ sub, all, naming, site, onClose }: { sub: Subscription | null; all: readonly Subscription[]; naming: Naming; site: string; onClose: () => void }) {
  const rename = useRename()
  return (
    <Sheet
      open={sub !== null}
      onClose={onClose}
      title={sub?.label ? '改个名字' : '给这份起个名字'}
      dismissible={!rename.isPending}
      actions={
        <>
          <Btn tone="soft" onClick={onClose}>
            取消
          </Btn>
          <Btn type="submit" form={FORM_ID} busy={rename.isPending}>
            保存
          </Btn>
        </>
      }
    >
      {sub && <RenameForm key={sub.id} sub={sub} all={all} naming={naming} site={site} rename={rename} onClose={onClose} />}
    </Sheet>
  )
}

function RenameForm({
  sub,
  all,
  naming,
  site,
  rename,
  onClose,
}: {
  sub: Subscription
  all: readonly Subscription[]
  naming: Naming
  site: string
  rename: ReturnType<typeof useRename>
  onClose: () => void
}) {
  const toast = useToast()
  const [value, setValue] = useState(sub.label ?? '')
  const [error, setError] = useState<string | null>(null)
  const tail = naming.tail(sub)

  function save() {
    if (rename.isPending) return
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
    <form
      id={FORM_ID}
      className={css.body}
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <p className={css.faint}>
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
        error={error ?? undefined}
        data-autofocus
      />
      <div className={css.ideas}>
        {nameIdeas(all, sub)
          .slice(0, 4)
          .map((v) => (
            <button key={v} type="button" className={css.idea} onClick={() => setValue(v)}>
              {v}
            </button>
          ))}
      </div>
      <p className={css.faint}>
        App 里会显示成「{profileName(site, value, sub.plan_name)}」，在 App 里更新一次就能看到。随时能改。
      </p>
    </form>
  )
}
