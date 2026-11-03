/**
 * Port vectors for the `tools` group: src/main/editor.ts, difftool.ts, ssh.ts and
 * the pure logic they share with the renderer (src/shared/editors.ts,
 * diffTools.ts, sshKeys.ts). Replayed by internal/shared, internal/editor,
 * internal/difftool and internal/ssh.
 *
 * - Pure logic → testdata/vectors/{editors,diffTools,sshKeys}.json.
 * - Process behaviour → internal/<pkg>/testdata/<pkg>.json. The real TypeScript
 *   functions run against internal/fakebin/fake.sh standing in for ssh,
 *   ssh-add, ssh-keygen, git, open and the editor CLIs (PATH holds nothing
 *   else), so each case pins the exact argv, the environment the child sees,
 *   what an askpass helper delivers, whether the child was detached, and how
 *   output and exit codes become results. No network, no agent, no ~/.ssh.
 *
 * PORT_WRITE_VECTORS=1 regenerates the files; otherwise this recomputes
 * everything and fails when the TypeScript output drifts from them.
 */
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync
} from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, dirname, join, resolve } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import { EDITOR_PRESETS, editorArgs, editorLaunch, supportsLine } from '../../src/shared/editors'
import type { EditorSetting, EditorTarget, EditorVars } from '../../src/shared/editors'
import { parseToolHelp, sortTools } from '../../src/shared/diffTools'
import {
  agentFingerprints,
  parseKeygenLine,
  privateKeyName,
  publicKeyFiles,
  readSshTest
} from '../../src/shared/sshKeys'

const h = vi.hoisted(() => ({ handlers: new Map<string, (...a: unknown[]) => unknown>() }))
vi.mock('electron', () => ({
  ipcMain: { handle: (ch: string, fn: (...a: unknown[]) => unknown) => h.handlers.set(ch, fn) }
}))

const write = !!process.env.PORT_WRITE_VECTORS
const ROOT_DIR = resolve(__dirname, '../..')
function check(rel: string, data: unknown): void {
  const file = join(ROOT_DIR, rel)
  if (write) {
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, JSON.stringify(data, null, 2) + '\n')
  }
  expect(JSON.parse(readFileSync(file, 'utf8'))).toEqual(JSON.parse(JSON.stringify(data)))
}
/** JSON round trip: what the file holds, undefined fields dropped. */
const plain = <T>(v: T): T => (v === undefined ? (null as T) : (JSON.parse(JSON.stringify(v)) as T))

// ═══ pure vectors ══════════════════════════════════════════════════════════

