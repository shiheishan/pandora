import { useState } from 'react'
import type { Holdings } from '../common/holdings'
import { ImportSheet, type ImportTarget } from '../common/ImportSheet'
import type { Subscription } from '../common/subscriptions'
import { RenameSheet } from './RenameSheet'
import { RotateSheet } from './RotateSheet'
import { MovePackSheet, type MoveTarget } from './MovePackSheet'

/** 卡片上的三个弹层：添加到 App、改名、换新链接 */
export function useSheets(h: Holdings) {
  const [importing, setImporting] = useState<ImportTarget | null>(null)
  const [renaming, setRenaming] = useState<Subscription | null>(null)
  const [rotating, setRotating] = useState<Subscription | null>(null)
  const [moving, setMoving] = useState<MoveTarget | null>(null)
  const actions = {
    onImport: (sub: Subscription) => {
      const url = h.urlOf(sub.id)
      if (url) setImporting({ sub, url })
    },
    onRename: setRenaming,
    onRotate: setRotating,
    onMove: (sub: Subscription) => setMoving({ from: sub, to: moveTargets(h, sub) }),
  }
  const sheets = (
    <>
      <ImportSheet target={importing} naming={h.naming} onClose={() => setImporting(null)} />
      <RenameSheet sub={renaming} all={h.subs.data ?? []} naming={h.naming} site={h.site} onClose={() => setRenaming(null)} />
      <RotateSheet sub={rotating} held={h.held} naming={h.naming} onClose={() => setRotating(null)} />
      <MovePackSheet target={moving} naming={h.naming} onClose={() => setMoving(null)} />
    </>
  )
  return { actions, sheets }
}


/** 旧流量包能挪去的那几份：手上的（生效中或可救回）、不是这一份 */
export const moveTargets = (h: Holdings, sub: Subscription) => h.held.filter((s) => s.id !== sub.id)
