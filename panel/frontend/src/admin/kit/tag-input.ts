const SPLIT_RE = /[,，、;；\n\r\s]+/

/** 把一段输入或粘贴的文字拆成多个：按英文逗号、中文逗号、顿号、分号、换行、空白拆分，去掉空串 */
export function splitTagInput(raw: string): string[] {
  return raw.split(SPLIT_RE).filter((part) => part.length > 0)
}

const defaultNormalize = (value: string) => value.trim().toLowerCase()

/**
 * 把新来的若干个并进已有列表：每个先 normalize（默认 trim + 转小写），空串丢掉；
 * 已存在的（normalize 后相同）跳过；超出 max 的丢掉。
 * 返回新列表（原顺序在前、新的按输入顺序追加）和被丢掉的个数（重复 + 超上限 + validate 不过）。
 */
export function mergeTags(
  existing: readonly string[],
  incoming: readonly string[],
  options: { max: number; normalize?: (value: string) => string; validate?: (value: string) => boolean },
): { values: string[]; dropped: number } {
  const normalize = options.normalize ?? defaultNormalize
  const validate = options.validate ?? (() => true)
  const seen = new Set(existing.map((v) => normalize(v)))
  const values = [...existing]
  let dropped = 0

  for (const raw of incoming) {
    const value = normalize(raw)
    if (value.length === 0) {
      dropped += 1
      continue
    }
    if (!validate(value)) {
      dropped += 1
      continue
    }
    if (seen.has(value)) {
      dropped += 1
      continue
    }
    if (values.length >= options.max) {
      dropped += 1
      continue
    }
    seen.add(value)
    values.push(value)
  }

  return { values, dropped }
}
