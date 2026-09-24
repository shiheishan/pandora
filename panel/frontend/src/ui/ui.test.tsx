/**
 * [INPUT]: 依赖 react-dom/server 的 renderToStaticMarkup，依赖 ./index 的全部组件
 * [OUTPUT]: 对外提供组件库的无障碍与结构测试
 * [POS]: ui 的单元测试：不引入 DOM 库，用服务端渲染核对角色、aria 属性与关键结构；交互（方向键、弹层开合、焦点）在 showcase 里用浏览器验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import {
  Button,
  Checkbox,
  CountBadge,
  Drawer,
  Empty,
  Input,
  Menu,
  Modal,
  Segmented,
  Select,
  Skeleton,
  Switch,
  Table,
  Tabs,
  TextArea,
} from './index'

const html = (node: ReactElement) => renderToStaticMarkup(node)
const noop = () => {}

describe('Button', () => {
  it('defaults to type="button" so it never submits a form by accident', () => {
    expect(html(<Button>导出</Button>)).toContain('type="button"')
  })

  it('busy disables the button and announces it', () => {
    const out = html(<Button busy>发布配置</Button>)
    expect(out).toContain('disabled=""')
    expect(out).toContain('aria-busy="true"')
  })
})

describe('form controls', () => {
  it('Input wires label, error message and aria attributes', () => {
    const out = html(<Input id="coupon" label="优惠码" error="优惠码已过期" />)
    expect(out).toContain('<label')
    expect(out).toContain('for="coupon"')
    expect(out).toContain('aria-invalid="true"')
    expect(out).toContain('aria-describedby="coupon-message"')
    expect(out).toMatch(/id="coupon-message"[^>]*role="alert"/)
  })

  it('Input without hint or error has no dangling aria-describedby', () => {
    expect(html(<Input label="邮箱" />)).not.toContain('aria-describedby')
  })

  it('TextArea shares the field wiring', () => {
    expect(html(<TextArea id="body" label="内容" hint="支持换行" />)).toContain('aria-describedby="body-message"')
  })

  it('Select renders a disabled placeholder and every option', () => {
    const out = html(
      <Select
        label="节点池"
        placeholder="请选择"
        defaultValue=""
        options={[
          { value: 'hk', label: '香港' },
          { value: 'jp', label: '东京', disabled: true },
        ]}
      />,
    )
    expect(out).toMatch(/<option value="" disabled=""[^>]*>请选择<\/option>/)
    expect(out).toContain('<option value="jp" disabled="">东京</option>')
  })

  it('Switch is a real checkbox with the switch role', () => {
    const out = html(<Switch aria-label="启用" defaultChecked />)
    expect(out).toContain('type="checkbox"')
    expect(out).toContain('role="switch"')
  })

  it('Checkbox renders a native checkbox', () => {
    expect(html(<Checkbox label="记住我" />)).toContain('type="checkbox"')
  })
})

describe('CountBadge', () => {
  it('hides at zero and caps large numbers', () => {
    expect(html(<CountBadge count={0} />)).toBe('')
    expect(html(<CountBadge count={120} />)).toContain('99+')
  })
})

describe('Tabs and Segmented', () => {
  const items = [
    { value: 'list', label: '用户列表' },
    { value: 'groups', label: '用户组' },
  ] as const

  it('Tabs uses roving tabindex and aria-selected', () => {
    const out = html(<Tabs label="用户" items={items} value="groups" onChange={noop} idPrefix="users" />)
    expect(out).toContain('role="tablist"')
    expect(out).toMatch(/aria-selected="false"[^>]*tabindex="-1"/)
    expect(out).toMatch(/aria-selected="true"[^>]*tabindex="0"/)
    expect(out).toContain('aria-controls="users-groups"')
  })

  it('Segmented is a radio group', () => {
    const out = html(<Segmented label="周期" options={items} value="list" onChange={noop} />)
    expect(out).toContain('role="radiogroup"')
    expect(out).toMatch(/role="radio" aria-checked="true" tabindex="0"/)
  })
})

describe('Table', () => {
  const columns = [
    { key: 'name', header: '节点', render: (r: { id: string; name: string }) => r.name, mono: true },
    { key: 'op', header: '操作', render: () => '详情', align: 'right' as const },
  ]
  const rows = [
    { id: 'a', name: 'hk-01.edge' },
    { id: 'b', name: 'jp-03.edge' },
  ]

  it('renders a labelled semantic table', () => {
    const out = html(<Table label="节点列表" columns={columns} rows={rows} rowKey={(r) => r.id} />)
    expect(out).toContain('<table')
    expect(out).toContain('aria-label="节点列表"')
    expect(out).toContain('<th scope="col"')
    expect(out).toContain('hk-01.edge')
  })

  it('checks the header box when every row is selected', () => {
    const out = html(
      <Table
        label="节点列表"
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        selection={{ selected: new Set(['a', 'b']), onChange: noop }}
      />,
    )
    expect(out).toMatch(/aria-label="全选"[^>]*checked=""/)
  })

  it('shows the empty state and the loading state', () => {
    expect(html(<Table label="x" columns={columns} rows={[]} rowKey={(r) => r.id} />)).toContain('暂无数据')
    expect(html(<Table label="x" columns={columns} rows={rows} rowKey={(r) => r.id} loading />)).toContain('aria-busy="true"')
  })
})

describe('overlays', () => {
  it('Modal keeps its content out of the DOM while closed', () => {
    const out = html(
      <Modal open={false} onClose={noop} title="退出登录？">
        当前设备需要重新登录
      </Modal>,
    )
    expect(out).toContain('<dialog')
    expect(out).not.toContain('退出登录？')
  })

  it('Modal labels the dialog with its title', () => {
    const out = html(
      <Modal open onClose={noop} title="退出登录？">
        当前设备需要重新登录
      </Modal>,
    )
    const labelledBy = /aria-labelledby="([^"]+)"/.exec(out)?.[1]
    expect(labelledBy).toBeTruthy()
    expect(out).toContain(`id="${labelledBy}"`)
  })

  it('Drawer has a labelled close button', () => {
    expect(html(<Drawer open onClose={noop} title="PD-2609-3312" />)).toContain('aria-label="关闭"')
  })

  it('Menu starts closed with a menu button', () => {
    const out = html(<Menu label="账户" trigger="张" triggerLabel="账户菜单" entries={[{ key: 'a', label: '钱包', onSelect: noop }]} />)
    expect(out).toContain('aria-haspopup="menu"')
    expect(out).toContain('aria-expanded="false"')
    expect(out).not.toContain('role="menu"')
  })
})

describe('feedback', () => {
  it('Skeleton is hidden from assistive technology', () => {
    expect(html(<Skeleton />)).toContain('aria-hidden="true"')
  })

  it('Empty renders title, description and one action', () => {
    const out = html(<Empty title="还没有工单" description="遇到问题可以在这里联系客服。" action={<Button>新建工单</Button>} />)
    expect(out).toContain('还没有工单')
    expect(out).toContain('新建工单')
  })
})
