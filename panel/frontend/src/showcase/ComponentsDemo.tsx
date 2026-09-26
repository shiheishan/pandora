/**
 * [INPUT]: 依赖 ../ui 的全部组件与 useToast，依赖 ../core/theme 的 useTheme / setTheme，依赖 ./Showcase.module.css
 * [OUTPUT]: 对外提供 ComponentsDemo
 * [POS]: showcase 的组件演示区，按设计规范 05「组件」的顺序排列；每个组件的全部变体都在这里，可以切主题、切入口、调窗口宽度逐一对照
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState, type Key, type ReactNode } from 'react'
import { setTheme, useTheme } from '../core/theme'
import {
  Button,
  Card,
  Checkbox,
  ConfirmModal,
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
  Tag,
  TextArea,
  useToast,
  type TableColumn,
} from '../ui'
import css from './Showcase.module.css'

function Row(props: { label: string; children: ReactNode }) {
  return (
    <div className={css.demoRow}>
      <div className={css.demoLabel}>{props.label}</div>
      <div className={css.demoBody}>{props.children}</div>
    </div>
  )
}

interface NodeRow {
  id: string
  name: string
  region: string
  status: 'ok' | 'warn' | 'danger'
}

const NODES: readonly NodeRow[] = [
  { id: 'hk', name: 'hk-01.edge', region: '香港 · 沪港', status: 'ok' },
  { id: 'jp', name: 'jp-03.edge', region: '东京 · IEPL', status: 'warn' },
  { id: 'sg', name: 'sg-02.edge', region: '新加坡', status: 'danger' },
]

const STATUS_TEXT = { ok: '在线', warn: '维护中', danger: '离线' } as const

export function ComponentsDemo() {
  const toast = useToast()
  const theme = useTheme()
  const [cycle, setCycle] = useState<'m' | 'q' | 'y'>('m')
  const [tab, setTab] = useState<'list' | 'groups' | 'bulk' | 'devices'>('list')
  const [notify, setNotify] = useState(true)
  const [selected, setSelected] = useState<Set<Key>>(() => new Set(['jp']))
  const [tableState, setTableState] = useState<'data' | 'loading' | 'empty'>('data')
  const [modal, setModal] = useState<null | 'plain' | 'confirm' | 'reauth'>(null)
  const [drawer, setDrawer] = useState(false)
  const [password, setPassword] = useState('')

  const columns: TableColumn<NodeRow>[] = [
    { key: 'name', header: '节点', render: (r) => r.name, mono: true },
    { key: 'region', header: '地区', render: (r) => <span className={css.muted}>{r.region}</span> },
    { key: 'status', header: '状态', width: '96px', render: (r) => <Tag tone={r.status}>{STATUS_TEXT[r.status]}</Tag> },
    {
      key: 'op',
      header: '操作',
      width: '80px',
      align: 'right',
      render: () => (
        <Button variant="link" size="xs">
          详情
        </Button>
      ),
    },
  ]

  return (
    <div className={css.demo}>
      <Row label="按钮">
        <Button variant="primary">立即续费</Button>
        <Button variant="outline">立即续费 · 描边</Button>
        <Button>复制订阅地址</Button>
        <Button variant="ghost">稍后支付</Button>
        <Button variant="danger">吊销身份</Button>
        <Button variant="primary" disabled>
          不可用
        </Button>
        <Button variant="primary" busy>
          发布中
        </Button>
        <Button variant="link">查看详情</Button>
      </Row>
      <Row label="按钮尺寸">
        <Button variant="primary">md · --h-control</Button>
        <Button size="sm">sm · --h-control-sm</Button>
        <Button size="xs">xs · --h-control-xs</Button>
        <Button size="dialog">dialog · 弹窗按钮</Button>
      </Row>

      <Row label="输入">
        <div className={css.demoGrid}>
          <Input label="邮箱" placeholder="you@example.com" type="email" />
          <Input label="验证码" mono defaultValue="4829" inputMode="numeric" />
          <Input label="优惠码" mono defaultValue="AUTUMN25" error="优惠码已过期，可在消息中心查看最新活动" />
          <Input label="节点名" size="sm" defaultValue="hk-01.edge" hint="sm 尺寸，后台表格内" />
          <Select
            label="支付方式"
            defaultValue="alipay"
            options={[
              { value: 'alipay', label: '支付宝' },
              { value: 'wxpay', label: '微信支付' },
              { value: 'balance', label: '余额', disabled: true },
            ]}
          />
          <TextArea label="问题描述" placeholder="写清楚现象、时间和使用的客户端" />
        </div>
      </Row>

      <Row label="选择">
        <Segmented
          label="计费周期"
          value={cycle}
          onChange={setCycle}
          options={[
            { value: 'm', label: '月付' },
            { value: 'q', label: '季付' },
            { value: 'y', label: '年付' },
          ]}
        />
        <Switch label="开关" checked={notify} onChange={(e) => setNotify(e.target.checked)} />
        <Switch label="关闭" defaultChecked={false} />
        <Switch label="不可用" disabled />
        <Checkbox label="勾选" defaultChecked />
        <Checkbox label="未勾选" />
        <Checkbox label="部分" indeterminate />
      </Row>

      <Row label="标签">
        <Tag tone="ok">在线</Tag>
        <Tag tone="warn">待审核</Tag>
        <Tag tone="danger">离线</Tag>
        <Tag tone="info">处理中</Tag>
        <Tag tone="neutral">已关闭</Tag>
        <Tag tone="brand">当前套餐</Tag>
        <Tag tone="brandSolid">推荐</Tag>
        <Tag tone="outline">VLESS Reality</Tag>
        <CountBadge count={3} />
        <CountBadge count={128} />
      </Row>

      <Row label="卡片">
        <div className={css.demoGrid}>
          <Card tint title="标准版" extra={<Tag tone="ok">38 天后到期</Tag>}>
            <div className={css.muted}>剩余流量</div>
            <div className={css.bigNumber}>
              138 <span className={css.muted}>GB</span>
            </div>
            <div className={css.progress}>
              <i style={{ width: '31%' }} />
            </div>
          </Card>
          <Card>
            <div className={css.muted}>账户余额</div>
            <div className={`num ${css.bigNumber}`}>¥128.00</div>
            <a href="#top">充值 →</a>
          </Card>
          <Card title="节点池" extra={<Button size="xs">编辑</Button>}>
            <div className={css.muted}>香港 · 4 个节点 · 绑定 标准版、专业版</div>
          </Card>
        </div>
      </Row>

      <Row label="表格">
        <div className={css.demoStack}>
          <Segmented
            size="sm"
            label="表格状态"
            value={tableState}
            onChange={setTableState}
            options={[
              { value: 'data', label: '有数据' },
              { value: 'loading', label: '加载中' },
              { value: 'empty', label: '空' },
            ]}
          />
          <Table
            label="节点列表"
            columns={columns}
            rows={tableState === 'empty' ? [] : NODES}
            rowKey={(r) => r.id}
            loading={tableState === 'loading'}
            empty={<Empty bare title="还没有节点" description="新建节点后，用一键安装命令把它接入面板。" action={<Button>新建节点</Button>} />}
            selection={{ selected, onChange: setSelected }}
            onRowClick={(r) => toast(`打开 ${r.name}`)}
          />
        </div>
      </Row>

      <Row label="标签页">
        <Tabs
          label="用户运营"
          value={tab}
          onChange={setTab}
          items={[
            { value: 'list', label: '用户列表' },
            { value: 'groups', label: '用户组' },
            { value: 'bulk', label: '批量运营' },
            { value: 'devices', label: '设备', badge: 12 },
          ]}
        />
      </Row>

      <Row label="弹窗 · 抽屉">
        <Button onClick={() => setModal('plain')}>普通弹窗</Button>
        <Button variant="danger" onClick={() => setModal('confirm')}>
          危险确认
        </Button>
        <Button onClick={() => setModal('reauth')}>重新验证身份</Button>
        <Button onClick={() => setDrawer(true)}>打开抽屉</Button>
        <span className={css.muted}>窄于 640 时弹窗变成底部抽屉</span>
      </Row>

      <Row label="Toast">
        <Button onClick={() => toast('已复制订阅地址')}>成功</Button>
        <Button onClick={() => toast('请填写标题', 'danger')}>错误</Button>
      </Row>

      <Row label="菜单">
        <Menu
          label="账户"
          triggerLabel="账户菜单"
          triggerClassName={css.avatarTrigger}
          trigger={<span className={css.avatar}>张</span>}
          align="start"
          header={
            <>
              <div className={css.menuName}>
                <b>张伟</b> <Tag tone="brand">专业版</Tag>
              </div>
              <div className={css.muted}>zhang.wei@example.com</div>
            </>
          }
          entries={[
            { key: 'wallet', label: '钱包', hint: '¥26.50', current: true, onSelect: () => toast('钱包') },
            { key: 'tickets', label: '工单支持', onSelect: () => toast('工单支持') },
            { key: 'sep', kind: 'separator' },
            { key: 'dark', kind: 'toggle', label: '深色模式', checked: theme === 'dark', onChange: (on) => setTheme(on ? 'dark' : 'light') },
            { key: 'logout', label: '退出登录', danger: true, onSelect: () => setModal('confirm') },
          ]}
        />
      </Row>

      <Row label="空状态 · 加载">
        <div className={css.demoGrid}>
          <Empty title="还没有工单" description="遇到连接或账单问题时，可以在这里联系客服。" action={<Button size="sm">新建工单</Button>} />
          <Card>
            <Skeleton width="30%" />
            <Skeleton width="55%" height={28} />
            <Skeleton height={8} />
            <Skeleton width="75%" />
          </Card>
        </div>
      </Row>

      <Modal
        open={modal === 'plain'}
        onClose={() => setModal(null)}
        title="导入到客户端"
        actions={
          <>
            <Button size="dialog" onClick={() => setModal(null)}>
              稍后
            </Button>
            <Button size="dialog" variant="primary" onClick={() => toast('已唤起客户端，弹窗仍开着：Toast 应在遮罩之上')}>
              打开 Clash
            </Button>
          </>
        }
      >
        订阅地址会交给客户端，导入后在客户端里选择节点即可。
      </Modal>
      <ConfirmModal
        open={modal === 'confirm'}
        title="退出登录？"
        body="当前设备需要重新登录，订阅地址不受影响。"
        confirmLabel="退出"
        tone="danger"
        onCancel={() => setModal(null)}
        onConfirm={() => new Promise<void>((resolve) => setTimeout(resolve, 800)).then(() => setModal(null))}
      />
      <Modal
        open={modal === 'reauth'}
        onClose={() => setModal(null)}
        eyebrow="敏感操作 · 需要重新认证"
        title="发布配置到全部节点"
        actions={
          <>
            <Button size="dialog" onClick={() => setModal(null)}>
              取消
            </Button>
            <Button size="dialog" variant="primary" disabled={!password} onClick={() => setModal(null)}>
              验证并发布
            </Button>
          </>
        }
      >
        <div className={css.demoStack}>
          <span>在线节点会在下次拉取时应用新配置。</span>
          <Input label="当前登录密码" type="password" data-autofocus value={password} onChange={(e) => setPassword(e.target.value)} />
        </div>
      </Modal>
      <Drawer
        open={drawer}
        onClose={() => setDrawer(false)}
        width={480}
        title={<span className="mono">PD-2609-3312</span>}
        subtitle={
          <>
            <Tag tone="ok">已支付</Tag>
            <span>2026-09-23 21:04</span>
          </>
        }
        actions={
          <>
            <Button size="dialog" onClick={() => setDrawer(false)}>
              关闭
            </Button>
            <Button size="dialog" variant="danger">
              取消订单
            </Button>
          </>
        }
      >
        <div className={css.demoStack}>
          <Card title="订单明细">
            <div className={css.muted}>专业版 · 年付 · ¥899.00</div>
          </Card>
          <Skeleton height={160} />
        </div>
      </Drawer>
    </div>
  )
}
