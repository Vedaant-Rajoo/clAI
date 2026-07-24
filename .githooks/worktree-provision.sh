#!/bin/sh
# Provision a freshly created git worktree with the untracked files it needs and
# leave it ready to run. Fires from .githooks/post-checkout on `git worktree add`,
# and can also be run manually from inside a worktree to (re)provision it.
#
# As a post-checkout hook the args are: <prev-HEAD> <new-HEAD> <branch-flag>.
# branch-flag is 1 for a branch/HEAD checkout (including worktree add) and 0 for a
# file checkout. Manual runs pass no args.

# Skip file checkouts; always allow manual (arg-less) runs.
if [ "$#" -ge 3 ] && [ "$3" != "1" ]; then
  exit 0
fi

log() { printf 'worktree-provision: %s\n' "$1" >&2; }

# Resolve the primary checkout (source of truth) and the current worktree.
common_dir=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) || exit 0
primary=$(dirname "$common_dir")
here=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0

# Nothing to do in, or without, the primary checkout.
[ "$here" = "$primary" ] && exit 0
[ -d "$primary" ] || exit 0

# 1) Copy .env and .env.* (never overwrite what the worktree already has).
for src in "$primary"/.env "$primary"/.env.*; do
  [ -e "$src" ] || continue
  name=$(basename "$src")
  dest="$here/$name"
  [ -e "$dest" ] && continue
  cp -p "$src" "$dest" 2>/dev/null && log "copied $name"
done

# 2) Copy local Claude settings and plans (never overwrite).
if [ -d "$primary/.claude" ]; then
  mkdir -p "$here/.claude"
  if [ -f "$primary/.claude/settings.local.json" ] && [ ! -e "$here/.claude/settings.local.json" ]; then
    cp -p "$primary/.claude/settings.local.json" "$here/.claude/settings.local.json" 2>/dev/null \
      && log "copied .claude/settings.local.json"
  fi
  if [ -d "$primary/.claude/plans" ] && [ ! -e "$here/.claude/plans" ]; then
    cp -R "$primary/.claude/plans" "$here/.claude/plans" 2>/dev/null && log "copied .claude/plans"
  fi
fi

# 3) Symlink .local -> primary (single source of truth for the private contract).
if [ -d "$primary/.local" ] && [ ! -e "$here/.local" ] && [ ! -L "$here/.local" ]; then
  ln -s "$primary/.local" "$here/.local" 2>/dev/null && log "linked .local -> $primary/.local"
fi

# 4) Ready-to-run build, non-fatal, only when no binary is present yet.
if [ ! -x "$here/clai" ]; then
  if ( cd "$here" && make build ) 1>&2; then
    log "built clai"
  else
    log "make build failed (non-fatal); worktree still created"
  fi
fi

exit 0
