# Fish Integration

The Fish integration runs `clai --print-command`, captures the accepted command, and inserts it into the current prompt without executing it.

## Build

From the project root:

```sh
go build -o clai ./cmd/clai
```

For development, add the project root to your Fish `PATH`:

```fish
set -gx PATH /Users/newedia/Development/clai $PATH
```

Check that Fish can find the binary:

```fish
type clai
```

## Load The Integration

For the current Fish session:

```fish
source /Users/newedia/Development/clai/shell/fish/clai.fish
```

For permanent use, add that same line to:

```text
~/.config/fish/config.fish
```

## Keybinding

The default binding is:

```text
ctrl-x ctrl-a
```

Press the keybinding, enter an intent in `clai`, review the suggested command, then accept it. The command is inserted into the Fish prompt but is not executed.

## Configuration

The integration reads two optional Fish variables.

Use a custom `clai` binary path:

```fish
set -gx CLAI_COMMAND /Users/newedia/Development/clai/clai
```

Use a custom keybinding:

```fish
set -gx CLAI_BINDING \cg
```

Set those variables before sourcing the integration:

```fish
set -gx CLAI_COMMAND /Users/newedia/Development/clai/clai
set -gx CLAI_BINDING \cg
source /Users/newedia/Development/clai/shell/fish/clai.fish
```

## Manual Test

Test the command output mode directly:

```fish
clai --print-command
```

Accept a command. It should print only the selected command after the TUI exits.

Test Fish capture:

```fish
set -l cmd (clai --print-command)
echo $cmd
```

If you accept `git status`, `echo $cmd` should print:

```text
git status
```

## Troubleshooting

If the keybinding does nothing, reload the integration:

```fish
source /Users/newedia/Development/clai/shell/fish/clai.fish
```

If no command is inserted, make sure you accepted the command in the review screen. Cancelling with `esc` or `ctrl-c` inserts nothing.

If Fish cannot find `clai`, rebuild it and check your `PATH`:

```fish
go build -o clai ./cmd/clai
set -gx PATH /Users/newedia/Development/clai $PATH
type clai
```

If you want a different keybinding, edit `shell/fish/clai.fish` and change:

```fish
bind \cx\ca __clai_insert_command
```

Or configure it without editing the script:

```fish
set -gx CLAI_BINDING \cg
source /Users/newedia/Development/clai/shell/fish/clai.fish
```
