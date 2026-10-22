import { useState } from 'react'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { Button, Modal, useToast } from '../../../ui'
import x from './infra.module.css'
import type { InstallSecret } from './serverActions'

/** 安装 agent 弹窗：一条安装命令（含一次性令牌，脚本不再另要令牌）；关掉就再也看不到 */
export function InstallModal({ secret, onClose }: { secret: InstallSecret; onClose: () => void }) {
  const toast = useToast()
  const [copied, setCopied] = useState(false)

  const copy = () =>
    navigator.clipboard.writeText(secret.command).then(
      () => {
        setCopied(true)
        toast('安装命令已复制')
      },
      () => toast('浏览器不允许写剪贴板，请手动选中复制', 'danger'),
    )

  return (
    <Modal
      open={true}
      onClose={onClose}
      size={560}
      eyebrow="安装 agent · 接入面板"
      title={secret.title}
      footerStart={
        <span className={x.installFooterNotice}>
          命令 {formatDateTime(secret.expiresAt)} 前有效，关闭后不再显示；过期或丢失可重新签发。
        </span>
      }
      actions={
        <Button size="dialog" variant="primary" onClick={onClose}>
          完成
        </Button>
      }
    >
      <div className={x.installSteps}>
        <div className={x.installStep}>
          <span className={x.installNo}>1</span>
          <div className={x.installContent}>
            <span className={x.installStepTitle}>登录服务器，用 root 执行安装命令</span>
            <div className={x.installBox}>
              <code className={x.installCode}>{secret.command}</code>
              <button type="button" className={`${x.installCopyBtn} ${copied ? x.installCopied : ''}`} onClick={() => void copy()}>
                {copied ? '已复制 ✓' : '复制'}
              </button>
            </div>
            <span className={x.installHint}>
              脚本会下载 agent、注册为系统服务，并用命令里的一次性令牌接入面板（接入一次即作废）。接入后地区、IP、系统与规格自动回填。
            </span>
          </div>
        </div>
        <div className={x.installStep}>
          <span className={x.installNo}>2</span>
          <div className={x.installContent}>
            <span className={x.installStepTitle}>接入后：新建节点，或把没绑服务器的节点绑到这台</span>
            <span className={x.installHint}>
              机器上执行完命令、服务器卡片显示「已接入」后，在节点上点「上线」，这台服务器随之自动投入服务。
            </span>
            <div className={x.installAction}>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => {
                  onClose()
                  navigate('/nodes/nodes', { query: { new: secret.serverId } })
                }}
              >
                新建节点（预选这台）
              </Button>
            </div>
          </div>
        </div>
      </div>
    </Modal>
  )
}
