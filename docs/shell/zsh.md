# Zsh Integration

The Zsh integration opens `clai` from a ZLE keybinding and replaces the complete
Zsh command-line buffer with an accepted command. It never executes the command
automatically.

See the [shell integration overview](README.md) for shared behavior and the
internal adapter contract.

## Setup

Make sure `clai` is on `PATH`, then load the integration in the current Zsh
session:

```zsh
eval "$(clai init zsh)"
```

For permanent use, add that line to `~/.zshrc`:

```zsh
eval "$(clai init zsh)"
```

Restart Zsh or reload the configuration:

```zsh
source ~/.zshrc
```

The default binding is `ctrl-x ctrl-a`. Press it, enter an intent, review the
suggested command, and accept it. The accepted command replaces the entire
current buffer with the cursor at the end, but it is not run. Cancelling with
`esc` or `ctrl-c` leaves the existing buffer unchanged.

## Configuration

Set configuration variables before evaluating `clai init zsh`.

Use a custom ZLE key sequence:

```zsh
export CLAI_ZSH_BINDING='^G'
eval "$(clai init zsh)"
```

Use a `clai` binary that is not on `PATH`:

```zsh
export CLAI_COMMAND="$HOME/.local/bin/clai"
eval "$(clai init zsh)"
```

Add persistent variable settings before the initialization line in `~/.zshrc`.
If the selected key sequence is already bound, the integration reports the
conflict rather than replacing the existing binding.

## Troubleshooting

Check that Zsh can find the binary:

```zsh
command -v clai
```

Inspect the generated integration without evaluating it:

```zsh
clai init zsh
```

Test the scripting-compatible stdout mode separately:

```zsh
command_to_review=$(clai --print-command)
printf '%s\n' "$command_to_review"
```

An accepted command is printed but not executed. Cancellation produces no
command. The interactive widget itself uses a protected temporary result file,
removes it on every completion path, and changes the prompt only after a
successful acceptance.
