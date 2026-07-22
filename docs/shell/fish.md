# Fish Integration

The Fish integration opens `clai` from a keybinding and replaces the complete
Fish command-line buffer with an accepted command. It never executes the
command automatically.

See the [shell integration overview](README.md) for shared behavior and the
internal adapter contract.

## Setup

Make sure `clai` is on `PATH`, then load the integration in the current Fish
session:

```fish
clai init fish | source
```

For permanent use, add that line to `~/.config/fish/config.fish`:

```fish
clai init fish | source
```

Restart Fish or reload the configuration:

```fish
source ~/.config/fish/config.fish
```

The default binding is `ctrl-x ctrl-a`. Press it, enter an intent, review the
suggested command, and accept it. The accepted command is inserted at the
cursor without running it, so anything already on the prompt is preserved.
Cancelling with `esc` or `ctrl-c` leaves the existing buffer unchanged.

## Configuration

Set configuration variables before `clai init fish | source`.

Use a custom Fish keybinding:

```fish
set -gx CLAI_FISH_BINDING '\cg'
clai init fish | source
```

`CLAI_BINDING` remains accepted for compatibility with the earlier Fish
integration, but `CLAI_FISH_BINDING` is preferred.

Use a `clai` binary that is not on `PATH`:

```fish
set -gx CLAI_COMMAND $HOME/.local/bin/clai
clai init fish | source
```

Add persistent variable settings before the initialization line in
`~/.config/fish/config.fish`. If the selected key sequence is already bound,
the integration reports the conflict rather than replacing the existing
binding.

## Migrating From the Old Fish Script

If your Fish configuration manually sources a checkout or copied script, remove
that line. For example, replace an entry shaped like:

```fish
source /path/to/clai/shell/fish/clai.fish
```

with the public initialization command:

```fish
clai init fish | source
```

The generated script is embedded in the installed binary, so setup no longer
depends on the repository path. Existing `CLAI_COMMAND` and `CLAI_BINDING`
settings continue to work, although `CLAI_FISH_BINDING` is now the preferred
binding variable.

## Troubleshooting

Check that Fish can find the binary:

```fish
type -q clai; and echo 'clai is available'
```

Inspect the generated integration without loading it:

```fish
clai init fish
```

Test the scripting-compatible stdout mode separately:

```fish
set -l command_to_review (clai --print-command)
printf '%s\n' "$command_to_review"
```

An accepted command is printed but not executed. Cancellation produces no
command. The interactive widget itself uses a protected temporary result file,
removes it on every completion path, and changes the prompt only after a
successful acceptance.
