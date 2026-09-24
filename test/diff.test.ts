import { describe, it, expect } from 'vitest'
import {
  parseDiff,
  wordDiff,
  wordRangesByLine,
  buildSplitRows,
  computeGutterChanges,
  gutterMarksByLine,
  fillContext,
  changeBlocks,
  detectIndentUnit,
  indentColumns,
  visibleRange
} from '../src/renderer/src/lib/diff'

const SAMPLE = `@@ -1,3 +1,3 @@
 ctx line
-const x = foo(a, b)
+const x = foo(a, c)
 tail`

describe('parseDiff', () => {
  it('types lines and tracks old/new line numbers', () => {
    const lines = parseDiff(SAMPLE)
    expect(lines.map((l) => l.kind)).toEqual(['hunk', 'ctx', 'del', 'add', 'ctx'])
    const del = lines.find((l) => l.kind === 'del')!
    const add = lines.find((l) => l.kind === 'add')!
    expect(del.oldNo).toBe(2)
    expect(del.newNo).toBeNull()
    expect(add.newNo).toBe(2)
  })

  // A new file's header carries `new file mode 100644`, which has no +/- prefix
  // and used to parse as context — an unprefixed line, numbered 0, above the
  // first hunk of every added file.
  it('treats git extended headers as meta', () => {
    const added = `diff --git a/new.md b/new.md
new file mode 100644
index 0000000..0111bd8
--- /dev/null
+++ b/new.md
@@ -0,0 +1,2 @@
+---
+title: hello`
    const lines = parseDiff(added)
    expect(lines.map((l) => l.kind)).toEqual(['meta', 'meta', 'meta', 'meta', 'meta', 'hunk', 'add', 'add'])
    expect(lines.filter((l) => l.kind === 'add').map((l) => l.newNo)).toEqual([1, 2])
  })

  it('treats rename and binary headers as meta', () => {
    const renamed = `diff --git a/a.txt b/b.txt
similarity index 92%
rename from a.txt
rename to b.txt
Binary files a/logo.png and b/logo.png differ`
    expect(parseDiff(renamed).every((l) => l.kind === 'meta')).toBe(true)
  })
})

describe('wordDiff', () => {
  it('isolates only the changed token', () => {
    const { del, add } = wordDiff('const x = foo(a, b)', 'const x = foo(a, c)')
    expect(del.map(([s, e]) => 'const x = foo(a, b)'.slice(s, e))).toEqual(['b'])
    expect(add.map(([s, e]) => 'const x = foo(a, c)'.slice(s, e))).toEqual(['c'])
  })

  it('returns empty ranges for identical lines', () => {
    expect(wordDiff('same', 'same')).toEqual({ del: [], add: [] })
  })
})

describe('wordRangesByLine', () => {
  it('keys ranges by the line index of paired del/add', () => {
    const lines = parseDiff(SAMPLE)
    const map = wordRangesByLine(lines)
    // del at index 2, add at index 3 each get one changed range.
    expect(map.get(2)?.length).toBe(1)
    expect(map.get(3)?.length).toBe(1)
    expect(map.has(1)).toBe(false) // ctx untouched
  })
})

describe('buildSplitRows', () => {
  it('mirrors ctx and zips del/add into the same row', () => {
    const rows = buildSplitRows(parseDiff(SAMPLE))
    expect(rows[0].hunk).toBeDefined()
    const ctx = rows[1]
    expect(ctx.left?.text).toBe('ctx line')
    expect(ctx.right?.text).toBe('ctx line')
    const change = rows[2]
    expect(change.left?.kind).toBe('del')
    expect(change.right?.kind).toBe('add')
  })

  it('emits one-sided rows for unbalanced edits', () => {
    const rows = buildSplitRows(parseDiff(`@@ -1,1 +1,2 @@\n-old\n+new1\n+new2`))
    const changes = rows.filter((r) => r.left || r.right).filter((r) => r.left?.kind !== 'ctx')
    // 2 rows: (old↔new1) then (∅↔new2)
    expect(changes.length).toBe(2)
    expect(changes[1].left).toBeUndefined()
    expect(changes[1].right?.text).toBe('new2')
  })
})

