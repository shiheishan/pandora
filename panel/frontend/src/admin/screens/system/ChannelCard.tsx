import { Tag, type TagTone } from '../../../ui'
import type { Tone } from './logic'

const TONE_MAP: Record<Tone, TagTone> = {
  ok: 'ok',
  warn: 'warn',
  muted: 'neutral',
}

/** 弹窗头部的状态标签（用 ui Tag 渲染） */
export function StatusChip({ label, tone }: { label: string; tone: Tone }) {
  return <Tag tone={TONE_MAP[tone]}>{label}</Tag>
}

