#!/bin/sh
# Stand-in for the programs the tools group spawns (ssh, ssh-add, ssh-keygen,
# git, open, editor CLIs). The TypeScript capture (test/port/tools.test.ts) and
# the Go replay (internal/{editor,difftool,ssh}) both drive their real spawn
# code against it, so the goldens pin argv, environment and output handling
# without a network, an agent or a real ~/.ssh.
#
# Each call claims $FAKE_DIR/calls/<n>/ (mkdir is atomic, so concurrent calls
# get distinct slots) and records there:
#   argv            the program name and its arguments, each NUL-terminated
#   env             the selected variables that are set, as NAME\0VALUE\0 pairs
#   askpass         what $SSH_ASKPASS printed, when it names an executable
#   askpass_script  the helper's contents
#   leader          yes/no: does this process lead its own session (Linux only)
#   done            written last
# It then answers from the first $FAKE_DIR/replies/r*/ whose argv file equals
# this call's argv, else from replies/default: files stdout, stderr, code, and
# create (paths to create, one per line). No reply: exit 99.
name=$(basename "$0")
calls="$FAKE_DIR/calls"
mkdir -p "$calls"
i=0
while ! mkdir "$calls/$i" 2>/dev/null; do i=$((i + 1)); done
call="$calls/$i"
{
  printf '%s\0' "$name"
  for a in "$@"; do printf '%s\0' "$a"; done
} > "$call/argv"
: > "$call/env"
for k in DISPLAY SSH_ASKPASS SSH_ASKPASS_REQUIRE GIT_TERMINAL_PROMPT GITCITO_SSH_PASSPHRASE; do
  eval "isset=\${$k+set}"
  if [ "$isset" = set ]; then
    eval "v=\$$k"
    printf '%s\0%s\0' "$k" "$v" >> "$call/env"
  fi
done
if [ -n "$SSH_ASKPASS" ] && [ -x "$SSH_ASKPASS" ]; then
  "$SSH_ASKPASS" "Enter passphrase:" > "$call/askpass"
  cat "$SSH_ASKPASS" > "$call/askpass_script"
fi
if [ -r "/proc/$$/stat" ]; then
  sid=$(cut -d' ' -f6 "/proc/$$/stat")
  if [ "$sid" = "$$" ]; then echo yes > "$call/leader"; else echo no > "$call/leader"; fi
fi
reply=""
for r in "$FAKE_DIR"/replies/r*; do
  if [ -f "$r/argv" ] && cmp -s "$r/argv" "$call/argv"; then
    reply=$r
    break
  fi
done
if [ -z "$reply" ] && [ -d "$FAKE_DIR/replies/default" ]; then reply="$FAKE_DIR/replies/default"; fi
if [ -z "$reply" ]; then
  : > "$call/done"
  echo "fake $name: no reply for this call" >&2
  exit 99
fi
if [ -f "$reply/create" ]; then
  while IFS= read -r f; do
    if [ -n "$f" ]; then : > "$f"; fi
  done < "$reply/create"
fi
if [ -f "$reply/stdout" ]; then cat "$reply/stdout"; fi
if [ -f "$reply/stderr" ]; then cat "$reply/stderr" >&2; fi
code=0
if [ -f "$reply/code" ]; then code=$(cat "$reply/code"); fi
: > "$call/done"
exit "$code"
