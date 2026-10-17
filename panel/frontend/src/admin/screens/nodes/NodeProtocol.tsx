import { PencilEdit01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { useState } from 'react'
import { Callout, KvList, KvRow, SectionTitle } from '../../kit'
import { Button, ConfirmModal, Empty, Modal, QueryView, type QueryLike } from '../../../ui'
import css from './NodeProtocol.module.css'
import detailCss from './detail.module.css'
import { protocolSummary } from './logic'
import { NodeForm } from './NodeForm'
import { useProtocolSchemas } from './queries'
import type { NodeDetail, NodeRow } from './schemas'

/**
 * 「协议参数」页签：只读摘要 + 编辑弹窗。
 * node 是列表行（编辑按钮的可用性看它），detail 是单取查询（带编辑字段，协议表单要等它到了再画）。
 */
export function NodeProtocol({ node, detail }: { node: NodeRow; detail: QueryLike<NodeDetail | null> }) {
  const schemas = useProtocolSchemas()
  const [open, setOpen] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [confirmClose, setConfirmClose] = useState(false)
  const retired = node.serving_status === 'retired'

  // 有未保存的修改时先确认再关
  const requestClose = () => {
    if (dirty) setConfirmClose(true)
    else setOpen(false)
  }

  return (
    <div className={detailCss.stack}>
      <QueryView
        query={detail}
        rows={4}
        isEmpty={(d) => d === null}
        empty={<Empty bare title="不存在或已删除" description="这个节点可能刚被删除，回到列表刷新看看。" />}
      >
        {(d) =>
          d && (
            <>
              {d.warnings?.map((w) => (
                <Callout key={w}>配置提示：{w}</Callout>
              ))}
              <SectionTitle
                action={
                  <Button size="sm" icon={PencilEdit01Icon} disabled={retired} onClick={() => setOpen(true)}>
                    编辑
                  </Button>
                }
              >
                协议参数
              </SectionTitle>
              <KvList boxed>
                {protocolSummary(
                  schemas.data?.find((s) => s.node_type === d.node_type),
                  d,
                ).map((r) => (
                  <KvRow key={r.label} label={r.label}>
                    {r.muted ? <span className={css.muted}>{r.value}</span> : r.value}
                  </KvRow>
                ))}
              </KvList>
              <div className={detailCss.faint}>敏感字段（密码、私钥）读接口不回显，编辑时留空即保持原值。</div>
              <Modal
                open={open}
                onClose={requestClose}
                size={760}
                tall
                divided
                title={`编辑协议参数 · ${node.name}`}
                bodyClassName={detailCss.modalBody}
              >
                <div className={detailCss.scroll}>
                  <NodeForm
                    key={d.row_version}
                    node={d}
                    onSaved={() => {
                      setDirty(false)
                      setOpen(false)
                    }}
                    onDirtyChange={setDirty}
                  />
                </div>
              </Modal>
              <ConfirmModal
                open={confirmClose}
                title="放弃未保存的修改？"
                body="关掉后这次填的内容不会保存。"
                confirmLabel="放弃修改"
                tone="danger"
                onConfirm={() => {
                  setConfirmClose(false)
                  setDirty(false)
                  setOpen(false)
                }}
                onCancel={() => setConfirmClose(false)}
              />
            </>
          )
        }
      </QueryView>
    </div>
  )
}
