import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, Modal, QueryView, Switch, Tag, TextArea, useToast } from '../../../ui'
import { switchBody, switchViews, validateSwitchReason, type SwitchView } from './logic'
import { useCan, useFailure, useInvalidateSecurity, useSwitches } from './queries'
import { switchSaved } from './schemas'
import css from './security.module.css'

export function SwitchesTab() {
  const switches = useSwitches()
  const can = useCan()
  const [target, setTarget] = useState<SwitchView | null>(null)
  const writable = can('platform.settings.write')

  return (
    <div className={css.stack}>
      <div className={css.notice}>降级开关用于事故期间快速止血。每次切换都需要二次认证、写入审计，并广播给在线管理员。</div>
      <QueryView query={switches} rows={6} isEmpty={(rows) => rows.length === 0} empty={<Empty title="没有降级开关" description="这个租户还没有初始化开关；新开关缺行时按「未暂停」处理。" />}>
        {(rows) => {
          const views = switchViews(rows)
          const readonly = views.find((v) => v.code === 'admin.writes' && v.degraded)
          return (
            <>
              {readonly && <div className={css.alarm}>管理端只读模式已开启：除降级开关、登录与二次认证、修改自己的密码外，后台所有写操作都会被拒绝。{readonly.reason && `原因：${readonly.reason}`}</div>}
              <div className={css.panel}>
                {views.map((v) => (
                  <SwitchLine key={v.code} view={v} writable={writable} onToggle={() => setTarget(v)} />
                ))}
              </div>
            </>
          )
        }}
      </QueryView>
      {target && <ToggleModal view={target} onClose={() => setTarget(null)} />}
    </div>
  )
}

function SwitchLine({ view: v, writable, onToggle }: { view: SwitchView; writable: boolean; onToggle: () => void }) {
  const toggleable = v.kind === 'toggle' && writable
  return (
    <div className={`${css.switchRow} ${v.degraded && v.kind === 'toggle' ? css.switchOn : ''}`}>
      <div className={css.switchMain}>
        <div className={css.switchTitle}>
          <span className={css.strong}>{v.title}</span>
          <span className={`${css.mono} ${css.faint}`}>{v.code}</span>
          {v.kind === 'essential' && <Tag tone="neutral">核心 · 不可关闭</Tag>}
          {v.kind === 'missing' && <Tag tone="outline">缺行</Tag>}
        </div>
        <div className={css.switchDesc}>{v.desc}</div>
        {v.note && <div className={css.switchNote}>{v.note}</div>}
        {v.degraded && v.reason && <div className={css.switchDesc}>原因：{v.reason}</div>}
      </div>
      <span className={`${css.switchStatus} ${css[v.tone]}`}>{v.status}</span>
      {v.kind === 'essential' ? (
        // 核心项没有「降级」这一态，不画开关，留等宽占位让状态字对齐
        <span className={css.switchSlot} aria-hidden="true" />
      ) : (
        <Switch
          aria-label={v.title}
          checked={v.degraded}
          disabled={!toggleable}
          title={v.kind === 'toggle' && !writable ? '需要平台设置写权限' : undefined}
          // 受控开关：点了先弹确认，确认成功后由重拉的数据改状态
          onChange={onToggle}
        />
      )}
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
