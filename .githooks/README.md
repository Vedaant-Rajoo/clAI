# .githooks — worktree auto-provisioning

`post-checkout` fires whenever any tool runs `git worktree add` (Claude Code
`EnterWorktree`, the t3-code agent, or a manual add). It delegates to
`worktree-provision.sh`, which, for every new worktree:

1. copies `.env` and `.env.*` from the primary checkout (never overwriting),
2. copies `.claude/settings.local.json` and `.claude/plans/`,
3. symlinks `.local/` to the primary checkout (single source of truth),
4. runs `make build` (non-fatal) so the worktree is ready to run.

The script is idempotent: existing files are left untouched and the build is
skipped once a `clai` binary exists.

## One-time activation (per clone/machine)

```sh
git config core.hooksPath "$(git rev-parse --show-toplevel)/.githooks"
```

This is stored in the shared `.git/config`, so it applies to every worktree of
this clone. It uses an absolute path to the primary checkout's `.githooks`, so
new worktrees run the hook even though `.githooks/` is not committed and even
when the worktree lives outside the repo tree.

To re-provision an existing worktree, run from inside it:

```sh
sh /path/to/primary/.githooks/worktree-provision.sh
```

To disable: `git config --unset core.hooksPath`.
