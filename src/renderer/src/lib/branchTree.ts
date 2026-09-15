// Folding a flat list of refs into the sidebar's folder tree. Local branches,
// each remote's branches and tags all share this shape, so the rule for what
// becomes a folder lives here rather than three times inside the Sidebar.

/** A node in a ref folder tree. `item` is set when the node is itself a ref —
 *  a leaf, or a folder name that is also a ref (`feature` beside `feature/x`). */
export interface TreeNode<T> {
  seg: string
  item?: T
  children: Map<string, TreeNode<T>>
}

/** Fold a flat list of refs into a folder tree keyed by their "/" prefix. */
export function buildPrefixTree<T>(items: T[], nameOf: (t: T) => string): TreeNode<T> {
  const root: TreeNode<T> = { seg: '', children: new Map() }
  for (const it of items) {
    let node = root
    const parts = nameOf(it).split('/')
    parts.forEach((seg, i) => {
      let child = node.children.get(seg)
      if (!child) {
        child = { seg, children: new Map() }
        node.children.set(seg, child)
      }
      node = child
      if (i === parts.length - 1) node.item = it
    })
  }
  return root
}

/** Number of actual refs under a node, used for the folder's count badge. */
export function leafCount<T>(node: TreeNode<T>): number {
  let n = node.item ? 1 : 0
  for (const c of node.children.values()) n += leafCount(c)
  return n
}

/** Every ref under a node, in tree order — the scope of a folder-wide action. */
export function collectLeaves<T>(node: TreeNode<T>, out: T[] = []): T[] {
  if (node.item) out.push(node.item)
  for (const c of node.children.values()) collectLeaves(c, out)
  return out
}

/** What a node renders as once the chain rule below has been applied: a ref
 *  row, or a collapsible folder whose header may span several path segments. */
export type FoldedNode<T> =
  | { kind: 'leaf'; item: T; label: string }
  | { kind: 'folder'; node: TreeNode<T>; title: string }

/**
 * Decide whether a node is a row or a folder.
 *
 * A prefix is *always* a folder, even when it holds a single branch — otherwise
 * `refactor/v2` reads as a branch with a slash in its name, and the sidebar
 * reshuffles itself the moment a second `refactor/*` branch lands. What still
 * collapses is a *run* of single-child folders: `dependabot/npm_and_yarn` is one
 * header, not two levels holding one child each. The run stops before the last
 * segment, so the ref itself always sits inside a folder rather than becoming
 * its header.
 */
export function foldNode<T>(node: TreeNode<T>): FoldedNode<T> {
  let cur = node
  let title = node.seg
  while (!cur.item && cur.children.size === 1) {
    const only = [...cur.children.values()][0]
    if (only.children.size === 0) break
    cur = only
    title = `${title}/${only.seg}`
  }
  if (cur.item && cur.children.size === 0) return { kind: 'leaf', item: cur.item, label: title }
  return { kind: 'folder', node: cur, title }
}
