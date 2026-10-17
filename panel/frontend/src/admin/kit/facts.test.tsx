import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ToastProvider } from '../../ui'
import { DetailPage } from './DetailPage'
import { Avatar, KvList, SectionTitle, SideFact, StatusDot } from './Facts'
import cssFacts from './Facts.module.css'
import { CopyButton, DangerRow } from './Notes'

const html = (node: ReactElement) => renderToStaticMarkup(node)
const noop = () => {}

describe('DetailPage', () => {
  it('renders back link with href and label', () => {
    const out = html(
      <DetailPage back={{ href: '#/users/list?q=a', label: '用户列表' }} title="用户">
        <p>内容</p>
      </DetailPage>,
    )
    expect(out).toContain('href="#/users/list?q=a"')
    expect(out).toContain('用户列表')
  })

  it('renders aside when side is given', () => {
    const out = html(
      <DetailPage back={{ href: '#/', label: '返回' }} title="标题" side={<span>侧栏</span>}>
        <p>主栏</p>
      </DetailPage>,
    )
    expect(out).toContain('<aside')
    expect(out).toContain('侧栏')
  })

  it('omits aside without side', () => {
    const out = html(
      <DetailPage back={{ href: '#/', label: '返回' }} title="标题">
        <p>主栏</p>
      </DetailPage>,
    )
    expect(out).not.toContain('<aside')
  })

  it('renders tablist when tabs are provided', () => {
    const out = html(
      <DetailPage
        back={{ href: '#/', label: '返回' }}
        title="标题"
        tabs={{
          label: '详情页签',
          items: [{ value: 'a', label: '资料' }],
          value: 'a',
          onChange: noop,
        }}
      >
        <p>内容</p>
      </DetailPage>,
    )
    expect(out).toContain('role="tablist"')
  })
})

describe('SideFact', () => {
  it('puts label in dt and value in dd', () => {
    const out = html(<SideFact label="渠道">支付宝</SideFact>)
    expect(out).toContain('<dt')
    expect(out).toContain('渠道')
    expect(out).toContain('<dd')
    expect(out).toContain('支付宝')
  })

  it('renders an extra dd for action', () => {
    const out = html(
      <SideFact label="用户" action={<button type="button">编辑</button>}>
        zhang@qq.com
      </SideFact>,
    )
    expect(out.match(/<dd/g)?.length).toBe(2)
    expect(out).toContain('编辑')
  })
})

describe('KvList', () => {
  it('sets --kv-label from labelWidth', () => {
    const out = html(
      <KvList labelWidth={96}>
        <span>行</span>
      </KvList>,
    )
    expect(out).toContain('--kv-label:96px')
  })
})

describe('StatusDot', () => {
  it('applies tone class and renders text', () => {
    const out = html(<StatusDot tone="ok">正常</StatusDot>)
    expect(out).toContain(cssFacts.statusOk)
    expect(out).toContain('正常')
  })
})

describe('SectionTitle', () => {
  it('renders as h2 with hint and action', () => {
    const out = html(
      <SectionTitle as="h2" hint="说明" action={<a href="#">全部</a>}>
        最近订单
      </SectionTitle>,
    )
    expect(out).toContain('<h2')
    expect(out).toContain('最近订单')
    expect(out).toContain('说明')
    expect(out).toContain('全部')
  })
})

describe('DangerRow', () => {
  it('renders action label and disabled on button', () => {
    const out = html(
      <DangerRow title="吊销" actionLabel="吊销身份" onAction={noop} disabled />,
    )
    expect(out).toContain('吊销身份')
    expect(out).toContain('disabled=""')
  })
})

describe('CopyButton', () => {
  it('renders aria-label inside ToastProvider', () => {
    const out = html(
      <ToastProvider>
        <CopyButton value="1a2b" label="复制用户 ID" />
      </ToastProvider>,
    )
    expect(out).toContain('aria-label="复制用户 ID"')
  })
})

describe('Avatar', () => {
  it('shows only the first character', () => {
    const out = html(<Avatar text="林舟" />)
    expect(out).toContain('林')
    expect(out).not.toContain('舟')
  })
})
