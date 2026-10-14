import { useState } from 'react'
import { Button, Modal, useToast } from '../../../ui'
import { appLink, CLIENTS, copyText, DEVICE_SAY, DEVICES, detectDevice, guessDeviceFromName, shareMessage, type ClientApp, type Device } from './clients'
import { Chips, ChoiceList, flowCss } from './Flow'
import css from './ImportSheet.module.css'
import type { Naming } from './purchase'
import { QrCode } from './qr'
import type { Subscription } from './subscriptions'

// ---------------------------------------------------------------------------
// 弹层「把这份添加到 App」（原型 sheetImport，我的套餐与结果页共用）：
// 先问添加到哪台设备——就是现在这台（按 UA 猜系统，推荐 App）/ 别的设备（按备注名猜系统，主按钮是
// 「复制链接和说明，发给对方」，扫码收进「对方就在你旁边？」）。顶部始终写添加的是哪一份、App 里显示成什么。
// ---------------------------------------------------------------------------
type Step = 'ask' | 'here' | 'other'

export interface ImportTarget {
  sub: Subscription
  url: string
}

export function ImportSheet({ target, naming, onClose }: { target: ImportTarget | null; naming: Naming; onClose: () => void }) {
  return (
    <Modal open={target !== null} onClose={onClose} title={target ? `把${naming.multi ? `「${naming.sn(target.sub)}」` : '这份'}添加到 App` : ''}>
      {target && <ImportBody key={target.sub.id} target={target} naming={naming} onClose={onClose} />}
    </Modal>
  )
}

function ImportBody({ target, naming, onClose }: { target: ImportTarget; naming: Naming; onClose: () => void }) {
  const [step, setStep] = useState<Step>('ask')
  const { sub, url } = target
  const tail = naming.tail(sub)
  return (
    <div className={css.body} data-step={step}>
      <div className={css.what}>
        添加的是：<b>{naming.multi ? naming.dn(sub) : sub.plan_name}</b>
        {tail && `（链接 ····${tail}）`}
        <br />
        App 里会显示为：<b>{sub.client_name}</b>
      </div>
      {step === 'ask' && <Ask sub={sub} onPick={setStep} />}
      {step === 'here' && <Here sub={sub} url={url} onOther={() => setStep('other')} onDone={onClose} />}
      {step === 'other' && <Other sub={sub} url={url} onHere={() => setStep('here')} />}
      <Button block onClick={onClose}>
        关闭
      </Button>
    </div>
  )
}

function Ask({ sub, onPick }: { sub: Subscription; onPick: (s: Step) => void }) {
  return (
    <>
      <p className={flowCss.q}>要添加到哪台设备？</p>
      <ChoiceList
        label="添加到哪台设备"
        selected={null}
        onSelect={(k) => onPick(k as Step)}
        items={[
          { key: 'here', label: '就是现在这台', desc: '在这台手机或电脑上打开 App 添加' },
          { key: 'other', label: '别的设备', desc: sub.label ? `例如${sub.label}：把链接发给对方，或当面扫码` : '把链接发给对方，或当面扫码' },
        ]}
      />
    </>
  )
}

