import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Drawer, QueryView, Tag, useToast } from '../../../ui'
import { noContent } from '../../../core/api'
import { CertFormModal } from './CertFormModal'
import { CA_LABEL, errorCodeLabel, EXPIRY_VIEW, ORDER_REASON_LABEL, ORDER_STATE_VIEW, PROVIDER_META, remainingText, statusView, WILDCARD_WARNING } from './logic'
import { useCan, useCertificate, useFailure, useInvalidateCerts } from './queries'
import { certificateSchema, orderBriefSchema, type CertificateDetail } from './schemas'
import css from './certs.module.css'

export function CertDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const detail = useCertificate(id)
  const title = detail.data?.certificate.name ?? '证书'
  return (
    <Drawer open onClose={onClose} title={title} subtitle={detail.data?.certificate.identifiers.join(', ')} width={560}>
      <QueryView query={detail} rows={6} empty={null}>
        {(d) => <DetailBody detail={d} onDeleted={onClose} />}
      </QueryView>
    </Drawer>
  )
}

function fmt(at: string | null): string {
  return at ? formatDateTime(at) : '—'
}

function DetailBody({ detail, onDeleted }: { detail: CertificateDetail; onDeleted: () => void }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateCerts()
  const [editing, setEditing] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const c = detail.certificate
  const status = statusView(c)
  const expiry = EXPIRY_VIEW[c.expiry_level]
  const writable = can('node.certificate.write')
  const busy = c.active_order !== null

  const renew = useMutation({
    mutationFn: () => api.post(`v1/certificates/${c.id}/renew`, orderBriefSchema),
    onSuccess: (o) => {
      toast(o.state === 'running' ? '这张证书正在签发' : '已排队，通常一两分钟内签好')
      void invalidate()
    },
    onError: (e) => fail(e),
  })
  const pause = useMutation({
    mutationFn: (paused: boolean) => api.post(`v1/certificates/${c.id}/${paused ? 'pause' : 'resume'}`, certificateSchema),
    onSuccess: (cert, paused) => {
      toast(paused ? '已暂停自动签发' : cert.active_order ? '已恢复自动签发，正在排队' : '已恢复自动签发，到续期时间会自动续期；要马上换一张点「立即续期」')
      void invalidate()
    },
    onError: (e) => fail(e),
  })
  const remove = useMutation({
    mutationFn: () => api.delete(`v1/certificates/${c.id}`, noContent),
    onSuccess: () => {
      toast('证书已删除')
      void invalidate()
      onDeleted()
    },
    onError: (e) => fail(e),
  })

  return (
    <div className={css.stack}>
      {writable && (
        <div className={css.toolbar}>
          <Button
            size="sm"
            variant="primary"
            busy={renew.isPending}
            disabled={busy || c.status === 'paused' || c.status === 'blocked_credential'}
            title={busy ? '已有进行中的签发' : undefined}
            onClick={() => renew.mutate()}
          >
            立即续期
          </Button>
          {c.status === 'paused' ? (
            <Button size="sm" busy={pause.isPending} onClick={() => pause.mutate(false)}>
              恢复
            </Button>
          ) : (
            <Button size="sm" busy={pause.isPending} disabled={c.status === 'blocked_credential'} onClick={() => pause.mutate(true)}>
              暂停
            </Button>
          )}
          <Button size="sm" onClick={() => setEditing(true)}>
            编辑
          </Button>
          <span className={css.spacer} />
          <Button size="sm" variant="danger" disabled={c.active_order?.state === 'running'} onClick={() => setConfirmDelete(true)}>
            删除
          </Button>
        </div>
      )}
      {c.status === 'blocked_credential' && (
        <div className={css.alarm}>DNS 凭据「{c.dns_credential_name}」校验没通过，自动签发已停。去「DNS 凭据」改好并重新校验，通过后会自动恢复。</div>
      )}
      {c.status === 'paused' && c.paused_reason === 'failures' && (
        <div className={css.notice}>连续 {c.consecutive_failures} 次签发失败，已停止自动重试（避开 CA 的失败次数上限）。按下面的错误排查后点「恢复」。</div>
      )}
      {c.wildcard && <div className={css.notice}>{WILDCARD_WARNING}</div>}

      <section className={css.section}>
        <div className={css.sectionTitle}>概况</div>
        <dl className={css.kv}>
          <dt>状态</dt>
          <dd>
            <Tag tone={status.tone}>{status.label}</Tag>
          </dd>
          <dt>到期</dt>
          <dd className={css.inline}>
            <Tag tone={expiry.tone}>{expiry.label}</Tag>
            {c.not_after && (
              <span className={css.muted}>
                {fmt(c.not_after)}（{remainingText(c.not_after)}）
              </span>
            )}
          </dd>
          <dt>当前版本</dt>
          <dd>{c.current_version ? `v${c.current_version} · ${CA_LABEL[c.current_ca ?? ''] ?? c.current_ca}` : '还没签出'}</dd>
          <dt>下次续期</dt>
          <dd>
            {fmt(c.renew_after)}
            {c.ari_window_start && <span className={css.faint}>（CA 建议窗口 {fmt(c.ari_window_start)} – {fmt(c.ari_window_end)}）</span>}
          </dd>
          <dt>DNS 凭据</dt>
          <dd>
            {c.dns_credential_name} · {PROVIDER_META[c.dns_provider].label}
          </dd>
          <dt>密钥类型</dt>
          <dd className={css.mono}>{c.key_type}</dd>
          {c.last_error_code && (
            <>
              <dt>最近错误</dt>
              <dd>
                <div className={css.dangerText}>{errorCodeLabel(c.last_error_code)}</div>
                {c.last_error && <div className={css.faint}>{c.last_error}</div>}
                {c.next_attempt_at && c.status !== 'paused' && <div className={css.faint}>{fmt(c.next_attempt_at)} 之后自动重试</div>}
              </dd>
            </>
          )}
        </dl>
      </section>

      <section className={css.section}>
        <div className={css.sectionTitle}>版本（每次签发都换新私钥，私钥不在这里显示）</div>
        {detail.versions.length === 0 ? (
          <div className={css.faint}>还没有签出的版本</div>
        ) : (
          <div className={css.list}>
            {detail.versions.map((v) => (
              <div key={v.id} className={css.listRow}>
                <span className={css.inline}>
                  <span className={css.strong}>v{v.version}</span>
                  {v.current && <Tag tone="ok">当前</Tag>}
                  <span className={css.muted}>{CA_LABEL[v.ca] ?? v.ca}</span>
                  <span className={css.faint}>{v.is_renewal ? '续期' : '新签'}</span>
                </span>
                <span className={css.faint}>
                  {fmt(v.not_before)} – {fmt(v.not_after)}
                </span>
                <span className={`${css.faint} ${css.mono} ${css.ellipsis}`} title={`序列号 ${v.serial}\n证书链 SHA-256 ${v.chain_sha256}`}>
                  序列号 {v.serial}
                </span>
              </div>
            ))}
          </div>
        )}
      </section>

      <section className={css.section}>
        <div className={css.sectionTitle}>签发记录（最近 50 次）</div>
        {detail.orders.length === 0 ? (
          <div className={css.faint}>还没有签发记录</div>
        ) : (
          <div className={css.list}>
            {detail.orders.map((o) => {
              const v = ORDER_STATE_VIEW[o.state]
              return (
                <div key={o.id} className={css.listRow}>
                  <span className={css.inline}>
                    <Tag tone={v.tone}>{v.label}</Tag>
                    <span>{ORDER_REASON_LABEL[o.reason]}</span>
                    {o.ca && <span className={css.muted}>{CA_LABEL[o.ca] ?? o.ca}</span>}
                    {o.version && <span className={css.muted}>→ v{o.version}</span>}
                    {o.attempt > 1 && <span className={css.faint}>第 {o.attempt} 次尝试</span>}
                  </span>
                  <span className={css.faint}>
                    {fmt(o.created_at)}
                    {o.finished_at && ` → ${fmt(o.finished_at)}`}
                  </span>
                  {o.error_code && (
                    <span className={css.dangerText} title={o.error_detail ?? undefined}>
                      {errorCodeLabel(o.error_code)}
                      {o.error_detail && `：${o.error_detail}`}
                    </span>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </section>

      {editing && <CertFormModal cert={c} onClose={() => setEditing(false)} onSaved={() => setEditing(false)} />}
      <ConfirmModal
        open={confirmDelete}
        title="删除证书"
        body={`删除「${c.name}」和它的全部版本（含加密保存的私钥）与签发记录，不能恢复。CA 那边已签出的证书不会被吊销。`}
        confirmLabel="删除"
        tone="danger"
        onConfirm={() => remove.mutateAsync().then(() => setConfirmDelete(false), () => undefined)}
        onCancel={() => setConfirmDelete(false)}
      />
    </div>
  )
}
