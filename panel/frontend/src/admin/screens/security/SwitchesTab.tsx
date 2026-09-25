/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的开关字典与视图，依赖 ./queries，依赖 ./schemas 的 switchSaved，依赖 ./security.module.css
 * [OUTPUT]: 对外提供 SwitchesTab（安全与运维 · 降级开关标签）
 * [POS]: admin/screens/security 的降级开关（设计稿 t_switches）：黄底提示条 + 一行一个开关（中文名、code、说明、连带影响、当前原因、状态字、开关）。极性按后端：enabled = 功能可用，设计稿的「开启『暂停…』」= enabled=false，所以开关打开表示「降级中」（红）。
 *        切换（platform.settings.write + reauth，R58）先弹确认框：进入降级时原因必填（数据库 CHECK，后端回 409 不是 422，前端先拦），恢复时可选；成功后失效开关与审计。核心三项锁定、不画开关；D-A-3 已决（5.A.2、R102）：「订阅下发使用缓存」不做，三个没有代码读取的开关后端删行、这里不再列；R58 四个新开关缺行视为开启、auth.registration 缺行即暂停，缺行的列出来但不能切；notify.email 旁提示会连带让开了邮箱验证的注册走不通；admin.writes 关闭时顶部另给红色提示
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
