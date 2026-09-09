import { AlertTriangle, ArrowRight, Minus, Pencil, Plus, type LucideIcon } from 'lucide-react'
import { fileStats, summaryChips } from '../lib/fileStats'
import { interp, useT } from '../i18n'

const ICONS: Record<string, LucideIcon> = {
  'tb-modified': Pencil,
  'tb-added': Plus,
  'tb-deleted': Minus,
  'tb-renamed': ArrowRight,
  'tb-conflicted': AlertTriangle
}

/** The change breakdown above a file list — "5 modified · 1 added · 1 deleted".
 *  Shares the `tb-*` palette with the collapsed-folder badges so the header and
 *  the tree under it read as one colour language. */
export function ChangeSummary({ files, title }: { files: { status: string }[]; title?: string }): React.JSX.Element {
  const t = useT()
  const chips = summaryChips(fileStats(files))
  return (
    // The plain total moves to the tooltip: the row is narrow and the chips
    // already sum to it.
    <span className="change-summary" title={title} aria-label={title}>
      {chips.map((c) => {
        const Icon = ICONS[c.cls]
        return (
          <span key={c.cls} className={`tree-badge ${c.cls}`}>
            <Icon size={11} strokeWidth={2.5} />
            {interp(t(c.labelKey), { n: c.n })}
          </span>
        )
      })}
    </span>
  )
}
