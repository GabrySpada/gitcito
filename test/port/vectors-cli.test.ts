// Vectors for src/shared/cli.ts parseCliOpenArgs, replayed by the Go port
// (internal/shared/cli_test.go). PORT_WRITE_VECTORS=1 regenerates the file;
// otherwise this asserts the TypeScript still produces what the file says, so
// neither implementation can drift without a test failing.
import { describe, it, expect } from 'vitest'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { parseCliOpenArgs } from '../../src/shared/cli'

const FILE = resolve(__dirname, '../../testdata/vectors/cli.json')

const inputs: string[][] = [
  [],
  ['/app/Gitcito'],
  ['/app/Gitcito', '--open=/repo'],
  ['/app/Gitcito', '--open=/repo', '--name=Main', '--group=Work'],
  ['--view=blame', '--open=/r', '--arg=src/a.ts', '--line=42'],
  ['--open=/r', '--view=nonsense'],
  ['--open=/r', '--line=abc'],
  ['--open=/r', '--line=1234567890'],
  ['--open=/r', '--edit=/r/.git/COMMIT_EDITMSG', '--wait=/tmp/s'],
  ['--open', '/r'],
  ['--open=/a', '--open=/b'],
  ['--open=/with=equals'],
  ['--open=/r', '--view=time-machine']
]

describe('cli vectors', () => {
  it('parseCliOpenArgs matches the vector file', () => {
    // JSON drops undefined fields, which is exactly the Go omitempty shape.
    const cases = inputs.map((argv) => ({ argv, out: JSON.parse(JSON.stringify(parseCliOpenArgs(argv))) }))
    if (process.env.PORT_WRITE_VECTORS) writeFileSync(FILE, JSON.stringify(cases, null, 2) + '\n')
    expect(JSON.parse(readFileSync(FILE, 'utf8'))).toEqual(cases)
  })
})