describe('computeGutterChanges', () => {
  it('marks a paired del/add run as a modification anchored at the new line', () => {
    const changes = computeGutterChanges(SAMPLE)
    expect(changes).toEqual([
      {
        index: 0,
        type: 'mod',
        lineStart: 2,
        lineEnd: 2,
        edge: 'before',
        removed: ['const x = foo(a, b)'],
        added: ['const x = foo(a, c)']
      }
    ])
  })

  it('marks an add-only run as an insertion spanning every added line', () => {
    const diff = `@@ -1,1 +1,3 @@\n ctx\n+new1\n+new2`
    const changes = computeGutterChanges(diff)
    expect(changes).toEqual([
      { index: 0, type: 'add', lineStart: 2, lineEnd: 3, edge: 'before', removed: [], added: ['new1', 'new2'] }
    ])
  })

  it('anchors a mid-file deletion to the line that now follows it', () => {
    const diff = `@@ -1,3 +1,2 @@\n ctx1\n-gone\n ctx2`
    const changes = computeGutterChanges(diff)
    expect(changes).toEqual([
      { index: 0, type: 'del', lineStart: 2, lineEnd: 2, edge: 'before', removed: ['gone'], added: [] }
    ])
  })

  it('anchors a trailing deletion (EOF) after the last remaining line', () => {
    const diff = `@@ -1,2 +1,1 @@\n ctx1\n-gone`
    const changes = computeGutterChanges(diff)
    expect(changes).toEqual([
      { index: 0, type: 'del', lineStart: 1, lineEnd: 1, edge: 'after', removed: ['gone'], added: [] }
    ])
  })

  it('returns nothing for an unchanged file', () => {
    expect(computeGutterChanges('')).toEqual([])
  })
})

describe('gutterMarksByLine', () => {
  it('maps every line of a multi-line change to the same change object', () => {
    const changes = computeGutterChanges(`@@ -1,1 +1,3 @@\n ctx\n+new1\n+new2`)
    const map = gutterMarksByLine(changes)
    expect(map.get(2)).toBe(changes[0])
    expect(map.get(3)).toBe(changes[0])
    expect(map.has(1)).toBe(false)
  })
})

describe('parseDiff trailing lines', () => {
  it('does not turn the final newline of git output into a context line', () => {
    const lines = parseDiff(`${SAMPLE}\n`)
    expect(lines.map((l) => l.kind)).toEqual(['hunk', 'ctx', 'del', 'add', 'ctx'])
  })

  it('does not number the no-newline marker', () => {
    const lines = parseDiff('@@ -1 +1,2 @@\n-a\n\\ No newline at end of file\n+a\n+b')
    const marker = lines.find((l) => l.text.startsWith('\\'))!
    expect(marker.oldNo).toBeNull()
    expect(marker.newNo).toBeNull()
    expect(lines.filter((l) => l.kind === 'add').map((l) => l.newNo)).toEqual([1, 2])
  })
})