function Here({ sub, url, onOther, onDone }: { sub: Subscription; url: string; onOther: () => void; onDone: () => void }) {
  const toast = useToast()
  const [guess] = useState<Device>(() => detectDevice(navigator.userAgent, navigator.maxTouchPoints))
  const [device, setDevice] = useState<Device | null>(null)
  const [picking, setPicking] = useState(false)
  const dev = device ?? guess
  const apps = CLIENTS[dev]

  async function copy(app?: ClientApp) {
    const ok = await copyText(url)
    toast(ok ? (app ? `已复制链接，打开 ${app.name}，在添加配置的地方粘贴` : '已复制链接，打开 App 粘贴就能添加') : '复制失败，请长按链接手动复制', ok ? 'ok' : 'danger')
  }

  const row = (app: ClientApp) => {
    const link = appLink(app.name, url, sub.client_name)
    return (
      <div key={app.name} className={css.app}>
        <span className={css.appIcon} aria-hidden="true">
          {app.name[0]}
        </span>
        <span className={css.appText}>
          <b>{app.name}</b>
          {app.note && <small>{app.note}</small>}
        </span>
        {link ? (
          <a
            className={flowCss.miniPrimary}
            href={link}
            onClick={() => {
              toast(`正在打开 ${app.name}，会添加「${sub.client_name}」`)
              onDone()
            }}
          >
            添加
          </a>
        ) : (
          <button type="button" className={flowCss.mini} onClick={() => void copy(app)}>
            复制链接
          </button>
        )}
      </div>
    )
  }

  return (
    <>
      {picking ? (
        <div className={css.devLine}>
          <span className={flowCss.faint}>这台是</span>
          <Chips
            label="这台是"
            selected={dev}
            onSelect={(k) => {
              setDevice(k as Device)
              setPicking(false)
            }}
            items={DEVICES.map((d) => ({ key: d, label: DEVICE_SAY[d] }))}
          />
        </div>
      ) : (
        <p className={css.devLine}>
          {device ? `这台是${DEVICE_SAY[dev]}` : `看起来这台是${DEVICE_SAY[dev]}`}。不对？
          <button type="button" className={flowCss.textButton} onClick={() => setPicking(true)}>
            换一个
          </button>
        </p>
      )}
      <div>{apps.top.map(row)}</div>
      <details className={css.more}>
        <summary>其他 App（{apps.rest.length} 个）</summary>
        {apps.rest.map(row)}
      </details>
      <Button block onClick={() => void copy()}>
        没有这些 App？复制链接，手动粘贴
      </Button>
      <button type="button" className={flowCss.altButton} onClick={onOther}>
        ← 其实是要添加到别的设备
      </button>
    </>
  )
}

function Other({ sub, url, onHere }: { sub: Subscription; url: string; onHere: () => void }) {
  const toast = useToast()
  const [device, setDevice] = useState<Device>(() => guessDeviceFromName(sub.label))
  const [copied, setCopied] = useState(false)
  const message = shareMessage(device, url)
  const app = CLIENTS[device].top[0]!.name

  async function copy(again: boolean) {
    const ok = await copyText(message)
    if (ok) setCopied(true)
    toast(ok ? (again ? '已复制这段话' : '已复制链接和说明') : '复制失败，请长按下面的文字手动复制', ok ? 'ok' : 'danger')
  }

  return (
    <>
      <p className={flowCss.q}>对方用的是</p>
      <Chips
        label="对方的设备"
        selected={device}
        onSelect={(k) => {
          setDevice(k as Device)
          setCopied(false)
        }}
        items={DEVICES.map((d) => ({ key: d, label: DEVICE_SAY[d] }))}
      />
      {copied ? (
        <>
          <div className={css.message} id="share-msg">
            <div className={flowCss.faint}>已复制，直接转发给对方这段话：</div>
            <p>{message}</p>
          </div>
          <Button block onClick={() => void copy(true)}>
            再复制一次这段话
          </Button>
        </>
      ) : (
        <Button variant="primary" block onClick={() => void copy(false)}>
          复制链接和说明，发给对方
        </Button>
      )}
      <p className={flowCss.warnBox}>这条链接谁拿到谁能用，只发给本人。发错了可以在卡片底部「换新链接」。</p>
      <details className={css.more}>
        <summary>对方就在你旁边？让他扫码</summary>
        <div className={css.qrSide}>
          <QrCode value={url} label={`「${sub.client_name}」的链接二维码`} className={css.qr} />
          <p className={flowCss.faint}>在对方的设备上用相机或 {app} 扫这个码，添加的就是这一份。</p>
        </div>
      </details>
      <button type="button" className={flowCss.altButton} onClick={onHere}>
        ← 改成在这台设备上添加
      </button>
    </>
  )
}
