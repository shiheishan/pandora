import { describe, expect, it } from 'vitest'
import { moveActive, placeList, typeahead, type Opt } from './select-logic'

describe('select-logic', () => {
  describe('moveActive', () => {
    const options: Opt[] = [
      { value: '0', label: 'Item 0' },
      { value: '1', label: 'Item 1', disabled: true },
      { value: '2', label: 'Item 2' },
      { value: '3', label: 'Item 3', disabled: true },
      { value: '4', label: 'Item 4' },
      { value: '5', label: 'Item 5' },
    ]

    it('跳过禁用项', () => {
      // from 0 -> 下一个可用项应跳过 1，到达 2
      expect(moveActive(options, 0, 'ArrowDown')).toBe(2)
      // from 2 -> 上一个可用项应跳过 1，到达 0
      expect(moveActive(options, 2, 'ArrowUp')).toBe(0)
      // from 4 -> 上一个可用项应跳过 3，到达 2
      expect(moveActive(options, 4, 'ArrowUp')).toBe(2)
    })

    it('到头不循环', () => {
      // 最底部 (5) 按 ArrowDown 仍为 5
      expect(moveActive(options, 5, 'ArrowDown')).toBe(5)
      // 最顶部 (0) 按 ArrowUp 仍为 0
      expect(moveActive(options, 0, 'ArrowUp')).toBe(0)
    })

    it('全禁用回 -1', () => {
      const allDisabled: Opt[] = [
        { value: 'a', label: 'A', disabled: true },
        { value: 'b', label: 'B', disabled: true },
      ]
      expect(moveActive(allDisabled, 0, 'ArrowDown')).toBe(-1)
      expect(moveActive(allDisabled, 0, 'ArrowUp')).toBe(-1)
      expect(moveActive(allDisabled, -1, 'Home')).toBe(-1)
      expect(moveActive(allDisabled, -1, 'End')).toBe(-1)
      expect(moveActive([], 0, 'ArrowDown')).toBe(-1)
    })

    it('Home 和 End 定位到第一项和最后一项可用项', () => {
      const opts: Opt[] = [
        { value: '0', label: 'Zero', disabled: true },
        { value: '1', label: 'One' },
        { value: '2', label: 'Two' },
        { value: '3', label: 'Three', disabled: true },
      ]
      expect(moveActive(opts, 2, 'Home')).toBe(1)
      expect(moveActive(opts, 1, 'End')).toBe(2)
    })

    it('PageDown 和 PageUp 跳至多 10 项并在边界停止', () => {
      const manyOpts: Opt[] = Array.from({ length: 30 }, (_, i) => ({
        value: String(i),
        label: `Item ${i}`,
        disabled: i % 5 === 0, // 0, 5, 10, 15, 20, 25 禁用
      }))
      // 启用项有：1..4, 6..9, 11..14, 16..19, 21..24, 26..29 (共 24 个)
      // 从 1 开始 PageDown 跳 10 个可用项
      const p1 = moveActive(manyOpts, 1, 'PageDown')
      expect(p1).toBeGreaterThan(1)
      expect(manyOpts[p1]?.disabled).toBeFalsy()

      // 从最后一项 PageDown 停在最后一项可用项
      expect(moveActive(manyOpts, 29, 'PageDown')).toBe(29)

      // 从 29 开始 PageUp
      const p2 = moveActive(manyOpts, 29, 'PageUp')
      expect(p2).toBeLessThan(29)
      expect(manyOpts[p2]?.disabled).toBeFalsy()

      // 从第一可用项 PageUp 停在第一可用项
      expect(moveActive(manyOpts, 1, 'PageUp')).toBe(1)
    })
  })

  describe('typeahead', () => {
    const options: Opt[] = [
      { value: 'apple', label: 'Apple' },
      { value: 'banana', label: 'Banana' },
      { value: 'berry', label: 'Berry' },
      { value: 'blueberry', label: 'Blueberry', disabled: true },
      { value: 'cherry', label: 'Cherry' },
    ]

    it('前缀匹配不分大小写', () => {
      expect(typeahead(options, 0, 'ba')).toBe(1)
      expect(typeahead(options, 0, 'CHE')).toBe(4)
    })

    it('跨过禁用项', () => {
      // 从 2 (Berry) 往下找以 "b" 开头，3 (Blueberry) 是 disabled，应循环回到 1 (Banana)
      expect(typeahead(options, 2, 'b')).toBe(1)
    })

    it('同字符重复轮转', () => {
      // 连续按 'b'，在 Banana(1) 和 Berry(2) 之间轮转
      expect(typeahead(options, 0, 'b')).toBe(1)
      expect(typeahead(options, 1, 'bb')).toBe(2)
      expect(typeahead(options, 2, 'bbb')).toBe(1)
    })

    it('找不到原地不动', () => {
      expect(typeahead(options, 1, 'xyz')).toBe(1)
    })
  })

  describe('placeList', () => {
    it('下方够放时 up=false、top = bottom + 4', () => {
      // 视口高度 800，按钮 top 100, bottom 132, left 50, width 200, count 5
      // h = 5 * 33 + 10 = 175
      // below = 800 - 132 = 668 >= 175 + 8
      const pos = placeList({ top: 100, bottom: 132, left: 50, width: 200 }, 800, 5)
      expect(pos.up).toBe(false)
      expect(pos.top).toBe(136)
      expect(pos.left).toBe(50)
      expect(pos.width).toBe(200)
      expect(pos.maxHeight).toBe(280)
    })

    it('下方不够且上方更大时 up=true', () => {
      // 视口高度 500，按钮 top 400, bottom 432, left 50, width 120, count 8
      // h = min(280, 8 * 33 + 10) = 274
      // below = 500 - 432 = 68 < 274 + 8, 且 rect.top (400) > below (68)
      // top = max(8, 400 - 274 - 4) = 122
      const pos = placeList({ top: 400, bottom: 432, left: 50, width: 120 }, 500, 8)
      expect(pos.up).toBe(true)
      expect(pos.top).toBe(122)
      expect(pos.width).toBe(140) // 最小宽度 140
    })

    it('宽度至少 140', () => {
      const pos = placeList({ top: 50, bottom: 82, left: 20, width: 90 }, 600, 3)
      expect(pos.width).toBe(140)
    })
  })
})
