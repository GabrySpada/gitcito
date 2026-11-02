#!/usr/bin/env python3
"""Fill the "new home" column of docs/port/TEST-MAP.md.

Usage: python3 scripts/port/testmap-set.py <mapping-file>
Each mapping line: `<file> :: <substring of the full test name> => <new home>`.
Every substring must match exactly one row of that file, or the script fails.
"""
import sys

path = 'docs/port/TEST-MAP.md'
lines = open(path).read().split('\n')
fail = False
for raw in open(sys.argv[1]):
    raw = raw.strip()
    if not raw or raw.startswith('#'):
        continue
    left, home = raw.split(' => ', 1)
    file, needle = left.split(' :: ', 1)
    hits = [i for i, l in enumerate(lines) if l.startswith(f'| {file} | ') and needle in l.split(' | ')[1]]
    if len(hits) != 1:
        print(f'{len(hits)} rows for {file} :: {needle}')
        fail = True
        continue
    cells = lines[hits[0]].split(' | ')
    cells[2] = home
    lines[hits[0]] = ' | '.join(cells)
open(path, 'w').write('\n'.join(lines))
sys.exit(1 if fail else 0)
