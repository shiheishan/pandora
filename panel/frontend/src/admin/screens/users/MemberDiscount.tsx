import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Input, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { memberDiscountSchema, okSchema, useInvalidateMemberDiscount, useMemberDiscount } from './api'
import { memberDiscountLabel } from './model'
import css from './Users.module.css'

export function MemberDiscount({ userId }: { userId: string }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const discountQuery = useMemberDiscount(userId)
  const invalidate = useInvalidateMemberDiscount()

  const [editing, setEditing] = useState(false)
  const [percentInput, setPercentInput] = useState('')
  const [fieldError, setFieldError] = useState<string | null>(null)
  const [confirmClear, setConfirmClear] = useState(false)
  const [busy, setBusy] = useState(false)

  if (discountQuery.isPending) {
    return <span className={css.small}>加载中…</span>
  }

  const currentPercent = discountQuery.data?.percent ?? null
  const hasDiscount = currentPercent !== null && currentPercent > 0

  const formatText = () => {
    if (!hasDiscount || currentPercent === null) return '无'
    const label = memberDiscountLabel(currentPercent)
    return label.startsWith('减免') ? label : `减免 ${currentPercent}%（${label}）`
  }

  const handleSave = async () => {
    const n = Number(percentInput.trim())
    if (!Number.isInteger(n) || n < 1 || n > 100) {
      setFieldError('请填 1 到 100 的整数（10 表示打九折）')
      return
    }

    setBusy(true)
    try {
      await api.put(`v1/member-discounts/${encodeURIComponent(userId)}`, memberDiscountSchema, {
        body: { percent: n },
      })
      toast('会员折扣已保存')
      setEditing(false)
      setFieldError(null)
      void invalidate(userId)
    } catch (e) {
      fail(e, (fields) => {
        if (fields.percent) setFieldError(fields.percent)
      })
    } finally {
      setBusy(false)
    }
  }

  const handleClear = async () => {
    setBusy(true)
    try {
      await api.delete(`v1/member-discounts/${encodeURIComponent(userId)}`, okSchema)
      toast('会员折扣已清除')
      setConfirmClear(false)
      void invalidate(userId)
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  if (editing) {
    return (
      <div style={{ display: 'inline-flex', flexDirection: 'column', gap: '4px' }}>
        <div style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--space-8)' }}>
          <Input
            size="sm"
            mono
            style={{ width: '70px' }}
            placeholder="1–100"
            value={percentInput}
            onChange={(e) => {
              setPercentInput(e.target.value)
              setFieldError(null)
            }}
            error={fieldError}
          />
          <span className={css.small}>%</span>
          <Button size="xs" variant="primary" busy={busy} onClick={() => void handleSave()}>
            保存
          </Button>
          <Button
            size="xs"
            variant="ghost"
            disabled={busy}
            onClick={() => {
              setEditing(false)
              setFieldError(null)
            }}
          >
            取消
          </Button>
        </div>
      </div>
    )
  }

  return (
    <>
      <div style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--space-8)' }}>
        <span>{formatText()}</span>
        <Button
          size="xs"
          variant="ghost"
          onClick={() => {
            setPercentInput(currentPercent !== null ? String(currentPercent) : '')
            setFieldError(null)
            setEditing(true)
          }}
        >
          设置
        </Button>
        {hasDiscount && (
          <Button size="xs" variant="ghost" className={css.dangerText} onClick={() => setConfirmClear(true)}>
            清除
          </Button>
        )}
      </div>

      <ConfirmModal
        open={confirmClear}
        title="清除会员折扣"
        body="清除后这个用户以后下单按原价算；已下的单不变。"
        confirmLabel="清除会员折扣"
        tone="danger"
        onCancel={() => setConfirmClear(false)}
        onConfirm={handleClear}
      />
    </>
  )
}
