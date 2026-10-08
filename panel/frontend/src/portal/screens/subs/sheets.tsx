import { useState } from 'react'
import type { Holdings } from '../common/holdings'
import { ImportSheet, type ImportTarget } from '../common/ImportSheet'
import type { Subscription } from '../common/subscriptions'
import { RenameSheet } from './RenameSheet'
import { RotateSheet } from './RotateSheet'

/** 卡片上的三个弹层：添加到 App、改名、换新链接 */
export function useSheets(h: Holdings) {
  const [importing, setImporting] = useState<ImportTarget | null>(null)
  const [renaming, setRenaming] = useState<Subscription | null>(null)
  const [rotating, setRotating] = useState<Subscription | null>(null)
  const actions = {
    onImport: (sub: Subscription) => {
      const url = h.urlOf(sub.id)
      if (url) setImporting({ sub, url })
    },
    onRename: setRenaming,
    onRotate: setRotating,
  }
  const sheets = (
    <>
      <ImportSheet target={importing} naming={h.naming} onClose={() => setImporting(null)} />
      <RenameSheet sub={renaming} all={h.subs.data ?? []} naming={h.naming} site={h.site} onClose={() => setRenaming(null)} />
      <RotateSheet sub={rotating} held={h.held} naming={h.naming} onClose={() => setRotating(null)} />
    </>
  )
  return { actions, sheets }
}

