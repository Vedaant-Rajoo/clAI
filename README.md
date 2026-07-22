# clai

Local-first command compiler TUI.

## Shell Integration

`clai` can install an interactive widget for Fish, Bash, or Zsh. The widget
opens the TUI from a keybinding and replaces the complete command-line buffer
with the accepted command. It never executes the command automatically, so you
can inspect or edit it before pressing Enter.

- [Shell integration overview](docs/shell/README.md)
- [Fish setup](docs/shell/fish.md)
- [Bash setup](docs/shell/bash.md)
- [Zsh setup](docs/shell/zsh.md)

## Providers

Local rules by default; LLM providers (OpenRouter first) are opt-in.

- [Provider setup and auth](docs/providers.md)
