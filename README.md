# clai

Local-first command compiler TUI.

## Commands

- `clai auth` — manage provider credentials (login, status, logout)
- `clai init` — print the shell integration script for fish, bash, or zsh
- `clai version` — print the clai version

Run `clai help` or `clai <command> help` for details. Running `clai` with no
command starts the interactive TUI; `--copy` puts the accepted command on the
clipboard and `--print-command` prints it to stdout.

## Shell Integration

`clai` can install an interactive widget for Fish, Bash, or Zsh. The widget
opens the TUI from a keybinding and inserts the accepted command at the current
cursor while preserving surrounding prompt text. It never executes the command
automatically, so you can inspect or edit it before pressing Enter.

- [Shell integration overview](docs/shell/README.md)
- [Fish setup](docs/shell/fish.md)
- [Bash setup](docs/shell/bash.md)
- [Zsh setup](docs/shell/zsh.md)

## Providers

Local rules by default; LLM providers (OpenRouter first) are opt-in. Remote
OpenRouter requests default to a minimal context policy that sends the intent
plus normalized OS family, shell family, and project kind only. Absolute paths
and Git details require invocation-scoped `--context-policy remote-explicit`
with repeatable `--share-context` approvals; `local-only` fails closed for remote
endpoints.

- [Provider setup, auth, and context privacy](docs/providers.md)
