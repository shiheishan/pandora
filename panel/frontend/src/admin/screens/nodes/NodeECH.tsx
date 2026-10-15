import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Input, QueryView, Tag, TextArea, useToast } from '../../../ui'
import { useFailure, useIntentKey, useInvalidateNodes } from './queries'
import { echSchema, okResponse, type NodeECH as NodeECHState, type NodeRow } from './schemas'
import { useNodeECH } from './xbQueries'
import css from './xb.module.css'

export function NodeECH({ node }: { node: NodeRow }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const echIntent = useIntentKey()

  const retired = node.serving_status === 'retired'
  const echQuery = useNodeECH(node.id)

  const [publicNameInput, setPublicNameInput] = useState('')
  const [publicNameError, setPublicNameError] = useState<string | null>(null)
  const [rotateConfirmOpen, setRotateConfirmOpen] = useState(false)
  const [disableConfirmOpen, setDisableConfirmOpen] = useState(false)

  // 开启 / 轮换 ECH (POST)
  const echPostMutation = useMutation({
    mutationFn: (body: { public_name: string }) =>
      api.post(`v1/nodes/${node.id}/ech`, echSchema, {
        body,
        idempotencyKey: echIntent.keyFor([node.id, body]),
      }),
    onSuccess: () => {
      echIntent.reset()
      setRotateConfirmOpen(false)
      // 发请求前已经开着 = 这次是轮换；否则是开启
      if (echQuery.data?.enabled) {
        toast('密钥已轮换，旧钥再下发 7 天')
      } else {
        toast('已开启 ECH，订阅与节点端几秒内生效')
        setPublicNameInput('')
      }
      void invalidate()
    },
    onError: (e) => {
      fail(e, {
        intent: echIntent,
        fields: (fields) => {
          if (fields.public_name) setPublicNameError(fields.public_name)
        },
      })
    },
  })

  // 停用 ECH (DELETE)
  const echDeleteMutation = useMutation({
    mutationFn: () => api.delete(`v1/nodes/${node.id}/ech`, okResponse),
    onSuccess: () => {
      setDisableConfirmOpen(false)
      toast('已停用 ECH')
      void invalidate()
    },
    onError: (e) => fail(e),
  })

  const copyPem = (pem: string) => {
    navigator.clipboard
      .writeText(pem)
      .then(() => toast('ECH 配置已复制到剪贴板'))
      .catch(() => toast('复制失败，请手动选中文本复制', 'danger'))
  }

  const currentPublicName = echQuery.data?.public_name ?? ''

  return (
    <Card>
      <div className={css.sectionTitle}>ECH（加密客户端问候）</div>
      <div className={css.cardDesc}>
        ECH 可以在客户端握手时加密真实的 SNI，外层只暴露公开域名，防止 SNI 被中间人监听或阻断。
      </div>

      <QueryView query={echQuery} rows={3} isEmpty={() => false} empty={null}>
        {(ech: NodeECHState) => {
          if (!ech.supported) {
            return (
              <div className={css.stack}>
                <div className={css.faint}>{ech.unsupported_reason || '当前协议不支持 ECH'}</div>
                <div className={css.faint}>ECH 只用于自带证书的 TLS 入站（不含 REALITY）。</div>
                {ech.enabled && (
                  <div className={css.warnTagRow}>
                    <Tag tone="warn">已有密钥但当前协议不下发</Tag>
                    <div className={css.btnRow}>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={retired}
                        onClick={() => setDisableConfirmOpen(true)}
                      >
                        停用 ECH
                      </Button>
                    </div>
                  </div>
                )}
              </div>
            )
          }

          if (!ech.enabled) {
            return (
              <div className={css.stack}>
                <div className={css.faint}>
                  开启后客户端握手里的真实 SNI 被加密，外层只露出公开名；节点只接受 TLS 1.3。订阅（Clash Meta / sing-box）自动带上配置。
                </div>
                <div>
                  <Input
                    label="公开域名（public_name）"
                    value={publicNameInput}
                    placeholder="留空用节点地址"
                    disabled={retired}
                    error={publicNameError ?? undefined}
                    onChange={(e) => {
                      setPublicNameInput(e.target.value)
                      if (publicNameError) setPublicNameError(null)
                    }}
                  />
                </div>
                <div className={css.actionRow}>
                  <Button
                    size="sm"
                    disabled={retired}
                    busy={echPostMutation.isPending}
                    onClick={() => echPostMutation.mutate({ public_name: publicNameInput.trim() })}
                  >
                    开启 ECH
                  </Button>
                </div>
              </div>
            )
          }

          return (
            <div className={css.stack}>
              <div className={css.echInfoRow}>
                <span className={css.echInfoLabel}>公开名</span>
                <span className={css.mono}>{ech.public_name}</span>
              </div>
              {ech.created_at && (
                <div className={css.echInfoRow}>
                  <span className={css.echInfoLabel}>创建时间</span>
                  <span>{formatDateTime(ech.created_at)}</span>
                </div>
              )}
              {ech.retired_keys > 0 && (
                <div>
                  <Tag tone="neutral">
                    另有 {ech.retired_keys} 把旧钥在过渡期内继续下发，7 天后自动清理
                  </Tag>
                </div>
              )}

              <div className={css.echPemBox}>
                <div className={css.echPemHead}>
                  <span className={css.echInfoLabel}>ECH 配置 (PEM)</span>
                  <Button size="xs" variant="ghost" onClick={() => copyPem(ech.config_pem)}>
                    复制配置
                  </Button>
                </div>
                <TextArea
                  aria-label="ECH 配置（PEM）"
                  mono
                  readOnly
                  rows={4}
                  className={css.pemArea}
                  value={ech.config_pem}
                />
              </div>

              <div className={css.actionRow}>
                <Button
                  size="sm"
                  disabled={retired}
                  onClick={() => setRotateConfirmOpen(true)}
                >
                  轮换密钥
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={retired}
                  onClick={() => setDisableConfirmOpen(true)}
                >
                  停用 ECH
                </Button>
              </div>
            </div>
          )
        }}
      </QueryView>

      {/* 轮换确认框：在 QueryView 外渲染 */}
      <ConfirmModal
        open={rotateConfirmOpen}
        title="轮换 ECH 密钥？"
        body="旧钥再下发 7 天，期间拿旧订阅的客户端仍能连上。需要重新验证身份。"
        confirmLabel="确认轮换"
        onConfirm={() =>
          echPostMutation
            .mutateAsync({ public_name: currentPublicName })
            .then(
              () => undefined,
              () => undefined,
            )
        }
        onCancel={() => setRotateConfirmOpen(false)}
      />

      {/* 停用确认框：在 QueryView 外渲染 */}
      <ConfirmModal
        open={disableConfirmOpen}
        tone="danger"
        title="停用 ECH？"
        body="立即删掉本节点全部 ECH 密钥（含过渡期内的旧钥）：拿着旧订阅的客户端会因 ECH 被拒而连不上，直到重新拉取订阅。需要重新验证身份。"
        confirmLabel="确认停用"
        onConfirm={() =>
          echDeleteMutation.mutateAsync().then(
            () => undefined,
            () => undefined,
          )
        }
        onCancel={() => setDisableConfirmOpen(false)}
      />
    </Card>
  )
}
