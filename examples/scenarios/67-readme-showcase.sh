# shellcheck shell=bash disable=SC2154
# 67. readme-showcase — a repository whose README is worth reading.
#
# Every other scenario writes its README with `rand_text`, which is right for
# them: a file that has to change, not one anybody reads. The repository details
# drawer renders the README, so it needs the opposite — a front page with the
# things a renderer is judged on (headings, a table, a fenced block, a list,
# inline links) and an origin on a recognisable host, which is what puts the
# "Open on …" button beside it.
R="$ROOT/readme-showcase"
new_repo "$R"

cat > "$R/README.md" <<'EOF'
# Tidepool

**A tiny cache for expensive things.** Put a value in, get it back until it
goes stale, and never pay for the same computation twice in one request.

Tidepool is deliberately small: one file, no dependencies, and no eviction
policy cleverer than "oldest first". If you need more than that, you need a
real cache.

## Install

```bash
npm install tidepool
```

## Use it

```ts
import { pool } from 'tidepool'

const users = pool({ ttl: 30_000, max: 500 })

const user = await users.get(id, () => db.users.findById(id))
```

The second argument runs only on a miss. Two callers asking for the same key
while it is in flight share one call — that de-duplication is the whole point,
and it is why `get` takes the loader rather than making you write the
check-then-set dance yourself.

## Options

| Option | Default | What it does |
|---|---|---|
| `ttl` | `60_000` | How long a value stays fresh, in milliseconds |
| `max` | `1000` | Entries kept before the oldest is dropped |
| `staleOk` | `false` | Serve an expired value while refreshing it in the background |
| `onEvict` | — | Called with `(key, value)` whenever an entry leaves the pool |

## What it will not do

- **No persistence.** The pool lives in the process and dies with it.
- **No sharing between processes.** Two workers keep two pools; if that matters
  to you, reach for Redis.
- **No size accounting.** `max` counts entries, not bytes, so a pool of large
  buffers can still surprise you.

See [docs/design.md](docs/design.md) for why the de-duplication is keyed on the
loader's identity, and [CONTRIBUTING.md](CONTRIBUTING.md) before opening a PR.

## Licence

MIT.
EOF

mkdir -p "$R/docs" "$R/src"
rand_text 24 "design" > "$R/docs/design.md"
rand_text 12 "contributing" > "$R/CONTRIBUTING.md"
rand_text 30 "pool" > "$R/src/pool.ts"
rand_text 18 "index" > "$R/src/index.ts"
git -C "$R" add -A && git -C "$R" commit -qm "feat: the first useful version"

rand_text 34 "pool-v2" > "$R/src/pool.ts"
git -C "$R" add -A && git -C "$R" commit -qm "perf: share in-flight loads between callers"

# Unreachable on purpose — the drawer only parses the URL to name the button.
git -C "$R" remote add origin "https://github.com/tidepool/tidepool.git"

summary "readme-showcase" "a repository with a real README (headings, table, code fences, links) and a GitHub origin, for the repository details drawer"
