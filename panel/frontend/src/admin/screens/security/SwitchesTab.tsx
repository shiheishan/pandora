import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Modal, QueryView, Switch, Tag, TextArea, useToast } from '../../../ui'
import { switchBody, switchViews, validateSwitchReason, type SwitchView } from './logic'
import { useFailure, useInvalidateSecurity, useSwitches } from './queries'
import { switchSaved } from './schemas'
import css from './security.module.css'

export function SwitchesTab() {
  const switches = useSwitches()
  const [target, setTarget] = useState<SwitchView | null>(null)

  return (
    <div className={css.stack}>
      <QueryView query={switches} rows={6} isEmpty={(rows) => rows.length === 0} empty={<Empty title="没有降级开关" description="这个租户还没有初始化开关；新开关缺行时按「未暂停」处理。" />}>
        {(rows) => {
          const views = switchViews(rows)
          return (
            <div className={css.switchList}>
              {views.map((v) => (
                <SwitchLine key={v.code} view={v} onToggle={() => setTarget(v)} />
              ))}
            </div>
          )
        }}
      </QueryView>
      {target && <ToggleModal view={target} onClose={() => setTarget(null)} />}
    </div>
  )
}

function SwitchLine({ view: v, onToggle }: { view: SwitchView; onToggle: () => void }) {
  const toggleable = v.kind === 'toggle'
  return (
    <div className={`${css.switchRow} ${v.degraded && v.kind === 'toggle' ? css.switchOn : ''}`}>
      <div className={css.switchMain}>
        <div className={css.switchTitle}>
          <span className={css.switchName}>{v.title}</span>
          <span className={`${css.mono} ${css.faint}`}>{v.code}</span>
          {v.kind === 'essential' && <Tag tone="neutral">核心 · 不可关闭</Tag>}
          {v.kind === 'missing' && <Tag tone="outline">缺行</Tag>}
        </div>
        <div className={css.switchDesc}>{v.desc}</div>
        {v.note && <div className={css.switchNote}>{v.note}</div>}
        {v.degraded && v.reason && <div className={css.switchReason}>原因：{v.reason}</div>}
      </div>
      <span className={`${css.switchStatus} ${css[v.tone]}`}>{v.status}</span>
      <Switch
        aria-label={v.title}
        // 核心项与缺行画成禁用的打开开关，不可点击（设计稿如此）；可切换项点了先弹确认
        checked={v.degraded || v.kind === 'essential'}
        disabled={!toggleable}
        className={v.kind === 'essential' ? css.switchLocked : undefined}
        onChange={onToggle}
      />
    </div>
  )
}

function ToggleModal({ view: v, onClose }: { view: SwitchView; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSecurity()
  const degrade = !v.degraded
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')

  const save = useMutation({
    mutationFn: (body: { enabled: boolean; reason: string }) => api.post(`v1/switches/${encodeURIComponent(v.code)}`, switchSaved, { body }),
    onSuccess: () => {
      toast(`「${v.title}」${degrade ? '已开启' : '已关闭'}`)
      void invalidate('switches')
      void invalidate('audit')
      onClose()
    },
    onError: (e) => fail(e),
  })

  const submit = () => {
    const err = validateSwitchReason(degrade, reason)
    setError(err)
    if (!err) save.mutate(switchBody(degrade, reason))
  }

  return (
    <Modal
      open
      onClose={() => !save.isPending && onClose()}
      dismissible={!save.isPending}
      eyebrow={v.code}
      title={`${degrade ? '开启' : '关闭'}「${v.title}」？`}
      actions={
        <>
          <Button size="dialog" disabled={save.isPending} onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant={degrade ? 'danger' : 'primary'} busy={save.isPending} onClick={submit}>
            {degrade ? '开启' : '关闭'}
          </Button>
        </>
      }
    >
      <div className={css.form}>
        <p className={css.lead}>{degrade ? v.desc : '恢复后这项功能立即可用。'}</p>
        {degrade && v.note && <p className={css.switchNote}>{v.note}</p>}
        <TextArea
          label={degrade ? '原因（必填）' : '原因（可选）'}
          rows={2}
          value={reason}
          error={error}
          placeholder={degrade ? '例如：支付渠道故障，暂停下单' : '例如：故障已恢复'}
          disabled={save.isPending}
          onChange={(e) => setReason(e.target.value)}
        />
        <p className={css.note}>需要二次认证；切换会写入审计，并广播给在线管理员。</p>
      </div>
    </Modal>
  )
}
