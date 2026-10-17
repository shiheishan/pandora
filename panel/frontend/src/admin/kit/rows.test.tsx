import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { BulkAction, BulkBar, Row, RowList, SaveBar } from './Rows'

const html = (node: ReactElement) => renderToStaticMarkup(node)
const noop = () => {}

describe('RowList', () => {
  it('renders role="list" with aria-label', () => {
    const out = html(
      <RowList label="佣金设置">
        <Row title="a@example.test" />
      </RowList>,
    )
    expect(out).toContain('role="list"')
    expect(out).toContain('aria-label="佣金设置"')
    expect(out).toContain('role="listitem"')
  })
})

describe('Row', () => {
  it('renders title / meta / extra / value / actions', () => {
    const out = html(
      <Row
        title="kol.zhou@example.test"
        meta="比例 15% · 范围跟全局"
        extra={<span>备注</span>}
        value="¥12.30"
        actions={<button type="button">修改</button>}
      />,
    )
    expect(out).toContain('kol.zhou@example.test')
    expect(out).toContain('比例 15% · 范围跟全局')
    expect(out).toContain('备注')
    expect(out).toContain('¥12.30')
    expect(out).toContain('修改')
  })

  it('is focusable only when onClick is given', () => {
    const clickable = html(<Row title="行" onClick={noop} />)
    expect(clickable).toContain('tabindex="0"')
    const plain = html(<Row title="行" />)
    expect(plain).not.toContain('tabindex')
  })
})

describe('BulkBar', () => {
  it('renders nothing when count is 0', () => {
    expect(
      html(
        <BulkBar count={0} onClear={noop}>
          <BulkAction onClick={noop}>启用</BulkAction>
        </BulkBar>,
      ),
    ).toBe('')
  })

  it('renders count, toolbar and clear button when count > 0', () => {
    const out = html(
      <BulkBar count={2} onClear={noop}>
        <BulkAction onClick={noop}>启用</BulkAction>
      </BulkBar>,
    )
    expect(out).toContain('已选 2')
    expect(out).toContain('role="toolbar"')
    expect(out).toContain('aria-label="批量操作"')
    expect(out).toContain('aria-label="取消选择"')
    expect(out).toContain('启用')
  })

  it('disables BulkAction when busy', () => {
    const out = html(<BulkAction onClick={noop} busy>停用</BulkAction>)
    expect(out).toContain('disabled=""')
    expect(out).toContain('aria-busy="true"')
    expect(out).toContain('停用')
  })
})

describe('SaveBar', () => {
  it('renders nothing when not dirty', () => {
    expect(html(<SaveBar dirty={false} onSave={noop} onReset={noop} />)).toBe('')
  })

  it('renders message and both buttons when dirty', () => {
    const out = html(
      <SaveBar dirty onSave={noop} onReset={noop} hint="保存时要再验证一次身份" />,
    )
    expect(out).toContain('有未保存的改动')
    expect(out).toContain('保存时要再验证一次身份')
    expect(out).toContain('还原')
    expect(out).toContain('保存')
  })

  it('marks the save button busy', () => {
    const out = html(<SaveBar dirty busy onSave={noop} onReset={noop} />)
    // Button busy 时会带 spinner；aria-busy 只出现在保存按钮上（还原按钮只 disabled）
    expect(out).toContain('aria-busy="true"')
    expect(out).toContain('disabled=""')
    // 主文案「有未保存的改动」里也有「保存」二字，按钮文字取最后一处
    expect(out.indexOf('aria-busy="true"')).toBeLessThan(out.lastIndexOf('保存'))
    expect(out.indexOf('aria-busy="true"')).toBeGreaterThan(out.indexOf('还原'))
  })
})
