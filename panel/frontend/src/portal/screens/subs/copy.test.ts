import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ToastProvider } from '../../../ui'
import { makeNaming } from '../common/purchase'
import { link, sub, GIB } from '../common/testing'
import { Copy, type CopyActions } from './Copy'

describe('Copy', () => {
  const noop = () => {}
  const actions: CopyActions = {
    onImport: noop,
    onRename: noop,
    onRotate: noop,
  }
  const far = {
    current_period_end: '2099-01-19T12:00:00Z',
    renew_until: '2099-02-19T12:00:00Z',
  }

  it('1. 一次性可叠加：有「再买同款，叠加上去」，stack=o1 出现在 html', () => {
    const s = sub({
      id: 'o1',
      plan_kind: 'one_time',
      stackable: true,
      renewable: false,
      ...far,
      renew_until: null,
      reset_price: null,
    })
    const html = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: s,
          naming: makeNaming([s], []),
          link: link('o1', 'a3f9'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(html).toContain('再买同款，叠加上去')
    expect(html).toMatch(/stack=o1/)
  })

  it('2. 开了付费重置（reset_price: 500, reset_limit: 2, resets_used: 0）：有「重置本期流量」与 reset=r1', () => {
    const s = sub({
      id: 'r1',
      reset_price: 500,
      reset_limit: 2,
      resets_used: 0,
      ...far,
    })
    const html = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: s,
          naming: makeNaming([s], []),
          link: link('r1', 'b7c2'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(html).toContain('重置本期流量')
    expect(html).toMatch(/reset=r1/)
  })

  it('3. 用了 85G/100G：有「流量快用完了」', () => {
    const s = sub({
      id: 's-low',
      usedGiB: 85,
      capGiB: 100,
      traffic_limit: 100 * 1024 ** 3,
      traffic_used: 85 * 1024 ** 3,
      ...far,
    })
    const html = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: s,
          naming: makeNaming([s], []),
          link: link('s-low', 'c8d3'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(html).toContain('流量快用完了')
  })

  it('4. 已到期（status: expired, current_period_end: 2020-01-01T00:00:00Z, renew_until: 2099-02-19T12:00:00Z, changeable: true）：有「续费，恢复使用」「换成别的，马上恢复」「另买一份」，没有「添加到 App」', () => {
    const s = sub({
      id: 's-exp',
      status: 'expired',
      current_period_end: '2020-01-01T00:00:00Z',
      renew_until: '2099-02-19T12:00:00Z',
      changeable: true,
      renewable: true,
    })
    const html = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: s,
          naming: makeNaming([s], []),
          link: link('s-exp', 'd9e4'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(html).toContain('续费，恢复使用')
    expect(html).toContain('换成别的，马上恢复')
    expect(html).toContain('另买一份')
    expect(html).not.toContain('添加到 App')
  })

  it('5. 多份且没起名：有「起个名字，好分清是谁的」，且 html 里「起个名字」只出现在这条提示里（没有起名行）', () => {
    const s1 = sub({ id: 's1', label: null, ...far })
    const s2 = sub({ id: 's2', label: '家里的', ...far })
    const naming = makeNaming([s1, s2], [])
    const html = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: s1,
          naming,
          link: link('s1', 'e1f2'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(html).toContain('起个名字，好分清是谁的')
    // 「起个名字」在 html 中只出现一次（提示条按钮里的文字），起名行不画
    const matches = html.match(/起个名字/g)
    expect(matches?.length).toBe(1)
  })

  it('6. hidden: true 时 html 的 article 带 hidden', () => {
    const s = sub({ id: 's-hid', ...far })
    const html = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: s,
          naming: makeNaming([s], []),
          link: link('s-hid', 'f3a4'),
          site: 'Pandora',
          actions,
          hidden: true,
        }),
      ),
    )
    expect(html).toMatch(/<article[^>]*\shidden=""/)
  })

  it('7. 试用标记：is_trial: true 时 html 含「试用」；缺省（false）时不含「试用」', () => {
    const trialSub = sub({ id: 's-trial', is_trial: true, ...far })
    const regularSub = sub({ id: 's-reg', ...far })
    const trialHtml = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: trialSub,
          naming: makeNaming([trialSub], []),
          link: link('s-trial', 't1a2'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(trialHtml).toContain('试用')

    const regHtml = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: regularSub,
          naming: makeNaming([regularSub], []),
          link: link('s-reg', 'r1a2'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(regHtml).not.toContain('试用')
  })

  it('8. 下期用量变更：next_usage 存在时 html 含「下期起每期 200G」；next_usage: null 时不含「下期起」', () => {
    const nextSub = sub({
      id: 's-next',
      next_usage: { traffic_bytes: 200 * GIB, device_limit: 3, throttle_kbps: null },
      ...far,
    })
    const htmlWithNext = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: nextSub,
          naming: makeNaming([nextSub], []),
          link: link('s-next', 'n1a2'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(htmlWithNext).toContain('下期起每期 200G')

    const defaultSub = sub({ id: 's-def', next_usage: null, ...far })
    const htmlDefault = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: defaultSub,
          naming: makeNaming([defaultSub], []),
          link: link('s-def', 'd1a2'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(htmlDefault).not.toContain('下期起')
  })

  it('9. 已到期（照用例 4 覆盖参数）即使给了 next_usage 也不含「下期起」', () => {
    const expiredSub = sub({
      id: 's-exp-next',
      status: 'expired',
      current_period_end: '2020-01-01T00:00:00Z',
      renew_until: '2099-02-19T12:00:00Z',
      changeable: true,
      renewable: true,
      next_usage: { traffic_bytes: 200 * GIB, device_limit: 3, throttle_kbps: null },
    })
    const html = renderToStaticMarkup(
      createElement(
        ToastProvider,
        null,
        createElement(Copy, {
          sub: expiredSub,
          naming: makeNaming([expiredSub], []),
          link: link('s-exp-next', 'e9f4'),
          site: 'Pandora',
          actions,
        }),
      ),
    )
    expect(html).not.toContain('下期起')
  })
})
