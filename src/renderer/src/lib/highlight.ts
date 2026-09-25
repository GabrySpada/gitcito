import hljs from 'highlight.js'

/** Lowercase file extension (no dot), or '' when none. */
export function fileExt(name: string): string {
  return name.split('.').pop()?.toLowerCase() || ''
}

/** HTML-escape the three characters that matter inside a text node. */
export function escapeHtml(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/** Map a filename to a highlight.js language id, or '' when unknown. */
export function guessLanguage(filename: string): string {
  const ext = fileExt(filename)
  const map: Record<string, string> = {
    js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
    ts: 'typescript', tsx: 'typescript', py: 'python', rb: 'ruby', go: 'go',
    rs: 'rust', java: 'java', cpp: 'cpp', cc: 'cpp', c: 'c', h: 'cpp',
    cs: 'csharp', php: 'php', swift: 'swift', kt: 'kotlin', scala: 'scala',
    sh: 'bash', bash: 'bash', zsh: 'bash', json: 'json', xml: 'xml',
    html: 'xml', htm: 'xml', vue: 'xml', css: 'css', scss: 'scss', less: 'less',
    md: 'markdown', markdown: 'markdown', yml: 'yaml', yaml: 'yaml',
    toml: 'ini', ini: 'ini', sql: 'sql', r: 'r', dart: 'dart', lua: 'lua',
    pl: 'perl', dockerfile: 'dockerfile', makefile: 'makefile'
  }
  return map[ext] || ''
}

/** Syntax-highlight a single line to HTML, falling back to plain escaped text
 *  when the language is unknown or highlight.js throws. */
export function highlightLine(text: string, lang: string): string {
  if (!lang || !hljs.getLanguage(lang)) return escapeHtml(text)
  try {
    return hljs.highlight(text, { language: lang }).value
  } catch {
    return escapeHtml(text)
  }
}

/** Split highlight.js HTML at its newlines. A span open at a break is closed at
 *  the end of that line and reopened at the start of the next, so every line is
 *  well-formed HTML on its own and still carries the colour it is inside. */
export function splitHighlighted(html: string): string[] {
  const out: string[] = []
  const open: string[] = []
  let cur = ''
  for (const [tok] of html.matchAll(/<span[^>]*>|<\/span>|\n|[^<\n]+/g)) {
    if (tok === '\n') {
      out.push(cur + '</span>'.repeat(open.length))
      cur = open.join('')
    } else {
      if (tok === '</span>') open.pop()
      else if (tok.startsWith('<span')) open.push(tok)
      cur += tok
    }
  }
  out.push(cur)
  return out
}

/** Syntax-highlight consecutive lines as one block, so a construct spanning
 *  lines — a block comment, a template string — keeps its colour on each of
 *  them. One HTML string per input line; an empty line stays empty, since a
 *  lone empty span would collapse its row to zero height. */
export function highlightLines(lines: string[], lang: string): string[] {
  if (!lang || !hljs.getLanguage(lang)) return lines.map(escapeHtml)
  let split: string[]
  try {
    split = splitHighlighted(hljs.highlight(lines.join('\n'), { language: lang, ignoreIllegals: true }).value)
  } catch {
    return lines.map(escapeHtml)
  }
  if (split.length !== lines.length) return lines.map((l) => highlightLine(l, lang))
  return split.map((h, i) => (lines[i] === '' ? '' : h))
}

/** Above this many lines a diff is highlighted line by line, lazily, as before:
 *  a whole-file pass up front would stall the first paint of a huge diff. */
export const BLOCK_HIGHLIGHT_MAX_LINES = 20000

/**
 * Syntax HTML for every line of a diff, highlighted per side rather than per
 * line: the old side is its context and deleted lines in order, the new side
 * its context and added lines. A deleted line takes its old-side colours;
 * context and added lines take the new side's. A side's run restarts wherever
 * its line numbers skip — between hunks the text is missing, and carrying a
 * comment across that gap would colour code that is not in one. `null` where a
 * line has no syntax HTML (hunk and meta lines, an unknown language, an
 * oversized diff) — the caller highlights those lines alone.
 */
export function highlightDiffLines(
  lines: { kind: string; text: string; oldNo: number | null; newNo: number | null }[],
  lang: string,
  maxLines = BLOCK_HIGHLIGHT_MAX_LINES
): (string | null)[] {
  const out: (string | null)[] = lines.map(() => null)
  if (!lang || !hljs.getLanguage(lang) || lines.length > maxLines) return out
  const side = (take: (kind: string) => boolean, no: (i: number) => number | null, own: string): void => {
    let run: number[] = []
    let last: number | null = null
    const flush = (): void => {
      const html = highlightLines(run.map((i) => lines[i].text), lang)
      run.forEach((i, k) => {
        if (lines[i].kind === own || lines[i].kind === 'ctx') out[i] = html[k]
      })
      run = []
    }
    lines.forEach((l, i) => {
      if (!take(l.kind)) return
      const n = no(i)
      if (run.length && (n === null || last === null || n !== last + 1)) flush()
      run.push(i)
      last = n
    })
    flush()
  }
  // Old side first, so context lines end up with the new side's colours.
  side((k) => k === 'del' || k === 'ctx', (i) => lines[i].oldNo, 'del')
  side((k) => k === 'add' || k === 'ctx', (i) => lines[i].newNo, 'add')
  return out
}
