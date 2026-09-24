// Diff focus: opening a diff collapses the left sidebar so the two columns get
// the width, and closing it gives the sidebar back — the way a code review tool
// takes the screen for the change and returns it after.

export interface SidebarState {
  collapsed: boolean
  /** The sidebar was collapsed by opening a diff, not by the user — so closing
   *  the viewer may reopen it. */
  auto: boolean
}

/**
 * The sidebar after the file viewer goes from `prev` to `next` (null: closed).
 *
 * - Entering diff mode (opening a diff, or switching the viewer to Diff) while
 *   the sidebar is open collapses it, and remembers that it did.
 * - Closing the viewer reopens it — only if the diff collapsed it, and it is
 *   still collapsed. A sidebar the user closed themselves stays closed.
 * - Anything else (another file, another mode) leaves it alone.
 */
export function sidebarForFileView(
  prev: { mode: string } | null,
  next: { mode: string } | null,
  sidebar: SidebarState
): SidebarState {
  if (next === null) {
    return sidebar.auto && sidebar.collapsed ? { collapsed: false, auto: false } : { ...sidebar, auto: false }
  }
  const enteringDiff = next.mode === 'diff' && prev?.mode !== 'diff'
  if (enteringDiff && !sidebar.collapsed) return { collapsed: true, auto: true }
  return sidebar
}
