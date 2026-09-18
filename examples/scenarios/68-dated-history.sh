# shellcheck shell=bash disable=SC2154
# 68. dated-history — commits spread across every rung of the date-divider
# ladder: today, yesterday, a few days, a week, a couple of weeks, months, a
# year. The graph draws one divider per span, so this is the repo that shows
# them; every other playground repo commits everything at "now" and lands in a
# single bucket with nothing to divide.
#
# Dates are relative to the moment the playground is built, not fixed points.
# A fixed 2026-08-01 would drift through the ladder as real time passes and
# the shot would silently lose its dividers; counting back from now keeps the
# same set of buckets on every rebuild.
R="$ROOT/dated-history"
new_repo "$R"

NOW=$(date +%s)
DAY=86400

# `@<epoch> +0000` is the one date form git parses identically on BSD and GNU
# userlands — `date -v` / `date -d` are not portable between them.
commit_ago() {
  local days="$1" msg="$2" when
  when=$(( NOW - days * DAY ))
  GIT_AUTHOR_DATE="@$when +0000" GIT_COMMITTER_DATE="@$when +0000" \
    git -C "$R" commit -q -m "$msg"
}

write_commit() {
  local days="$1" file="$2" line="$3" msg="$4"
  printf '%s\n' "$line" >> "$R/$file"
  git -C "$R" add -A
  commit_ago "$days" "$msg"
}

printf 'export const VERSION = "0.1.0"\n' > "$R/index.ts"
git -C "$R" add -A
commit_ago 400 "chore: initial commit"

# One commit per rung, oldest first. The day counts are chosen to sit well
# inside each bucket rather than on its edge, so a rebuild a few hours either
# side of midnight cannot tip one into its neighbour.
write_commit 400 index.ts 'export const NAME = "ledger"' "feat: name the package"
write_commit 200 index.ts 'export const API = "/v1"' "feat: pin the API version"
write_commit  95 index.ts 'export const RETRIES = 3' "feat: retry failed requests"
write_commit  40 index.ts 'export const TIMEOUT = 30' "feat: time requests out"
write_commit  17 index.ts 'export const CACHE = true' "perf: cache resolved entries"
write_commit   9 index.ts 'export const DEBUG = false' "chore: add a debug switch"

# A side branch that merges, so the dividers are seen crossing real lanes rather
# than a single straight column. It is merged *before* the remaining main-line
# commits are made, and dated after its own parents: --date-order shows a merge
# above the commits it merges, so a merge dated earlier than them would put a
# newer commit below an older one in the graph.
git -C "$R" checkout -q -b feat/audit-log
write_commit 7 audit.ts 'export function record(): void {}' "feat: record an audit line"
write_commit 7 audit.ts 'export const SINK = "stdout"' "feat: send audit lines to stdout"
git -C "$R" checkout -q main
MERGE_AT=$(( NOW - 6 * DAY ))
GIT_AUTHOR_DATE="@$MERGE_AT +0000" GIT_COMMITTER_DATE="@$MERGE_AT +0000" \
  git -C "$R" merge -q --no-ff feat/audit-log -m "Merge branch 'feat/audit-log'"

write_commit   5 index.ts 'export const LOCALE = "en"' "feat: carry a locale"
write_commit   4 index.ts 'export const TZ = "UTC"' "fix: pin the timezone"
# A second commit in the same bucket, so the top of the graph shows a divider
# closing a run of rows rather than one appearing under every single commit.
write_commit   4 index.ts 'export const CLOCK = "system"' "refactor: read the clock once"
write_commit   1 index.ts 'export const TRACE = false' "chore: add a trace switch"
write_commit   0 index.ts 'export const READY = true' "feat: expose a ready flag"

summary "dated-history" "commits spread across every date-divider bucket (today through a year ago), for the graph's date dividers"
