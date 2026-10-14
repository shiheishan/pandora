import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, QueryView, Select, useToast } from '../../../ui'
import {
  buildOverrideBody,
  overrideSummary,
  type OverrideForm,
} from './commissionLevels'
import local from './Commission.module.css'
import { formatDateTime, type FieldErrors } from './logic'
import css from './marketing.module.css'
import { useCommissionOverrides, useFailure, useInvalidateMarketing } from './queries'
import {
  okResponse,
  overrideSetResponse,
  type CommissionOverride,
} from './schemas'

const emptyForm: OverrideForm = {
  email: '',
  rate: '',
  scope: '',
}

export function CommissionOverrides() {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const overridesQuery = useCommissionOverrides()

  const [form, setForm] = useState<OverrideForm>(emptyForm)
  const [errors, setErrors] = useState<FieldErrors>({})
  const [deleting, setDeleting] = useState<CommissionOverride | null>(null)

  const saveMutation = useMutation({
    mutationFn: (body: object) =>
      api.post('v1/commission/overrides', overrideSetResponse, { body }),
    onSuccess: () => {
      toast('已保存单独设置')
      setForm(emptyForm)
      setErrors({})
      void invalidate()
    },
    onError: (error) => fail(error, setErrors),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildOverrideBody(form)
    if (!built.ok) return setErrors(built.errors)
    setErrors({})
    saveMutation.mutate(built.body)
  }

  const deleteMutation = useMutation({
    mutationFn: (userId: string) =>
      api.delete(`v1/commission/overrides/${userId}`, okResponse),
    onSuccess: () => {
      toast('已删除')
      setDeleting(null)
      void invalidate()
    },
    onError: (error) => {
      fail(error)
      setDeleting(null)
    },
  })

  const editOverride = (o: CommissionOverride) => {
    setForm({
      email: o.email,
      rate: o.rate_percent !== null ? String(o.rate_percent) : '',
      scope: o.scope ?? '',
    })
    setErrors({})
  }

  return (
    <section className={css.panel}>
      <div className={css.panelHead}>
        <h3 className={css.panelTitle}>单独佣金设置</h3>
        <span className={css.faint}>
          只影响这个人作为直接邀请人的以后订单；已计提的佣金不变。设置与删除都需要重新验证身份
        </span>
      </div>

      <form className={local.form} onSubmit={submit} noValidate>
        <div className={local.pair}>
          <Input
            label="用户邮箱"
            type="email"
            value={form.email}
            onChange={(e) => setForm({ ...form, email: e.target.value })}
            error={errors.email}
            placeholder="name@example.com"
          />
          <Input
            label="佣金比例（%，留空跟全局）"
            mono
            inputMode="numeric"
            value={form.rate}
            onChange={(e) => setForm({ ...form, rate: e.target.value })}
            error={errors.rate_percent}
            placeholder="0–50"
          />
        </div>
        <div>
          <Select
            label="计佣范围"
            value={form.scope}
            onChange={(e) =>
              setForm({
                ...form,
                scope: e.target.value as OverrideForm['scope'],
              })
            }
            emptyOption="跟全局"
            options={[
              { value: 'first_order', label: '仅首单' },
              { value: 'every_order', label: '每笔订单' },
            ]}
            error={errors.scope}
          />
        </div>
        <div className={css.toolbar}>
          <span className={css.faint}>同一邮箱再保存会覆盖原设置</span>
          <span className={css.spacer} />
          <Button type="submit" variant="primary" busy={saveMutation.isPending}>
            保存
          </Button>
        </div>
      </form>

      <QueryView
        query={overridesQuery}
        empty={
          <Empty
            bare
            title="还没有单独设置"
            description="不设置时按全局比例与计佣范围计佣。"
          />
        }
      >
        {(rows) =>
          rows.map((o) => (
            <div key={o.user_id} className={local.row}>
              <span className={local.who}>
                <span className={local.ellipsis}>{o.email}</span>
                <span className={`${css.faint} ${local.ellipsis}`}>
                  {overrideSummary(o)} · {formatDateTime(o.updated_at)}
                </span>
              </span>
              <span className={local.actions}>
                <Button size="xs" onClick={() => editOverride(o)}>
                  修改
                </Button>
                <Button
                  size="xs"
                  className={local.reject}
                  onClick={() => setDeleting(o)}
                  disabled={deleteMutation.isPending}
                >
                  删除
                </Button>
              </span>
            </div>
          ))
        }
      </QueryView>

      <ConfirmModal
        open={deleting !== null}
        title="删除单独设置？"
        body={`删除 ${deleting?.email ?? ''} 的单独设置？之后按全局比例与范围计佣。`}
        confirmLabel="删除"
        tone="danger"
        onConfirm={async () => {
          if (deleting) {
            // 失败已由 onError 提示，这里吞掉拒绝，免得成为未处理的 Promise
            await deleteMutation.mutateAsync(deleting.user_id).catch(() => undefined)
          }
        }}
        onCancel={() => setDeleting(null)}
      />
    </section>
  )
}