describe('tools pure vectors', () => {
  it('editors: presets, editorArgs, supportsLine, editorLaunch', () => {
    const argCases: Array<{ template: string; vars: EditorVars }> = [
      { template: '-g {path}:{line}:{col}', vars: { path: '/r/a.ts', line: 12, col: 3 } },
      { template: '{path}:{line}:{col}', vars: { path: '/r/a.ts', line: 9 } },
      { template: '{path}:{line}:{col}', vars: { path: '/r/a.ts' } },
      { template: '--line {line} --column {col} {path}', vars: { path: '/r/a.ts' } },
      { template: '--line {line} --column {col} {path}', vars: { path: '/r/a.ts', line: 4 } },
      { template: '"{path}"', vars: { path: '/r/my dir/a.ts' } },
      { template: '{path}', vars: { path: '/r/my dir/a.ts' } },
      { template: "'{path}' --x", vars: { path: '/r/q\'s/a.ts' } },
      { template: '-g {path}:{line}:{col}', vars: { path: '/r/a.ts', line: 0, col: 5 } },
      { template: '-g {path}:{line}:{col}', vars: { path: '/r/a.ts', line: -3, col: 5 } },
      { template: '-g {path}:{line}:{col}', vars: { path: '/r/a.ts', line: 7, col: 0 } },
      { template: '-g {path}:{line}:{col}', vars: { path: '/r/a.ts', line: 7, col: -2 } },
      { template: '-g {path}:{line}:{col}', vars: { path: '/r/a.ts', line: 1.5, col: 2.25 } },
      { template: '--folder-uri {repo} {path}', vars: { path: '/r/a.ts' } },
      { template: '--folder-uri {repo} {path}', vars: { path: '/r/a.ts', repo: '/r' } },
      { template: '{repo}', vars: { path: '/r/a.ts', repo: '' } },
      { template: 'x{line} {path}', vars: { path: '/r/a.ts' } },
      { template: '--goto={path}:{line}', vars: { path: '/r/a.ts', line: 2 } },
      { template: '--goto={path}:{line}', vars: { path: '/r/a.ts' } },
      { template: '{path}:{line}', vars: { path: '/r/odd:', line: 0 } },
      { template: '   -a\t\t{path}   ', vars: { path: '/r/a.ts' } },
      { template: '-a {path} --b', vars: { path: '/r/a.ts' } },
      { template: '""', vars: { path: '/r/a.ts' } },
      { template: '"" {path}', vars: { path: '/r/a.ts' } },
      { template: '"unterminated {path}', vars: { path: '/r/a b.ts' } },
      { template: 'a"b c"d {path}', vars: { path: '/r/a.ts' } },
      { template: '', vars: { path: '/r/a.ts' } },
      { template: '{path}', vars: { path: '' } },
      { template: '--file {path}', vars: { path: '' } },
      { template: 'plain {path}', vars: { path: '' } },
      { template: '{path}', vars: { path: '/r/$&-$$-$\'-$`-$1.ts' } },
      { template: '{path}:{line}', vars: { path: '/r/{line}.ts', line: 3 } },
      { template: '{PATH} {path}', vars: { path: '/r/a.ts' } },
      { template: '-g {path}:{line}:{col}', vars: { path: '/r/é ü/日本.ts', line: 1, col: 1 } }
    ]
    const vscode: EditorSetting = { id: 'vscode', name: 'Visual Studio Code', command: '/usr/local/bin/code', source: 'cli' }
    const settings: EditorSetting[] = [
      vscode,
      { ...vscode, command: '/Applications/Visual Studio Code.app', source: 'app' },
      { id: 'custom', name: 'nano', command: '/usr/bin/nano', source: 'cli' },
      { id: 'custom', name: 'vim', command: 'vim', source: 'cli', fileArgs: '+{line} {path}', folderArgs: '{path}' },
      { id: 'custom', name: 'emacs', command: 'emacs', source: 'app', fileArgs: '+{line} {path}' },
      { id: 'custom', name: 'ws', command: 'code', source: 'cli', fileArgs: '-g {path}:{line}:{col}', folderArgs: '--folder-uri {repo}' },
      { id: 'jetbrains', name: 'JetBrains IDE', command: '/usr/local/bin/idea', source: 'cli' },
      { id: 'xcode', name: 'Xcode', command: '/usr/bin/xed', source: 'cli' },
      { id: 'zed', name: 'Zed', command: '/usr/bin/zed', source: 'cli' },
      { id: 'sublime', name: 'Sublime Text', command: '/Applications/Sublime Text.app', source: 'app' },
      { id: 'bogus' as EditorSetting['id'], name: 'x', command: '/bin/x', source: 'cli' },
      { id: 'custom', name: '', command: '', source: 'cli' }
    ]
    const targets: EditorTarget[] = [
      { path: '/r/a.ts' },
      { path: '/r/a.ts', line: 7 },
      { path: '/r/a.ts', line: 7, col: 4 },
      { path: '/r', isDir: true },
      { path: '/r/sub', isDir: true, repo: '/r' },
      { path: '/r/my file.ts', line: 3, repo: '/r' }
    ]
    const launch: unknown[] = []
    for (const setting of settings) {
      for (const target of targets) launch.push({ setting, target, out: plain(editorLaunch(setting, target)) })
    }
    check('testdata/vectors/editors.json', {
      presets: EDITOR_PRESETS,
      args: argCases.map((c) => ({ ...c, out: editorArgs(c.template, c.vars) })),
      supportsLine: settings.map((setting) => ({ setting, out: supportsLine(setting) })),
      launch
    })
  })

  it('diffTools: parseToolHelp and sortTools', () => {
    const HELP = [
      "'git difftool --tool=<tool>' may be set to one of the following:",
      '\t\taraxis           Use Araxis Merge (requires a graphical session)',
      '\t\topendiff         Use FileMerge (requires a graphical session)',
      '\t\tvimdiff          Use Vim',
      '',
      'The following tools are valid, but not currently available:',
      '\t\tbc               Use Beyond Compare (requires a graphical session)',
      '\t\tmeld             Use Meld (requires a graphical session)',
      '',
      'Some of the tools listed above only work in a windowed',
      'environment. If run in a terminal-only session, they will fail.'
    ].join('\n')
    const inputs = [
      HELP,
      HELP.split('\n').join('\r\n'),
      '',
      'fatal: not a git repository',
      [
        "'git mergetool --tool=<tool>' may be set to one of the following:",
        '\t\tvimdiff\t\tUse Vim with a custom layout (see `git help mergetool`\'s `BACKEND SPECIFIC HINTS` section)',
        '\t\tnvimdiff1        Use Neovim with a custom layout',
        '\t\tmy.tool+x        Custom',
        '\t\tmy_tool-2',
        '\t\tbad/id           Slash is not an id',
        '   weird          Non-breaking space indent',
        '\t\tcafé             Accented id',
        '',
        'The following tools are valid, but NOT CURRENTLY AVAILABLE:',
        '\t\tkdiff3           Use KDiff3 (requires a graphical session)   ',
        '\t\tsmerge\r',
        '\tSome of the tools listed above only work in a windowed',
        '\tenvironment.',
        '\t\tTortoiseMerge    Windows only',
        '\t\ttool\rwith       carriage return inside'
      ].join('\n'),
      'user-defined:\n\t\tmine.cmd   echo\n\t\t\n\t\t\t\n'
    ]
    const sortInputs = [
      parseToolHelp(HELP),
      [
        { id: 'Meld', description: '', available: false },
        { id: 'araxis', description: '', available: true },
        { id: 'bc', description: 'a', available: false },
        { id: 'bc', description: 'b', available: false },
        { id: 'vimdiff', description: '', available: true },
        { id: 'nvimdiff10', description: '', available: true },
        { id: 'nvimdiff2', description: '', available: true },
        { id: 'nvimdiff1', description: '', available: true },
        { id: 'my.tool', description: '', available: true },
        { id: 'my-tool', description: '', available: true },
        { id: 'my_tool', description: '', available: true },
        { id: 'my+tool', description: '', available: true },
        { id: 'mytool', description: '', available: true },
        { id: 'MyTool', description: '', available: true },
        { id: 'bc3', description: '', available: false },
        { id: 'bc4', description: '', available: false },
        { id: 'BC', description: '', available: false },
        { id: 'tortoisemerge', description: '', available: false },
        { id: 'TortoiseMerge', description: '', available: false },
        { id: 'xxdiff', description: '', available: true },
        { id: 'emerge', description: '', available: true },
        { id: 'p4merge', description: '', available: true },
        { id: 'p4', description: '', available: true }
      ],
      []
    ]
    check('testdata/vectors/diffTools.json', {
      parse: inputs.map((stdout) => ({ stdout, out: parseToolHelp(stdout) })),
      sort: sortInputs.map((tools) => ({ tools, out: sortTools(tools) }))
    })
  })

  it('sshKeys: parsers and verdicts', () => {
    const lines = [
      '256 SHA256:abc123 you@example.com (ED25519)',
      '4096 SHA256:xyz old laptop key (RSA)',
      '256 SHA256:def no comment (ED25519)',
      '256 SHA256:abc you@example.com',
      '521 SHA256:abc key (ECDSA-SK)',
      'The agent has no identities.',
      'Error connecting to agent: No such file or directory',
      '',
      '   ',
      '2048 MD5:aa:bb:cc:dd comment (RSA)',
      '2048 aa:bb:cc:dd:ee comment (RSA)',
      '2048 AB:cd:ef comment (DSA)',
      '2048 zz:bb:cc comment (DSA)',
      '  256 SHA256:x   spaced   comment   (ED25519)  ',
      '256 SHA256:x (ED25519)',
      '256 SHA256:x',
      '256 SHA256:x comment (ED25519) trailing',
      '256 SHA256:x c ((RSA))',
      '256 SHA256:x c (ed25519-sk)',
      '256 SHA256:x c (ECDSA)',
      '256 SHA256:x c (DSA)',
      '256 SHA256:x c (Ed448)',
      '256 SHA256:x c ()',
      '256\tSHA256:x\tc\t(RSA)',
      '256 SHA256:a x (RSA)\n256 SHA256:b y (RSA)',
      '256 SHA256:a x\r(RSA)',
      '256 SHA256:a com ment (RSA)',
      'abc SHA256:x c (RSA)',
      '00256 SHA256:x c (RSA)',
      '256 notafingerprint c (RSA)',
      '﻿256 SHA256:x c (RSA)',
      '256 SHA256:x c (RSA) '
    ]
    const agentOutputs = [
      '256 SHA256:aaa a@b (ED25519)\n4096 SHA256:bbb c@d (RSA)\nThe agent has no identities.',
      'Could not open a connection to your authentication agent.',
      '256 SHA256:aaa a@b (ED25519)\r\n256 SHA256:aaa dup (ED25519)\r\n3072 SHA256:ccc (RSA)\r\n',
      ''
    ]
    const listings = [
      ['config', 'known_hosts', 'id_rsa', 'id_rsa.pub', 'id_ed25519', 'id_ed25519.pub', 'authorized_keys'],
      [
        'Work.pub',
        'work.pub',
        'id_ed25519_sk.pub',
        'id-rsa.pub',
        'a.pub',
        'B.pub',
        '.pub',
        'github_ed25519.pub',
        '10.pub',
        '9.pub',
        'known_hosts.pub',
        'config.pub',
        'z key.pub',
        'environment',
        'rc',
        'x.PUB',
        'é.pub',
        'e.pub'
      ],
      []
    ]
    const names = ['id_ed25519.pub', '/home/me/.ssh/id_rsa.pub', 'x.pub.pub', 'x', '.pub', 'x.PUB', 'x.pubx', '']
    const sshOutputs = [
      "Hi octocat! You've successfully authenticated, but GitHub does not provide shell access.",
      'Welcome to GitLab, @octocat!',
      'git@github.com: Permission denied (publickey).',
      'ssh: Could not resolve hostname github.com',
      'something else entirely',
      'logged in as octocat.\n\nYou can use git to connect to Bitbucket. Shell access is disabled.',
      'remote: Shell access is not supported.\nAuthentication failed.',
      'ssh: connect to host ssh.dev.azure.com port 22: Connection timed out',
      'ssh: connect to host github.com port 22: Connection refused',
      'ssh: connect to host x port 22: Network is unreachable',
      'ssh: connect to host x port 22: No route to host',
      'Access denied',
      'Permission denied but successfully authenticated',
      ''
    ]
    check('testdata/vectors/sshKeys.json', {
      parseKeygenLine: lines.map((line) => ({ line, out: parseKeygenLine(line) })),
      agentFingerprints: agentOutputs.map((stdout) => ({ stdout, out: [...agentFingerprints(stdout)] })),
      publicKeyFiles: listings.map((entries) => ({ entries, out: publicKeyFiles(entries) })),
      privateKeyName: names.map((name) => ({ name, out: privateKeyName(name) })),
      readSshTest: sshOutputs.map((output) => ({ output, out: readSshTest(output) }))
    })
  })
})

