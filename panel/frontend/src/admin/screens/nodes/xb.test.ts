import { describe, expect, it } from 'vitest'
import { dedupeTags, draftsFromRanges, limitToGiBText, parseLimitGiB, parseResetDay, rangesFromDrafts, scheduleError, splitTags, tagsError, trafficPercent, trafficUsageLabel } from './xb'

describe('节点标签', () => {
  it('按中英文逗号、顿号、换行拆分并去掉空项', () => {
    expect(splitTags(' 香港, IPLC，流媒体、\n 低延迟 ,, ')).toEqual(['香港', 'IPLC', '流媒体', '低延迟'])
  })
  it('去重忽略大小写，留第一次的写法', () => {
    expect(dedupeTags(['IPLC', 'iplc', '香港', ' 香港 '])).toEqual(['IPLC', '香港'])
  })
  it('校验与后端同一套文案', () => {
    expect(tagsError(['香港'])).toBeNull()
    expect(tagsError(['香港', ' '])).toBe('第 2 个标签不能为空')
    expect(tagsError(['一二三四五六七八九十一二三四五六七'])).toBe('第 1 个标签最多 16 个字')
    expect(tagsError(['a\u0007'])).toBe('第 1 个标签含有控制字符')
    expect(tagsError(Array.from({ length: 11 }, (_, i) => `t${i}`))).toBe('最多 10 个标签')
    // 去重后不超过 10 个就合法（后端先去重再数）
    expect(tagsError([...Array.from({ length: 10 }, (_, i) => `t${i}`), 'T0'])).toBeNull()
  })
})

describe('流量上限与重置日', () => {
  it('GiB 文本与字节互转', () => {
    expect(limitToGiBText(null)).toBe('')
    expect(limitToGiBText(100 * 1024 ** 3)).toBe('100')
    expect(parseLimitGiB('')).toBeNull()
    expect(parseLimitGiB('1.5')).toBe(1.5 * 1024 ** 3)
    expect(parseLimitGiB('0')).toBe('invalid')
    expect(parseLimitGiB('abc')).toBe('invalid')
  })
  it('重置日 1–31，空为不自动重置', () => {
    expect(parseResetDay('')).toBeNull()
    expect(parseResetDay('31')).toBe(31)
    expect(parseResetDay('0')).toBe('invalid')
    expect(parseResetDay('32')).toBe('invalid')
    expect(parseResetDay('1.5')).toBe('invalid')
  })
  it('用量显示与百分比', () => {
    const t = { limit_bytes: 4 * 1024 ** 3, reset_day: 1, used_bytes: 1024 ** 3, reset_at: '2026-10-01T00:00:00Z', exceeded_at: null }
    expect(trafficPercent(t)).toBe(25)
    expect(trafficUsageLabel(t)).toContain(' / ')
    expect(trafficPercent({ ...t, limit_bytes: null })).toBeNull()
    expect(trafficUsageLabel({ ...t, limit_bytes: null })).not.toContain('/')
  })
})

describe('时段倍率', () => {
  it('合法：不重叠、结束可写 24:00、倍率可为 0', () => {
    expect(scheduleError([])).toBeNull()
    expect(scheduleError([{ start: '22:00', end: '24:00', rate: 0.5 }, { start: '00:00', end: '06:00', rate: 0 }])).toBeNull()
  })
  it('格式、先后、倍率、重叠与后端同一套文案', () => {
    expect(scheduleError([{ start: '8:00', end: '12:00', rate: 1 }])).toBe('第 1 段：时间要写成 HH:MM（如 08:00），结束可写 24:00')
    expect(scheduleError([{ start: '00:00', end: '00:00', rate: 1 }])).toBe('第 1 段：时间要写成 HH:MM（如 08:00），结束可写 24:00')
    expect(scheduleError([{ start: '12:00', end: '08:00', rate: 1 }])).toContain('开始要早于结束')
    expect(scheduleError([{ start: '08:00', end: '12:00', rate: 1.234 }])).toBe('第 1 段：倍率要在 0 到 9999.99 之间，最多两位小数')
    expect(scheduleError([{ start: '10:00', end: '14:00', rate: 2 }, { start: '08:00', end: '12:00', rate: 1 }])).toBe('第 2 段与第 1 段时间重叠')
    expect(scheduleError(Array.from({ length: 13 }, (_, i) => ({ start: `${String(i).padStart(2, '0')}:00`, end: `${String(i).padStart(2, '0')}:30`, rate: 1 })))).toBe('最多 12 段')
  })
})

describe('时段倍率编辑稿', () => {
  it('来回转换，空倍率报错', () => {
    const ranges = [{ start: '08:00', end: '12:00', rate: 1.5 }]
    expect(rangesFromDrafts(draftsFromRanges(ranges))).toEqual(ranges)
    expect(scheduleError(rangesFromDrafts([{ start: ' 08:00 ', end: '12:00', rate: '' }]))).toBe('第 1 段：倍率要在 0 到 9999.99 之间，最多两位小数')
  })
})
