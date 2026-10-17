// 拼 className：去掉 false / null / undefined（与 ui/cx 相同，套件内自带一份，页面只从 kit import）
export function cx(...names: (string | false | null | undefined)[]): string {
  return names.filter(Boolean).join(' ')
}
