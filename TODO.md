# TODO — fork-local

Notes for this fork (`GabrySpada/gitcito`), branched from `MyAppDesk/gitcito` at
v4.7.0 (`0bab0666`). Not upstream issues — file them there if you want them fixed
for everyone.

---

## 1. Unsanitized HTML from the spreadsheet preview

**Severity:** Medium — HTML injection. *Not* script execution: the renderer CSP
blocks it. Fix it anyway, because the CSP is the only thing standing in the way
and the identical code path next door already does the right thing.

**Where:** `src/renderer/src/components/PreviewPane.tsx:265`

```tsx
<div className="sheet-body" dangerouslySetInnerHTML={{ __html: sheets[sheetIdx]?.html ?? '' }} />
```

`html` comes from `XLSX.utils.sheet_to_html()` (same file, ~line 133) and is
passed through **without sanitization**. Compare the `.docx` branch at line 140,
which correctly wraps its converted HTML in `sanitizeHtml()`.

**Root cause:** SheetJS escapes cell *text* but not the `data-v` attribute it
writes alongside it. A double quote in a cell value closes the attribute early
and escapes into raw markup.

**Reproduce:**

```js
const XLSX = require('xlsx')
const ws = XLSX.utils.aoa_to_sheet([['"><img src=x onerror=alert(1)>']])
console.log(XLSX.utils.sheet_to_html(ws))
// => <td data-t="s" data-v=""><img src=x onerror=alert(1)>" id="sjs-A1">...
//                             ^ attribute closed, <img> is now live markup
```

**Attack path:** malicious `.xlsx` committed to any repo -> user opens the file
preview -> injected markup renders in the renderer process.

**Why it is not critical:** `src/renderer/index.html` sets
`script-src 'self'` with no `unsafe-inline`, so the `onerror` handler never
fires. Verified present in the built `out/renderer/index.html` too. Residual
risk is in-pane UI spoofing and an `img-src https:` beacon.

**Fix — two options:**

- **(a) Wrap in `sanitizeHtml()`** (from `../preview/markdown`). One line, matches
  the `.docx` path. DOMPurify keeps `data-*` attributes by default, so the sheet
  still renders. *Recommended.*
- **(b) Stop using `sheet_to_html`** and build the table from `sheet_to_json` as
  React elements. Structurally immune since JSX auto-escapes, but loses SheetJS's
  colspan/merged-cell handling.

---

## 2. Vulnerable parsers on the untrusted-file path

`npm audit --omit=dev` reports 7 vulnerabilities in **shipping** dependencies.
These matter more than most audit noise because `PreviewPane.tsx` feeds bytes
from repository files straight into all three parsers:

| Package | Issue | Fix |
|---|---|---|
| `xlsx` | Prototype pollution + ReDoS | **No fix available** |
| `echarts` (via `pptx-preview`) | XSS | Breaking: `pptx-preview@1.0.0` |
| `js-yaml` | Quadratic CPU on merge keys | `npm audit fix` |

Worth deciding whether the Office-preview feature earns its attack surface at
all, given `xlsx` has no upstream fix.

---

## 3. Test suite assumes specific global git config

Two tests fail on a machine whose global git config differs. Not app bugs, but
they make `npm test` noisy locally:

- `test/newFeatures.test.ts` — assumes `init.defaultBranch=main`; fails where
  `git init` still creates `master`.
- `test/gitOps.test.ts` — assumes no `url.*.insteadOf` rewriting; a global
  `https://github.com/` -> `ssh://` rule makes `httpsHosts` come back empty.

Workaround: `GIT_CONFIG_GLOBAL=/dev/null npm test` (269/269 pass).
Proper fix: have `test/fixtures.ts` isolate the global config for all git calls.

---

## 4. Keep the updater disabled

`UPDATES_DISABLED` in `src/main/updater.ts` is a fork-local change. It gates the
launch check, the 3-hourly re-check, manual check/download/install, and the
release-notes fetch in `src/main/index.ts`.

**Do not flip it to `false` while `REPO` still points at `MyAppDesk/gitcito`** —
that would offer binaries this fork never built. Repoint `REPO` first.

Watch for merge conflicts here when pulling upstream.
