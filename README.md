# clai

Local-first command compiler TUI.

## Install

clai requires Go 1.26.2 or newer. Install the latest release directly:

```sh
go install github.com/Vedaant-Rajoo/clai/cmd/clai@latest
```

Or install from a cloned checkout:

```sh
git clone https://github.com/Vedaant-Rajoo/clai.git
cd clai
make install
```

Both commands install `clai` into Go's binary directory. Make sure `GOBIN` (or
`$(go env GOPATH)/bin` when `GOBIN` is unset) is on your `PATH`.

## Quickstart

1. Run `clai` to open the interactive screen.
2. Describe the task you want the shell to perform in plain language, such as
   `run the project tests`.
3. Review the suggested command, then accept it only when you are comfortable
   running it.

For a prompt widget, run `clai init <shell>` with `fish`, `bash`, or `zsh`, then
follow the matching [shell setup guide](docs/shell/README.md). The widget inserts
accepted commands for review; it never runs them automatically.

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

Local `rules` by default; the LLM providers `openrouter` and direct `anthropic`
are opt-in. Direct Anthropic uses the official Anthropic SDK and defaults to
`claude-sonnet-5`; its streaming is internal only, so partial model output never
enters review — the TUI stays on loading until one complete candidate is ready.
Remote providers default to a minimal context policy that sends the intent plus
normalized OS family, shell family, project kind, platform architecture, and
presence/version status for a fixed 14-tool allowlist. It never sends executable
paths, raw probe output, history, prior output, environment dumps, identity,
credentials, or repository contents. Absolute paths and Git details require
invocation-scoped `--context-policy remote-explicit` with repeatable
`--share-context` approvals; `local-only` omits context and fails closed for
remote endpoints.

To exercise a remote provider without spending API calls, run
`go run ./cmd/clai-stubprovider` and pass `--dev-endpoint http://127.0.0.1:8747`.
The override accepts loopback hosts only and labels every screen, so it cannot
send traffic off-host or be mistaken for a real session.

- [Provider setup, auth, and context privacy](docs/providers.md)