describe('fillContext', () => {
  // New file: 10 lines, line 5 edited, one line inserted after line 8.
  const NEW = ['l1', 'l2', 'l3', 'l4', 'L5', 'l6', 'l7', 'l8', 'ins', 'l9', 'l10'].join('\n') + '\n'
  const DIFF = [
    'diff --git a/f b/f',
    '--- a/f',
    '+++ b/f',
    '@@ -4,3 +4,3 @@',
    ' l4',
    '-l5',
    '+L5',
    ' l6',
    '@@ -8,2 +8,3 @@',
    ' l8',
    '+ins',
    ' l9'
  ].join('\n')

  it('splices the unchanged stretches in, numbered on both sides', () => {
    const out = fillContext(parseDiff(DIFF), NEW)!
    const body = out.filter((l) => l.kind !== 'meta' && l.kind !== 'hunk')
    expect(body.map((l) => l.text)).toEqual(['l1', 'l2', 'l3', 'l4', 'l5', 'L5', 'l6', 'l7', 'l8', 'ins', 'l9', 'l10'])
    const l7 = body.find((l) => l.text === 'l7')!
    expect([l7.oldNo, l7.newNo]).toEqual([7, 7])
    // After the insertion the two sides are one line apart.
    const l10 = body.find((l) => l.text === 'l10')!
    expect([l10.oldNo, l10.newNo]).toEqual([10, 11])
  })

  it('keeps hunk rows and hunk indices, so staging still maps', () => {
    const out = fillContext(parseDiff(DIFF), NEW)!
    expect(out.filter((l) => l.kind === 'hunk').length).toBe(2)
    expect(out.find((l) => l.text === 'ins')!.hunkIdx).toBe(1)
    expect(out.find((l) => l.text === 'l1')!.hunkIdx).toBe(-1)
  })

  it('gives up when the text is not the file the diff was taken against', () => {
    expect(fillContext(parseDiff(DIFF), NEW.replace('l8', 'zz'))).toBeNull()
  })

  it('tolerates whitespace-only differences in context (diff -w) and CRLF', () => {
    const crlf = NEW.replace('l4', '  l4').split('\n').join('\r\n')
    const out = fillContext(parseDiff(DIFF), crlf)!
    expect(out.find((l) => l.text === 'l2')).toBeDefined()
  })

  it('adds nothing past a deleted file, whatever the read fell back to', () => {
    const del = 'deleted file mode 100644\n--- a/f\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b'
    const out = fillContext(parseDiff(del), 'something else\n')!
    expect(out.filter((l) => l.kind === 'ctx')).toEqual([])
  })

  it('returns null for a diff without hunks', () => {
    expect(fillContext(parseDiff('Binary files a/x and b/x differ'), 'x')).toBeNull()
  })
})

describe('buildSplitRows full file', () => {
  it('drops the @@ rows and tags the next row with its hunk', () => {
    const rows = buildSplitRows(parseDiff(SAMPLE), true)
    expect(rows.some((r) => r.hunk !== undefined)).toBe(false)
    expect(rows[0].startsHunk).toBe(0)
    expect(rows[1].startsHunk).toBeUndefined()
  })
})

describe('changeBlocks', () => {
  it('groups consecutive changed rows and says which side they touch', () => {
    const rows = buildSplitRows(parseDiff('@@ -1,4 +1,4 @@\n a\n-b\n+B\n+B2\n c\n-d'))
    expect(changeBlocks(rows)).toEqual([
      { start: 2, end: 4, del: true, add: true },
      { start: 5, end: 6, del: true, add: false }
    ])
  })
})

describe('indent guides', () => {
  it('detects a two-space file and ignores block-comment stars', () => {
    const src = ['function f() {', '  if (x) {', '    y()', '  }', '  /**', '   * doc', '   */', '}']
    expect(detectIndentUnit(src, 4)).toBe(2)
  })

  it('reads tabs as the tab size', () => {
    expect(detectIndentUnit(['a', '\tb'], 4)).toBe(4)
    expect(indentColumns(['\t\tx'], 4)).toEqual([8])
  })

  it('carries the smaller neighbouring indent through blank lines, and none into fillers', () => {
    expect(indentColumns(['    a', '', '  b', null, '      c'], 4)).toEqual([4, 2, 2, 0, 6])
  })
})

describe('visibleRange', () => {
  it('covers the rows in view plus the overscan, snapped to chunks', () => {
    // 20px rows, 400px tall view scrolled to row 100: rows 100..120 visible.
    expect(visibleRange(2000, 400, 20, 1000, 40, 20)).toEqual([60, 160])
  })

  it('moves in chunk steps, not per row', () => {
    // Mid-chunk, one row further changes nothing; each edge only moves across a chunk line.
    const a = visibleRange(2040, 400, 20, 1000, 40, 20)
    expect(visibleRange(2040 + 20, 400, 20, 1000, 40, 20)).toEqual(a)
    for (let px = 0; px < 4000; px += 7) {
      const [s, e] = visibleRange(px, 400, 20, 1000, 40, 20)
      expect(s % 20).toBe(0)
      expect(e % 20).toBe(0)
    }
  })

  it('clamps to the list at both ends', () => {
    expect(visibleRange(0, 400, 20, 1000)).toEqual([0, 60])
    expect(visibleRange(19800, 400, 20, 1000)).toEqual([940, 1000])
    expect(visibleRange(0, 400, 20, 5)).toEqual([0, 5])
    expect(visibleRange(0, 400, 20, 0)).toEqual([0, 0])
  })
})
