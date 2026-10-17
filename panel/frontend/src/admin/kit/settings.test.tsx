import { Shield01Icon } from '@hugeicons-pro/core-stroke-rounded'
import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { nextRadioIndex, RadioCards, SettingRow } from './Settings'
import { TagInput } from './TagInput'
import { mergeTags, splitTagInput } from './tag-input'

const html = (node: ReactElement) => renderToStaticMarkup(node)
const noop = () => {}

const threeEnabled = [false, false, false] as const

describe('nextRadioIndex', () => {
  it('moves with arrow keys and wraps', () => {
    expect(nextRadioIndex(0, 'ArrowRight', threeEnabled)).toBe(1)
    expect(nextRadioIndex(2, 'ArrowRight', threeEnabled)).toBe(0)
    expect(nextRadioIndex(0, 'ArrowLeft', threeEnabled)).toBe(2)
  })

  it('jumps to ends with Home and End', () => {
    expect(nextRadioIndex(0, 'End', threeEnabled)).toBe(2)
    expect(nextRadioIndex(1, 'Home', threeEnabled)).toBe(0)
  })

  it('skips disabled options', () => {
    expect(nextRadioIndex(0, 'ArrowRight', [false, true, false])).toBe(2)
  })

  it('returns -1 for unrelated keys or all disabled', () => {
    expect(nextRadioIndex(0, 'a', threeEnabled)).toBe(-1)
    expect(nextRadioIndex(0, 'ArrowRight', [true, true, true])).toBe(-1)
  })
})

describe('splitTagInput', () => {
  it('splits on common separators', () => {
    expect(splitTagInput('qq.com, 163.com，gmail.com\noutlook.com  foo.com')).toEqual([
      'qq.com',
      '163.com',
      'gmail.com',
      'outlook.com',
      'foo.com',
    ])
    expect(splitTagInput(' , ')).toEqual([])
  })
})

describe('mergeTags', () => {
  it('dedupes, normalizes and enforces max', () => {
    expect(mergeTags(['qq.com'], ['QQ.com', ' 163.com ', 'x.com'], { max: 2 })).toEqual({
      values: ['qq.com', '163.com'],
      dropped: 2,
    })
  })

  it('counts validate failures as dropped', () => {
    expect(mergeTags(['qq.com'], ['abc'], { max: 10, validate: (v) => v.includes('.') })).toEqual({
      values: ['qq.com'],
      dropped: 1,
    })
  })
})

describe('SettingRow', () => {
  it('omits children when off and renders them when on', () => {
    const off = html(
      <SettingRow icon={Shield01Icon} title="标题" checked={false} onChange={noop}>
        <p>配置</p>
      </SettingRow>,
    )
    expect(off).not.toContain('配置')

    const on = html(
      <SettingRow icon={Shield01Icon} title="标题" checked onChange={noop}>
        <p>配置</p>
      </SettingRow>,
    )
    expect(on).toContain('配置')
    expect(on).toContain('role="switch"')
    expect(on).toContain('aria-labelledby')
  })
})

describe('RadioCards', () => {
  it('renders radiogroup with checked card focused', () => {
    const out = html(
      <RadioCards
        label="验证服务"
        value="b"
        onChange={noop}
        options={[
          { value: 'a', title: 'A' },
          { value: 'b', title: 'B' },
        ]}
      />,
    )
    expect(out).toContain('role="radiogroup"')
    expect(out).toContain('aria-checked="true"')
    expect(out).toContain('tabindex="0"')
    expect(out).toContain('tabindex="-1"')
  })
})

describe('TagInput', () => {
  it('renders chips, remove labels and count', () => {
    const out = html(
      <TagInput label="邮箱后缀" values={['qq.com', '163.com']} onChange={noop} />,
    )
    expect(out).toContain('qq.com')
    expect(out).toContain('163.com')
    expect(out).toContain('aria-label="移除 qq.com"')
    expect(out).toContain('已加 2 / 100 个')
  })
})