// ═══ the fake binaries ═════════════════════════════════════════════════════

const FAKE_SCRIPT = join(ROOT_DIR, 'internal/fakebin/fake.sh')
/** What fake.sh itself runs; linked into the bin dir so PATH holds nothing real. */
const UTILS = ['basename', 'mkdir', 'cat', 'cmp', 'cut']
const ORIGINAL_PATH = process.env.PATH ?? ''

interface Reply {
  /** Absent: the default reply, for any argv. */
  argv?: string[]
  stdout?: string
  stderr?: string
  code?: number
  create?: string[]
}
interface Call {
  argv: string[]
  env: Record<string, string>
  askpass?: string
  askpassScript?: string
  leader?: string
}

function which(bin: string): string {
  for (const dir of ORIGINAL_PATH.split(delimiter).filter(Boolean)) {
    if (existsSync(join(dir, bin))) return join(dir, bin)
  }
  throw new Error(`${bin} is not on PATH`)
}

/** bin/ with the fake under each name plus fake.sh's utilities; fake/ with the replies. */
function installFake(root: string, bins: string[], replies: Reply[]): { bin: string; fake: string } {
  const bin = join(root, 'fakebin')
  const fake = join(root, 'fake')
  mkdirSync(bin, { recursive: true })
  mkdirSync(join(fake, 'replies'), { recursive: true })
  copyFileSync(FAKE_SCRIPT, join(bin, '.fake.sh'))
  chmodSync(join(bin, '.fake.sh'), 0o755)
  for (const name of bins) symlinkSync(join(bin, '.fake.sh'), join(bin, name))
  for (const util of UTILS) symlinkSync(which(util), join(bin, util))
  replies.forEach((r, i) => {
    const dir = join(fake, 'replies', r.argv ? `r${String(i).padStart(3, '0')}` : 'default')
    mkdirSync(dir)
    if (r.argv) writeFileSync(join(dir, 'argv'), r.argv.map((a) => a + '\0').join(''))
    if (r.stdout !== undefined) writeFileSync(join(dir, 'stdout'), r.stdout)
    if (r.stderr !== undefined) writeFileSync(join(dir, 'stderr'), r.stderr)
    if (r.code !== undefined) writeFileSync(join(dir, 'code'), String(r.code))
    if (r.create) writeFileSync(join(dir, 'create'), r.create.map((f) => f + '\n').join(''))
  })
  return { bin, fake }
}

function readCalls(fake: string): Call[] {
  const dir = join(fake, 'calls')
  if (!existsSync(dir)) return []
  return readdirSync(dir)
    .map(Number)
    .sort((a, b) => a - b)
    .map((n) => {
      const c = join(dir, String(n))
      const split = (s: string): string[] => s.split('\0').slice(0, -1)
      const pairs = split(readFileSync(join(c, 'env'), 'utf8'))
      const env: Record<string, string> = {}
      for (let i = 0; i < pairs.length; i += 2) env[pairs[i]] = pairs[i + 1]
      const opt = (f: string): string | undefined =>
        existsSync(join(c, f)) ? readFileSync(join(c, f), 'utf8') : undefined
      return {
        argv: split(readFileSync(join(c, 'argv'), 'utf8')),
        env,
        askpass: opt('askpass'),
        askpassScript: opt('askpass_script'),
        leader: opt('leader')?.trim()
      }
    })
}

/** Wait for a detached child to finish recording itself. */
async function waitCalls(fake: string, n: number): Promise<void> {
  for (let i = 0; i < 250; i++) {
    const dir = join(fake, 'calls')
    if (existsSync(dir)) {
      const done = readdirSync(dir).filter((d) => existsSync(join(dir, d, 'done')))
      if (done.length >= n) return
    }
    await new Promise((r) => setTimeout(r, 20))
  }
  throw new Error('fake was never called')
}

const esc = (p: string): string => JSON.stringify(p).slice(1, -1)
function expand<T>(v: T, root: string): T {
  return JSON.parse(JSON.stringify(v).split('<ROOT>').join(esc(root))) as T
}
/** Machine paths → placeholders. The askpass helper lives in a fresh mkdtemp. */
function normalize<T>(v: T, root: string): T {
  let s = JSON.stringify(v ?? null)
  s = s.replace(/[^"\\]*gitcito-askpass-[A-Za-z0-9]+(\\\/|\/)askpass\.sh/g, '<ASKPASS>')
  s = s.split(esc(root)).join('<ROOT>')
  return JSON.parse(s) as T
}

/** Environment a case runs in. Everything an askpass decision reads starts unset. */
const CLEARED = ['DISPLAY', 'SSH_ASKPASS', 'SSH_ASKPASS_REQUIRE', 'GIT_TERMINAL_PROMPT', 'GITCITO_SSH_PASSPHRASE']
async function withEnv<T>(
  vars: Record<string, string | undefined>,
  platform: string | undefined,
  argvFlag: string | undefined,
  fn: () => Promise<T>
): Promise<T> {
  const saved = { ...process.env }
  const platformDesc = Object.getOwnPropertyDescriptor(process, 'platform')!
  for (const k of CLEARED) delete process.env[k]
  delete process.env.GITCITO_SSH_DIR
  for (const [k, v] of Object.entries(vars)) {
    if (v === undefined) delete process.env[k]
    else process.env[k] = v
  }
  if (platform) Object.defineProperty(process, 'platform', { value: platform })
  if (argvFlag) process.argv.push(argvFlag)
  try {
    return await fn()
  } finally {
    if (argvFlag) process.argv.splice(process.argv.lastIndexOf(argvFlag), 1)
    Object.defineProperty(process, 'platform', platformDesc)
    for (const k of Object.keys(process.env)) if (!(k in saved)) delete process.env[k]
    Object.assign(process.env, saved)
  }
}

/** Create files (string content) and directories (null) under root. */
function layFiles(root: string, files: Record<string, string | null> | undefined, modes?: Record<string, number>): void {
  for (const [rel, content] of Object.entries(files ?? {})) {
    const full = join(root, rel)
    if (content === null) {
      mkdirSync(full, { recursive: true })
      continue
    }
    mkdirSync(dirname(full), { recursive: true })
    writeFileSync(full, content)
    if (modes?.[rel] !== undefined) chmodSync(full, modes[rel])
  }
}

async function outcome(fn: () => Promise<unknown>): Promise<{ result?: unknown; error?: string }> {
  try {
    return { result: plain(await fn()) }
  } catch (e) {
    return { error: (e as Error).message }
  }
}

// ═══ ssh ═══════════════════════════════════════════════════════════════════

interface SshCase {
  name: string
  platform?: 'darwin'
  shot: boolean
  env: Record<string, string>
  files?: Record<string, string | null>
  bins: string[]
  replies: Reply[]
  call: { method: 'status' | 'generate' | 'addToAgent' | 'test'; args: string[] }
  /** Inspected after the call: the mode of this directory (sshGenerate's mkdir + chmod). */
  modeOf?: string
}

const SSH = '<ROOT>/ssh'
const SSH_BINS = ['ssh', 'ssh-add', 'ssh-keygen']
const shotEnv = { GITCITO_SSH_DIR: SSH, HOME: '<ROOT>/home' }
const keyFiles = {
  'ssh/id_ed25519': 'PRIVATE — never read\n',
  'ssh/id_ed25519.pub': 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample me@example.com\n',
  'ssh/id_rsa.pub': '﻿  ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQExample  \n\n',
  'ssh/broken.pub': 'not a key\n',
  'ssh/folder.pub': null,
  'ssh/Work Key.pub': 'ecdsa-sha2-nistp256 AAAAE2VjZHNhExample laptop key\n',
  'ssh/Work Key': 'PRIVATE\n',
  'ssh/config': 'Host *\n',
  'ssh/known_hosts': 'github.com ssh-ed25519 AAAA\n',
  'home/.keep': ''
}
const keygen = (file: string, stdout: string, code = 0, stderr?: string): Reply => ({
  argv: ['ssh-keygen', '-lf', `${SSH}/${file}`],
  stdout,
  code,
  stderr
})
const allKeygen = [
  keygen('id_ed25519.pub', '256 SHA256:aaa me@example.com (ED25519)\n'),
  keygen('id_rsa.pub', '3072 SHA256:bbb no comment (RSA)\n'),
  keygen('broken.pub', '', 255, `${SSH}/broken.pub is not a public key file.\n`),
  keygen('folder.pub', '256 SHA256:ddd folder (ED25519)\n'),
  keygen('Work Key.pub', '256 SHA256:ccc laptop key (ECDSA)\n')
]

const sshCases: SshCase[] = [
  {
    name: 'status with keys, one of them in the agent',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [{ argv: ['ssh-add', '-l'], stdout: '256 SHA256:aaa me@example.com (ED25519)\n' }, ...allKeygen],
    call: { method: 'status', args: [] }
  },
  {
    name: 'status with no agent to talk to',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [
      { argv: ['ssh-add', '-l'], code: 2, stderr: 'Could not open a connection to your authentication agent.\n' },
      ...allKeygen
    ],
    call: { method: 'status', args: [] }
  },
  {
    name: 'status with an empty agent',
    shot: true,
    env: shotEnv,
    files: { 'ssh/id_ed25519.pub': 'ssh-ed25519 AAAA me\n' },
    bins: SSH_BINS,
    replies: [{ argv: ['ssh-add', '-l'], code: 1, stdout: 'The agent has no identities.\n' }, allKeygen[0]],
    call: { method: 'status', args: [] }
  },
  {
    name: 'status when the agent socket is dead',
    shot: true,
    env: shotEnv,
    files: { 'ssh/id_ed25519.pub': 'ssh-ed25519 AAAA me\n' },
    bins: SSH_BINS,
    replies: [{ argv: ['ssh-add', '-l'], code: 2, stderr: 'Error connecting to agent: No such file or directory\n' }],
    call: { method: 'status', args: [] }
  },
  {
    name: 'status without any ssh binaries',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: [],
    replies: [],
    call: { method: 'status', args: [] }
  },
  {
    name: 'status when the directory does not exist',
    shot: true,
    env: { GITCITO_SSH_DIR: '<ROOT>/absent', HOME: '<ROOT>/home' },
    files: { 'home/.keep': '' },
    bins: SSH_BINS,
    replies: [],
    call: { method: 'status', args: [] }
  },
  {
    name: 'status ignores GITCITO_SSH_DIR outside shot mode',
    shot: false,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [],
    call: { method: 'status', args: [] }
  },
  {
    name: 'status reads ~/.ssh outside shot mode',
    shot: false,
    env: shotEnv,
    files: { 'home/.ssh/id_ed25519.pub': 'ssh-ed25519 AAAA home\n', 'ssh/id_rsa.pub': 'ssh-rsa AAAA other\n' },
    bins: SSH_BINS,
    replies: [
      { argv: ['ssh-add', '-l'], stdout: '' },
      { argv: ['ssh-keygen', '-lf', '<ROOT>/home/.ssh/id_ed25519.pub'], stdout: '256 SHA256:hhh home (ED25519)\n' }
    ],
    call: { method: 'status', args: [] }
  },
  {
    name: 'generate creates the directory and sanitises the name',
    shot: true,
    env: { GITCITO_SSH_DIR: '<ROOT>/new/ssh', HOME: '<ROOT>/home' },
    bins: SSH_BINS,
    replies: [
      {
        argv: ['ssh-keygen', '-t', 'ed25519', '-f', '<ROOT>/new/ssh/mykey@host', '-C', 'me@example.com', '-N', ''],
        stdout: 'Generating public/private ed25519 key pair.\n',
        create: ['<ROOT>/new/ssh/mykey@host', '<ROOT>/new/ssh/mykey@host.pub']
      }
    ],
    call: { method: 'generate', args: ['  my key!@host  ', '  me@example.com  ', ''] },
    modeOf: '<ROOT>/new/ssh'
  },
  {
    name: 'generate keeps dots, dashes and underscores and drops a slash',
    shot: true,
    env: shotEnv,
    files: { 'ssh/.keep': '' },
    bins: SSH_BINS,
    replies: [
      {
        argv: ['ssh-keygen', '-t', 'ed25519', '-f', `${SSH}/a.b-c_dx`, '-C', '', '-N', ''],
        create: [`${SSH}/a.b-c_dx.pub`]
      }
    ],
    call: { method: 'generate', args: ['a.b-c_d/x', '', ''] },
    modeOf: SSH
  },
  {
    name: 'generate surfaces ssh-keygen stderr when no key appeared',
    shot: true,
    env: shotEnv,
    files: { 'ssh/.keep': '' },
    bins: SSH_BINS,
    replies: [
      {
        argv: ['ssh-keygen', '-t', 'ed25519', '-f', `${SSH}/x`, '-C', 'c', '-N', ''],
        code: 1,
        stderr: '  Saving key "x" failed: Permission denied\n'
      }
    ],
    call: { method: 'generate', args: ['x', 'c', ''] }
  },
  {
    name: 'generate falls back to a generic message',
    shot: true,
    env: shotEnv,
    files: { 'ssh/.keep': '' },
    bins: SSH_BINS,
    replies: [{ argv: ['ssh-keygen', '-t', 'ed25519', '-f', `${SSH}/x`, '-C', 'c', '-N', ''], code: 1 }],
    call: { method: 'generate', args: ['x', 'c', ''] }
  },
  {
    name: 'generate without ssh-keygen',
    shot: true,
    env: shotEnv,
    files: { 'ssh/.keep': '' },
    bins: [],
    replies: [],
    call: { method: 'generate', args: ['x', 'c', ''] }
  },
  {
    name: 'generate trusts the .pub, not the exit code',
    shot: true,
    env: shotEnv,
    files: { 'ssh/.keep': '' },
    bins: SSH_BINS,
    replies: [
      {
        argv: ['ssh-keygen', '-t', 'ed25519', '-f', `${SSH}/x`, '-C', 'c', '-N', ''],
        code: 1,
        stderr: 'warning\n',
        create: [`${SSH}/x.pub`]
      }
    ],
    call: { method: 'generate', args: ['x', 'c', ''] }
  },
  {
    name: 'generate refuses an empty name',
    shot: true,
    env: shotEnv,
    bins: SSH_BINS,
    replies: [],
    call: { method: 'generate', args: ['  !!! //  ', 'c', ''] }
  },
  {
    name: 'generate refuses to overwrite a private key',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [],
    call: { method: 'generate', args: ['id_ed25519', 'c', ''] }
  },
  {
    name: 'generate refuses when only the public half exists',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [],
    call: { method: 'generate', args: ['id_rsa', 'c', ''] }
  },
  {
    name: 'generate refuses a name that resolves to a directory',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [],
    call: { method: 'generate', args: ['..', 'c', ''] }
  },
  {
    name: 'add refuses a key whose private half is missing',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [],
    call: { method: 'addToAgent', args: [`${SSH}/id_rsa.pub`, 'secret'] }
  },
  {
    name: 'add without a passphrase',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [{ argv: ['ssh-add', `${SSH}/id_ed25519`], stderr: `Identity added: ${SSH}/id_ed25519 (me@example.com)\n` }],
    call: { method: 'addToAgent', args: [`${SSH}/id_ed25519.pub`, ''] }
  },
  {
    name: 'add that prints nothing is a success',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [{ argv: ['ssh-add', `${SSH}/Work Key`] }],
    call: { method: 'addToAgent', args: [`${SSH}/Work Key.pub`, ''] }
  },
  {
    name: 'add failure without a passphrase',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [
      {
        argv: ['ssh-add', `${SSH}/id_ed25519`],
        code: 2,
        stdout: 'partial\n',
        stderr: '  Could not open a connection to your authentication agent.\n'
      }
    ],
    call: { method: 'addToAgent', args: [`${SSH}/id_ed25519.pub`, ''] }
  },
  {
    name: 'add with a passphrase goes through an askpass helper',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [{ argv: ['ssh-add', `${SSH}/id_ed25519`], stderr: `Identity added: ${SSH}/id_ed25519 (me@example.com)\n` }],
    call: { method: 'addToAgent', args: [`${SSH}/id_ed25519.pub`, 'hunter2 "$HOME" `id` \\ \'q\' é'] }
  },
  {
    name: 'add with a wrong passphrase',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [
      { argv: ['ssh-add', `${SSH}/id_ed25519`], code: 1, stderr: `Bad passphrase, try again for ${SSH}/id_ed25519: \n` }
    ],
    call: { method: 'addToAgent', args: [`${SSH}/id_ed25519.pub`, 'wrong'] }
  },
  {
    name: 'add on macOS keeps the passphrase in the keychain',
    platform: 'darwin',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [{ argv: ['ssh-add', '--apple-use-keychain', `${SSH}/id_ed25519`], stderr: 'Identity added: x\n' }],
    call: { method: 'addToAgent', args: [`${SSH}/id_ed25519.pub`, ''] }
  },
  {
    name: 'add on macOS with a passphrase',
    platform: 'darwin',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: SSH_BINS,
    replies: [{ argv: ['ssh-add', '--apple-use-keychain', `${SSH}/id_ed25519`], stderr: 'Identity added: x\n' }],
    call: { method: 'addToAgent', args: [`${SSH}/id_ed25519.pub`, 'pw'] }
  },
  {
    name: 'add without ssh-add',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: [],
    replies: [],
    call: { method: 'addToAgent', args: [`${SSH}/id_ed25519.pub`, ''] }
  },
  {
    name: 'add with a passphrase without ssh-add',
    shot: true,
    env: shotEnv,
    files: keyFiles,
    bins: [],
    replies: [],
    call: { method: 'addToAgent', args: [`${SSH}/id_ed25519.pub`, 'pw'] }
  },
  {
    name: 'test reads GitHub success out of a failing exit code',
    shot: true,
    env: shotEnv,
    bins: SSH_BINS,
    replies: [
      {
        argv: ['ssh', '-T', '-o', 'StrictHostKeyChecking=accept-new', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', 'git@github.com'],
        code: 1,
        stderr: "Hi octocat! You've successfully authenticated, but GitHub does not provide shell access.\n"
      }
    ],
    call: { method: 'test', args: ['github'] }
  },
  {
    name: 'test denied on GitLab',
    shot: true,
    env: shotEnv,
    bins: SSH_BINS,
    replies: [
      {
        argv: ['ssh', '-T', '-o', 'StrictHostKeyChecking=accept-new', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', 'git@gitlab.com'],
        code: 255,
        stdout: '\n',
        stderr: 'git@gitlab.com: Permission denied (publickey).\n'
      }
    ],
    call: { method: 'test', args: ['gitlab'] }
  },
  {
    name: 'test unreachable on Bitbucket',
    shot: true,
    env: shotEnv,
    bins: SSH_BINS,
    replies: [
      {
        argv: ['ssh', '-T', '-o', 'StrictHostKeyChecking=accept-new', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', 'git@bitbucket.org'],
        code: 255,
        stderr: 'ssh: Could not resolve hostname bitbucket.org: Temporary failure in name resolution\n'
      }
    ],
    call: { method: 'test', args: ['bitbucket'] }
  },
  {
    name: 'test without ssh',
    shot: true,
    env: shotEnv,
    bins: [],
    replies: [],
    call: { method: 'test', args: ['azure'] }
  },
  {
    name: 'test refuses a host it has no endpoint for',
    shot: true,
    env: shotEnv,
    bins: SSH_BINS,
    replies: [],
    call: { method: 'test', args: ['example.com'] }
  }
]

describe('ssh process behaviour', () => {
  it('matches internal/ssh/testdata/ssh.json', async () => {
    const ssh = await import('../../src/main/ssh')
    const out: unknown[] = []
    for (const c of sshCases) {
      const root = mkdtempSync(join(tmpdir(), 'gitcito-tools-'))
      try {
        layFiles(root, c.files)
        const { bin, fake } = installFake(root, c.bins, expand(c.replies, root))
        const args = expand(c.call.args, root)
        const env = { ...expand(c.env, root), PATH: bin, FAKE_DIR: fake }
        const res = await withEnv(env, c.platform, c.shot ? '--shot' : undefined, () =>
          outcome(async () => {
            if (c.call.method === 'status') return ssh.sshStatus()
            if (c.call.method === 'generate') return ssh.sshGenerate(args[0], args[1], args[2])
            if (c.call.method === 'addToAgent') return ssh.sshAddToAgent(args[0], args[1])
            return ssh.sshTest(args[0])
          })
        )
        const calls = readCalls(fake)
        // The helper is a throwaway: by the time the call returns it is gone.
        const askpassRemoved = calls.every((k) => !k.env.SSH_ASKPASS || !existsSync(dirname(k.env.SSH_ASKPASS)))
        const mode = c.modeOf ? statSync(expand(c.modeOf, root)).mode & 0o777 : undefined
        out.push(normalize({ ...c, ...res, calls, askpassRemoved, mode }, root))
      } finally {
        rmSync(root, { recursive: true, force: true })
      }
    }
    check('internal/ssh/testdata/ssh.json', out)
  })
})

// ═══ difftool ══════════════════════════════════════════════════════════════

interface DiffCase {
  name: string
  bins: string[]
  replies: Reply[]
  call: { method: 'config' | 'set' | 'diff' | 'merge'; args: unknown[] }
}
const REPO = '<ROOT>/repo'
const g = (...args: string[]): string[] => ['git', '-C', REPO, ...args]
const TOOL_HELP = [
  "'git difftool --tool=<tool>' may be set to one of the following:",
  '\t\tvimdiff          Use Vim',
  '\t\tmeld             Use Meld (requires a graphical session)',
  '',
  'The following tools are valid, but not currently available:',
  '\t\tbc               Use Beyond Compare (requires a graphical session)',
  '\t\taraxis           Use Araxis Merge (requires a graphical session)',
  '',
  'Some of the tools listed above only work in a windowed',
  'environment. If run in a terminal-only session, they will fail.',
  ''
].join('\n')

const diffCases: DiffCase[] = [
  {
    name: 'config reads both lists and the three keys',
    bins: ['git'],
    replies: [
      { argv: g('difftool', '--tool-help'), stdout: TOOL_HELP },
      // Some builds exit non-zero after printing the list.
      { argv: g('mergetool', '--tool-help'), stdout: TOOL_HELP.replace('difftool', 'mergetool'), code: 1 },
      { argv: g('config', '--get', 'diff.tool'), stdout: '  vimdiff \n' },
      { argv: g('config', '--get', 'merge.tool'), code: 1 },
      { argv: g('config', '--get', 'mergetool.keepBackup'), stdout: 'false\n' }
    ],
    call: { method: 'config', args: [REPO] }
  },
  {
    name: 'config when every call fails',
    bins: ['git'],
    replies: [],
    call: { method: 'config', args: [REPO] }
  },
  {
    name: 'config keeps the backup unless told otherwise',
    bins: ['git'],
    replies: [
      { argv: g('difftool', '--tool-help'), stdout: '' },
      { argv: g('mergetool', '--tool-help'), stdout: '', stderr: 'fatal: boom\n', code: 128 },
      { argv: g('config', '--get', 'mergetool.keepBackup'), stdout: 'False\n' }
    ],
    call: { method: 'config', args: [REPO] }
  },
  {
    name: 'config without git',
    bins: [],
    replies: [],
    call: { method: 'config', args: [REPO] }
  },
  {
    name: 'set writes globally by default and unsets an empty choice',
    bins: ['git'],
    replies: [
      { argv: g('config', '--global', 'diff.tool', 'meld') },
      { argv: g('config', '--global', '--unset', 'merge.tool'), code: 5 },
      { argv: g('config', '--global', 'mergetool.keepBackup', 'false') }
    ],
    call: { method: 'set', args: [REPO, { diffTool: 'meld', mergeTool: '', keepBackup: false }] }
  },
  {
    name: 'set to the repository',
    bins: ['git'],
    replies: [
      { argv: g('config', 'merge.tool', 'kdiff3') },
      { argv: g('config', 'mergetool.keepBackup', 'true') }
    ],
    call: { method: 'set', args: [REPO, { mergeTool: 'kdiff3', keepBackup: true }, 'repo'] }
  },
  {
    name: 'set with an explicit global scope',
    bins: ['git'],
    replies: [{ argv: g('config', '--global', '--unset', 'diff.tool') }],
    call: { method: 'set', args: [REPO, { diffTool: '' }, 'global'] }
  },
  {
    name: 'set with nothing to change',
    bins: ['git'],
    replies: [],
    call: { method: 'set', args: [REPO, {}, 'global'] }
  },
  {
    name: 'set stops at the first failed write',
    bins: ['git'],
    replies: [
      { argv: g('config', '--global', 'diff.tool', 'x'), code: 255, stderr: 'error: could not lock config file /h/.gitconfig: Permission denied\n' }
    ],
    call: { method: 'set', args: [REPO, { diffTool: 'x', mergeTool: 'y' }] }
  },
  {
    name: 'diff against a revision with a chosen tool',
    bins: ['git'],
    replies: [{ argv: g('difftool', '--no-prompt', '--tool=meld', 'HEAD~1', '--', 'src/a b.ts') }],
    call: { method: 'diff', args: [REPO, 'src/a b.ts', 'HEAD~1', 'meld'] }
  },
  {
    name: 'diff with git defaults',
    bins: ['git'],
    replies: [{ argv: g('difftool', '--no-prompt', '--', '-dash.ts'), stdout: 'ignored\n' }],
    call: { method: 'diff', args: [REPO, '-dash.ts'] }
  },
  {
    name: 'diff failure reports stderr',
    bins: ['git'],
    replies: [
      { argv: g('difftool', '--no-prompt', '--tool=nope', '--', 'a.ts'), code: 1, stdout: 'out\n', stderr: '\nUnknown tool nope\n' }
    ],
    call: { method: 'diff', args: [REPO, 'a.ts', '', 'nope'] }
  },
  {
    name: 'diff failure without stderr reports the command',
    bins: ['git'],
    replies: [{ argv: g('difftool', '--no-prompt', '--', 'a.ts'), code: 3, stdout: 'only stdout\n' }],
    call: { method: 'diff', args: [REPO, 'a.ts'] }
  },
  {
    name: 'diff without git',
    bins: [],
    replies: [],
    call: { method: 'diff', args: [REPO, 'a.ts'] }
  },
  {
    name: 'merge success',
    bins: ['git'],
    replies: [{ argv: g('mergetool', '--no-prompt', '--tool=meld', '--', 'conflict.txt'), stdout: 'Merging:\nconflict.txt\n' }],
    call: { method: 'merge', args: [REPO, 'conflict.txt', 'meld'] }
  },
  {
    name: 'merge closed without saving reports stdout',
    bins: ['git'],
    replies: [{ argv: g('mergetool', '--no-prompt', '--', 'c.txt'), code: 1, stdout: 'c.txt seems unchanged.\nWas the merge successful [y/n]? \n' }],
    call: { method: 'merge', args: [REPO, 'c.txt'] }
  },
  {
    name: 'merge failure prefers stderr',
    bins: ['git'],
    replies: [{ argv: g('mergetool', '--no-prompt', '--', 'c.txt'), code: 1, stdout: 'x', stderr: 'merge of c.txt failed\n' }],
    call: { method: 'merge', args: [REPO, 'c.txt', ''] }
  },
  {
    name: 'merge failure with no output reports the command',
    bins: ['git'],
    replies: [{ argv: g('mergetool', '--no-prompt', '--', 'c.txt'), code: 2 }],
    call: { method: 'merge', args: [REPO, 'c.txt'] }
  }
]

describe('difftool process behaviour', () => {
  it('matches internal/difftool/testdata/difftool.json', async () => {
    const dt = await import('../../src/main/difftool')
    const out: unknown[] = []
    for (const c of diffCases) {
      const root = mkdtempSync(join(tmpdir(), 'gitcito-tools-'))
      try {
        mkdirSync(join(root, 'repo'))
        const { bin, fake } = installFake(root, c.bins, expand(c.replies, root))
        const a = expand(c.call.args, root) as never[]
        const res = await withEnv({ PATH: bin, FAKE_DIR: fake }, undefined, undefined, () =>
          outcome(async () => {
            if (c.call.method === 'config') return dt.diffToolConfig(a[0])
            if (c.call.method === 'set') return dt.setDiffToolConfig(a[0], a[1], a[2])
            if (c.call.method === 'diff') return dt.openDiffTool(a[0], a[1], a[2], a[3])
            return dt.openMergeTool(a[0], a[1], a[2])
          })
        )
        // config runs its five reads concurrently: order the calls canonically.
        const calls = readCalls(fake)
        if (c.call.method === 'config') calls.sort((x, y) => (x.argv.join('\0') < y.argv.join('\0') ? -1 : 1))
        out.push(normalize({ ...c, ...res, calls }, root))
      } finally {
        rmSync(root, { recursive: true, force: true })
      }
    }
    check('internal/difftool/testdata/difftool.json', out)
  })
})

// ═══ editor ════════════════════════════════════════════════════════════════

interface DetectCase {
  name: string
  platform: 'linux' | 'darwin' | 'win32'
  env: Record<string, string>
  files: Record<string, string | null>
  modes: Record<string, number>
}
const detectCases: DetectCase[] = [
  {
    name: 'linux: first executable on PATH, CLI names in preference order',
    platform: 'linux',
    env: { PATH: ['<ROOT>/bin1', '', '<ROOT>/bin2', '<ROOT>/missing'].join(delimiter), HOME: '<ROOT>/home' },
    files: {
      'bin1/code': '#!/bin/sh\n',
      'bin1/cursor': '#!/bin/sh\n',
      'bin1/zed': '#!/bin/sh\n',
      'bin2/code-insiders': '#!/bin/sh\n',
      'bin2/code': '#!/bin/sh\n',
      'bin2/subl': null,
      'bin2/webstorm': '#!/bin/sh\n',
      'bin1/pycharm': '#!/bin/sh\n',
      'bin2/xed': '#!/bin/sh\n',
      'home/Applications/Windsurf.app': null
    },
    modes: { 'bin1/code': 0o644, 'bin1/cursor': 0o755, 'bin1/zed': 0o700, 'bin2/code-insiders': 0o755, 'bin2/code': 0o755, 'bin2/webstorm': 0o755, 'bin1/pycharm': 0o755, 'bin2/xed': 0o755 }
  },
  {
    name: 'linux: nothing found',
    platform: 'linux',
    env: { PATH: '', HOME: '<ROOT>/home' },
    files: { 'home/.keep': '' },
    modes: {}
  },
  {
    name: 'macOS: CLI first, then bundles under ~/Applications',
    platform: 'darwin',
    env: { PATH: '<ROOT>/bin', HOME: '<ROOT>/home' },
    files: {
      'bin/code': '#!/bin/sh\n',
      'home/Applications/Visual Studio Code.app': null,
      'home/Applications/Cursor.app': null,
      'home/Applications/Zed.app': '',
      'home/Applications/PyCharm.app': null,
      'home/Applications/GoLand.app': null
    },
    modes: { 'bin/code': 0o755 }
  },
  {
    name: 'Windows: .cmd/.exe/.bat on PATH, then Program Files roots',
    platform: 'win32',
    env: {
      PATH: '<ROOT>/bin',
      HOME: '<ROOT>/home',
      ProgramFiles: '<ROOT>/pf',
      LOCALAPPDATA: '<ROOT>/la'
    },
    files: {
      'bin/code.cmd': '',
      'bin/zed.exe': '',
      'bin/subl.bat': '',
      'bin/cursor': '',
      'pf/Zed\\zed.exe': '',
      'la/Programs/Sublime Text\\subl.exe': '',
      'la/Programs/Windsurf\\Windsurf.exe': '',
      'pf/cursor\\Cursor.exe': ''
    },
    modes: { 'bin/code.cmd': 0o755, 'bin/zed.exe': 0o644, 'bin/subl.bat': 0o755, 'bin/cursor': 0o755 }
  },
  {
    name: 'Windows: the x86 root',
    platform: 'win32',
    env: { PATH: '', HOME: '<ROOT>/home', 'ProgramFiles(x86)': '<ROOT>/pf86' },
    files: { 'pf86/Microsoft VS Code\\Code.exe': '' },
    modes: {}
  }
]

interface OpenCase {
  name: string
  bins: string[]
  replies: Reply[]
  setting: EditorSetting | null
  target: EditorTarget | null
  /** Spawned detached: the result returns before the child records itself. */
  waitFor?: number
}
const BIN = '<ROOT>/fakebin'
const openCases: OpenCase[] = [
  {
    name: 'VS Code at a line, detached',
    bins: ['code'],
    replies: [{}],
    setting: { id: 'vscode', name: 'Visual Studio Code', command: `${BIN}/code`, source: 'cli' },
    target: { path: '/r/a b.ts', line: 7 },
    waitFor: 1
  },
  {
    name: 'a bare command is looked up on PATH',
    bins: ['subl'],
    replies: [{}],
    setting: { id: 'sublime', name: 'Sublime Text', command: 'subl', source: 'cli' },
    target: { path: '/r/a.ts', line: 3, col: 9 },
    waitFor: 1
  },
  {
    name: 'a custom template with the workspace',
    bins: ['myed'],
    replies: [{}],
    setting: {
      id: 'custom',
      name: 'Mine',
      command: `${BIN}/myed`,
      source: 'cli',
      fileArgs: '--ws "{repo}" +{line} "{path}"',
      folderArgs: '--ws {repo}'
    },
    target: { path: '/r/my file.ts', line: 2, repo: '/r' },
    waitFor: 1
  },
  {
    name: 'a folder',
    bins: ['idea'],
    replies: [{}],
    setting: { id: 'jetbrains', name: 'JetBrains IDE', command: `${BIN}/idea`, source: 'cli' },
    target: { path: '/r', isDir: true },
    waitFor: 1
  },
  {
    name: 'a macOS bundle goes through open -a',
    bins: ['open'],
    replies: [{ argv: ['open', '-a', '/Applications/Visual Studio Code.app', '/r/a.ts'] }],
    setting: { id: 'vscode', name: 'Visual Studio Code', command: '/Applications/Visual Studio Code.app', source: 'app' },
    target: { path: '/r/a.ts', line: 7 }
  },
  {
    name: 'open -a failing reports the error',
    bins: ['open'],
    replies: [{ argv: ['open', '-a', 'Nope', '/r/a.ts'], code: 1, stderr: "Unable to find application named 'Nope'\n" }],
    setting: { id: 'custom', name: 'Nope', command: 'Nope', source: 'app' },
    target: { path: '/r/a.ts' }
  },
  {
    name: 'open missing reports the spawn error',
    bins: [],
    replies: [],
    setting: { id: 'custom', name: 'Nope', command: 'Nope', source: 'app' },
    target: { path: '/r/a.ts' }
  },
  { name: 'no setting', bins: [], replies: [], setting: null, target: { path: '/r/a.ts' } },
  {
    name: 'no command',
    bins: [],
    replies: [],
    setting: { id: 'vscode', name: 'Visual Studio Code', command: '', source: 'cli' },
    target: { path: '/r/a.ts' }
  },
  {
    name: 'no target',
    bins: [],
    replies: [],
    setting: { id: 'vscode', name: 'Visual Studio Code', command: '/bin/code', source: 'cli' },
    target: null
  }
]

describe('editor process behaviour', () => {
  it('matches internal/editor/testdata/editor.json', async () => {
    const editor = await import('../../src/main/editor')
    editor.registerEditorHandlers()
    const open = h.handlers.get('editor:open')!

    const detect: unknown[] = []
    for (const c of detectCases) {
      // A Mac running this sees its own /Applications; the vector assumes none.
      if (c.platform === 'darwin' && existsSync('/Applications')) {
        if (write) throw new Error('capture the darwin detection vector on a machine without /Applications')
        continue
      }
      const root = mkdtempSync(join(tmpdir(), 'gitcito-tools-'))
      try {
        layFiles(root, c.files, c.modes)
        const result = await withEnv(expand(c.env, root), c.platform, undefined, async () => editor.detectEditors())
        detect.push(normalize({ ...c, result }, root))
      } finally {
        rmSync(root, { recursive: true, force: true })
      }
    }

    const opened: unknown[] = []
    for (const c of openCases) {
      const root = mkdtempSync(join(tmpdir(), 'gitcito-tools-'))
      try {
        const { bin, fake } = installFake(root, c.bins, expand(c.replies, root))
        const result = await withEnv({ PATH: bin, FAKE_DIR: fake }, undefined, undefined, async () => {
          const r = await open(null, expand(c.setting, root), expand(c.target, root))
          if (c.waitFor) await waitCalls(fake, c.waitFor)
          return r
        })
        opened.push(normalize({ ...c, result, calls: readCalls(fake) }, root))
      } finally {
        rmSync(root, { recursive: true, force: true })
      }
    }

    const file = join(ROOT_DIR, 'internal/editor/testdata/editor.json')
    if (!write && existsSync('/Applications')) {
      // Compare what this machine could run; the darwin rows stay as captured.
      const saved = JSON.parse(readFileSync(file, 'utf8')) as { detect: Array<{ platform: string }>; open: unknown[] }
      expect(saved.detect.filter((d) => d.platform !== 'darwin')).toEqual(plain(detect))
      expect(saved.open).toEqual(plain(opened))
      return
    }
    check('internal/editor/testdata/editor.json', { detect, open: opened })
  })
})
